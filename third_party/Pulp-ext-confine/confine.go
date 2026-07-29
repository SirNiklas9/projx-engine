// Package confineext provides the spawn.confine capability for Pulp cells:
// the ability to launch a host command with OS-level filesystem confinement
// applied — Landlock on Linux, AppContainer on Windows.
//
// The cell specifies a policy (root dir, RO paths, RW paths, allowed net
// destinations) and an argv to execute. The host applies the policy then
// launches the command as a confined child process tracked in an async task
// pool, matching the spawn.process async model exactly.
//
// Deployment:
//
//	import _ "github.com/BananaLabs-OSS/Pulp-ext-confine"
//
// Host imports exposed (msgpack request/response over linear memory):
//
//	confine_spawn(req_ptr, req_len) -> task_id_or_code   # id>=100 on success
//	confine_result(task_id, out_ptr_out, out_len_out) -> status
//	confine_cancel(task_id) -> code
package confineext

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BananaLabs-OSS/Pulp-ext-confine/caged"
	"github.com/BananaLabs-OSS/Pulp-ext-confine/core"
	"github.com/BananaLabs-OSS/Pulp/ext"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/vmihailenco/msgpack/v5"
)

const (
	codeOK             = uint32(0)
	codeEmptyReq       = uint32(1)
	codeMemRead        = uint32(2)
	codeMsgpackDecode  = uint32(3)
	codeInvalidRequest = uint32(4)
	codeQueueFull      = uint32(15)
	codeCellFull       = uint32(16)
	codeSaturated      = uint32(17)
	codeCapAbsent      = uint32(99)

	statusPending  = uint32(0)
	statusComplete = uint32(1)
	statusError    = uint32(2)
	statusUnknown  = uint32(4)

	firstTaskID    = uint32(100)
	defaultTimeout = 10 * time.Minute
	resultTTL      = 10 * time.Minute

	defaultMaxConcurrency = 8
	defaultMaxQueued      = 128
	defaultMaxPerCell     = 4
)

var globalPool *confinePool

func init() {
	ext.Register(ext.Capability{
		Name:         "spawn.confine",
		Setup:        setup,
		Teardown:     teardown,
		TeardownCell: teardownCell,
		Register:     bindActive,
		Stub:         bindStub,
	})
}

// ── request / result types ────────────────────────────────────────────────────

type spawnRequest struct {
	Argv      []string          `msgpack:"argv"`
	Root      string            `msgpack:"root"`
	ReadOnly  []string          `msgpack:"read_only,omitempty"`
	ReadWrite []string          `msgpack:"read_write,omitempty"`
	NetAllow  []string          `msgpack:"net_allow,omitempty"`
	Env       map[string]string `msgpack:"env,omitempty"`
	Dir       string            `msgpack:"dir,omitempty"`
}

type spawnResult struct {
	ExitCode int    `msgpack:"exit_code"`
	Error    string `msgpack:"error,omitempty"`
}

// cagedRequest is the wire form of caged.CagedPolicy for the confine.run_caged
// host fn. Secrets values are injected into the child env only and are NEVER
// echoed back in any result.
type cagedRequest struct {
	Argv      []string          `msgpack:"argv"`
	Root      string            `msgpack:"root"`
	ReadOnly  []string          `msgpack:"read_only,omitempty"`
	ReadWrite []string          `msgpack:"read_write,omitempty"`
	NetAllow  []string          `msgpack:"net_allow,omitempty"`
	JailBins  []string          `msgpack:"jail_bins,omitempty"`
	Secrets   map[string]string `msgpack:"secrets,omitempty"`
	Env       map[string]string `msgpack:"env,omitempty"`
	Dir       string            `msgpack:"dir,omitempty"`
}

// cagedAuditEvent mirrors caged.AuditEvent on the wire.
type cagedAuditEvent struct {
	Kind   string `msgpack:"kind"`
	Detail string `msgpack:"detail"`
	Denied bool   `msgpack:"denied,omitempty"`
}

// cagedResult is the wire form of the composed caged launch outcome. It carries
// audit events but never secret values.
type cagedResult struct {
	ExitCode    int               `msgpack:"exit_code"`
	Error       string            `msgpack:"error,omitempty"`
	AuditEvents []cagedAuditEvent `msgpack:"audit_events,omitempty"`
}

// ── pool ─────────────────────────────────────────────────────────────────────

type taskResult struct {
	data      []byte
	status    uint32
	completed time.Time
	cellID    string
}

type inflightTask struct {
	cancel context.CancelFunc
	done   chan struct{}
	cellID string
}

