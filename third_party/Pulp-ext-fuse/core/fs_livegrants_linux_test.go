//go:build linux

package core_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BananaLabs-OSS/Pulp-ext-fuse/core"
	grants "github.com/BananaLabs-OSS/Pulp-grants"
)

// grantApprover grants a canned decision and counts how often it is asked.
type grantApprover struct {
	mu    sync.Mutex
	calls int
	dec   grants.Decision
}

func (a *grantApprover) Decide(grants.Request) grants.Decision {
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	return a.dec
}

// TestVDriveLiveGrantViaBroker is the Slice-1 red->green proof. In
// TestVDrivePolicyAndPassthrough the identical path secret/key.txt is DENIED by
// the deny-on-miss stub. Here OnMiss routes the miss to a grants.Broker that
// grants it live, so the read SUCCEEDS through a real FUSE mount — and the grant
// persists and can be revoked.
func TestVDriveLiveGrantViaBroker(t *testing.T) {
	backing := t.TempDir()
	if err := os.MkdirAll(filepath.Join(backing, "secret"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backing, "secret", "key.txt"), []byte("TOPSECRET"), 0644); err != nil {
		t.Fatal(err)
	}

	// Empty policy: NOTHING is statically allowed. secret/ is denied unless the
	// broker grants it at runtime.
	policy := core.Policy{}

	store := grants.NewMemStore()
	ap := &grantApprover{dec: grants.Decision{Access: int(core.Read), Scope: grants.ScopePermanent}}
	broker := &grants.Broker{
		Store: store, Approver: ap, Timeout: 2 * time.Second,
		CellID: "test-cell", GrantedBy: "harness",
	}

	var mu sync.Mutex
	allowedSecret := false
	hooks := core.Hooks{
		Audit: func(op, relpath string, allowed bool) {
			mu.Lock()
			if allowed && strings.HasPrefix(relpath, "secret") {
				allowedSecret = true
			}
			mu.Unlock()
		},
		OnMiss: func(relpath string, want core.Access) core.Access {
			return core.Access(broker.Decide(grants.KindFS, relpath, int(want)))
		},
	}

	mountpoint := t.TempDir()
	vd := &core.VDrive{Backing: backing, Policy: policy, Hooks: hooks}
	srv, err := core.Mount(mountpoint, vd)
	if err != nil {
		t.Skipf("FUSE mount unavailable in this environment: %v", err)
	}
	defer func() {
		if err := srv.Unmount(); err != nil {
			t.Logf("unmount: %v", err)
		}
	}()
	time.Sleep(200 * time.Millisecond)

	// --- The proof: a statically-denied path is readable because of a live grant ---
	data, err := os.ReadFile(filepath.Join(mountpoint, "secret", "key.txt"))
	if err != nil {
		t.Fatalf("expected live-granted read to SUCCEED, got: %v", err)
	}
	if string(data) != "TOPSECRET" {
		t.Fatalf("expected TOPSECRET, got %q", string(data))
	}
	t.Logf("PASS: broker live-granted the denied path; read returned %q", string(data))

	// The approver was consulted at least once; the persisted grant then covered
	// the remaining child/open misses without re-asking (see grants_test for the
	// strict no-re-ask persistence proof).
	if ap.calls < 1 {
		t.Fatal("approver was never consulted on the miss")
	}
	t.Logf("PASS: approver consulted %d time(s); children covered by the persisted grant", ap.calls)

	// A grant was persisted and covers the file.
	if acc, ok := store.Lookup(grants.KindFS, "secret/key.txt", int(core.Read)); !ok || acc < int(core.Read) {
		t.Fatalf("no persisted grant covers secret/key.txt: acc=%d ok=%v", acc, ok)
	}
	if act, _ := store.Active("test-cell", grants.KindFS); len(act) == 0 {
		t.Fatal("expected an active fs grant for the cell")
	}

	// Audit recorded the now-allowed secret op.
	mu.Lock()
	gotAllowed := allowedSecret
	mu.Unlock()
	if !gotAllowed {
		t.Error("audit never recorded an allowed secret op")
	}

	// --- REVOKE (the 'remove' half): revoking removes the grant from the store ---
	// (Broker-level proof; FUSE's attribute cache makes a live re-deny through the
	// same mount flaky, so revocation effect is asserted on the store.)
	n, err := store.RevokeMatching(grants.KindFS, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("revoke matched nothing")
	}
	if _, ok := store.Lookup(grants.KindFS, "secret/key.txt", int(core.Read)); ok {
		t.Fatal("grant still active after revoke — remove did not take effect")
	}
	t.Logf("PASS: grant revoked; %d grant(s) removed, path no longer covered", n)
}
