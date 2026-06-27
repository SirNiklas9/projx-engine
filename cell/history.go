package main

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	store "github.com/SirNiklas9/projx-store"
)

// Store history — append-only journal at .projx/store-history.jsonl in the repo,
// written via storage.fs. Same format as the native engine's storehistory.go, so
// a native and a celled run share one history. Every put/delete appends a
// revision; undo inverts the most recent not-yet-undone op and records itself.

const journalPath = ".projx/store-history.jsonl"

type revision struct {
	Seq    int           `json:"seq"`
	Time   string        `json:"time"`
	Op     string        `json:"op"` // put | delete | undo
	ID     string        `json:"id"`
	Kind   string        `json:"kind"`
	Key    string        `json:"key"`
	By     string        `json:"by"`
	UndoOf int           `json:"undoOf,omitempty"`
	Before *store.Record `json:"before,omitempty"`
	After  *store.Record `json:"after,omitempty"`
}

func readRevisions() []revision {
	b, err := pulp.FS.Read(journalPath)
	if err != nil || len(b) == 0 {
		return nil
	}
	var revs []revision
	for _, ln := range strings.Split(string(b), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		var r revision
		if json.Unmarshal([]byte(ln), &r) == nil {
			revs = append(revs, r)
		}
	}
	return revs
}

func appendRevision(r revision) {
	revs := readRevisions()
	r.Seq = len(revs) + 1
	if r.Time == "" {
		r.Time = time.Now().UTC().Format(time.RFC3339)
	}
	line, err := json.Marshal(r)
	if err != nil {
		return
	}
	existing, _ := pulp.FS.Read(journalPath)
	buf := append(existing, line...)
	buf = append(buf, '\n')
	_ = pulp.FS.Write(journalPath, buf)
}

func recordStoreOp(op, id, kind, key string, before, after *store.Record) {
	appendRevision(revision{Op: op, ID: id, Kind: kind, Key: key, By: "ui", Before: before, After: after})
}

// undoLast inverts the most recent not-yet-undone change and records the undo.
func undoLast(s store.Store) (revision, bool) {
	revs := readRevisions()
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
		switch r.Op {
		case "put":
			if r.Before == nil {
				_ = s.Delete(r.ID)
			} else {
				_ = s.Put(*r.Before)
			}
		case "delete":
			if r.Before != nil {
				_ = s.Put(*r.Before)
			}
		}
		appendRevision(revision{Op: "undo", UndoOf: r.Seq, ID: r.ID, Kind: r.Kind, Key: r.Key})
		return r, true
	}
	return revision{}, false
}
