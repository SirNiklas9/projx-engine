package store

import "testing"

func TestWorkspaceRouting(t *testing.T) {
	yours, project := NewMem(), NewMem()
	w := NewWorkspace(yours, project)

	must := func(r Record) {
		if err := w.Put(r); err != nil {
			t.Fatalf("Put %q: %v", r.ID, err)
		}
	}
	must(Record{ID: "g1", Kind: KRecipe, Scope: ScopeGlobal, Key: "commit"})
	must(Record{ID: "w1", Kind: KConvention, Scope: ScopeWorkspace, Key: "gate"})
	must(Record{ID: "p1", Kind: KADR, Scope: ScopeProject, Key: "adr-001"})

	// The project ADR lands in the project store only.
	if _, ok := project.Get("p1"); !ok {
		t.Error("ADR not routed to project store")
	}
	if _, ok := yours.Get("p1"); ok {
		t.Error("ADR leaked into yours store")
	}
	// Global + workspace records land in yours only.
	for _, id := range []string{"g1", "w1"} {
		if _, ok := yours.Get(id); !ok {
			t.Errorf("%q not routed to yours store", id)
		}
		if _, ok := project.Get(id); ok {
			t.Errorf("%q leaked into project store", id)
		}
	}
}

func TestCompositeThreeLevels(t *testing.T) {
	global, space, project := NewMem(), NewMem(), NewMem()
	w := NewComposite(global, space, project)

	_ = w.Put(Record{ID: "g1", Kind: KRecipe, Scope: ScopeGlobal})
	_ = w.Put(Record{ID: "w1", Kind: KConvention, Scope: ScopeWorkspace})
	_ = w.Put(Record{ID: "p1", Kind: KADR, Scope: ScopeProject})

	// Each scope lands in its OWN level's store — no collapsing global+workspace.
	if _, ok := global.Get("g1"); !ok {
		t.Error("global record not in global store")
	}
	if _, ok := space.Get("w1"); !ok {
		t.Error("workspace record not in workspace store")
	}
	if _, ok := project.Get("p1"); !ok {
		t.Error("project record not in project store")
	}
	if _, ok := global.Get("w1"); ok {
		t.Error("workspace record leaked into global store")
	}
	// Unscoped List composes all three levels.
	if got := len(w.List(Filter{})); got != 3 {
		t.Errorf("composed List = %d records, want 3", got)
	}
	// A scope-pinned List hits only the owning level.
	sc := ScopeWorkspace
	if got := w.List(Filter{Scope: &sc}); len(got) != 1 || got[0].ID != "w1" {
		t.Errorf("workspace-pinned List = %v, want [w1]", got)
	}
}

// TestCompositeProjectOnly: with no workspace store, workspace-scoped writes fall back
// UP to global — so a bare project (no workspace) just works.
func TestCompositeProjectOnly(t *testing.T) {
	global, project := NewMem(), NewMem()
	w := NewComposite(global, nil, project)

	_ = w.Put(Record{ID: "w1", Kind: KConvention, Scope: ScopeWorkspace})
	if _, ok := global.Get("w1"); !ok {
		t.Error("with no workspace store, workspace record should fall back to global")
	}
	_ = w.Put(Record{ID: "p1", Kind: KADR, Scope: ScopeProject})
	if got := len(w.List(Filter{})); got != 2 {
		t.Errorf("project-only composed List = %d, want 2", got)
	}
}

func TestWorkspaceGetSpansBothStores(t *testing.T) {
	w := NewWorkspace(NewMem(), NewMem())
	_ = w.Put(Record{ID: "g1", Kind: KRecipe, Scope: ScopeGlobal})
	_ = w.Put(Record{ID: "p1", Kind: KADR, Scope: ScopeProject})

	if _, ok := w.Get("g1"); !ok {
		t.Error("Get could not find a yours-store record")
	}
	if _, ok := w.Get("p1"); !ok {
		t.Error("Get could not find a project-store record")
	}
	if _, ok := w.Get("missing"); ok {
		t.Error("Get found a record that does not exist")
	}
}

func TestWorkspaceList(t *testing.T) {
	w := NewWorkspace(NewMem(), NewMem())
	_ = w.Put(Record{ID: "a", Kind: KRecipe, Scope: ScopeGlobal})
	_ = w.Put(Record{ID: "c", Kind: KConvention, Scope: ScopeWorkspace})
	_ = w.Put(Record{ID: "b", Kind: KADR, Scope: ScopeProject})

	// Scoped query hits only the owning store.
	proj := w.List(InScope(ScopeProject))
	if len(proj) != 1 || proj[0].ID != "b" {
		t.Errorf("List(project) = %v, want [b]", proj)
	}
	// Unfiltered query merges both stores, sorted by ID.
	all := w.List(Filter{})
	if len(all) != 3 {
		t.Fatalf("List(all) = %d records, want 3", len(all))
	}
	for i, want := range []string{"a", "b", "c"} {
		if all[i].ID != want {
			t.Errorf("List(all)[%d].ID = %q, want %q", i, all[i].ID, want)
		}
	}
}

func TestWorkspaceDeleteSpansBothStores(t *testing.T) {
	yours, project := NewMem(), NewMem()
	w := NewWorkspace(yours, project)
	_ = w.Put(Record{ID: "g1", Kind: KRecipe, Scope: ScopeGlobal})
	_ = w.Put(Record{ID: "p1", Kind: KADR, Scope: ScopeProject})

	if err := w.Delete("g1"); err != nil {
		t.Fatalf("Delete g1: %v", err)
	}
	if err := w.Delete("p1"); err != nil {
		t.Fatalf("Delete p1: %v", err)
	}
	if _, ok := yours.Get("g1"); ok {
		t.Error("g1 still present in yours after Delete")
	}
	if _, ok := project.Get("p1"); ok {
		t.Error("p1 still present in project after Delete")
	}
}
