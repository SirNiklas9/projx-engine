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
