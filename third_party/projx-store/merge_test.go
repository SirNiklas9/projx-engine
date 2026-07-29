package store

import (
	"reflect"
	"testing"
)

func rec(id, body string, updated int64) Record {
	return Record{ID: id, Kind: KConvention, Scope: ScopeGlobal, Key: id, Body: body, UpdatedAt: updated}
}

func TestMergeUnionAndDedup(t *testing.T) {
	base := []Record{rec("a", "A", 10), rec("b", "B", 10)}
	incoming := []Record{rec("b", "B", 20), rec("c", "C", 5)} // b identical (newer ts), c new
	r := Merge(base, incoming)
	if r.Added != 1 || r.Unchanged != 1 || len(r.Conflicts) != 0 {
		t.Fatalf("want Added=1 Unchanged=1 conflicts=0, got Added=%d Unchanged=%d conflicts=%d", r.Added, r.Unchanged, len(r.Conflicts))
	}
	if len(r.Merged) != 3 {
		t.Fatalf("want 3 merged, got %d", len(r.Merged))
	}
	// identical-content dedup kept the newer-stamped copy (b@20)
	for _, m := range r.Merged {
		if m.ID == "b" && m.UpdatedAt != 20 {
			t.Fatalf("dedup should keep newer ts 20, got %d", m.UpdatedAt)
		}
	}
}

func TestMergeLWWAutoResolve(t *testing.T) {
	base := []Record{rec("x", "old-body", 10)}
	incoming := []Record{rec("x", "new-body", 20)} // same id, different body, newer
	r := Merge(base, incoming)
	if r.AutoWon != 1 || r.NeedReview != 0 || len(r.Conflicts) != 1 {
		t.Fatalf("want AutoWon=1 NeedReview=0 conflicts=1, got %+v", r)
	}
	if !r.Conflicts[0].Resolved() || r.Conflicts[0].Resolution != TookIncoming {
		t.Fatalf("want incoming to win, got %+v", r.Conflicts[0])
	}
	if r.Merged[0].Body != "new-body" {
		t.Fatalf("LWW should keep new-body, got %q", r.Merged[0].Body)
	}
	// reverse: base newer keeps base
	r2 := Merge([]Record{rec("x", "keep", 30)}, []Record{rec("x", "drop", 5)})
	if r2.Conflicts[0].Resolution != KeptBase || r2.Merged[0].Body != "keep" {
		t.Fatalf("want base to win, got %+v", r2)
	}
}

func TestMergeTieNeedsReview(t *testing.T) {
	base := []Record{rec("x", "mine", 0)}     // unknown ts
	incoming := []Record{rec("x", "theirs", 0)} // unknown ts -> tie
	r := Merge(base, incoming)
	if r.NeedReview != 1 || r.AutoWon != 0 {
		t.Fatalf("want NeedReview=1 AutoWon=0, got %+v", r)
	}
	c := r.Conflicts[0]
	if c.Resolved() {
		t.Fatalf("tie must be Unresolved")
	}
	// unresolved keeps base in Merged (no silent overwrite)
	if r.Merged[0].Body != "mine" {
		t.Fatalf("unresolved should keep base, got %q", r.Merged[0].Body)
	}
	// Apply picks incoming and bumps the clock
	defer pinClock(1234)()
	chosen := c.Apply(true)
	if chosen.Body != "theirs" || chosen.UpdatedAt != 1234 {
		t.Fatalf("Apply(true) should take incoming + stamp now, got %+v", chosen)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	recs := []Record{rec("a", "A", 1), rec("b", "B", 2)}
	blob, err := ExportJSON(recs)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ImportJSON(blob)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recs, got) {
		t.Fatalf("round-trip mismatch:\n want %+v\n got  %+v", recs, got)
	}
}

// pinClock pins nowMillis AND resets the monotonic counter so stamp() yields the pinned
// value first (deterministic); returns a restore func.
func pinClock(v int64) func() {
	prevFn, prevLast := nowMillis, lastStamp
	nowMillis = func() int64 { return v }
	lastStamp = 0
	return func() { nowMillis = prevFn; lastStamp = prevLast }
}
