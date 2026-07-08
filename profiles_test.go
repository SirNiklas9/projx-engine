package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	store "github.com/SirNiklas9/projx-store"
)

// TestSeedFloorAndGo proves the seed gives a fresh project a working baseline:
// floor + go records land, the steering + gates show up in the live preamble,
// and routing.json / cage.json are written with the right tiers and hosts.
func TestSeedFloorAndGo(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(root, ".projx", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	n, err := Seed(st, root, []string{"go"})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("seeded 0 records")
	}

	// Project floor is now project-specific only (dispatch-don't-mutate) + the go stack;
	// universal conventions + off-limits gates live at GLOBAL scope and inherit down, so a
	// bare project store no longer carries them.
	convs := st.List(store.OfKind(store.KConvention))
	if len(convs) < 2 { // dispatch-don't-mutate + at least one go-stack convention
		t.Fatalf("expected >=2 conventions, got %d", len(convs))
	}
	foundGo := false
	for _, c := range convs {
		if strings.Contains(c.Body, "GOWORK=off") {
			foundGo = true
		}
	}
	if !foundGo {
		t.Error("go stack convention missing")
	}

	// The project-scope steering is live in the agent preamble.
	pre := compileStorePreamble(st)
	if !strings.Contains(pre, "DISPATCHER") {
		t.Error("dispatch-don't-mutate steering not in preamble")
	}

	// A project may declare its OWN gate; it must render (project-scope gates compound on
	// top of the inherited global ones).
	_ = st.Put(store.Record{ID: "gate-rule/proj-secret", Kind: store.KGateRule,
		Scope: store.ScopeProject, Key: "project secrets", Body: "secret/**"})
	if !strings.Contains(compileStorePreamble(st), "secret/**") {
		t.Error("project-declared gate rule not in preamble")
	}

	// routing.json carries the three model tiers.
	rj := readFile(t, filepath.Join(root, ".projx", "routing.json"))
	for _, m := range []string{"claude-haiku", "claude-sonnet-4-6", "claude-opus-4-8"} {
		if !strings.Contains(rj, m) {
			t.Errorf("routing.json missing model %s", m)
		}
	}

	// cage.json carries floor + go egress hosts and tools.
	cj := readFile(t, filepath.Join(root, ".projx", "cage.json"))
	for _, want := range []string{"api.anthropic.com", "proxy.golang.org", "gofmt"} {
		if !strings.Contains(cj, want) {
			t.Errorf("cage.json missing %s", want)
		}
	}

	// Unknown profiles are rejected.
	if _, err := Seed(st, root, []string{"cobol"}); err == nil {
		t.Error("expected error for unknown profile")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestCageConfigWiring proves the seeded cage.json flows into the agent launch's
// allowlists: profile hosts/tools appear, flags extend, duplicates collapse, and
// an un-seeded project yields an empty (non-panicking) config.
func TestCageConfigWiring(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(root, ".projx", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := Seed(st, root, []string{"go"}); err != nil {
		t.Fatal(err)
	}

	hosts, bins := mergeAllowlists(loadCageConfig(root), []string{"extra.host"}, []string{"git"})
	for _, h := range []string{"api.anthropic.com", "proxy.golang.org", "extra.host"} {
		if !contains(hosts, h) {
			t.Errorf("hosts missing %s: %v", h, hosts)
		}
	}
	for _, b := range []string{"go", "gofmt"} {
		if !contains(bins, b) {
			t.Errorf("bins missing %s: %v", b, bins)
		}
	}
	if n := countStr(bins, "git"); n != 1 {
		t.Errorf("git not deduped (%d): %v", n, bins)
	}

	if empty := loadCageConfig(t.TempDir()); len(empty.NetAllow) != 0 || len(empty.Tools) != 0 {
		t.Error("absent cage.json should yield an empty config")
	}
}

// TestAutoSeed proves a fresh project self-seeds (floor + detected stack) and is
// idempotent — so nobody ever has to run `store seed`.
func TestAutoSeed(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	count := func() int {
		st, err := store.Open(filepath.Join(root, ".projx", "store.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		return len(st.List(store.Filter{}))
	}

	autoSeed(root) // fresh → floor + go
	n1 := count()
	if n1 < 9 { // 4 floor conventions + 4 gates + 1 go convention
		t.Fatalf("auto-seed should populate a fresh store, got %d records", n1)
	}
	if cj, err := os.ReadFile(filepath.Join(root, ".projx", "cage.json")); err != nil || !strings.Contains(string(cj), "proxy.golang.org") {
		t.Errorf("go stack not auto-detected into cage.json: %v", err)
	}

	autoSeed(root) // already seeded → must be a no-op
	if n2 := count(); n2 != n1 {
		t.Errorf("auto-seed not idempotent: %d -> %d records", n1, n2)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func countStr(s []string, v string) int {
	n := 0
	for _, x := range s {
		if x == v {
			n++
		}
	}
	return n
}
