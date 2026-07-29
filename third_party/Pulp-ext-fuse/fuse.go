// Package fuseext provides the storage.fuse capability for Pulp cells: a
// policy-enforcing pass-through virtual drive (FUSE on Linux) whose per-op file
// I/O is served at full native speed by core, while only POLICY DECISIONS cross
// the cell boundary.
//
// Design principle (do not violate): the cell exposes only COARSE operations —
// mount and unmount. Individual reads/writes are NEVER round-tripped through the
// cell; the core's policyNode answers them natively in the host. The cell learns
// what happened through asynchronous "fuse.audit" / "fuse.denied" step events
// (one per enforced operation), surfaced via Poll. The flow is host-authoritative:
// the cell declares a policy at mount time and then merely observes.
//
// Deployment:
//
//	import _ "github.com/BananaLabs-OSS/Pulp-ext-fuse"
//
// Host imports exposed (msgpack request over linear memory):
//
//	fuse_mount(req_ptr, req_len) -> mount_id_or_code   # id>=100 on success
//	fuse_unmount(mount_id)       -> code               # 0 on success
//
// Events delivered to the cell via the step loop (decode with DecodeFuseEvent):
//
//	fuse.audit  -> { mount_id, op, path, allowed }   # every enforced op
//	fuse.denied -> { mount_id, op, path, allowed=false }
//
// On non-Linux hosts core.Mount returns the platform stub error, so fuse_mount
// reports a mount failure; the capability still compiles and registers. The
// capability layer itself is platform-neutral — only core/fs_linux.go is tagged.
package fuseext

import (
	"context"
	"log/slog"
	"sync"

	"github.com/BananaLabs-OSS/Pulp-ext-fuse/core"
	grants "github.com/BananaLabs-OSS/Pulp-grants"
	"github.com/BananaLabs-OSS/Pulp/ext"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/vmihailenco/msgpack/v5"
)

const (
	codeOK            = uint32(0)
	codeEmptyReq      = uint32(1)
	codeMemRead       = uint32(2)
	codeMsgpackDecode = uint32(3)
	codeInvalidReq    = uint32(4)
	codeMountFailed   = uint32(5)
	codeNoMount       = uint32(6)
	codeCellFull      = uint32(16)
	codeCapAbsent     = uint32(99)

	firstMountID    = uint32(100)
	maxMountsPerCell = 16
	maxEventBuffer   = 4096 // per-cell un-drained events, then oldest dropped
)

// fuseEvent is the payload of a "fuse.audit" / "fuse.denied" step event: the
// outcome of one policy decision the core made while serving a native op.
type fuseEvent struct {
	MountID uint32 `msgpack:"mount_id"`
	Op      string `msgpack:"op"`
	Path    string `msgpack:"path"`
	Allowed bool   `msgpack:"allowed"`
}

// queuedEvent is an event awaiting Poll, tagged with whether it was a denial.
type queuedEvent struct {
	denied bool
	ev     fuseEvent
}

// mountRule mirrors core.Rule over the msgpack wire (access as an int).
type mountRule struct {
	Prefix string `msgpack:"prefix"`
	Access int    `msgpack:"access"`
}

// mountRequest is the fuse_mount payload: where to mount, what to back it with,
// and the policy rules the core enforces natively.
type mountRequest struct {
	Mountpoint string      `msgpack:"mountpoint"`
	Backing    string      `msgpack:"backing"`
	Rules      []mountRule `msgpack:"rules"`
	// LiveGrants opts this mount into runtime "request access" elevation: a
	// policy miss is routed to the host-side broker (SetLiveBroker) instead of a
	// flat deny. Default false → legacy deny-on-miss, behavior unchanged.
	LiveGrants bool `msgpack:"live_grants"`
}

// liveBroker is the host-installed broker that backs live FS grants for mounts
// created with LiveGrants=true. nil (the default) => OnMiss denies, exactly as
// before. Its Store and Approver are shared across cells; the per-mount copy in
// makeOnMiss scopes persisted grants to the mounting cell.
var (
	liveBrokerMu sync.RWMutex
	liveBroker   *grants.Broker
)

// SetLiveBroker installs (or clears, with nil) the host-side broker that turns
// OnMiss from a flat deny into a live grant decision. Called once at host
// setup, before mounts are created. Decisions are host-side — consistent with
// the design principle that per-op file bytes never cross the cell boundary.
func SetLiveBroker(b *grants.Broker) {
	liveBrokerMu.Lock()
	liveBroker = b
	liveBrokerMu.Unlock()
}

