package main

import (
	"testing"

	"github.com/SirNiklas9/projx-engine/internal/routing"
	store "github.com/SirNiklas9/projx-store"
)

// TestDecideWithStoreUsesTriage proves the engine forwards a triage func to the
// decider: an ambiguous task triggers the (here fake) triage and adopts its tier.
func TestDecideWithStoreUsesTriage(t *testing.T) {
	root := t.TempDir()
	st := openStore(root)
	defer st.Close()
	called := false
	d := routing.DecideWithStore(st, "handle the widget thing", routing.DefaultConfig(),
		func(string) (string, bool) { called = true; return "deep-reasoning", true })
	if !called {
		t.Error("triage not called for an ambiguous task")
	}
	if d.Class != "deep-reasoning" || d.Source != "triage" {
		t.Errorf("decision = %s/%s, want deep-reasoning/triage", d.Class, d.Source)
	}
}

// TestCanonTier covers the friendly alias resolution.
func TestCanonTier(t *testing.T) {
	cases := map[string]string{
		"opus": "deep-reasoning", "deep": "deep-reasoning", "DEEP-REASONING": "deep-reasoning",
		"haiku": "cheap-fast", "cheap": "cheap-fast",
		"sonnet": "default", "standard": "default",
		"nonsense": "",
	}
	for in, want := range cases {
		if got := canonTier(in); got != want {
			t.Errorf("canonTier(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRoutePinFlowsThroughDecider proves a pin written via the route command is read
// by the real store-backed decider: a cheap-looking task routes deep-reasoning/pin,
// and clearing the pin restores normal classification.
func TestRoutePinFlowsThroughDecider(t *testing.T) {
	root := t.TempDir()

	// Pin to deep-reasoning, then route a task that would normally be cheap-fast.
	routeSetTier(root, store.SettingRoutePin, "pin", []string{"opus"})

	st := openStore(root)
	d := routing.DecideWithStore(st, "rename a variable", routing.DefaultConfig(), nil)
	st.Close()
	if d.Class != "deep-reasoning" || d.Source != "pin" {
		t.Fatalf("pinned decision = %s/%s, want deep-reasoning/pin", d.Class, d.Source)
	}

	// Clear the pin → the keyword classifier takes over again (rename → cheap-fast).
	routeClear(root, []string{"pin"})
	st = openStore(root)
	d = routing.DecideWithStore(st, "rename a variable", routing.DefaultConfig(), nil)
	st.Close()
	if d.Class != "cheap-fast" {
		t.Fatalf("after clear, decision = %s, want cheap-fast", d.Class)
	}
}

// TestRouteFloorRaisesDeterministicOp confirms a deterministic-OP task is untouched by
// the floor (the floor only governs the agent-tier path), while an agent task is raised.
func TestRouteFloorRaisesDeterministicOp(t *testing.T) {
	root := t.TempDir()
	routeSetTier(root, store.SettingRouteFloor, "floor", []string{"default"})

	st := openStore(root)
	defer st.Close()
	// Deterministic op still routes to verify (no tier involved).
	if d := routing.DecideWithStore(st, "verify the boundaries", routing.DefaultConfig(), nil); d.Kind != "deterministic" || d.Op != "verify" {
		t.Errorf("verify task = %s/%s, want deterministic/verify", d.Kind, d.Op)
	}
	// Agent task that classifies cheap-fast is raised to the floor.
	if d := routing.DecideWithStore(st, "fix a typo", routing.DefaultConfig(), nil); d.Class != "default" {
		t.Errorf("floored typo task = %s, want default", d.Class)
	}
}
