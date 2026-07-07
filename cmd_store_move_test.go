package main

import (
	"path/filepath"
	"testing"

	store "github.com/SirNiklas9/projx-store"
)

// TestStoreMoveBetweenScopes proves `store move` RELOCATES a record between physical
// files (project store ↔ your global store) instead of recreating it: the id, key, and
// body survive, the row leaves its old file and appears in the new one with the new
// scope, the composite still resolves it by the same id, and undo restores it exactly —
// with NO duplicate left behind.
func TestStoreMoveBetweenScopes(t *testing.T) {
	root := t.TempDir()
	yours := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", yours)

	rec := store.Record{
		ID: "convention/promote-me", Kind: store.KConvention,
		Scope: store.ScopeProject, Key: "promote me", Body: "hello",
	}
	st := openStore(root)
	if err := st.Put(rec); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// Before the move it physically lives in the PROJECT file.
	if proj := openFileT(t, filepath.Join(root, ".projx", "store.db")); true {
		if _, ok := proj.Get(rec.ID); !ok {
			t.Fatal("record not in project store before move")
		}
		proj.Close()
	}

	storeMove(root, []string{rec.ID, "--to", "global"})

	// After: gone from project, present in yours with scope=global, content intact.
	proj := openFileT(t, filepath.Join(root, ".projx", "store.db"))
	if _, ok := proj.Get(rec.ID); ok {
		t.Error("record still in project store after move (should have relocated)")
	}
	proj.Close()

	yr := openFileT(t, filepath.Join(yours, "store.db"))
	got, ok := yr.Get(rec.ID)
	if !ok {
		t.Fatal("record not in yours store after move")
	}
	if got.Scope != store.ScopeGlobal {
		t.Errorf("moved record scope = %v, want global", got.Scope)
	}
	if got.Body != "hello" || got.Key != "promote me" {
		t.Errorf("move altered content: %+v", got)
	}
	yr.Close()

	// The composite still resolves it by the SAME id.
	st = openStore(root)
	if r, ok := st.Get(rec.ID); !ok || r.Scope != store.ScopeGlobal {
		t.Errorf("composite Get after move: ok=%v scope=%v", ok, r.Scope)
	}
	// Undo must restore project scope and remove the relocated copy (no duplicate).
	if _, ok := undoLastStore(root, st); !ok {
		t.Fatal("undo reported nothing to undo")
	}
	st.Close()

	proj = openFileT(t, filepath.Join(root, ".projx", "store.db"))
	if r, ok := proj.Get(rec.ID); !ok || r.Scope != store.ScopeProject {
		t.Errorf("after undo, project store: ok=%v scope=%v", ok, r.Scope)
	}
	proj.Close()

	yr = openFileT(t, filepath.Join(yours, "store.db"))
	if _, ok := yr.Get(rec.ID); ok {
		t.Error("after undo, relocated copy still in yours store (duplicate)")
	}
	yr.Close()
}

func openFileT(t *testing.T, path string) *store.SQLite {
	t.Helper()
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
