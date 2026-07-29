package verify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	core "github.com/SirNiklas9/projx-core"
	store "github.com/SirNiklas9/projx-store"
)

// mkproj builds a 2-file project where b.go::Outsider calls a.go::Helper — a
// resolved CROSS-FILE call that a boundary rule can forbid.
func mkproj(t *testing.T) *core.Project {
	t.Helper()
	dir := t.TempDir()
	must := func(name, src string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("a.go", "package p\n\nfunc Use() { Helper() }\n\nfunc Helper() {}\n")
	must("b.go", "package p\n\nfunc Outsider() { Helper() }\n")
	p, _, err := core.ParseDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCheckFindsViolation(t *testing.T) {
	p := mkproj(t)
	rules := []Rule{{From: "b.go", To: "a.go", Note: "ADR-1: b must not depend on a"}}
	vs := Check(rules, p)
	if len(vs) != 1 {
		t.Fatalf("violations = %d, want 1 (%+v)", len(vs), vs)
	}
	if vs[0].Edge.From != "b.go::Outsider" || vs[0].Edge.To != "a.go::Helper" {
		t.Errorf("violation edge = %+v, want b.go::Outsider → a.go::Helper", vs[0].Edge)
	}
	if vs[0].Rule.Note == "" {
		t.Error("violation lost the rule note (the ADR reason)")
	}
}

func TestCheckNoViolation(t *testing.T) {
	p := mkproj(t)
	// The intra-file call a.go::Use → a.go::Helper is fine; no rule forbids it.
	if vs := Check([]Rule{{From: "b.go", To: "nonexistent"}}, p); len(vs) != 0 {
		t.Errorf("expected no violations, got %+v", vs)
	}
}

// TestIdMatchesSeparator guards the separator-anchored prefix rule: a short pattern
// must not false-positive against a longer file name that happens to start with the
// same characters (e.g. pattern "b" must not match "b.go::Outsider").
func TestIdMatchesSeparator(t *testing.T) {
	cases := []struct {
		id, pattern string
		want        bool
	}{
		{"b.go::Outsider", "b.go", true},      // file prefix -> matches
		{"b.go::Outsider", "b.go::Outsider", true}, // exact match
		{"b.go::Outsider", "b", false},         // bare letter must NOT match file name
		{"b.go::Outsider", "b.go::Out", false}, // mid-symbol prefix must NOT match
		{"pkg/b.go::F", "pkg", true},           // directory prefix -> matches (next char '/')
		{"pkg/b.go::F", "pk", false},           // partial dir must NOT match
	}
	for _, c := range cases {
		if got := idMatches(c.id, c.pattern); got != c.want {
			t.Errorf("idMatches(%q, %q) = %v, want %v", c.id, c.pattern, got, c.want)
		}
	}
}

// The triangle in one test: a rule DECLARED in the store, checked against the
// ACTUAL code, produces the violation.
func TestRulesFromStoreFullFlow(t *testing.T) {
	s := store.NewMem()
	body, _ := json.Marshal(Rule{From: "b.go", To: "a.go", Note: "ADR-1"})
	if err := s.Put(store.Record{
		ID: "rule-1", Kind: store.KDeclaredStructure, Scope: store.ScopeProject,
		Key: "b-must-not-use-a", Body: string(body),
	}); err != nil {
		t.Fatal(err)
	}

	rules := RulesFromStore(s)
	if len(rules) != 1 || rules[0].From != "b.go" || rules[0].To != "a.go" {
		t.Fatalf("RulesFromStore = %+v, want one b.go→a.go rule", rules)
	}

	vs := Check(rules, mkproj(t))
	if len(vs) != 1 {
		t.Errorf("store-declared rule produced %d violations, want 1", len(vs))
	}
}
