package routing_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SirNiklas9/projx-engine/internal/routing"
	store "github.com/SirNiklas9/projx-store"
)

// ── DefaultConfig ─────────────────────────────────────────────────────────────

func TestDefaultConfigHasExpectedClasses(t *testing.T) {
	cfg := routing.DefaultConfig()
	want := []string{"default", "cheap-fast", "deep-reasoning", "local"}
	found := map[string]bool{}
	for _, p := range cfg.Providers {
		found[p.Class] = true
	}
	for _, c := range want {
		if !found[c] {
			t.Errorf("DefaultConfig: missing class %q", c)
		}
	}
	// Default providers should not hardcode any launch shape.
	for _, p := range cfg.Providers {
		if p.Cmd != "" || p.Provider != "" || p.Profile != "" || p.Model != "" {
			t.Errorf("DefaultConfig: class %q unexpectedly hardcoded launch config: %+v", p.Class, p)
		}
	}
}

// ── LoadConfig ────────────────────────────────────────────────────────────────

func writeTempRouting(t *testing.T, root string, providers []routing.Provider) {
	t.Helper()
	dir := filepath.Join(root, ".projx")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .projx: %v", err)
	}
	data, err := json.Marshal(routing.Config{Providers: providers})
	if err != nil {
		t.Fatalf("marshal routing.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "routing.json"), data, 0o644); err != nil {
		t.Fatalf("write routing.json: %v", err)
	}
}

func TestLoadConfigMissingFileReturnsDefaults(t *testing.T) {
	root := t.TempDir()
	cfg := routing.LoadConfig(root)
	defaults := routing.DefaultConfig()
	if len(cfg.Providers) != len(defaults.Providers) {
		t.Errorf("LoadConfig (no file): got %d providers, want %d", len(cfg.Providers), len(defaults.Providers))
	}
}

func TestLoadConfigMergesProviderOverride(t *testing.T) {
	root := t.TempDir()
	// Override the "deep-reasoning" class with a specific command.
	writeTempRouting(t, root, []routing.Provider{
		{Class: "deep-reasoning", Cmd: "claude --model opus"},
	})
	cfg := routing.LoadConfig(root)

	var drCmd string
	for _, p := range cfg.Providers {
		if p.Class == "deep-reasoning" {
			drCmd = p.Cmd
		}
	}
	if drCmd != "claude --model opus" {
		t.Errorf("merged deep-reasoning Cmd = %q, want %q", drCmd, "claude --model opus")
	}
	// Other classes should retain default empty launch config.
	for _, p := range cfg.Providers {
		if p.Class != "deep-reasoning" && (p.Cmd != "" || p.Provider != "" || p.Profile != "" || p.Model != "") {
			t.Errorf("class %q unexpectedly changed after merge: %+v", p.Class, p)
		}
	}
}

func TestLoadConfigMergesProviderProfileModelOverride(t *testing.T) {
	root := t.TempDir()
	writeTempRouting(t, root, []routing.Provider{
		{Class: "deep-reasoning", Provider: "codex", Profile: "deep-reasoning", Model: "gpt-5-codex", Effort: "ultra", NativeEffort: "ultra"},
	})
	cfg := routing.LoadConfig(root)

	var got routing.Provider
	for _, p := range cfg.Providers {
		if p.Class == "deep-reasoning" {
			got = p
		}
	}
	if got.Provider != "codex" || got.Profile != "deep-reasoning" || got.Model != "gpt-5-codex" {
		t.Fatalf("merged provider override = %+v", got)
	}
}

func TestLoadConfigBadJSONReturnsDefaults(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".projx")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "routing.json"), []byte("{bad json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg := routing.LoadConfig(root)
	defaults := routing.DefaultConfig()
	if len(cfg.Providers) != len(defaults.Providers) {
		t.Errorf("LoadConfig (bad JSON): got %d providers, want %d", len(cfg.Providers), len(defaults.Providers))
	}
}

// ── Decide — deterministic triage ────────────────────────────────────────────

type decideCase struct {
	task      string
	wantKind  string
	wantOp    string // non-empty only for deterministic
	wantClass string // non-empty only for agent
}

func TestDecideDeterministicVerify(t *testing.T) {
	cfg := routing.DefaultConfig()
	d := routing.Decide("please verify the boundaries", cfg)
	if d.Kind != "deterministic" {
		t.Errorf("Kind = %q, want deterministic", d.Kind)
	}
	if d.Op != "verify" {
		t.Errorf("Op = %q, want verify", d.Op)
	}
}

