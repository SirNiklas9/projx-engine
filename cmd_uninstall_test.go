package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestUninstallHookRoundtrip proves uninstall reverses install cleanly: after
// merge-then-remove, ProjX hooks are gone and a pre-existing unrelated hook survives.
func TestUninstallHookRoundtrip(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")

	// Pre-existing NON-ProjX hook the user already had.
	seed := map[string]any{
		"model": "opus",
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{"matcher": "Bash", "hooks": []any{
					map[string]any{"type": "command", "command": "my-own-linter", "timeout": 5},
				}},
			},
		},
	}
	data, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.WriteFile(settings, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Install ProjX hooks.
	if _, _, err := mergeGlobalHook(settings); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(settings)
	if !containsProjx(t, after) {
		t.Fatal("merge did not add ProjX hooks")
	}

	// Uninstall them.
	removed, err := removeGlobalHook(settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) == 0 {
		t.Fatal("removeGlobalHook reported nothing removed")
	}

	final, _ := os.ReadFile(settings)
	if containsProjx(t, final) {
		t.Error("ProjX hooks still present after uninstall")
	}
	// The user's own hook + top-level key must survive.
	var root map[string]any
	if err := json.Unmarshal(final, &root); err != nil {
		t.Fatal(err)
	}
	if root["model"] != "opus" {
		t.Error("top-level key 'model' was lost")
	}
	pre := root["hooks"].(map[string]any)["PreToolUse"].([]any)
	found := false
	for _, g := range pre {
		gm := g.(map[string]any)
		for _, h := range gm["hooks"].([]any) {
			if h.(map[string]any)["command"] == "my-own-linter" {
				found = true
			}
		}
	}
	if !found {
		t.Error("user's own PreToolUse hook was removed")
	}

	// Idempotent: a second uninstall is a no-op.
	if r2, _ := removeGlobalHook(settings); len(r2) != 0 {
		t.Error("second uninstall should be a no-op")
	}
}

// TestMergeSelfHealsStaleHook proves a re-run of init repairs a stale/broken ProjX hook
// command (the Windows path bug) instead of leaving it — no duplicate, user hooks intact.
func TestMergeSelfHealsStaleHook(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	seed := map[string]any{
		"hooks": map[string]any{
			// a STALE ProjX hook (old broken path form) + a user's own hook on the same event
			"SessionStart": []any{
				map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "~/.local/bin/projx-engine hook", "timeout": 30}}},
			},
			"PreToolUse": []any{
				map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": "my-linter", "timeout": 5}}},
			},
		},
	}
	data, _ := json.MarshalIndent(seed, "", "  ")
	_ = os.WriteFile(settings, data, 0o644)

	if _, _, err := mergeGlobalHook(settings); err != nil {
		t.Fatal(err)
	}

	root := map[string]any{}
	final, _ := os.ReadFile(settings)
	_ = json.Unmarshal(final, &root)
	hooks := root["hooks"].(map[string]any)

	// SessionStart: exactly ONE group, carrying the CURRENT command (not the stale one).
	ss := hooks["SessionStart"].([]any)
	if len(ss) != 1 {
		t.Fatalf("SessionStart has %d groups, want 1 (stale should be replaced, not duplicated)", len(ss))
	}
	if cmd := projxGroupCommand(ss[0]); cmd == "~/.local/bin/projx-engine hook" || cmd != projxHookCommand() {
		t.Errorf("SessionStart command not refreshed: %q", cmd)
	}
	// PreToolUse: the user's linter survives, and a ProjX hook was added.
	var sawLinter, sawProjx bool
	for _, g := range hooks["PreToolUse"].([]any) {
		if projxGroupCommand(g) == "my-linter" {
			sawLinter = true
		}
		if groupIsProjx(g) {
			sawProjx = true
		}
	}
	if !sawLinter {
		t.Error("user's my-linter hook was lost")
	}
	if !sawProjx {
		t.Error("ProjX hook not added to PreToolUse")
	}
}

func containsProjx(t *testing.T, data []byte) bool {
	t.Helper()
	var root map[string]any
	if json.Unmarshal(data, &root) != nil {
		return false
	}
	hooks, _ := root["hooks"].(map[string]any)
	for _, v := range hooks {
		arr, _ := v.([]any)
		for _, g := range arr {
			if groupIsProjx(g) {
				return true
			}
		}
	}
	return false
}
