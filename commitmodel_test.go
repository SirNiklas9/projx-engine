package main

import (
	"testing"

	store "github.com/SirNiklas9/projx-store"
)

// commitPut is a helper that does Put + recordStoreOp, mirroring storeCommit.
// Returns the seq of the recorded revision.
func commitPut(t *testing.T, root string, st store.Store, rec store.Record, by string) int {
	t.Helper()
	var bp *store.Record
	if before, had := st.Get(rec.ID); had {
		cp := before
		bp = &cp
	}
	if err := st.Put(rec); err != nil {
		t.Fatalf("commitPut Put: %v", err)
	}
	recordStoreOp(root, "put", by, bp, &rec)
	revs := readRevisions(root)
	return revs[len(revs)-1].Seq
}

// commitDel is a helper that does Delete + recordStoreOp, mirroring storeRm.
// Returns the seq of the recorded revision.
func commitDel(t *testing.T, root string, st store.Store, id string, by string) int {
	t.Helper()
	before, had := st.Get(id)
	if !had {
		t.Fatalf("commitDel: record %q not found", id)
	}
	if err := st.Delete(id); err != nil {
		t.Fatalf("commitDel Delete: %v", err)
	}
	recordStoreOp(root, "delete", by, &before, nil)
	revs := readRevisions(root)
	return revs[len(revs)-1].Seq
}

// TestRevertRestoresPriorValue: put v1, put v2, revert v2 → store should hold v1.
// Journal must have put/put/revert with all history intact.
func TestRevertRestoresPriorValue(t *testing.T) {
	root := mkRoot(t)
	st := openTestStore(t, root)
	defer st.Close()

	rec1 := store.Record{ID: "doc/k", Kind: store.KDoc, Scope: store.ScopeProject, Key: "K", Body: "v1"}
	commitPut(t, root, st, rec1, "ui")

	rec2 := store.Record{ID: "doc/k", Kind: store.KDoc, Scope: store.ScopeProject, Key: "K", Body: "v2"}
	seqV2 := commitPut(t, root, st, rec2, "ui")

	// Verify we're on v2 before the revert.
	got, ok := st.Get("doc/k")
	if !ok || got.Body != "v2" {
		t.Fatalf("pre-revert: want v2, got %q ok=%v", got.Body, ok)
	}

	newRev, err := revertRevision(root, st, seqV2)
	if err != nil {
		t.Fatalf("revertRevision: %v", err)
	}

	// Store should now hold v1.
	got, ok = st.Get("doc/k")
	if !ok {
		t.Fatal("after revert: record not found")
	}
	if got.Body != "v1" {
		t.Errorf("after revert: Body = %q, want %q", got.Body, "v1")
	}

	// New revision must be recorded correctly.
	if newRev.Op != "revert" {
		t.Errorf("new rev Op = %q, want \"revert\"", newRev.Op)
	}
	if newRev.RefSeq != seqV2 {
		t.Errorf("new rev RefSeq = %d, want %d", newRev.RefSeq, seqV2)
	}

	// Journal must have put/put/revert — 3 entries, history intact.
	revs := readRevisions(root)
	if len(revs) != 3 {
		t.Fatalf("journal length = %d, want 3", len(revs))
	}
	ops := []string{revs[0].Op, revs[1].Op, revs[2].Op}
	want := []string{"put", "put", "revert"}
	for i, op := range ops {
		if op != want[i] {
			t.Errorf("revs[%d].Op = %q, want %q", i, op, want[i])
		}
	}
}

// TestRevertReinstatesDeleted: put K, rm K, revert the delete → K restored.
func TestRevertReinstatesDeleted(t *testing.T) {
	root := mkRoot(t)
	st := openTestStore(t, root)
	defer st.Close()

	rec := store.Record{ID: "doc/del-me", Kind: store.KDoc, Scope: store.ScopeProject, Key: "del-me", Body: "alive"}
	commitPut(t, root, st, rec, "ui")
	seqDel := commitDel(t, root, st, "doc/del-me", "ui")

	// Confirm it's gone.
	if _, ok := st.Get("doc/del-me"); ok {
		t.Fatal("pre-revert: record should be deleted")
	}

	_, err := revertRevision(root, st, seqDel)
	if err != nil {
		t.Fatalf("revertRevision: %v", err)
	}

	// Record must be back.
	got, ok := st.Get("doc/del-me")
	if !ok {
		t.Fatal("after revert: record not found")
	}
	if got.Body != "alive" {
		t.Errorf("after revert: Body = %q, want %q", got.Body, "alive")
	}
}

