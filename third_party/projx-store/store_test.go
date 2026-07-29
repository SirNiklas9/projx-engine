package store

import (
	"errors"
	"testing"
)

func TestPutGetDelete(t *testing.T) {
	s := NewMem()
	r := Record{ID: "r1", Kind: KRecipe, Scope: ScopeGlobal, Key: "commit", Body: "{}"}
	if err := s.Put(r); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok := s.Get("r1")
	if !ok || got.Key != "commit" {
		t.Fatalf("Get: ok=%v key=%q", ok, got.Key)
	}
	if err := s.Delete("r1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := s.Get("r1"); ok {
		t.Error("record still present after Delete")
	}
}

func TestPutRejectsNoID(t *testing.T) {
	s := NewMem()
	if err := s.Put(Record{Key: "x"}); !errors.Is(err, ErrNoID) {
		t.Errorf("Put with no ID: err = %v, want ErrNoID", err)
	}
}

func TestListFilters(t *testing.T) {
	s := NewMem()
	must := func(r Record) {
		if err := s.Put(r); err != nil {
			t.Fatal(err)
		}
	}
	must(Record{ID: "a", Kind: KRecipe, Scope: ScopeGlobal})
	must(Record{ID: "b", Kind: KConvention, Scope: ScopeGlobal})
	must(Record{ID: "c", Kind: KADR, Scope: ScopeProject})
	must(Record{ID: "d", Kind: KGateRule, Scope: ScopeProject})

	if all := s.List(Filter{}); len(all) != 4 {
		t.Errorf("List(all) = %d records, want 4", len(all))
	}
	if proj := s.List(InScope(ScopeProject)); len(proj) != 2 {
		t.Errorf("List(project) = %d, want 2", len(proj))
	}
	if recipes := s.List(OfKind(KRecipe)); len(recipes) != 1 || recipes[0].ID != "a" {
		t.Errorf("List(recipe) = %v, want [a]", recipes)
	}
	// Combined filter: project-scoped ADRs only.
	sc, kd := ScopeProject, KADR
	if combo := s.List(Filter{Scope: &sc, Kind: &kd}); len(combo) != 1 || combo[0].ID != "c" {
		t.Errorf("List(project+adr) = %v, want [c]", combo)
	}
	// Deterministic ordering by ID.
	got := s.List(Filter{})
	for i := 1; i < len(got); i++ {
		if got[i-1].ID > got[i].ID {
			t.Errorf("List not sorted by ID: %q before %q", got[i-1].ID, got[i].ID)
		}
	}
}

func TestScopeOwner(t *testing.T) {
	cases := map[Scope]string{
		ScopeGlobal:    "global",
		ScopeWorkspace: "workspace",
		ScopeProject:   "project",
	}
	for sc, want := range cases {
		if got := sc.Owner(); got != want {
			t.Errorf("%v.Owner() = %q, want %q", sc, got, want)
		}
	}
}

func TestLifecycleStatusAndFilter(t *testing.T) {
	s := NewMem()
	for _, r := range []Record{
		{ID: "legacy"},
		{ID: "candidate", Status: StatusCandidate, Evidence: "agent observation", Model: "model-x", Confidence: 70},
		{ID: "rejected", Status: StatusRejected},
	} {
		if err := s.Put(r); err != nil {
			t.Fatal(err)
		}
	}
	got := s.List(Filter{})
	if len(got) != 1 || got[0].ID != "legacy" || !got[0].Authoritative() {
		t.Fatalf("active lifecycle filter = %+v", got)
	}
	if all := s.List(Filter{IncludeNonAuthoritative: true}); len(all) != 3 {
		t.Fatalf("audit lifecycle view = %+v", all)
	}
	if rejected := s.List(Filter{Status: StatusRejected}); len(rejected) != 1 || rejected[0].ID != "rejected" {
		t.Fatalf("explicit rejected view = %+v", rejected)
	}
}

func TestLifecycleFreshnessEvaluator(t *testing.T) {
	legacy := Record{}
	if !legacy.FreshAt(1000) || legacy.FreshnessAt(1000) != FreshnessFresh {
		t.Fatalf("legacy active record should remain fresh: %+v", legacy)
	}
	due := Record{ReviewAfter: 900}
	if !due.ReviewDueAt(1000) || due.FreshAt(1000) || due.FreshnessAt(1000) != FreshnessReviewDue {
		t.Fatalf("review-due evaluation wrong: %+v", due)
	}
	candidate := Record{Status: StatusCandidate, ReviewAfter: 900}
	if candidate.ReviewDueAt(1000) || candidate.FreshAt(1000) || candidate.Authoritative() {
		t.Fatalf("candidate must never become authoritative through freshness: %+v", candidate)
	}
}
