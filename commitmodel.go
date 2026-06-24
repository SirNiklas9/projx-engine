package main

// commitmodel.go — git-like versioning on top of the store journal.
//
// Three new operations, each appending a NEW revision (history is immutable):
//
//   revert <seq>       — apply the INVERSE of a prior put/delete, git-revert style
//   cherry-pick <seq>  — re-apply the EFFECT of a prior put/delete onto current state
//   checkout <seq>     — read-only replay of revisions 1..seq into an in-memory store
//
// Only "put" and "delete" revisions are revertable/cherry-pickable.
// Meta-ops (undo/revert/cherry-pick) carry their Before/After; checkout
// replays all op types using the recorded After field.

import (
	"fmt"
	"time"

	store "github.com/SirNiklas9/projx-store"
)

// isDataOp reports whether op is a revertable/cherry-pickable data operation.
func isDataOp(op string) bool {
	return op == "put" || op == "delete"
}

// findRevision returns the revision with Seq==targetSeq, or an error.
func findRevision(revs []storeRevision, targetSeq int) (storeRevision, error) {
	for _, r := range revs {
		if r.Seq == targetSeq {
			return r, nil
		}
	}
	return storeRevision{}, fmt.Errorf("revision #%d not found in journal", targetSeq)
}

// revertRevision applies the INVERSE of revision targetSeq onto the live store,
// then appends a new "revert" revision. It never rewrites history.
//
// Semantics:
//   - target Op=="put"    && Before==nil → it was a create → Delete(id)
//   - target Op=="put"    && Before!=nil → it was an update → Put(*Before) to restore prior value
//   - target Op=="delete" && Before!=nil → it was a delete → Put(*Before) to reinstate record
//
// Returns the newly appended revision.
func revertRevision(root string, st store.Store, targetSeq int) (storeRevision, error) {
	revs := readRevisions(root)

	target, err := findRevision(revs, targetSeq)
	if err != nil {
		return storeRevision{}, err
	}
	if !isDataOp(target.Op) {
		return storeRevision{}, fmt.Errorf(
			"revision #%d has op %q — only \"put\" and \"delete\" are revertable (meta-ops cannot be reverted)",
			targetSeq, target.Op,
		)
	}

	// Snapshot the current state of the affected record (Before for the new rev).
	var beforeRevert *store.Record
	if cur, ok := st.Get(target.ID); ok {
		c := cur
		beforeRevert = &c
	}

	// Apply the inverse.
	switch target.Op {
	case "put":
		if target.Before == nil {
			// Was a create — undo by deleting.
			if err := st.Delete(target.ID); err != nil {
				return storeRevision{}, fmt.Errorf("revert #%d: delete: %w", targetSeq, err)
			}
		} else {
			// Was an update — restore prior value.
			if err := st.Put(*target.Before); err != nil {
				return storeRevision{}, fmt.Errorf("revert #%d: put prior: %w", targetSeq, err)
			}
		}
	case "delete":
		if target.Before == nil {
			return storeRevision{}, fmt.Errorf(
				"revert #%d: delete revision has no Before — cannot reinstate", targetSeq)
		}
		if err := st.Put(*target.Before); err != nil {
			return storeRevision{}, fmt.Errorf("revert #%d: put reinstated: %w", targetSeq, err)
		}
	}

	// Capture After state.
	var afterRevert *store.Record
	if cur, ok := st.Get(target.ID); ok {
		c := cur
		afterRevert = &c
	}

	newRev := storeRevision{
		Seq:    len(revs) + 1,
		Time:   time.Now().UTC().Format(time.RFC3339),
		Op:     "revert",
		RefSeq: targetSeq,
		ID:     target.ID,
		Kind:   target.Kind,
		Key:    target.Key,
		By:     "cli",
		Before: beforeRevert,
		After:  afterRevert,
	}
	appendRevision(root, newRev)
	return newRev, nil
}

// cherryPickRevision re-applies the EFFECT of revision targetSeq onto the
// current live store state, then appends a new "cherry-pick" revision.
//
// Semantics:
//   - target Op=="put"    → Put(*After) to re-apply the recorded new value
//   - target Op=="delete" → Delete(id) to re-apply the recorded deletion
//
// Returns the newly appended revision.
func cherryPickRevision(root string, st store.Store, targetSeq int) (storeRevision, error) {
	revs := readRevisions(root)

	target, err := findRevision(revs, targetSeq)
	if err != nil {
		return storeRevision{}, err
	}
	if !isDataOp(target.Op) {
		return storeRevision{}, fmt.Errorf(
			"revision #%d has op %q — only \"put\" and \"delete\" are cherry-pickable",
			targetSeq, target.Op,
		)
	}

	// Snapshot Before state.
	var beforePick *store.Record
	if cur, ok := st.Get(target.ID); ok {
		c := cur
		beforePick = &c
	}

	// Apply the original effect.
	switch target.Op {
	case "put":
		if target.After == nil {
			return storeRevision{}, fmt.Errorf(
				"cherry-pick #%d: put revision has no After — cannot re-apply", targetSeq)
		}
		if err := st.Put(*target.After); err != nil {
			return storeRevision{}, fmt.Errorf("cherry-pick #%d: put: %w", targetSeq, err)
		}
	case "delete":
		if err := st.Delete(target.ID); err != nil {
			return storeRevision{}, fmt.Errorf("cherry-pick #%d: delete: %w", targetSeq, err)
		}
	}

	// Capture After state.
	var afterPick *store.Record
	if cur, ok := st.Get(target.ID); ok {
		c := cur
		afterPick = &c
	}

	newRev := storeRevision{
		Seq:    len(revs) + 1,
		Time:   time.Now().UTC().Format(time.RFC3339),
		Op:     "cherry-pick",
		RefSeq: targetSeq,
		ID:     target.ID,
		Kind:   target.Kind,
		Key:    target.Key,
		By:     "cli",
		Before: beforePick,
		After:  afterPick,
	}
	appendRevision(root, newRev)
	return newRev, nil
}

// checkoutState replays revisions 1..uptoSeq in order into an in-memory store
// and returns the resulting records. This is READ-ONLY — it never touches the
// real store on disk.
//
// All op types are replayed using their recorded After field:
//   - After != nil → Put(*After) (record existed at that seq)
//   - After == nil → Delete(id) (record was absent at that seq)
//
// Returns an error if uptoSeq is out of range.
func checkoutState(root string, uptoSeq int) ([]store.Record, error) {
	revs := readRevisions(root)
	if len(revs) == 0 {
		return nil, fmt.Errorf("checkout: journal is empty")
	}
	maxSeq := revs[len(revs)-1].Seq
	if uptoSeq < 1 {
		return nil, fmt.Errorf("checkout: seq must be >= 1 (got %d)", uptoSeq)
	}
	if uptoSeq > maxSeq {
		return nil, fmt.Errorf("checkout: seq %d is beyond the latest revision #%d", uptoSeq, maxSeq)
	}

	mem := store.NewMem()
	for _, r := range revs {
		if r.Seq > uptoSeq {
			break
		}
		if r.After != nil {
			_ = mem.Put(*r.After)
		} else {
			// After==nil means the record was absent at this point (delete or revert-to-nil).
			_ = mem.Delete(r.ID)
		}
	}
	return store.ListAll(mem), nil
}
