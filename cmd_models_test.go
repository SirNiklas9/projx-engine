package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/SirNiklas9/projx-engine/internal/routing"
)

func TestCodexProfilesFromJSONKeepsEverySupportedNativeEffort(t *testing.T) {
	profiles, err := codexProfilesFromJSON([]byte(`{
  "models": [
    {"slug":"gpt-5.6-sol","display_name":"GPT-5.6 Sol","visibility":"list","supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"xhigh"},{"effort":"ultra"}]},
    {"slug":"hidden","visibility":"hidden","supported_reasoning_levels":[{"effort":"high"}]}
  ]
}`), 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 4 {
		t.Fatalf("profiles = %d, want 4: %#v", len(profiles), profiles)
	}
	wantNative := []string{"low", "medium", "ultra", "xhigh"}
	gotNative := []string{profiles[0].NativeEffort, profiles[1].NativeEffort, profiles[2].NativeEffort, profiles[3].NativeEffort}
	if !reflect.DeepEqual(gotNative, wantNative) {
		t.Fatalf("native efforts = %v, want %v", gotNative, wantNative)
	}
	if profiles[2].Effort != routing.EffortUltra {
		t.Fatalf("ultra normalized to %q", profiles[2].Effort)
	}
}

func TestApplyCodexProjectProfilePreservesExistingConfiguration(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	before := "approval_policy = \"never\"\n\n[mcp_servers.projx]\ncommand = \"projx-engine\"\nargs = [\"mcp\"]\n"
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := routing.ModelProfile{Provider: "codex", Model: "gpt-5.6-sol", NativeEffort: "ultra"}
	if _, err := applyCodexProjectProfile(root, profile); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if _, err := toml.DecodeFile(path, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "gpt-5.6-sol" || got["model_reasoning_effort"] != "ultra" {
		t.Fatalf("model settings = %#v", got)
	}
	if got["approval_policy"] != "never" || got["mcp_servers"].(map[string]any)["projx"] == nil {
		t.Fatalf("unrelated config was lost: %#v", got)
	}
}

func TestCodexCLIArgsRenderModelAndNativeEffort(t *testing.T) {
	got := codexCLIArgs(routing.ModelProfile{Provider: "codex", Model: "gpt-5.6-sol", NativeEffort: "ultra"}, "fix it")
	want := []string{"exec", "--model", "gpt-5.6-sol", "--config", "model_reasoning_effort=ultra", "fix it"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestFindCodexProfilePrefersLiteralNativeEffort(t *testing.T) {
	profiles := []routing.ModelProfile{
		{Provider: "codex", Model: "gpt", Effort: routing.EffortUltra, NativeEffort: "max", Availability: routing.AvailabilityUsable},
		{Provider: "codex", Model: "gpt", Effort: routing.EffortUltra, NativeEffort: "ultra", Availability: routing.AvailabilityUsable},
		{Provider: "codex", Model: "gpt", Effort: routing.EffortHigh, NativeEffort: "high", Availability: routing.AvailabilityUsable},
		{Provider: "codex", Model: "gpt", Effort: routing.EffortHigh, NativeEffort: "xhigh", Availability: routing.AvailabilityUsable},
	}
	for _, tc := range []struct{ request, want string }{{"ultra", "ultra"}, {"max", "max"}, {"high", "high"}, {"xhigh", "xhigh"}} {
		got, err := findCodexProfile(profiles, "gpt", tc.request)
		if err != nil || got.NativeEffort != tc.want {
			t.Fatalf("%s => %+v, %v; want native %s", tc.request, got, err, tc.want)
		}
	}
}

func TestDiscoveredCodexProfilesReceiveGranularDefaults(t *testing.T) {
	data := []byte(`{"models":[{"slug":"gpt-5.6-luna","display_name":"Luna","visibility":"list","supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"}]}]}`)
	profiles, err := codexProfilesFromJSON(data, 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || profiles[0].Freshness != 1 || profiles[0].Capability == 0 || profiles[0].Efficiency == 0 {
		t.Fatalf("profiles lack routing metadata: %+v", profiles)
	}
	if profiles[0].Cost >= profiles[1].Cost || profiles[0].Latency >= profiles[1].Latency {
		t.Fatalf("low effort should be cheaper and faster: %+v", profiles)
	}
}

func TestClaudeProfilesFromHelpTracksModelsAndEfforts(t *testing.T) {
	help := "Provide alias 'fable', 'opus', or 'sonnet'; full name claude-fable-5. --effort (low, medium, high, xhigh, max)"
	profiles := claudeProfilesFromHelp(help, 42)
	seen := map[string]bool{}
	for _, profile := range profiles {
		seen[profile.Model+"|"+profile.NativeEffort] = true
		if profile.Provider != "claude" || profile.Freshness != 1 || profile.Capability == 0 {
			t.Fatalf("incomplete Claude profile: %+v", profile)
		}
	}
	for _, want := range []string{"haiku|low", "sonnet|medium", "opus|high", "fable|xhigh", "claude-fable-5|max"} {
		if !seen[want] {
			t.Fatalf("missing %s in %#v", want, seen)
		}
	}
}