type confinePool struct {
	logger *slog.Logger

	sem       chan struct{}
	maxQueued int

	cellsMu    sync.Mutex
	cellCount  map[string]int
	maxPerCell int

	nextID atomic.Uint32

	mu       sync.Mutex
	inflight map[uint32]*inflightTask
	results  map[uint32]*taskResult

	cleanupStop context.CancelFunc
	cleanupDone chan struct{}
}

func newConfinePool(logger *slog.Logger, maxConc, maxQueued, maxPerCell int) *confinePool {
	ctx, cancel := context.WithCancel(context.Background())
	p := &confinePool{
		logger:      logger,
		sem:         make(chan struct{}, maxConc),
		maxQueued:   maxQueued,
		cellCount:   map[string]int{},
		maxPerCell:  maxPerCell,
		inflight:    map[uint32]*inflightTask{},
		results:     map[uint32]*taskResult{},
		cleanupStop: cancel,
		cleanupDone: make(chan struct{}),
	}
	go p.cleanupLoop(ctx)
	return p
}

func (p *confinePool) acquireCell(cellID string) bool {
	if cellID == "" {
		return true
	}
	p.cellsMu.Lock()
	defer p.cellsMu.Unlock()
	if p.cellCount[cellID] >= p.maxPerCell {
		return false
	}
	p.cellCount[cellID]++
	return true
}

func (p *confinePool) releaseCell(cellID string) {
	if cellID == "" {
		return
	}
	p.cellsMu.Lock()
	defer p.cellsMu.Unlock()
	if p.cellCount[cellID] > 0 {
		p.cellCount[cellID]--
	}
	if p.cellCount[cellID] == 0 {
		delete(p.cellCount, cellID)
	}
}

func (p *confinePool) inflightCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.inflight)
}