// makeOnMiss builds the OnMiss hook for one mount. When the mount didn't opt
// into live grants, or no broker is installed, it returns the legacy deny
// (core.None). Otherwise a miss is routed to a per-cell copy of the broker,
// whose Decide consults the grant store (covering reload) then the approver.
func makeOnMiss(cellID string, live bool) func(string, core.Access) core.Access {
	deny := func(string, core.Access) core.Access { return core.None }
	if !live {
		return deny
	}
	liveBrokerMu.RLock()
	tmpl := liveBroker
	liveBrokerMu.RUnlock()
	if tmpl == nil {
		return deny
	}
	bc := *tmpl // per-cell copy: shares Store/Approver, scopes persistence to this cell
	bc.CellID = cellID
	return func(relpath string, want core.Access) core.Access {
		return core.Access(bc.Decide(grants.KindFS, relpath, int(want)))
	}
}

// mountHandle is one live core mount belonging to a cell.
type mountHandle struct {
	id     uint32
	server *core.Server
}

// cellState is a cell's isolated set of mounts plus its pending-event ring.
type cellState struct {
	mu     sync.Mutex
	mounts map[uint32]*mountHandle
	events []queuedEvent // FIFO; oldest dropped past maxEventBuffer
}

func newCellState() *cellState {
	return &cellState{mounts: map[uint32]*mountHandle{}}
}

// pushEvent appends an event, dropping the oldest if the ring is full. Must be
// non-blocking: it runs inside the core's Audit hook on the native I/O path.
func (cs *cellState) pushEvent(qe queuedEvent) {
	cs.mu.Lock()
	if len(cs.events) >= maxEventBuffer {
		cs.events = cs.events[1:]
	}
	cs.events = append(cs.events, qe)
	cs.mu.Unlock()
}

// drainOne pops the oldest pending event, if any.
func (cs *cellState) drainOne() (queuedEvent, bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if len(cs.events) == 0 {
		return queuedEvent{}, false
	}
	qe := cs.events[0]
	cs.events = cs.events[1:]
	return qe, true
}

var (
	mu       sync.Mutex
	cells    = map[string]*cellState{}
	order    []string // round-robin Poll fairness across cells
	nextID   uint32   = firstMountID - 1
	nextEvID uint64
	logger          = slog.Default()
)

func init() {
	ext.Register(ext.Capability{
		Name:         "storage.fuse",
		Setup:        setup,
		Teardown:     teardown,
		TeardownCell: teardownCell,
		Register:     bindActive,
		Stub:         bindStub,
		Poll:         pollEvents,
		// Finalize is a no-op: events are drained into the StepEvent at Poll time.
	})
}

func setup(env ext.SetupEnv) error {
	if env.Logger != nil {
		logger = env.Logger
	}
	logger.Info("storage.fuse ready")
	return nil
}

func teardown(_ context.Context) error {
	mu.Lock()
	defer mu.Unlock()
	for _, cs := range cells {
		cs.mu.Lock()
		for id, h := range cs.mounts {
			if h.server != nil {
				_ = h.server.Unmount()
			}
			delete(cs.mounts, id)
		}
		cs.mu.Unlock()
	}
	cells = map[string]*cellState{}
	order = nil
	return nil
}

