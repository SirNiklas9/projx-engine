package main

import (
	"os"
	"path/filepath"
	"testing"

	store "github.com/SirNiklas9/projx-store"
	verify "github.com/SirNiklas9/projx-verify"

	core "github.com/SirNiklas9/projx-core"
)

// mkRoot creates a temp dir wired as a projx root (with .projx/ subdir).
func mkRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// openTestStore opens (or creates) the store for the given root.
func openTestStore(t *testing.T, root string) *store.SQLite {
	t.Helper()
	st, err := store.Open(filepath.Join(root, ".projx", "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return st
}

func TestStoreCommitGetRoundtrip(t *testing.T) {
	root := mkRoot(t)
	st := openTestStore(t, root)
	defer st.Close()

	rec := store.Record{
		ID:    "doc/hello",
		Kind:  store.KDoc,
		Scope: store.ScopeProject,
		Key:   "hello",
		Body:  "Hello, world.",
	}
	if err := st.Put(rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	recordStoreOp(root, "put", "ui", nil, &rec)

	got, ok := st.Get("doc/hello")
	if !ok {
		t.Fatal("Get: not found after Put")
	}
	if got.Key != "hello" {
		t.Errorf("Key = %q, want %q", got.Key, "hello")
	}
	if got.Body != "Hello, world." {
		t.Errorf("Body = %q, want %q", got.Body, "Hello, world.")
	}
	if got.Kind != store.KDoc {
		t.Errorf("Kind = %v, want KDoc", got.Kind)
	}
}

func TestStoreRmIsNonDestructive(t *testing.T) {
	root := mkRoot(t)
	st := openTestStore(t, root)
	defer st.Close()

	rec := store.Record{
		ID:    "doc/target",
		Kind:  store.KDoc,
		Scope: store.ScopeProject,
		Key:   "target",
		Body:  "to be deleted",
	}
	if err := st.Put(rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	recordStoreOp(root, "put", "ui", nil, &rec)

	// Remove the record.
	before, _ := st.Get(rec.ID)
	if err := st.Delete(rec.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	recordStoreOp(root, "delete", "ui", &before, nil)

	// Record must be gone from the store.
	if _, ok := st.Get(rec.ID); ok {
		t.Error("record still present after Delete")
	}

	// But the history journal must contain a delete entry with Before populated.
	revs := readRevisions(root)
	var deleteRev *storeRevision
	for i := range revs {
		if revs[i].Op == "delete" && revs[i].ID == rec.ID {
			deleteRev = &revs[i]
		}
	}
	if deleteRev == nil {
		t.Fatal("no delete revision in journal")
	}
	if deleteRev.Before == nil {
		t.Error("delete revision has nil Before — history is not intact")
	}
	if deleteRev.Before.Body != rec.Body {
		t.Errorf("Before.Body = %q, want %q", deleteRev.Before.Body, rec.Body)
	}
}

func TestStoreUndoRestores(t *testing.T) {
	root := mkRoot(t)
	st := openTestStore(t, root)
	defer st.Close()

	rec := store.Record{
		ID:    "doc/restore-me",
		Kind:  store.KDoc,
		Scope: store.ScopeProject,
		Key:   "restore me",
		Body:  "restore this body",
	}
	if err := st.Put(rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	recordStoreOp(root, "put", "ui", nil, &rec)

	// Delete.
	before, _ := st.Get(rec.ID)
	if err := st.Delete(rec.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	recordStoreOp(root, "delete", "ui", &before, nil)

	// Undo.
	_, ok := undoLastStore(root, st)
	if !ok {
		t.Fatal("undoLastStore returned ok=false")
	}

	// Record must be back.
	got, ok := st.Get(rec.ID)
	if !ok {
		t.Fatal("record not found after undo")
	}
	if got.Body != rec.Body {
		t.Errorf("restored Body = %q, want %q", got.Body, rec.Body)
	}
}

func TestGateListReflectsStore(t *testing.T) {
	root := mkRoot(t)
	st := openTestStore(t, root)
	defer st.Close()

	pattern := "internal/secret"
	id := "gate-rule/" + slug(pattern)
	rec := store.Record{ID: id, Kind: store.KGateRule, Scope: store.ScopeProject, Key: pattern, Body: pattern}
	if err := st.Put(rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	recordStoreOp(root, "put", "ui", nil, &rec)

	// gate list equivalent: List(OfKind KGateRule)
	rules := st.List(store.OfKind(store.KGateRule))
	if len(rules) != 1 {
		t.Fatalf("gate list: got %d rules, want 1", len(rules))
	}
	if rules[0].Body != pattern {
		t.Errorf("gate pattern = %q, want %q", rules[0].Body, pattern)
	}
}

func TestAgentCannotWriteGate(t *testing.T) {
	// agentWritableKind(KGateRule) must return false — this is the enforcement point.
	if agentWritableKind(store.KGateRule) {
		t.Error("agentWritableKind(KGateRule) = true — gate rules must be human-only")
	}
	// Also verify the commit path rejects it.
	_, err := parseKindForCommit("gate-rule", "agent")
	if err == nil {
		t.Error("parseKindForCommit(gate-rule, agent): expected error, got nil")
	}
}

func TestVerifyRunsClean(t *testing.T) {
	// Build a tiny temp project with one .go file so ParseDir succeeds.
	projDir := t.TempDir()
	src := "package p\n\nfunc Foo() {}\n"
	if err := os.WriteFile(filepath.Join(projDir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	proj, _, err := core.ParseDir(projDir)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}

	// Empty store → no rules.
	st := store.NewMem()
	rules := verify.RulesFromStore(st)

	violations := verify.Check(rules, proj)
	if len(violations) != 0 {
		t.Errorf("unexpected violations on empty-rule check: %+v", violations)
	}
}