// TestCherryPickReappliesChange: put v1, put v2, revert→v1, cherry-pick v2 → back to v2.
func TestCherryPickReappliesChange(t *testing.T) {
	root := mkRoot(t)
	st := openTestStore(t, root)
	defer st.Close()

	rec1 := store.Record{ID: "doc/cp", Kind: store.KDoc, Scope: store.ScopeProject, Key: "cp", Body: "v1"}
	commitPut(t, root, st, rec1, "ui")

	rec2 := store.Record{ID: "doc/cp", Kind: store.KDoc, Scope: store.ScopeProject, Key: "cp", Body: "v2"}
	seqV2 := commitPut(t, root, st, rec2, "ui")

	// Revert to v1.
	if _, err := revertRevision(root, st, seqV2); err != nil {
		t.Fatalf("revertRevision: %v", err)
	}
	got, _ := st.Get("doc/cp")
	if got.Body != "v1" {
		t.Fatalf("after revert: want v1, got %q", got.Body)
	}

	// Cherry-pick the v2 revision → should re-apply v2.
	newRev, err := cherryPickRevision(root, st, seqV2)
	if err != nil {
		t.Fatalf("cherryPickRevision: %v", err)
	}

	got, ok := st.Get("doc/cp")
	if !ok {
		t.Fatal("after cherry-pick: record not found")
	}
	if got.Body != "v2" {
		t.Errorf("after cherry-pick: Body = %q, want %q", got.Body, "v2")
	}

	if newRev.Op != "cherry-pick" {
		t.Errorf("new rev Op = %q, want \"cherry-pick\"", newRev.Op)
	}
	if newRev.RefSeq != seqV2 {
		t.Errorf("new rev RefSeq = %d, want %d", newRev.RefSeq, seqV2)
	}
}

// TestCheckoutIsHistoricalAndReadOnly: puts A and B, removes A, then:
//   - checkout at seq when both existed → A and B present
//   - checkout at latest seq → only B
//   - real store unchanged (only B), not mutated by checkout
func TestCheckoutIsHistoricalAndReadOnly(t *testing.T) {
	root := mkRoot(t)
	st := openTestStore(t, root)
	defer st.Close()

	recA := store.Record{ID: "doc/a", Kind: store.KDoc, Scope: store.ScopeProject, Key: "A", Body: "alpha"}
	recB := store.Record{ID: "doc/b", Kind: store.KDoc, Scope: store.ScopeProject, Key: "B", Body: "beta"}

	commitPut(t, root, st, recA, "ui")
	seqBoth := commitPut(t, root, st, recB, "ui") // both A and B exist here
	seqDel := commitDel(t, root, st, "doc/a", "ui")

	// checkout at the seq where both existed.
	historical, err := checkoutState(root, seqBoth)
	if err != nil {
		t.Fatalf("checkoutState(%d): %v", seqBoth, err)
	}
	hasA, hasB := false, false
	for _, r := range historical {
		switch r.ID {
		case "doc/a":
			hasA = true
		case "doc/b":
			hasB = true
		}
	}
	if !hasA {
		t.Errorf("checkout at #%d: A not present (want both A and B)", seqBoth)
	}
	if !hasB {
		t.Errorf("checkout at #%d: B not present (want both A and B)", seqBoth)
	}

	// checkout at the latest seq → only B.
	latest, err := checkoutState(root, seqDel)
	if err != nil {
		t.Fatalf("checkoutState(%d): %v", seqDel, err)
	}
	for _, r := range latest {
		if r.ID == "doc/a" {
			t.Errorf("checkout at latest: A should be absent (was deleted at #%d)", seqDel)
		}
	}
	foundB := false
	for _, r := range latest {
		if r.ID == "doc/b" {
			foundB = true
		}
	}
	if !foundB {
		t.Errorf("checkout at latest: B should be present")
	}

	// The REAL store must still reflect the actual state (only B).
	if _, ok := st.Get("doc/a"); ok {
		t.Error("real store: A should be deleted, but found it — checkout mutated the live store")
	}
	if _, ok := st.Get("doc/b"); !ok {
		t.Error("real store: B should still exist")
	}
}

// TestRevertRejectsMetaOp: attempting to revert an undo or revert revision returns an error.
func TestRevertRejectsMetaOp(t *testing.T) {
	root := mkRoot(t)
	st := openTestStore(t, root)
	defer st.Close()

	// Build: put, delete, undo-the-delete.
	rec := store.Record{ID: "doc/meta", Kind: store.KDoc, Scope: store.ScopeProject, Key: "meta", Body: "body"}
	commitPut(t, root, st, rec, "ui")
	commitDel(t, root, st, "doc/meta", "ui")

	// undo (restores the record).
	_, ok := undoLastStore(root, st)
	if !ok {
		t.Fatal("undoLastStore: expected ok=true")
	}

	// Now journal has put/delete/undo (seqs 1,2,3).
	revs := readRevisions(root)
	var undoSeq int
	for _, r := range revs {
		if r.Op == "undo" {
			undoSeq = r.Seq
		}
	}
	if undoSeq == 0 {
		t.Fatal("no undo revision found in journal")
	}

	// Attempting to revert the undo revision must fail with a clear error.
	_, err := revertRevision(root, st, undoSeq)
	if err == nil {
		t.Errorf("revertRevision on undo seq #%d: expected error, got nil", undoSeq)
	}

	// Similarly, put a revert in the journal and try to revert that.
	putSeq := 1 // the original "put"
	newRev, err := revertRevision(root, st, putSeq)
	if err != nil {
		t.Fatalf("revertRevision(put seq): unexpected error: %v", err)
	}
	// Now try to revert the revert revision.
	_, err = revertRevision(root, st, newRev.Seq)
	if err == nil {
		t.Errorf("revertRevision on revert seq #%d: expected error, got nil", newRev.Seq)
	}
}