func TestDecideDeterministicCheckBoundaries(t *testing.T) {
	cfg := routing.DefaultConfig()
	d := routing.Decide("check boundaries now", cfg)
	if d.Kind != "deterministic" || d.Op != "verify" {
		t.Errorf("Kind=%q Op=%q, want deterministic/verify", d.Kind, d.Op)
	}
}

func TestDecideDeterministicHistory(t *testing.T) {
	cfg := routing.DefaultConfig()
	for _, task := range []string{
		"show me the history",
		"what changed last week",
		"changelog please",
	} {
		d := routing.Decide(task, cfg)
		if d.Kind != "deterministic" {
			t.Errorf("%q: Kind = %q, want deterministic", task, d.Kind)
		}
		if d.Op != "store log" {
			t.Errorf("%q: Op = %q, want store log", task, d.Op)
		}
	}
}

func TestDecideDeterministicStoreList(t *testing.T) {
	cfg := routing.DefaultConfig()
	for _, task := range []string{
		"list the store",
		"what's in the store",
		"show conventions",
		"show store",
	} {
		d := routing.Decide(task, cfg)
		if d.Kind != "deterministic" {
			t.Errorf("%q: Kind = %q, want deterministic", task, d.Kind)
		}
		if d.Op != "store list" {
			t.Errorf("%q: Op = %q, want store list", task, d.Op)
		}
	}
}

// ── Decide — agent triage ─────────────────────────────────────────────────────

func TestDecideAgentDeepReasoning(t *testing.T) {
	cfg := routing.DefaultConfig()
	d := routing.Decide("redesign the auth architecture", cfg)
	if d.Kind != "agent" {
		t.Errorf("Kind = %q, want agent", d.Kind)
	}
	if d.Class != "deep-reasoning" {
		t.Errorf("Class = %q, want deep-reasoning", d.Class)
	}
}

func TestDecideAgentCheapFast(t *testing.T) {
	cfg := routing.DefaultConfig()
	d := routing.Decide("fix this typo in the readme", cfg)
	if d.Kind != "agent" {
		t.Errorf("Kind = %q, want agent", d.Kind)
	}
	if d.Class != "cheap-fast" {
		t.Errorf("Class = %q, want cheap-fast", d.Class)
	}
}

func TestDecideAgentDefault(t *testing.T) {
	cfg := routing.DefaultConfig()
	d := routing.Decide("implement feature X", cfg)
	if d.Kind != "agent" {
		t.Errorf("Kind = %q, want agent", d.Kind)
	}
	if d.Class != "default" {
		t.Errorf("Class = %q, want default", d.Class)
	}
}

// ── Decide — provider resolution via config ───────────────────────────────────

func TestDecideResolvesProviderCmdFromConfig(t *testing.T) {
	root := t.TempDir()
	writeTempRouting(t, root, []routing.Provider{
		{Class: "deep-reasoning", Cmd: "claude --model opus"},
	})
	cfg := routing.LoadConfig(root)

	d := routing.Decide("redesign the auth architecture", cfg)
	if d.Kind != "agent" {
		t.Errorf("Kind = %q, want agent", d.Kind)
	}
	if d.Class != "deep-reasoning" {
		t.Errorf("Class = %q, want deep-reasoning", d.Class)
	}
	if d.ProviderCmd != "claude --model opus" {
		t.Errorf("ProviderCmd = %q, want %q", d.ProviderCmd, "claude --model opus")
	}
}

func TestDecideResolvesProviderTemplateFromConfig(t *testing.T) {
	root := t.TempDir()
	writeTempRouting(t, root, []routing.Provider{
		{Class: "deep-reasoning", Provider: "codex", Profile: "deep-reasoning", Model: "gpt-5-codex", Effort: "ultra", NativeEffort: "ultra"},
	})
	cfg := routing.LoadConfig(root)

	d := routing.Decide("redesign the auth architecture", cfg)
	if d.Kind != "agent" || d.Class != "deep-reasoning" {
		t.Fatalf("Decision = %+v", d)
	}
	if d.ProviderCmd != "" {
		t.Fatalf("ProviderCmd = %q, want empty for template-based provider", d.ProviderCmd)
	}
	if d.Provider != "codex" || d.Profile != "deep-reasoning" || d.Model != "gpt-5-codex" || d.Effort != "ultra" || d.NativeEffort != "ultra" {
		t.Fatalf("provider fields not resolved: %+v", d)
	}
}

