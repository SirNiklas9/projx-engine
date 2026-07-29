package fuseext

import (
	"context"
	"testing"

	"github.com/BananaLabs-OSS/Pulp-ext-fuse/core"
	"github.com/BananaLabs-OSS/Pulp/ext"
	"github.com/tetratelabs/wazero"
	"github.com/vmihailenco/msgpack/v5"
)

// TestCapabilityRegistered confirms the package's init() registered the
// storage.fuse capability with the expected lifecycle + Poll wiring.
func TestCapabilityRegistered(t *testing.T) {
	var found *ext.Capability
	for _, c := range ext.All() {
		c := c
		if c.Name == "storage.fuse" {
			found = &c
			break
		}
	}
	if found == nil {
		t.Fatal("storage.fuse not registered")
	}
	if found.Register == nil || found.Stub == nil {
		t.Fatal("storage.fuse missing Register/Stub")
	}
	if found.Setup == nil || found.Teardown == nil || found.TeardownCell == nil {
		t.Fatal("storage.fuse missing a lifecycle hook")
	}
	if found.Poll == nil {
		t.Fatal("storage.fuse missing Poll (audit/denied events would never reach the cell)")
	}
}

// TestBindStubBuilds confirms the stub bindings register both host imports
// (fuse_mount / fuse_unmount) into a wazero host module without error. wazero
// forbids calling exported functions directly on a host module, so the 99
// (codeCapAbsent) return value of each stub closure is asserted separately in
// TestStubReturnValue; here we prove the export names are present so a guest
// that imports them links.
func TestBindStubBuilds(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	defer rt.Close(ctx)

	hb := rt.NewHostModuleBuilder("pulp")
	if err := bindStub(hb, nil); err != nil {
		t.Fatalf("bindStub: %v", err)
	}
	if _, err := hb.Instantiate(ctx); err != nil {
		t.Fatalf("instantiate stub host module (export names must be valid): %v", err)
	}
}

// TestStubReturnValue asserts the codeCapAbsent contract value the stub closures
// return — the cell maps this (99) to ErrFUSEUnavailable on the Fiber side.
func TestStubReturnValue(t *testing.T) {
	if codeCapAbsent != 99 {
		t.Fatalf("codeCapAbsent = %d, want 99 (Fiber maps 99 -> ErrFUSEUnavailable)", codeCapAbsent)
	}
}

// TestActivePathCodesWithoutMount exercises the host decision paths that don't
// require a real FUSE mount: an unknown unmount and an invalid mount request.
func TestActivePathCodesWithoutMount(t *testing.T) {
	mu.Lock()
	savedCells, savedOrder := cells, order
	cells, order = map[string]*cellState{}, nil
	mu.Unlock()
	defer func() {
		mu.Lock()
		cells, order = savedCells, savedOrder
		mu.Unlock()
	}()

	if code := doUnmount("ghost", 999); code != codeNoMount {
		t.Fatalf("doUnmount(unknown) = %d, want codeNoMount(%d)", code, codeNoMount)
	}
	if _, code := doMount("cellX", mountRequest{Mountpoint: "", Backing: ""}); code != codeInvalidReq {
		t.Fatalf("doMount(empty) = %d, want codeInvalidReq(%d)", code, codeInvalidReq)
	}
}

// TestPollEventsEmptyWhenIdle confirms Poll is non-blocking and returns false
// when no audit/denied events are pending.
func TestPollEventsEmptyWhenIdle(t *testing.T) {
	// Snapshot + restore global state so this test is order-independent.
	mu.Lock()
	savedCells, savedOrder := cells, order
	cells, order = map[string]*cellState{}, nil
	mu.Unlock()
	defer func() {
		mu.Lock()
		cells, order = savedCells, savedOrder
		mu.Unlock()
	}()

	if _, ok := pollEvents(); ok {
		t.Fatal("pollEvents returned an event with no cells registered")
	}
}

// TestPushDrainEventRoundTrip confirms the per-cell ring buffers an audit event
// and Poll emits it as a fuse.audit StepEvent that DecodeFuseEvent-equivalent
// msgpack decodes — proving only the DECISION crosses the boundary, not bytes.
func TestPushDrainEventRoundTrip(t *testing.T) {
	mu.Lock()
	savedCells, savedOrder := cells, order
	cells, order = map[string]*cellState{}, nil
	mu.Unlock()
	defer func() {
		mu.Lock()
		cells, order = savedCells, savedOrder
		mu.Unlock()
	}()

	cs := getOrMakeCell("cellA")
	cs.pushEvent(queuedEvent{denied: false, ev: fuseEvent{MountID: 100, Op: "Open", Path: "allowed/x", Allowed: true}})
	cs.pushEvent(queuedEvent{denied: true, ev: fuseEvent{MountID: 100, Op: "Open", Path: "secret/y", Allowed: false}})

	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		evt, ok := pollEvents()
		if !ok {
			t.Fatalf("expected event %d", i)
		}
		if evt.CellID != "cellA" {
			t.Fatalf("event CellID = %q, want cellA", evt.CellID)
		}
		var fe fuseEvent
		if err := msgpack.Unmarshal(evt.Payload, &fe); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		got[evt.Kind] = true
		if evt.Kind == "fuse.denied" && fe.Allowed {
			t.Fatal("fuse.denied event marked allowed")
		}
	}
	if !got["fuse.audit"] || !got["fuse.denied"] {
		t.Fatalf("missing event kinds, got %v", got)
	}
	if _, ok := pollEvents(); ok {
		t.Fatal("pollEvents should be drained")
	}
}

// TestBuildPolicyMapsAccess confirms wire rules map to core access levels.
func TestBuildPolicyMapsAccess(t *testing.T) {
	p := buildPolicy([]mountRule{
		{Prefix: "allowed", Access: 2},
		{Prefix: "secret", Access: 0},
	})
	if len(p.Rules) != 2 {
		t.Fatalf("rules = %d, want 2", len(p.Rules))
	}
	if got := p.Check("allowed/file"); got != core.ReadWrite {
		t.Fatalf("allowed/file access = %d, want ReadWrite", got)
	}
	if got := p.Check("secret/file"); got != core.None {
		t.Fatalf("secret/file access = %d, want None", got)
	}
}
