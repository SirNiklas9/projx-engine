package main

// Store versioning. Every store mutation — from the UI or the agent's `projx store`
// CLI — is appended to a per-project journal (.projx/store-history.jsonl), so you
// can read the full timeline and undo changes. The journal is append-only; an undo
// reverts a change AND records itself, so history is never lost.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	store "github.com/SirNiklas9/projx-store"
)

// storeRevision is one entry in the journal.
type storeRevision struct {
	Seq    int           `json:"seq"`
	Time   string        `json:"time"`
	Op     string        `json:"op"` // "put" | "delete" | "move" | "undo" | "revert" | "cherry-pick"
	ID     string        `json:"id"`
	Kind   string        `json:"kind"`
	Key    string        `json:"key"`
	By     string        `json:"by"`               // "ui" | "agent" | "cli"
	UndoOf int           `json:"undoOf,omitempty"` // for op=="undo": the seq reverted
	RefSeq int           `json:"refSeq,omitempty"` // for op=="revert"/"cherry-pick": the targeted seq
	Before *store.Record `json:"before,omitempty"`
	After  *store.Record `json:"after,omitempty"`
}

func journalPath(root string) string { return filepath.Join(root, ".projx", "store-history.jsonl") }

func readRevisions(root string) []storeRevision {
	b, err := os.ReadFile(journalPath(root))
	if err != nil {
		return nil
	}
	var out []storeRevision
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r storeRevision
		if json.Unmarshal([]byte(line), &r) == nil {
			out = append(out, r)
		}
	}
	return out
}

func appendRevision(root string, rev storeRevision) {
	if root == "" {
		return
	}
	_ = os.MkdirAll(filepath.Join(root, ".projx"), 0o755)
	f, err := os.OpenFile(journalPath(root), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(rev)
	_, _ = f.Write(append(b, '\n'))
}

// recordStoreOp journals a put/delete. before is the prior record (nil if new);
// after is the new record (nil for a delete). `by` is "ui" or "agent".
func recordStoreOp(root, op, by string, before, after *store.Record) {
	rev := storeRevision{Time: time.Now().UTC().Format(time.RFC3339), Op: op, By: by}
	rev.Seq = len(readRevisions(root)) + 1
	switch {
	case after != nil:
		rev.ID, rev.Kind, rev.Key = after.ID, after.Kind.String(), after.Key
	case before != nil:
		rev.ID, rev.Kind, rev.Key = before.ID, before.Kind.String(), before.Key
	}
	rev.Before, rev.After = before, after
	appendRevision(root, rev)
}

// undoLastStore reverts the most recent not-yet-undone change and records the undo.
// Returns the reverted revision (ok=false if there's nothing to undo).
func undoLastStore(root string, st store.Store) (storeRevision, bool) {
	revs := readRevisions(root)
	undone := map[int]bool{}
	for _, r := range revs {
		if r.Op == "undo" && r.UndoOf > 0 {
			undone[r.UndoOf] = true
		}
	}
	for i := len(revs) - 1; i >= 0; i-- {
		r := revs[i]
		if r.Op == "undo" || undone[r.Seq] {
			continue
		}
		// apply the inverse
		switch r.Op {
		case "put":
			if r.Before == nil {
				_ = st.Delete(r.ID)
			} else {
				_ = st.Put(*r.Before)
			}
		case "delete":
			if r.Before != nil {
				_ = st.Put(*r.Before)
			}
		case "move":
			// A move relocated the record between physical files (old scope → new
			// scope). The generic put-inverse can't express that — it would restore
			// the old copy but leave the relocated one behind. Undo precisely: drop
			// the relocated copy from its file, restore the original to its file. This
			// needs the per-file handles, so type-assert to *projectStore; fall back
			// to a best-effort restore if some other Store impl is ever passed.
			if pj, ok := st.(*projectStore); ok && r.Before != nil {
				if r.After != nil {
					_ = pj.physicalFor(r.After.Scope).Delete(r.ID)
				}
				_ = pj.physicalFor(r.Before.Scope).Put(*r.Before)
			} else if r.Before != nil {
				_ = st.Put(*r.Before)
			}
		}
		appendRevision(root, storeRevision{
			Seq: len(revs) + 1, Time: time.Now().UTC().Format(time.RFC3339),
			Op: "undo", UndoOf: r.Seq, ID: r.ID, Kind: r.Kind, Key: r.Key, By: "ui",
		})
		return r, true
	}
	return storeRevision{}, false
}

// agentWritableKind reports whether the AGENT may create/remove this kind. The
// gate-rule (security door) and settings (the key) are human-only — the AI can
// never touch them. The CLI enforces this; the UI is unrestricted (it's you).
func agentWritableKind(k store.Kind) bool {
	switch k {
	case store.KConvention, store.KADR, store.KDoc, store.KDeclaredStructure:
		return true
	default: // KGateRule, KRecipe, KHistory, settings → no
		return false
	}
}

// parseKindName maps a CLI kind name to the enum (error for unknown).
func parseKindName(name string) (store.Kind, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "convention":
		return store.KConvention, nil
	case "adr":
		return store.KADR, nil
	case "doc":
		return store.KDoc, nil
	case "declared-structure", "module":
		return store.KDeclaredStructure, nil
	case "gate-rule":
		return store.KGateRule, fmt.Errorf("gate-rule is human-only — the AI cannot set the gate")
	}
	return 0, fmt.Errorf("unknown or non-writable kind %q (allowed: convention, adr, doc, declared-structure)", name)
}
