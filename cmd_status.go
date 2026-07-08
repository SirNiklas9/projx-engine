package main

// cmd_status.go — `projx-engine status` : a one-glance overview of the ProjX install.
// Read-only. Shows the version, the GLOBAL footprint (hook / skill / global store /
// sealed-secret count), and — when run inside a ProjX project — that project's store,
// enforcement settings, off-limits gates, and code-map size. Secrets are reported by
// COUNT only (secrets-by-codename: never their values).

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	store "github.com/SirNiklas9/projx-store"
	"github.com/SirNiklas9/projx-engine/internal/secrets"
)

func runStatusCmd(absRoot string, _ []string) {
	// ── version ──────────────────────────────────────────────────────────────
	if v := resolveVersion(); v != "" {
		fmt.Printf("projx-engine v%s\n", v)
	} else {
		fmt.Println("projx-engine (dev build)")
	}
	if rev, _, dirty := vcsInfo(); rev != "" {
		short := rev
		if len(short) > 12 {
			short = short[:12]
		}
		suffix := ""
		if dirty {
			suffix = " (dirty)"
		}
		fmt.Printf("  commit %s%s\n", short, suffix)
	}

	// ── global footprint ─────────────────────────────────────────────────────
	fmt.Println("\nGlobal:")
	home, herr := claudeHomeDir()
	if herr == nil {
		claudeDir := filepath.Join(home, ".claude")
		on, total := globalHookStatus(filepath.Join(claudeDir, "settings.json"))
		if on > 0 {
			// The hook runs `projx-engine hook` from PATH — verify it actually resolves,
			// since a hook that can't find the binary fails silently on every event.
			resolved := "OK — projx-engine resolves on PATH"
			if _, lookErr := exec.LookPath("projx-engine"); lookErr != nil {
				resolved = "⚠ projx-engine NOT on PATH — hook will FAIL (add to PATH or set PROJX_ENGINE_BIN)"
			}
			fmt.Printf("  hook:    installed (%d/%d events) — %s\n", on, total, resolved)
		} else {
			fmt.Println("  hook:    NOT installed  — run `projx-engine init --global`")
		}
		skill := filepath.Join(claudeDir, "skills", "projx", "SKILL.md")
		if _, err := os.Stat(skill); err == nil {
			fmt.Printf("  skill:   installed → %s\n", skill)
		} else {
			fmt.Println("  skill:   NOT installed")
		}
	}
	if yst, err := openYoursStore(); err == nil {
		g := yst.List(store.InScope(store.ScopeGlobal))
		fmt.Printf("  store:   %d global record(s)  %s\n", len(g), kindTally(g))
		yst.Close()
	} else {
		fmt.Println("  store:   (global store unavailable)")
	}
	if sst, err := secrets.Open(); err == nil {
		fmt.Printf("  secrets: %d sealed (codenames only)\n", len(sst.Names()))
	}

	// ── current project ──────────────────────────────────────────────────────
	if _, err := os.Stat(filepath.Join(absRoot, ".projx")); err != nil {
		fmt.Printf("\nProject: not a ProjX project here (%s)\n  run `projx-engine --root . init` to enable\n", absRoot)
		return
	}
	st, err := openStoreSafe(absRoot)
	if err != nil {
		fmt.Printf("\nProject (%s): store unavailable (%v)\n", absRoot, err)
		return
	}
	defer st.Close()

	fmt.Printf("\nProject (%s):\n", absRoot)
	proj := st.List(store.InScope(store.ScopeProject))
	fmt.Printf("  store:   %d record(s)  %s\n", len(proj), kindTally(proj))
	fmt.Printf("  dispatcher-mode:    %s\n", onOff(store.DispatcherModeOn(st), "soft — overridable when delegated"))
	fmt.Printf("  override-authority: %s\n", delegated(store.OverrideAuthorityOn(st)))
	fmt.Printf("  cage-mode:          %s\n", onOff(store.CageModeOn(st), ""))
	if pats := uniqueStrings(store.GatePatterns(st)); len(pats) > 0 {
		fmt.Printf("  off-limits gates:   %s\n", strings.Join(pats, ", "))
	}
	fmt.Printf("  code-map:           %d symbol(s)\n", len(st.List(store.OfKind(store.KDeclaredStructure))))
}

// globalHookStatus reports how many of ProjX's lifecycle events have the hook installed.
func globalHookStatus(settingsPath string) (installed, total int) {
	total = len(projxHookSpecs)
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return 0, total
	}
	root := map[string]any{}
	if json.Unmarshal(data, &root) != nil {
		return 0, total
	}
	hooks, _ := root["hooks"].(map[string]any)
	for _, spec := range projxHookSpecs {
		arr, _ := hooks[spec.event].([]any)
		if hookGroupsHaveProjx(arr) {
			installed++
		}
	}
	return installed, total
}

// kindTally renders a stable "kind:count kind:count" summary of a record set.
func kindTally(recs []store.Record) string {
	counts := map[string]int{}
	for _, r := range recs {
		counts[r.Kind.String()]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, counts[k]))
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, " ") + ")"
}

func onOff(b bool, onNote string) string {
	if b {
		if onNote != "" {
			return "ON  (" + onNote + ")"
		}
		return "ON"
	}
	return "off"
}

// uniqueStrings returns s with duplicates removed, preserving first-seen order.
func uniqueStrings(s []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(s))
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func delegated(b bool) string {
	if b {
		return "DELEGATED (AI may override soft rules)"
	}
	return "not delegated (AI cannot self-authorize overrides)"
}