// teardownCell unmounts ONLY the named cell's drives on `ctl reload`. Without
// this, a self-rebuild would leak FUSE mounts for the host's lifetime.
func teardownCell(_ context.Context, cellID string) error {
	mu.Lock()
	cs := cells[cellID]
	delete(cells, cellID)
	kept := order[:0]
	for _, id := range order {
		if id != cellID {
			kept = append(kept, id)
		}
	}
	order = kept
	mu.Unlock()
	if cs == nil {
		return nil
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	for id, h := range cs.mounts {
		if h.server != nil {
			_ = h.server.Unmount()
		}
		delete(cs.mounts, id)
	}
	return nil
}

// getOrMakeCell returns the cell's state, creating + registering it for Poll
// fairness on first use.
func getOrMakeCell(cellID string) *cellState {
	mu.Lock()
	defer mu.Unlock()
	cs := cells[cellID]
	if cs == nil {
		cs = newCellState()
		cells[cellID] = cs
		order = append(order, cellID)
	}
	return cs
}

func getCell(cellID string) *cellState {
	mu.Lock()
	defer mu.Unlock()
	return cells[cellID]
}

// buildPolicy converts wire rules into a core.Policy.
func buildPolicy(rules []mountRule) core.Policy {
	out := core.Policy{Rules: make([]core.Rule, 0, len(rules))}
	for _, r := range rules {
		out.Rules = append(out.Rules, core.Rule{
			Prefix: r.Prefix,
			Access: core.Access(r.Access),
		})
	}
	return out
}

// doMount builds the policy + audit/deny hooks and mounts the core drive. The
// hooks push events into the cell's ring; they MUST stay non-blocking because
// the core calls them inline on the native I/O path (see core enforce()).
func doMount(cellID string, req mountRequest) (uint32, uint32) {
	if req.Mountpoint == "" || req.Backing == "" {
		return 0, codeInvalidReq
	}
	cs := getOrMakeCell(cellID)

	cs.mu.Lock()
	tooMany := len(cs.mounts) >= maxMountsPerCell
	cs.mu.Unlock()
	if tooMany {
		return 0, codeCellFull
	}

	mu.Lock()
	nextID++
	id := nextID
	mu.Unlock()

	vd := &core.VDrive{
		Backing: req.Backing,
		Policy:  buildPolicy(req.Rules),
		Hooks: core.Hooks{
			Audit: func(op, relpath string, allowed bool) {
				ev := fuseEvent{MountID: id, Op: op, Path: relpath, Allowed: allowed}
				cs.pushEvent(queuedEvent{denied: !allowed, ev: ev})
			},
			// OnMiss routes a policy miss to the host-side live broker when this
			// mount opted in (LiveGrants) and a broker is installed; otherwise it
			// denies (core.None), the legacy behavior. Either way the outcome is
			// recorded by the Audit hook above.
			OnMiss: makeOnMiss(cellID, req.LiveGrants),
		},
	}

	srv, err := core.Mount(req.Mountpoint, vd)
	if err != nil {
		logger.Warn("storage.fuse mount failed", "cell", cellID, "mountpoint", req.Mountpoint, "err", err)
		return 0, codeMountFailed
	}

	cs.mu.Lock()
	cs.mounts[id] = &mountHandle{id: id, server: srv}
	cs.mu.Unlock()
	logger.Info("storage.fuse mounted", "cell", cellID, "id", id, "mountpoint", req.Mountpoint, "backing", req.Backing)
	return id, codeOK
}

// doUnmount tears down one of the cell's mounts.
func doUnmount(cellID string, id uint32) uint32 {
	cs := getCell(cellID)
	if cs == nil {
		return codeNoMount
	}
	cs.mu.Lock()
	h, ok := cs.mounts[id]
	if ok {
		delete(cs.mounts, id)
	}
	cs.mu.Unlock()
	if !ok {
		return codeNoMount
	}
	if h.server != nil {
		if err := h.server.Unmount(); err != nil {
			logger.Warn("storage.fuse unmount error", "cell", cellID, "id", id, "err", err)
			return codeMountFailed
		}
	}
	return codeOK
}

// pollEvents drains one cell's oldest pending event per call (round-robin across
// cells) and emits it as a fuse.audit / fuse.denied event for that cell. This is
// the ONLY data that crosses the boundary — never the file bytes themselves.
func pollEvents() (ext.StepEvent, bool) {
	mu.Lock()
	ids := append([]string(nil), order...)
	mu.Unlock()
	for _, cellID := range ids {
		cs := getCell(cellID)
		if cs == nil {
			continue
		}
		qe, ok := cs.drainOne()
		if !ok {
			continue
		}
		payload, err := msgpack.Marshal(qe.ev)
		if err != nil {
			continue
		}
		mu.Lock()
		nextEvID++
		evID := nextEvID
		// rotate this cell to the back for fairness
		for i, x := range order {
			if x == cellID {
				order = append(append(order[:i:i], order[i+1:]...), cellID)
				break
			}
		}
		mu.Unlock()
		kind := "fuse.audit"
		if qe.denied {
			kind = "fuse.denied"
		}
		return ext.StepEvent{Kind: kind, Payload: payload, ID: evID, CellID: cellID}, true
	}
	return ext.StepEvent{}, false
}

// ── bindings ──────────────────────────────────────────────────────────────────

func bindActive(b wazero.HostModuleBuilder, cell ext.Cell) error {
	cellID := ""
	if cell != nil {
		cellID = cell.Name()
	}

	b.NewFunctionBuilder().WithFunc(func(_ context.Context, m api.Module, reqPtr, reqLen uint32) uint32 {
		if reqLen == 0 {
			return codeEmptyReq
		}
		data, ok := m.Memory().Read(reqPtr, reqLen)
		if !ok {
			return codeMemRead
		}
		var req mountRequest
		if err := msgpack.Unmarshal(data, &req); err != nil {
			return codeMsgpackDecode
		}
		id, code := doMount(cellID, req)
		if code != codeOK {
			return code
		}
		return id
	}).Export("fuse_mount")

	b.NewFunctionBuilder().WithFunc(func(_ context.Context, _ api.Module, mountID uint32) uint32 {
		return doUnmount(cellID, mountID)
	}).Export("fuse_unmount")

	return nil
}

func bindStub(b wazero.HostModuleBuilder, _ ext.Cell) error {
	b.NewFunctionBuilder().WithFunc(func(_ context.Context, _ api.Module, _, _ uint32) uint32 { return codeCapAbsent }).Export("fuse_mount")
	b.NewFunctionBuilder().WithFunc(func(_ context.Context, _ api.Module, _ uint32) uint32 { return codeCapAbsent }).Export("fuse_unmount")
	return nil
}
