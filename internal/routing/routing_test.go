package routing_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SirNiklas9/projx-engine/internal/routing"
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
	// All default Cmds should be empty (override via config).
	for _, p := range cfg.Providers {
		if p.Cmd != "" {
			t.Errorf("DefaultConfig: class %q Cmd = %q, want empty", p.Class, p.Cmd)
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
	// Other classes should retain default empty Cmd.
	for _, p := range cfg.Providers {
		if p.Class != "deep-reasoning" && p.Cmd != "" {
			t.Errorf("class %q Cmd = %q, want empty after merge", p.Class, p.Cmd)
		}
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
	task     string
	wantKind string
	wantOp   string  // non-empty only for deterministic
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