func TestDecideWithStoreOverridesProviderTemplateFromStore(t *testing.T) {
	root := t.TempDir()
	writeTempRouting(t, root, []routing.Provider{
		{Class: "deep-reasoning", Provider: "claude", Profile: "default", Model: "claude-opus"},
	})
	cfg := routing.LoadConfig(root)
	st := store.NewMem()
	for _, rec := range []store.Record{
		{ID: "route/store-provider", Kind: store.KRoute, Scope: store.ScopeProject, Key: "setting/route-provider/deep-reasoning", Body: "codex"},
		{ID: "route/store-profile", Kind: store.KRoute, Scope: store.ScopeProject, Key: "setting/route-profile/deep-reasoning", Body: "deep-reasoning"},
		{ID: "route/store-model", Kind: store.KRoute, Scope: store.ScopeProject, Key: "setting/route-model/deep-reasoning", Body: "gpt-5-codex"},
	} {
		if err := st.Put(rec); err != nil {
			t.Fatalf("seed store policy: %v", err)
		}
	}

	d := routing.DecideWithStore(st, "redesign the auth architecture", cfg, nil)
	if d.Kind != "agent" || d.Class != "deep-reasoning" {
		t.Fatalf("Decision = %+v", d)
	}
	if d.ProviderCmd != "" {
		t.Fatalf("ProviderCmd = %q, want empty for template-based provider", d.ProviderCmd)
	}
	if d.Provider != "codex" || d.Profile != "deep-reasoning" || d.Model != "gpt-5-codex" {
		t.Fatalf("store-backed provider fields not resolved: %+v", d)
	}
}

func TestDecideWithStoreOverridesProviderCmdFallbackFromStore(t *testing.T) {
	cfg := routing.DefaultConfig()
	st := store.NewMem()
	for _, rec := range []store.Record{
		{ID: "route/tier", Kind: store.KRoute, Scope: store.ScopeProject, Key: "cheap-fast", Body: ""},
		{ID: "route/fallback", Kind: store.KRoute, Scope: store.ScopeProject, Key: "setting/route-fallback/cheap-fast", Body: "codex exec --model gpt-5-mini"},
	} {
		if err := st.Put(rec); err != nil {
			t.Fatalf("seed store fallback: %v", err)
		}
	}

	d := routing.DecideWithStore(st, "fix this typo in the readme", cfg, nil)
	if d.Kind != "agent" || d.Class != "cheap-fast" {
		t.Fatalf("Decision = %+v", d)
	}
	if d.ProviderCmd != "codex exec --model gpt-5-mini" {
		t.Fatalf("ProviderCmd = %q", d.ProviderCmd)
	}
}

func TestDecideDefaultProviderCmdIsEmpty(t *testing.T) {
	cfg := routing.DefaultConfig()
	d := routing.Decide("implement feature X", cfg)
	if d.ProviderCmd != "" {
		t.Errorf("ProviderCmd = %q, want empty (use ambient PROJX_AGENT_CMD)", d.ProviderCmd)
	}
}

// ── Reason field is non-empty ─────────────────────────────────────────────────

func TestDecideAlwaysSetsReason(t *testing.T) {
	cfg := routing.DefaultConfig()
	tasks := []string{
		"verify", "show history", "list the store",
		"redesign architecture", "fix typo", "implement feature X",
	}
	for _, task := range tasks {
		d := routing.Decide(task, cfg)
		if d.Reason == "" {
			t.Errorf("Decide(%q): Reason is empty", task)
		}
	}
}

func TestLoadConfigAcceptsUTF8BOM(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".projx")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .projx: %v", err)
	}
	data := append([]byte{0xef, 0xbb, 0xbf}, []byte(`{"providers":[{"class":"deep-reasoning","provider":"codex","profile":"deep-reasoning","model":"gpt-5-codex"}]}`)...)
	if err := os.WriteFile(filepath.Join(dir, "routing.json"), data, 0o644); err != nil {
		t.Fatalf("write routing.json: %v", err)
	}
	cfg := routing.LoadConfig(root)
	for _, p := range cfg.Providers {
		if p.Class == "deep-reasoning" {
			if p.Provider != "codex" || p.Profile != "deep-reasoning" || p.Model != "gpt-5-codex" {
				t.Fatalf("BOM-prefixed config not loaded: %+v", p)
			}
			return
		}
	}
	t.Fatal("deep-reasoning provider missing")
}

