//go:build linux

package core_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/BananaLabs-OSS/Pulp-ext-fuse/core"
)

func TestVDrivePolicyAndPassthrough(t *testing.T) {
	// Set up backing directory with allowed and secret subtrees.
	backing := t.TempDir()

	if err := os.MkdirAll(filepath.Join(backing, "allowed"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backing, "allowed", "in.txt"), []byte("INSIDE"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(backing, "secret"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backing, "secret", "key.txt"), []byte("TOPSECRET"), 0644); err != nil {
		t.Fatal(err)
	}

	// Policy: allowed/ → ReadWrite; everything else (including secret/) → None.
	policy := core.Policy{Rules: []core.Rule{
		{Prefix: "allowed", Access: core.ReadWrite},
	}}

	var mu sync.Mutex
	type auditEntry struct {
		op      string
		path    string
		allowed bool
	}
	var auditLog []auditEntry

	hooks := core.Hooks{
		Audit: func(op, relpath string, allowed bool) {
			mu.Lock()
			auditLog = append(auditLog, auditEntry{op, relpath, allowed})
			mu.Unlock()
		},
		OnMiss: func(relpath string, want core.Access) core.Access {
			return core.None // always deny on miss
		},
	}

	mountpoint := t.TempDir()
	vd := &core.VDrive{Backing: backing, Policy: policy, Hooks: hooks}
	srv, err := core.Mount(mountpoint, vd)
	if err != nil {
		t.Skipf("FUSE mount failed (unavailable in this environment): %v", err)
	}
	defer func() {
		if err := srv.Unmount(); err != nil {
			t.Logf("unmount: %v", err)
		}
	}()

	// Give FUSE a moment to be ready.
	time.Sleep(200 * time.Millisecond)

	// --- Test 1: read allowed file through mount ---
	data, err := os.ReadFile(filepath.Join(mountpoint, "allowed", "in.txt"))
	if err != nil {
		t.Fatalf("reading allowed/in.txt through mount: %v", err)
	}
	if string(data) != "INSIDE" {
		t.Fatalf("expected INSIDE, got %q", string(data))
	}
	t.Logf("PASS: allowed read returned %q", string(data))

	// --- Test 2: write new file through mount, verify in backing ---
	newContent := []byte("NEWFILE")
	if err := os.WriteFile(filepath.Join(mountpoint, "allowed", "new.txt"), newContent, 0644); err != nil {
		t.Fatalf("writing allowed/new.txt through mount: %v", err)
	}
	// Read from the REAL backing dir, bypassing FUSE entirely.
	got, err := os.ReadFile(filepath.Join(backing, "allowed", "new.txt"))
	if err != nil {
		t.Fatalf("reading new.txt from backing: %v", err)
	}
	if string(got) != string(newContent) {
		t.Fatalf("backing file mismatch: got %q, want %q", string(got), string(newContent))
	}
	t.Logf("PASS: write through mount landed in backing: %q", string(got))

	// --- Test 3: read secret file through mount — must be denied ---
	_, err = os.ReadFile(filepath.Join(mountpoint, "secret", "key.txt"))
	if err == nil {
		t.Fatal("FAIL: expected permission denied for secret/key.txt, got nil error")
	}
	t.Logf("PASS: secret read correctly denied: %v", err)

	// --- Test 4: audit log recorded both allowed and denied ops ---
	mu.Lock()
	snapshot := make([]auditEntry, len(auditLog))
	copy(snapshot, auditLog)
	mu.Unlock()

	var hasAllowed, hasDenied bool
	for _, e := range snapshot {
		if e.allowed {
			hasAllowed = true
		} else {
			hasDenied = true
		}
	}
	if !hasAllowed {
		t.Error("audit: no allowed ops recorded")
	}
	if !hasDenied {
		t.Error("audit: no denied ops recorded")
	}
	t.Logf("PASS: audit log has %d entries (allowed=%v denied=%v)", len(snapshot), hasAllowed, hasDenied)
	for _, e := range snapshot {
		t.Logf("  audit: op=%s path=%q allowed=%v", e.op, e.path, e.allowed)
	}
}
