package store

import (
	"errors"
	"testing"
)

// openSQLite opens an in-memory SQLite store for a test and registers its Close.
func openSQLite(t *testing.T) *SQLite {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSQLitePutGetDelete(t *testing.T) {
	s := openSQLite(t)
	defer s.Close()

	defer pinClock(1000)() // deterministic UpdatedAt stamp (Put stamps fresh writes)
	r := Record{ID: "r1", Kind: KRecipe, Scope: ScopeGlobal, Key: "commit", Body: "{}"}
	if err := s.Put(r); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok := s.Get("r1")
	if !ok || got.Key != "commit" {
		t.Fatalf("Get: ok=%v key=%q", ok, got.Key)
	}
	r.UpdatedAt = 1000 // Put stamps the write; expect it on round-trip
	if got != r {
		t.Errorf("Get round-trip: got %+v, want %+v", got, r)
	}
	if err := s.Delete("r1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := s.Get("r1"); ok {
		t.Error("record still present after Delete")
	}
}

func TestSQLitePutReplaces(t *testing.T) {
	s := openSQLite(t)
	defer s.Close()

	if err := s.Put(Record{ID: "x", Key: "first", Body: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Record{ID: "x", Key: "second", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get("x")
	if !ok || got.Key != "second" || got.Body != "b" {
		t.Errorf("Put did not replace by ID: got %+v", got)
	}
	if all := s.List(Filter{}); len(all) != 1 {
		t.Errorf("List after replace = %d records, want 1", len(all))
	}
}

func TestSQLiteLifecycleRoundTripAndFilter(t *testing.T) {
	s := openSQLite(t)
	r := Record{
		ID: "claim/new", Status: StatusCandidate, Supersedes: "claim/old",
		ClaimClass: "volatile-fact", VerifiedAt: 100, ReviewAfter: 200,
		Verifier: "live-source", Evidence: "test://result/1", Model: "model-x",
		Confidence: 85, Approval: "pending",
	}
	if err := s.Put(r); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get(r.ID)
	if !ok {
		t.Fatal("lifecycle record not found")
	}
	r.UpdatedAt = got.UpdatedAt
	if got != r {
		t.Fatalf("lifecycle round-trip: got %+v, want %+v", got, r)
	}
	if candidates := s.List(Filter{Status: StatusCandidate}); len(candidates) != 1 || candidates[0].ID != r.ID {
		t.Fatalf("candidate filter = %+v", candidates)
	}
	if active := s.List(Filter{Status: StatusActive}); len(active) != 0 {
		t.Fatalf("candidate leaked into active records: %+v", active)
	}
}

func TestSQLiteDeleteMissingIsNoOp(t *testing.T) {
	s := openSQLite(t)
	defer s.Close()

	if err := s.Delete("nope"); err != nil {
		t.Errorf("Delete(missing) = %v, want nil", err)
	}
}

func TestSQLitePutRejectsNoID(t *testing.T) {
	s := openSQLite(t)
	defer s.Close()

	if err := s.Put(Record{Key: "x"}); !errors.Is(err, ErrNoID) {
		t.Errorf("Put with no ID: err = %v, want ErrNoID", err)
	}
}

func TestSQLiteListFilters(t *testing.T) {
	s := openSQLite(t)
	defer s.Close()

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

func TestSQLiteListEmpty(t *testing.T) {
	s := openSQLite(t)
	defer s.Close()

	if got := s.List(Filter{}); len(got) != 0 {
		t.Errorf("List on empty store = %d records, want 0", len(got))
	}
}

func TestSQLiteOpenIsIdempotent(t *testing.T) {
	// Re-running migrations on a fresh DB must succeed (no version drift).
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := migrate(s.c); err != nil {
		t.Errorf("re-migrate: %v", err)
	}
	s.Close()
}
