package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	store "github.com/SirNiklas9/projx-store"
)

func TestStoreCommitLifecycleDefaultsAndMetadata(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "yours"))

	storeCommit(root, []string{
		"--kind", "doc", "--key", "observed behavior", "--body", "candidate evidence",
		"--by", "agent", "--claim-class", "volatile", "--verified-at", "2026-07-21",
		"--review-after", "2026-08-04", "--verifier", "go test ./...", "--confidence", "80",
	})
	st := openStore(root)
	candidate, ok := st.Get("doc/observed-behavior")
	st.Close()
	if !ok {
		t.Fatal("agent candidate was not committed")
	}
	if candidate.LifecycleStatus() != store.StatusCandidate || candidate.Authoritative() {
		t.Fatalf("agent record lifecycle = %q authoritative=%v", candidate.LifecycleStatus(), candidate.Authoritative())
	}
	if candidate.ClaimClass != "volatile" || candidate.Verifier != "go test ./..." || candidate.Confidence != 80 {
		t.Fatalf("lifecycle metadata not preserved: %+v", candidate)
	}
	wantVerified := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC).UnixMilli()
	if candidate.VerifiedAt != wantVerified || candidate.ReviewAfter <= candidate.VerifiedAt {
		t.Fatalf("verification window = %d..%d", candidate.VerifiedAt, candidate.ReviewAfter)
	}

	storeCommit(root, []string{"--kind", "adr", "--key", "approved decision", "--body", "accepted", "--by", "ui"})
	st = openStore(root)
	approved, ok := st.Get("adr/approved-decision")
	st.Close()
	if !ok || approved.LifecycleStatus() != store.StatusActive || !approved.Authoritative() {
		t.Fatalf("human record should be active: %+v, found=%v", approved, ok)
	}
}

func TestOpenStoreExistingSafeDoesNotCreateProjectStore(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "yours"))
	if err := os.MkdirAll(filepath.Join(root, ".projx-workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := store.Open(filepath.Join(root, ".projx-workspace", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.Put(store.Record{
		ID:    "doc/workspace-root",
		Kind:  store.KDoc,
		Scope: store.ScopeWorkspace,
		Key:   "workspace/root",
		Body:  "workspace root",
	}); err != nil {
		t.Fatal(err)
	}
	ws.Close()

	st, err := openStoreExistingSafe(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(st.List(store.InScope(store.ScopeWorkspace))); got != 1 {
		t.Fatalf("workspace records = %d, want 1", got)
	}
	st.Close()

	if hasProjectStore(root) {
		t.Fatalf("openStoreExistingSafe created %s", filepath.Join(root, ".projx", "store.db"))
	}
}

func TestSeedWorkspaceFloorIsIdempotent(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, ".projx-workspace")
	if err := os.MkdirAll(marker, 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := store.Open(filepath.Join(marker, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	seeded, present, err := seedWorkspaceFloor(ws, root)
	if err != nil {
		t.Fatal(err)
	}
	want := len(workspaceFloorConventions) + len(workspaceFloorDocs)
	if len(seeded) != want {
		t.Fatalf("first seed wrote %d record(s); want %d", len(seeded), want)
	}
	if len(present) != 0 {
		t.Fatalf("first seed reported already-present = %v; want none", present)
	}

	for _, id := range []string{
		"convention/workspace-shared-rules-live-here",
		"convention/workspace-before-project-duplication",
		"convention/scope-placement",
		"convention/workspace-shared-gates-belong-here",
		"convention/workspace-routing-defaults",
		"convention/workspace-commit-durable-cross-repo-facts",
		"doc/workspace-root",
		"doc/workspace-member-repos",
	} {
		rec, ok := ws.Get(id)
		if !ok {
			t.Fatalf("workspace floor record %q missing", id)
		}
		if rec.Scope != store.ScopeWorkspace {
			t.Fatalf("record %q scope = %v; want workspace", id, rec.Scope)
		}
	}

	seeded2, present2, err := seedWorkspaceFloor(ws, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded2) != 0 {
		t.Fatalf("second seed wrote %v; want nothing", seeded2)
	}
	if len(present2) != want {
		t.Fatalf("second seed reported %d already-present; want %d", len(present2), want)
	}
}
