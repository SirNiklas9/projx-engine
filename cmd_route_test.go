package main

import (
	"os"
	"path/filepath"
	"strings"
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

func TestRouteClearPinAndFloorDeleteOnlyProjectScope(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "global"))
	st := openStore(root)
	if err := st.physicalFor(store.ScopeGlobal).Put(store.Record{
		ID: store.SettingRoutePin, Key: store.SettingRoutePin,
		Kind: store.KRoute, Scope: store.ScopeGlobal, Body: "default",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.physicalFor(store.ScopeGlobal).Put(store.Record{
		ID: store.SettingRouteFloor, Key: store.SettingRouteFloor,
		Kind: store.KRoute, Scope: store.ScopeGlobal, Body: "cheap-fast",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.physicalFor(store.ScopeProject).Put(store.Record{
		ID: store.SettingRoutePin, Key: store.SettingRoutePin,
		Kind: store.KRoute, Scope: store.ScopeProject, Body: "deep-reasoning",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.physicalFor(store.ScopeProject).Put(store.Record{
		ID: store.SettingRouteFloor, Key: store.SettingRouteFloor,
		Kind: store.KRoute, Scope: store.ScopeProject, Body: "default",
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	routeClear(root, []string{"pin"})
	routeClear(root, []string{"floor"})

	st = openStore(root)
	defer st.Close()
	if _, ok := st.physicalFor(store.ScopeProject).Get(store.SettingRoutePin); ok {
		t.Fatal("project pin was not cleared")
	}
	if _, ok := st.physicalFor(store.ScopeProject).Get(store.SettingRouteFloor); ok {
		t.Fatal("project floor was not cleared")
	}
	rec, ok := st.physicalFor(store.ScopeGlobal).Get(store.SettingRoutePin)
	if !ok || rec.Body != "default" {
		t.Fatalf("global pin was changed: %+v, found=%t", rec, ok)
	}
	rec, ok = st.physicalFor(store.ScopeGlobal).Get(store.SettingRouteFloor)
	if !ok || rec.Body != "cheap-fast" {
		t.Fatalf("global floor was changed: %+v, found=%t", rec, ok)
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

func TestRoutePolicySetFlowsThroughAgentDecision(t *testing.T) {
	root := t.TempDir()
	routePolicySet(root, []string{"deep", "--provider", "codex", "--profile", "deep-reasoning", "--model", "gpt-5-codex", "--effort", "ultra", "--native-effort", "max"})

	st := openStore(root)
	d := routing.DecideWithStore(st, "redesign the auth architecture", routing.DefaultConfig(), nil)
	st.Close()
	if d.Provider != "codex" || d.Profile != "deep-reasoning" || d.Model != "gpt-5-codex" || d.Effort != "ultra" || d.NativeEffort != "max" {
		t.Fatalf("store-backed policy not applied: %+v", d)
	}
}

func TestRoutePolicyCrossProviderFallbackIsExplicit(t *testing.T) {
	root := t.TempDir()
	routePolicySet(root, []string{"deep", "--provider", "codex", "--cross-provider-fallback"})
	st := openStore(root)
	defer st.Close()
	if rec, ok := st.Get("setting/route-cross-provider-fallback/deep-reasoning"); !ok || rec.Body != "true" {
		t.Fatalf("cross-provider fallback policy missing: %+v %v", rec, ok)
	}
}

func TestRunOverridesAreEphemeralAndSelectCatalogProfile(t *testing.T) {
	dry, overrides, task, err := parseRunArgs([]string{"--provider", "codex", "--model", "gpt-5.6-sol", "--effort", "ultra", "implement", "this"})
	if err != nil || dry || strings.Join(task, " ") != "implement this" {
		t.Fatalf("parseRunArgs = dry=%v overrides=%+v task=%q err=%v", dry, overrides, task, err)
	}
	d := routing.Decision{Kind: "agent", Class: "deep-reasoning", Provider: "claude", Model: "opus", ProviderCmd: "claude -p"}
	cfg := routing.Config{Catalog: routing.ModelCatalog{Profiles: []routing.ModelProfile{{
		Provider: "codex", Model: "gpt-5.6-sol", Effort: routing.EffortUltra, NativeEffort: "ultra", Availability: routing.AvailabilityUsable,
	}}}}
	overrides.apply(&d, cfg)
	if d.Provider != "codex" || d.Model != "gpt-5.6-sol" || d.Effort != "ultra" || d.NativeEffort != "ultra" || d.ProviderCmd != "" {
		t.Fatalf("per-run override not applied: %+v", d)
	}
}

func TestRoutePolicyClearRemovesOneField(t *testing.T) {
	root := t.TempDir()
	routePolicySet(root, []string{"cheap", "--provider", "codex", "--model", "gpt-5-mini"})
	routePolicyClear(root, []string{"cheap-fast", "provider"})

	st := openStore(root)
	defer st.Close()
	if _, ok := st.Get("setting/route-provider/cheap-fast"); ok {
		t.Fatal("provider policy still present after clear")
	}
	if rec, ok := st.Get("setting/route-model/cheap-fast"); !ok || rec.Body != "gpt-5-mini" {
		t.Fatalf("model policy was cleared unexpectedly: %+v %v", rec, ok)
	}
}

func TestRouteShowJSONIncludesPolicy(t *testing.T) {
	root := t.TempDir()
	routePolicySet(root, []string{"deep", "--provider", "codex", "--profile", "deep-reasoning"})
	out := captureStdout(t, func() { routeShow(root, true) })
	if !strings.Contains(out, "\"policy\"") || !strings.Contains(out, "\"deep-reasoning\"") || !strings.Contains(out, "\"codex\"") {
		t.Fatalf("route show json missing policy snapshot: %s", out)
	}
}

func TestRoutePolicySetWorkspaceScopeFallsBackIntoDecision(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".projx-workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	routePolicySet(root, []string{"deep", "--scope", "workspace", "--provider", "codex", "--profile", "deep-reasoning"})

	st := openStore(root)
	defer st.Close()
	if st.space == nil {
		t.Fatal("workspace store was not opened")
	}
	if _, ok := st.space.Get("setting/route-provider/deep-reasoning"); !ok {
		t.Fatal("workspace-scoped provider policy not written to workspace store")
	}
	if d := routing.DecideWithStore(st, "redesign the auth architecture", routing.DefaultConfig(), nil); d.Provider != "codex" || d.Profile != "deep-reasoning" {
		t.Fatalf("workspace policy not applied in decision: %+v", d)
	}
}

func TestRoutePolicyPrecedenceGlobalWorkspaceProjectThenPerRun(t *testing.T) {
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "yours"))
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".projx-workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	routePolicySet(root, []string{"deep", "--scope", "global", "--provider", "claude", "--model", "opus", "--effort", "high"})
	routePolicySet(root, []string{"deep", "--scope", "workspace", "--provider", "codex", "--model", "gpt-5.6-terra", "--effort", "high"})
	routePolicySet(root, []string{"deep", "--scope", "project", "--model", "gpt-5.6-sol", "--effort", "ultra"})

	st := openStore(root)
	d := routing.DecideWithStore(st, "redesign the auth architecture", routing.DefaultConfig(), nil)
	st.Close()
	if d.Provider != "codex" || d.Model != "gpt-5.6-sol" || d.Effort != "ultra" {
		t.Fatalf("persistent precedence = %+v", d)
	}
	runOverrides{Provider: "codex", Model: "gpt-5.6-luna", Effort: "medium"}.apply(&d, routing.DefaultConfig())
	if d.Model != "gpt-5.6-luna" || d.Effort != "medium" {
		t.Fatalf("per-run override did not win: %+v", d)
	}
}

func TestRoutePolicyClearScopedLeavesProjectOverride(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".projx-workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	routePolicySet(root, []string{"deep", "--scope", "workspace", "--provider", "codex"})
	routePolicySet(root, []string{"deep", "--provider", "claude"})
	routePolicyClear(root, []string{"deep", "provider", "--scope", "workspace"})

	st := openStore(root)
	defer st.Close()
	if _, ok := st.space.Get("setting/route-provider/deep-reasoning"); ok {
		t.Fatal("workspace-scoped provider policy still present after scoped clear")
	}
	if rec, ok := st.project.Get("setting/route-provider/deep-reasoning"); !ok || rec.Body != "claude" {
		t.Fatalf("project override was removed unexpectedly: %+v %v", rec, ok)
	}
}
