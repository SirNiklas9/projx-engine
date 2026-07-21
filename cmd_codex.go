package main

// Codex adapter: lifecycle registration, skill installation, and project-local
// MCP configuration. The engine/store behavior remains shared with Claude Code.

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

//go:embed codex-skill/SKILL.md
var codexSkillMD string

var codexHookSpecs = lifecycleHookSpecs("Bash|Read|Edit|Write|exec_command|shell|apply_patch")

func codexHookCommand() string { return `"` + selfBinaryPath() + `" hook` }
func codexDashboardCommand() string {
	return `"` + selfBinaryPath() + `" status --ensure-server`
}

func mergeCodexHooks(path string) (added, skipped []string, err error) {
	root := map[string]any{}
	if data, rerr := os.ReadFile(path); rerr == nil {
		if len(bytes.TrimSpace(data)) > 0 && json.Unmarshal(data, &root) != nil {
			return nil, nil, fmt.Errorf("%s exists but isn't valid JSON - merge the ProjX hooks by hand", path)
		}
	} else if !os.IsNotExist(rerr) {
		return nil, nil, rerr
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	want := codexHookCommand()
	dashboard := codexDashboardCommand()
	changed := false
	for _, s := range codexHookSpecs {
		arr, _ := hooks[s.event].([]any)
		kept, hasCurrent, dropped := pruneStaleProjxGroups(arr, want)
		if hasCurrent && !hookGroupHasSpec(kept, want, s) {
			kept = dropHookGroupCommand(kept, want)
			hasCurrent = false
			dropped = true
		}
		if hasCurrent && !dropped {
			if s.event == "SessionStart" {
				var dashboardChanged bool
				kept, dashboardChanged = ensureCodexDashboardHook(kept, want, dashboard)
				if dashboardChanged {
					hooks[s.event] = kept
					added = append(added, s.event)
					changed = true
					continue
				}
			}
			skipped = append(skipped, s.event)
			continue
		}
		if !hasCurrent {
			handler := map[string]any{
				"type": "command", "command": want, "commandWindows": want,
				"timeout": s.timeout, "statusMessage": "Loading ProjX",
			}
			handlers := []any{handler}
			if s.event == "SessionStart" {
				handlers = append(handlers, codexDashboardHandler(dashboard))
			}
			group := map[string]any{"hooks": handlers}
			if s.matcher != "" {
				group["matcher"] = s.matcher
			}
			kept = append(kept, group)
			added = append(added, s.event)
		}
		hooks[s.event] = kept
		changed = true
	}
	if !changed {
		return added, skipped, nil
	}
	root["description"] = "ProjX lifecycle integration for Codex."
	root["hooks"] = hooks
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return nil, nil, err
	}
	return added, skipped, nil
}

func codexDashboardHandler(command string) map[string]any {
	return map[string]any{
		"type": "command", "command": command, "commandWindows": command,
		"timeout": 5, "statusMessage": "Starting ProjX dashboard",
	}
}

func ensureCodexDashboardHook(groups []any, hookCommand, dashboardCommand string) ([]any, bool) {
	changed := false
	for _, group := range groups {
		gm, ok := group.(map[string]any)
		if !ok || projxGroupCommand(group) != hookCommand {
			continue
		}
		inner, _ := gm["hooks"].([]any)
		kept := make([]any, 0, len(inner)+1)
		have := false
		for _, item := range inner {
			hm, _ := item.(map[string]any)
			cmd, _ := hm["command"].(string)
			if strings.Contains(cmd, "status --ensure-server") {
				if cmd == dashboardCommand && !have {
					kept = append(kept, item)
					have = true
				} else {
					changed = true
				}
				continue
			}
			kept = append(kept, item)
		}
		if !have {
			kept = append(kept, codexDashboardHandler(dashboardCommand))
			changed = true
		}
		gm["hooks"] = kept
		break
	}
	return groups, changed
}

func installCodexSkill(codexDir string) (path string, wrote bool, err error) {
	dst := filepath.Join(codexDir, "skills", "projx", "SKILL.md")
	if existing, rerr := os.ReadFile(dst); rerr == nil && string(existing) == codexSkillMD {
		return dst, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return dst, false, err
	}
	if err := os.WriteFile(dst, []byte(codexSkillMD), 0o644); err != nil {
		return dst, false, err
	}
	return dst, true, nil
}

func installCodexProjectConfig(absRoot string) (string, error) {
	path := filepath.Join(absRoot, ".codex", "config.toml")
	cfg := map[string]any{}
	if data, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(data)) > 0 {
		if _, err := toml.Decode(string(data), &cfg); err != nil {
			return "", fmt.Errorf("%s exists but isn't valid TOML: %w", path, err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	servers, _ := cfg["mcp_servers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers["projx"] = map[string]any{
		"command": mcpBinaryPath(), "args": []string{"mcp"},
		"startup_timeout_sec": int64(30), "required": true,
	}
	cfg["mcp_servers"] = servers
	var out bytes.Buffer
	if err := toml.NewEncoder(&out).Encode(cfg); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func bootstrapCodex(home string) error {
	codexDir := filepath.Join(home, ".codex")
	hooksPath := filepath.Join(codexDir, "hooks.json")
	added, skipped, err := mergeCodexHooks(hooksPath)
	if err != nil {
		return err
	}
	if len(added) > 0 {
		fmt.Printf("  codex hook: added/refreshed %s -> %s\n", strings.Join(added, ", "), hooksPath)
	}
	if len(skipped) > 0 {
		fmt.Printf("  codex hook: already present for %s\n", strings.Join(skipped, ", "))
	}
	skillPath, wrote, err := installCodexSkill(codexDir)
	if err != nil {
		return err
	}
	if wrote {
		fmt.Printf("  codex skill: installed -> %s\n", skillPath)
	} else {
		fmt.Printf("  codex skill: already up to date -> %s\n", skillPath)
	}
	return nil
}

func runCodexGlobalBootstrap() {
	home, err := claudeHomeDir()
	if err != nil {
		die("bootstrap: cannot resolve home dir: %v", err)
	}
	fmt.Println("projx bootstrap: installing Codex adapter (idempotent - binary NOT touched)")
	if err := bootstrapCodex(home); err != nil {
		die("bootstrap: install Codex adapter: %v", err)
	}
	seeded, present, err := seedGlobalFloor()
	if err != nil {
		die("bootstrap: seed global floor: %v", err)
	}
	fmt.Printf("  floor: %d seeded, %d already present\n", len(seeded), len(present))
	fmt.Println("projx bootstrap: Codex adapter ready. Restart Codex to load hooks and MCP changes.")
}