func TestDecideSelectsWeightedProviderCandidateDeterministically(t *testing.T) {
	cfg := routing.Config{
		Providers: []routing.Provider{
			{Class: "default", Provider: "claude", Profile: "balanced", Model: "claude-sonnet", Weight: 1, Quality: 0.7, Cost: 0.2, Latency: 0.3, Reliability: 0.9},
			{Class: "default", Provider: "codex", Profile: "fast", Model: "gpt-5-mini", Weight: 2, Quality: 0.6, Cost: 0.1, Latency: 0.2, Reliability: 0.95},
		},
		Weights: map[string]routing.SelectionWeights{
			"default": {Quality: 1, Cost: 2, Latency: 1, Reliability: 1},
		},
	}
	d := routing.Decide("implement the requested feature", cfg)
	if d.Provider != "codex" || d.Profile != "fast" || d.Model != "gpt-5-mini" {
		t.Fatalf("weighted provider selection = %+v", d)
	}
	if d.Capabilities == nil && d.PermissionProfile != "" {
		t.Fatalf("unexpected provider metadata state: %+v", d)
	}
}

func TestGlobalProviderDisablePrecedesProjectPolicyAndCrossProviderFallback(t *testing.T) {
	cfg := routing.Config{
		Providers: []routing.Provider{
			{Class: "deep-reasoning", Provider: "claude", Model: "claude-opus", Weight: 10, AllowCrossProviderFallback: true},
			{Class: "deep-reasoning", Provider: "codex", Model: "gpt-sol", Weight: 1},
		},
		Catalog: routing.ModelCatalog{Profiles: []routing.ModelProfile{
			{Provider: "claude", Model: "claude-opus", Effort: routing.EffortHigh, NativeEffort: "high", Availability: routing.AvailabilityUsable},
			{Provider: "codex", Model: "gpt-sol", Effort: routing.EffortHigh, NativeEffort: "high", Availability: routing.AvailabilityUsable},
		}},
	}
	st := store.NewMem()
	for _, rec := range []store.Record{
		{ID: "setting/provider-enabled/claude", Key: "setting/provider-enabled/claude", Kind: store.KRoute, Scope: store.ScopeGlobal, Body: "false"},
		{ID: "setting/route-provider/deep-reasoning", Key: "setting/route-provider/deep-reasoning", Kind: store.KRoute, Scope: store.ScopeProject, Body: "claude"},
		{ID: "route/deep-reasoning", Key: "deep-reasoning", Kind: store.KRoute, Scope: store.ScopeProject, Body: "claude --model claude-opus"},
	} {
		if err := st.Put(rec); err != nil {
			t.Fatal(err)
		}
	}
	d := routing.DecideWithStore(st, "redesign this difficult architecture", cfg, nil)
	if d.Provider != "codex" || d.Model != "gpt-sol" {
		t.Fatalf("disabled Claude escaped the global hard gate: %+v", d)
	}
	if d.ProviderCmd != "" {
		t.Fatalf("disabled Claude escaped through a legacy command: %+v", d)
	}
}

func TestLoadConfigPreservesMultipleCandidatesAndWeights(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".projx")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"providers":[{"class":"default","provider":"claude","model":"claude-sonnet","weight":1},{"class":"default","provider":"codex","model":"gpt-5-mini","weight":3}],"weights":{"default":{"cost":2,"latency":1}}}`
	if err := os.WriteFile(filepath.Join(dir, "routing.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := routing.LoadConfig(root)
	count := 0
	for _, p := range cfg.Providers {
		if p.Class == "default" {
			count++
		}
	}
	if count != 2 || cfg.Weights["default"].Cost != 2 {
		t.Fatalf("multiple candidate config was not preserved: %+v weights=%+v", cfg.Providers, cfg.Weights)
	}
	if cfg.Weights["default"].Capability == 0 || cfg.Weights["default"].Efficiency == 0 {
		t.Fatalf("partial project weights erased built-in objective dimensions: %+v", cfg.Weights["default"])
	}
}

func TestCapabilityClassConstrainsNeutralEffortBand(t *testing.T) {
	cfg := routing.Config{
		Providers: []routing.Provider{{Class: "deep-reasoning", Provider: "codex"}},
		Weights:   routing.DefaultConfig().Weights,
		Catalog: routing.ModelCatalog{Profiles: []routing.ModelProfile{
			{Provider: "codex", Model: "luna", Effort: routing.EffortLow, NativeEffort: "low", Availability: routing.AvailabilityUsable, Quality: .9},
			{Provider: "codex", Model: "sol", Effort: routing.EffortHigh, NativeEffort: "high", Availability: routing.AvailabilityUsable, Quality: 1, Capability: 1},
		}},
	}
	d := routing.Decide("investigate a difficult architecture", cfg)
	if d.Model != "sol" || d.Effort != "high" {
		t.Fatalf("deep-reasoning did not enforce its effort band: %+v", d)
	}
}
