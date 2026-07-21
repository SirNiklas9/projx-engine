package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	store "github.com/SirNiklas9/projx-store"
)

func TestMergeCodexHooksAndProjectConfig(t *testing.T) {
	root := t.TempDir()
	hooksPath := filepath.Join(root, ".codex-home", "hooks.json")
	added, _, err := mergeCodexHooks(hooksPath)
	if err != nil || len(added) != len(codexHookSpecs) {
		t.Fatalf("merge hooks: added=%v err=%v", added, err)
	}
	added, skipped, err := mergeCodexHooks(hooksPath)
	if err != nil || len(added) != 0 || len(skipped) != len(codexHookSpecs) {
		t.Fatalf("idempotent merge: added=%v skipped=%v err=%v", added, skipped, err)
	}
	configDir := filepath.Join(root, ".codex")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("model = \"keep-me\"\n\n[mcp_servers.other]\ncommand = \"other\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := installCodexProjectConfig(root); err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["model"] != "keep-me" {
		t.Fatal("existing Codex config was not preserved")
	}
	servers := cfg["mcp_servers"].(map[string]any)
	if servers["other"] == nil || servers["projx"] == nil {
		t.Fatalf("MCP merge lost a server: %v", servers)
	}
}

func TestCodexApplyPatchIsMutatingAndGated(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(root, "yours"))
	st := openStore(root)
	if err := st.Put(store.Record{ID: "gate-rule/secret", Kind: store.KGateRule, Scope: store.ScopeProject, Key: "secret", Body: "secret/**"}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	payload := map[string]any{
		"session_id": "codex-test", "hook_event_name": "PreToolUse", "cwd": root,
		"tool_name":  "apply_patch",
		"tool_input": map[string]any{"command": "*** Begin Patch\n*** Update File: secret/token.txt\n@@\n-old\n+new\n*** End Patch"},
	}
	data, _ := json.Marshal(payload)
	_, reason, code := handleHook(root, data)
	if code != 2 || !strings.Contains(reason, "off-limits") {
		t.Fatalf("apply_patch was not gated: code=%d reason=%q", code, reason)
	}
}
