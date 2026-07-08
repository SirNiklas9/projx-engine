package main

// cmd_uninstall.go — `projx-engine uninstall --global` : the exact mirror of
// `init --global`. It removes the ProjX lifecycle hook from ~/.claude/settings.json
// (PRESERVING every other hook and top-level key) and removes the installed `projx`
// skill. By default it KEEPS all declared knowledge and secrets — an uninstall is not a
// data wipe. `--purge-store` additionally drops the per-user global store (never secrets).
// Cross-platform and robust (Go JSON, no jq/PowerShell surgery), so it works natively on
// Windows. The binary itself is left in place — delete it by hand if you want it gone.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// runUninstallCmd dispatches `projx-engine uninstall [--global] [--purge-store]`.
func runUninstallCmd(absRoot string, args []string) {
	global := false
	purge := false
	for _, a := range args {
		switch a {
		case "--global":
			global = true
		case "--purge-store":
			purge = true
		}
	}
	if !global {
		die("uninstall: only `uninstall --global` is supported (removes the machine-wide hook + skill).\n" +
			"  Per-project ProjX files live in each repo's .claude/ and .projx/ — remove those per project.")
	}
	runGlobalUninstall(purge)
}

// runGlobalUninstall reverses runGlobalBootstrap: remove the hook + skill; keep data.
func runGlobalUninstall(purge bool) {
	home, err := claudeHomeDir()
	if err != nil {
		die("uninstall: cannot resolve home dir: %v", err)
	}
	claudeDir := filepath.Join(home, ".claude")
	settingsPath := filepath.Join(claudeDir, "settings.json")

	fmt.Println("projx uninstall (global): removing the ProjX hook + skill (stores & secrets KEPT)")

	// 1. Remove the ProjX hook entries, preserving all other hooks.
	removed, err := removeGlobalHook(settingsPath)
	if err != nil {
		die("uninstall: hook removal: %v", err)
	}
	if len(removed) > 0 {
		fmt.Printf("  hook: removed ProjX entries for %s (backup → %s.projx-bak)\n",
			strings.Join(removed, ", "), settingsPath)
	} else {
		fmt.Println("  hook: no ProjX entries found (nothing to remove)")
	}

	// 2. Remove the installed skill.
	skillDir := filepath.Join(claudeDir, "skills", "projx")
	if _, statErr := os.Stat(skillDir); statErr == nil {
		if rmErr := os.RemoveAll(skillDir); rmErr != nil {
			fmt.Printf("  skill: could NOT remove %s: %v\n", skillDir, rmErr)
		} else {
			fmt.Printf("  skill: removed %s\n", skillDir)
		}
	} else {
		fmt.Println("  skill: not installed (nothing to remove)")
	}

	// 3. Optionally drop the per-user global store — NEVER secrets.
	if purge {
		if dir := yoursDir(); dir != "" {
			sp := filepath.Join(dir, "store.db")
			if _, statErr := os.Stat(sp); statErr == nil {
				if rmErr := os.Remove(sp); rmErr != nil {
					fmt.Printf("  store: could NOT remove %s: %v\n", sp, rmErr)
				} else {
					fmt.Printf("  store: removed global store %s (sealed secrets left intact)\n", sp)
				}
			}
		}
	}

	fmt.Println("\nprojx uninstall: done.")
	fmt.Println("  • The binary is untouched — delete it by hand if you want it gone.")
	fmt.Println("  • Per-project .projx stores and .claude/ files are left as-is.")
	if !purge {
		fmt.Println("  • Global store + secrets kept. Add --purge-store to also drop the global store (never secrets).")
	}
}

// removeGlobalHook parses settings.json and removes every hook group whose command is a
// ProjX hook, pruning any event left empty and dropping the "hooks" key if it becomes
// empty. Every non-ProjX hook and top-level key is preserved. Returns the events it
// touched; writes a .projx-bak backup before overwriting. A missing/empty file is a no-op.
func removeGlobalHook(settingsPath string) (removed []string, err error) {
	data, rerr := os.ReadFile(settingsPath)
	if os.IsNotExist(rerr) {
		return nil, nil
	}
	if rerr != nil {
		return nil, rerr
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	root := map[string]any{}
	if json.Unmarshal(data, &root) != nil {
		return nil, fmt.Errorf("%s isn't valid JSON — remove the ProjX hooks by hand", settingsPath)
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		return nil, nil
	}

	changed := false
	for event, v := range hooks {
		arr, _ := v.([]any)
		kept := make([]any, 0, len(arr))
		for _, g := range arr {
			if groupIsProjx(g) {
				changed = true
				removed = append(removed, event)
				continue
			}
			kept = append(kept, g)
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	if !changed {
		return nil, nil
	}
	if len(hooks) == 0 {
		delete(root, "hooks")
	} else {
		root["hooks"] = hooks
	}

	out, merr := json.MarshalIndent(root, "", "  ")
	if merr != nil {
		return nil, merr
	}
	_ = os.WriteFile(settingsPath+".projx-bak", data, 0o644) // best-effort backup
	if werr := os.WriteFile(settingsPath, append(out, '\n'), 0o644); werr != nil {
		return nil, werr
	}
	return removed, nil
}

// groupIsProjx reports whether a single hook group carries a ProjX command.
func groupIsProjx(g any) bool {
	gm, ok := g.(map[string]any)
	if !ok {
		return false
	}
	inner, _ := gm["hooks"].([]any)
	for _, h := range inner {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if cmd, _ := hm["command"].(string); strings.Contains(cmd, "projx-engine hook") {
			return true
		}
	}
	return false
}