func (p *confinePool) submit(cellID string, req spawnRequest) (uint32, uint32) {
	if len(req.Argv) == 0 {
		return 0, codeInvalidRequest
	}
	if p.inflightCount() >= p.maxQueued {
		return 0, codeQueueFull
	}
	if !p.acquireCell(cellID) {
		return 0, codeCellFull
	}

	var id uint32
	for {
		id = p.nextID.Add(1)
		if id >= firstTaskID {
			break
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	task := &inflightTask{cancel: cancel, done: make(chan struct{}), cellID: cellID}

	p.mu.Lock()
	p.inflight[id] = task
	p.mu.Unlock()

	select {
	case p.sem <- struct{}{}:
	default:
		p.mu.Lock()
		delete(p.inflight, id)
		p.mu.Unlock()
		cancel()
		p.releaseCell(cellID)
		return 0, codeSaturated
	}

	go func() {
		defer func() {
			<-p.sem
			p.releaseCell(cellID)
			close(task.done)
		}()

		policy := core.Policy{
			Root:      req.Root,
			ReadOnly:  req.ReadOnly,
			ReadWrite: req.ReadWrite,
			NetAllow:  req.NetAllow,
		}

		// Build child environment: overlay request env onto host env.
		env := os.Environ()
		for k, v := range req.Env {
			env = append(env, k+"="+v)
		}

		// LaunchConfined blocks until the child exits.
		confiner := core.Detect()
		var res spawnResult

		// Honour the context cancellation by running in a goroutine with a
		// timeout wrapper. The context cancel (from cancel()) propagates via
		// a watchdog goroutine that kills the pool task record.
		done := make(chan struct{})
		var exitCode int
		var launchErr error
		go func() {
			defer close(done)
			exitCode, launchErr = confiner.LaunchConfined(policy, req.Argv, env, req.Dir)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			// The context was cancelled (confine_cancel called). We cannot
			// directly kill the child from here without a handle. Mark as
			// error and let the OS child outlive the task record, which is
			// the same trade-off as ext-process for non-killable children.
			// A future improvement is to expose a kill handle from
			// LaunchConfined.
			res.Error = "task cancelled"
			res.ExitCode = -1
			data, _ := msgpack.Marshal(res)
			p.mu.Lock()
			delete(p.inflight, id)
			p.results[id] = &taskResult{data: data, status: statusError, completed: time.Now(), cellID: cellID}
			p.mu.Unlock()
			return
		}

		if launchErr != nil {
			res.Error = launchErr.Error()
			res.ExitCode = -1
		} else {
			res.ExitCode = exitCode
		}

		data, encErr := msgpack.Marshal(res)
		status := statusComplete
		if encErr != nil {
			data = []byte(encErr.Error())
			status = statusError
		} else if res.Error != "" {
			status = statusError
		}

		p.mu.Lock()
		delete(p.inflight, id)
		p.results[id] = &taskResult{data: data, status: status, completed: time.Now(), cellID: cellID}
		p.mu.Unlock()
	}()

	return id, codeOK
}

// submitCaged queues a composed caged launch (full jail+secrets+egress+FS
// composition via caged.RunCaged) on the same async pool as confine_spawn.
func (p *confinePool) submitCaged(cellID string, req cagedRequest) (uint32, uint32) {
	if len(req.Argv) == 0 {
		return 0, codeInvalidRequest
	}
	if p.inflightCount() >= p.maxQueued {
		return 0, codeQueueFull
	}
	if !p.acquireCell(cellID) {
		return 0, codeCellFull
	}

	var id uint32
	for {
		id = p.nextID.Add(1)
		if id >= firstTaskID {
			break
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	task := &inflightTask{cancel: cancel, done: make(chan struct{}), cellID: cellID}

	p.mu.Lock()
	p.inflight[id] = task
	p.mu.Unlock()

	select {
	case p.sem <- struct{}{}:
	default:
		p.mu.Lock()
		delete(p.inflight, id)
		p.mu.Unlock()
		cancel()
		p.releaseCell(cellID)
		return 0, codeSaturated
	}

	go func() {
		defer func() {
			<-p.sem
			p.releaseCell(cellID)
			close(task.done)
		}()

		policy := caged.CagedPolicy{
			Argv:      req.Argv,
			Root:      req.Root,
			ReadOnly:  req.ReadOnly,
			ReadWrite: req.ReadWrite,
			NetAllow:  req.NetAllow,
			JailBins:  req.JailBins,
			Secrets:   req.Secrets,
			Env:       req.Env,
			Dir:       req.Dir,
		}

		done := make(chan struct{})
		var cres caged.CagedResult
		var launchErr error
		go func() {
			defer close(done)
			cres, launchErr = caged.RunCaged(policy)
		}()

		var res cagedResult
		select {
		case <-done:
		case <-ctx.Done():
			res.Error = "task cancelled"
			res.ExitCode = -1
			data, _ := msgpack.Marshal(res)
			p.mu.Lock()
			delete(p.inflight, id)
			p.results[id] = &taskResult{data: data, status: statusError, completed: time.Now(), cellID: cellID}
			p.mu.Unlock()
			return
		}

		// Map audit events (codenames/details only — never secret values).
		for _, ev := range cres.AuditEvents {
			res.AuditEvents = append(res.AuditEvents, cagedAuditEvent{Kind: ev.Kind, Detail: ev.Detail, Denied: ev.Denied})
		}
		if launchErr != nil {
			res.Error = launchErr.Error()
			res.ExitCode = cres.ExitCode
			if res.ExitCode == 0 {
				res.ExitCode = -1
			}
		} else {
			res.ExitCode = cres.ExitCode
		}

		data, encErr := msgpack.Marshal(res)
		status := statusComplete
		if encErr != nil {
			data = []byte(encErr.Error())
			status = statusError
		} else if res.Error != "" {
			status = statusError
		}

		p.mu.Lock()
		delete(p.inflight, id)
		p.results[id] = &taskResult{data: data, status: status, completed: time.Now(), cellID: cellID}
		p.mu.Unlock()
	}()

	return id, codeOK
}

func (p *confinePool) result(cellID string, id uint32) ([]byte, uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r, ok := p.results[id]; ok {
		if r.cellID != cellID {
			return nil, statusUnknown
		}
		delete(p.results, id)
		return r.data, r.status
	}
	if t, ok := p.inflight[id]; ok {
		if t.cellID != cellID {
			return nil, statusUnknown
		}
		return nil, statusPending
	}
	return nil, statusUnknown
}

func (p *confinePool) cancel(cellID string, id uint32) uint32 {
	p.mu.Lock()
	task, ok := p.inflight[id]
	if ok && task.cellID != cellID {
		ok = false
	}
	p.mu.Unlock()
	if !ok {
		return 1
	}
	task.cancel()
	return 0
}

func (p *confinePool) cleanupLoop(ctx context.Context) {
	defer close(p.cleanupDone)
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			p.mu.Lock()
			for id, r := range p.results {
				if now.Sub(r.completed) > resultTTL {
					delete(p.results, id)
				}
			}
			p.mu.Unlock()
		}
	}
}

func (p *confinePool) teardownAll() {
	p.cleanupStop()
	<-p.cleanupDone
	p.mu.Lock()
	tasks := make([]*inflightTask, 0, len(p.inflight))
	for _, t := range p.inflight {
		tasks = append(tasks, t)
	}
	p.mu.Unlock()
	for _, t := range tasks {
		t.cancel()
	}
	deadline := time.After(5 * time.Second)
	for _, t := range tasks {
		select {
		case <-t.done:
		case <-deadline:
			return
		}
	}
}

func (p *confinePool) teardownCell(cellID string) {
	p.mu.Lock()
	tasks := make([]*inflightTask, 0)
	for _, t := range p.inflight {
		if t.cellID == cellID {
			tasks = append(tasks, t)
		}
	}
	for id, r := range p.results {
		if r.cellID == cellID {
			delete(p.results, id)
		}
	}
	p.mu.Unlock()
	for _, t := range tasks {
		t.cancel()
	}
}

// ── lifecycle ─────────────────────────────────────────────────────────────────

func setup(env ext.SetupEnv) error {
	logger := env.Logger
	if logger == nil {
		logger = slog.Default()
	}
	globalPool = newConfinePool(logger, defaultMaxConcurrency, defaultMaxQueued, defaultMaxPerCell)
	c := core.Detect()
	logger.Info("spawn.confine ready", "level", c.Level(), "available", c.Available())
	return nil
}

func teardown(_ context.Context) error {
	if globalPool != nil {
		globalPool.teardownAll()
	}
	return nil
}

func teardownCell(_ context.Context, cellID string) error {
	if globalPool != nil {
		globalPool.teardownCell(cellID)
	}
	return nil
}

// ── bindings ──────────────────────────────────────────────────────────────────

func bindActive(b wazero.HostModuleBuilder, cell ext.Cell) error {
	cellID := ""
	if cell != nil {
		cellID = cell.Name()
	}

	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, reqPtr, reqLen uint32) uint32 {
		if reqLen == 0 {
			return codeEmptyReq
		}
		data, ok := m.Memory().Read(reqPtr, reqLen)
		if !ok {
			return codeMemRead
		}
		var req spawnRequest
		if err := msgpack.Unmarshal(data, &req); err != nil {
			return codeMsgpackDecode
		}
		id, code := globalPool.submit(cellID, req)
		if code != codeOK {
			return code
		}
		return id
	}).Export("confine_spawn")

	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, taskID, outPtrOut, outLenOut uint32) uint32 {
		data, status := globalPool.result(cellID, taskID)
		if status == statusPending || status == statusUnknown {
			return status
		}
		if len(data) == 0 {
			m.Memory().WriteUint32Le(outPtrOut, 0)
			m.Memory().WriteUint32Le(outLenOut, 0)
			return status
		}
		allocFn := m.ExportedFunction("pulp_alloc")
		if allocFn == nil {
			return status
		}
		res, err := allocFn.Call(ctx, uint64(len(data)))
		if err != nil || len(res) == 0 {
			return status
		}
		ptr := uint32(res[0])
		if ptr == 0 || !m.Memory().Write(ptr, data) {
			return status
		}
		m.Memory().WriteUint32Le(outPtrOut, ptr)
		m.Memory().WriteUint32Le(outLenOut, uint32(len(data)))
		return status
	}).Export("confine_result")

	b.NewFunctionBuilder().WithFunc(func(_ context.Context, _ api.Module, taskID uint32) uint32 {
		return globalPool.cancel(cellID, taskID)
	}).Export("confine_cancel")

	// confine.run_caged — the composed caged launch (jail + secrets + egress +
	// FS confinement) in one capability call. Async, same pool model as
	// confine_spawn; results are fetched with the same confine_result export
	// (task ids are shared), so only the submit fn is distinct.
	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, reqPtr, reqLen uint32) uint32 {
		if reqLen == 0 {
			return codeEmptyReq
		}
		data, ok := m.Memory().Read(reqPtr, reqLen)
		if !ok {
			return codeMemRead
		}
		var req cagedRequest
		if err := msgpack.Unmarshal(data, &req); err != nil {
			return codeMsgpackDecode
		}
		id, code := globalPool.submitCaged(cellID, req)
		if code != codeOK {
			return code
		}
		return id
	}).Export("caged_run")

	return nil
}

func bindStub(b wazero.HostModuleBuilder, _ ext.Cell) error {
	b.NewFunctionBuilder().WithFunc(func(_ context.Context, _ api.Module, _, _ uint32) uint32 { return codeCapAbsent }).Export("confine_spawn")
	b.NewFunctionBuilder().WithFunc(func(_ context.Context, _ api.Module, _, _, _ uint32) uint32 { return statusUnknown }).Export("confine_result")
	b.NewFunctionBuilder().WithFunc(func(_ context.Context, _ api.Module, _ uint32) uint32 { return 1 }).Export("confine_cancel")
	b.NewFunctionBuilder().WithFunc(func(_ context.Context, _ api.Module, _, _ uint32) uint32 { return codeCapAbsent }).Export("caged_run")
	return nil
}
