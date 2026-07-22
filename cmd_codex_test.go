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
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	var hooksFile map[string]any
	if err := json.Unmarshal(data, &hooksFile); err != nil {
		t.Fatal(err)
	}
	hooks := hooksFile["hooks"].(map[string]any)
	sessionGroups := hooks["SessionStart"].([]any)
	sessionHooks := sessionGroups[0].(map[string]any)["hooks"].([]any)
	if len(sessionHooks) != 1 {
		t.Fatalf("SessionStart handlers = %d, want one combined lifecycle handler", len(sessionHooks))
	}
	lifecycleWindows := sessionHooks[0].(map[string]any)["commandWindows"].(string)
	if lifecycleWindows != codexHookCommandWindows() || !strings.HasPrefix(lifecycleWindows, `& "`) {
		t.Fatalf("lifecycle commandWindows = %q", lifecycleWindows)
	}
	if !strings.HasSuffix(lifecycleWindows, " hook --codex") {
		t.Fatalf("combined Codex hook command = %q", lifecycleWindows)
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

func TestMergeCodexHooksRepairsCmdStyleWindowsCommands(t *testing.T) {
	old := configuredBinary
	configuredBinary = filepath.Join(t.TempDir(), "runtime with spaces", "projx-engine.exe")
	t.Cleanup(func() { configuredBinary = old })

	hooksPath := filepath.Join(t.TempDir(), ".codex", "hooks.json")
	if _, _, err := mergeCodexHooks(hooksPath); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	hooks := root["hooks"].(map[string]any)
	for _, groupsRaw := range hooks {
		groups := groupsRaw.([]any)
		inner := groups[0].(map[string]any)["hooks"].([]any)
		for _, item := range inner {
			handler := item.(map[string]any)
			handler["commandWindows"] = handler["command"]
		}
	}
	stale, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, append(stale, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	added, _, err := mergeCodexHooks(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != len(codexHookSpecs) {
		t.Fatalf("refreshed events = %v, want all lifecycle events", added)
	}
	data, err = os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	hooks = root["hooks"].(map[string]any)
	for event, groupsRaw := range hooks {
		inner := groupsRaw.([]any)[0].(map[string]any)["hooks"].([]any)
		for _, item := range inner {
			handler := item.(map[string]any)
			windowsCommand, _ := handler["commandWindows"].(string)
			if !strings.HasPrefix(windowsCommand, `& "`) {
				t.Fatalf("%s commandWindows was not repaired: %q", event, windowsCommand)
			}
		}
	}
}

func TestHarnessLifecycleSpecsStayInParity(t *testing.T) {
	if len(projxHookSpecs) != len(codexHookSpecs) {
		t.Fatalf("lifecycle event count differs: Claude=%d Codex=%d", len(projxHookSpecs), len(codexHookSpecs))
	}
	for i := range projxHookSpecs {
		claude, codex := projxHookSpecs[i], codexHookSpecs[i]
		if claude.event != codex.event || claude.timeout != codex.timeout {
			t.Fatalf("lifecycle spec %d differs: Claude=%+v Codex=%+v", i, claude, codex)
		}
	}
	if !strings.Contains(codexHookSpecs[2].matcher, "apply_patch") || strings.Contains(projxHookSpecs[2].matcher, "apply_patch") {
		t.Fatalf("adapter-specific tool matchers lost: Claude=%q Codex=%q", projxHookSpecs[2].matcher, codexHookSpecs[2].matcher)
	}
	if !strings.Contains(projxHookSpecs[2].matcher, "Bash") {
		t.Fatalf("Claude shell enforcement missing from PreToolUse matcher: %q", projxHookSpecs[2].matcher)
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

func TestSessionStartLeavesDashboardPresentationToCodexAdapter(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "yours"))
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := openStoreSafe(root)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	data, _ := json.Marshal(map[string]any{
		"session_id":      "codex-chat",
		"hook_event_name": "SessionStart",
		"cwd":             root,
	})
	stdout, _, code := handleHook(root, data)
	if code != 0 {
		t.Fatalf("SessionStart code = %d", code)
	}
	if strings.Contains(stdout, "ProjX dashboard") || strings.Contains(stdout, "127.0.0.1:47632") {
		t.Fatalf("SessionStart context contains dashboard presentation: %q", stdout)
	}
	if !strings.Contains(stdout, `source="ProjX"`) {
		t.Fatalf("SessionStart context missing ProjX frame: %q", stdout)
	}
}

func TestCodexSessionStartCombinesContextAndDashboardMessage(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{
		"session_id":      "codex-chat",
		"hook_event_name": "SessionStart",
		"cwd":             root,
	})
	out := codexHookOutput(root, data, "project context")
	var payload struct {
		SystemMessage      string `json:"systemMessage"`
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SystemMessage != "ProjX live status: http://127.0.0.1:47632/" {
		t.Fatalf("systemMessage = %q", payload.SystemMessage)
	}
	if payload.HookSpecificOutput.HookEventName != "SessionStart" || payload.HookSpecificOutput.AdditionalContext != "project context" {
		t.Fatalf("hookSpecificOutput = %+v", payload.HookSpecificOutput)
	}
}
