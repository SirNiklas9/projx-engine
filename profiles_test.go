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

	n, err := Seed(st, root, []string{"go"})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("seeded 0 records")
	}

	convs := st.List(store.OfKind(store.KConvention))
	if len(convs) < 5 { // 4 floor + 1 go
		t.Fatalf("expected >=5 conventions, got %d", len(convs))
	}
	if gates := st.List(store.OfKind(store.KGateRule)); len(gates) < 4 {
		t.Fatalf("expected >=4 gate rules, got %d", len(gates))
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

	// The steering + gates are live in the agent preamble.
	pre := compileStorePreamble(st)
	if !strings.Contains(pre, "secret/**") {
		t.Error("gate rule not in preamble")
	}
	if !strings.Contains(pre, "Read this store contract first") {
		t.Error("floor steering not in preamble")
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
