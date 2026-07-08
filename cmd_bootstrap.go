package main

// cmd_bootstrap.go — `projx-engine init --global` : the ONE-TIME, per-machine
// on-ramp for ProjX itself (distinct from `init`, which ProjX-enables a project).
//
// The ProjX model (adr/projx-bootstrap-skill-idea + adr/global-hook-single-injection):
// bootstrap GLOBALLY once, then init workspaces/projects on demand. The single global
// hook in ~/.claude/settings.json does ALL context injection; per-project hooks would
// double-inject. This command:
//
//   1. Merges the ProjX lifecycle hook into ~/.claude/settings.json, PRESERVING any
//      existing hooks (parse, add only ProjX entries, never clobber; skip if present).
//   2. Seeds the GLOBAL-scope floor (working-protocol + secrets-by-codename conventions,
//      off-limits gate rules) if absent — idempotent.
//   3. Installs the `projx` skill to ~/.claude/skills/projx/SKILL.md (embedded here).
//   4. Prints a summary of what it did vs what was already present.
//
// Deterministic + idempotent: re-running never duplicates a hook, a floor record, or
// re-writes an up-to-date skill. It does NOT (re)install the binary.

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

// projxSkillMD is the `projx` skill source, embedded so the binary can write it out
// during `init --global` (the skill self-bootstraps ProjX on any machine).
//
//go:embed skill/SKILL.md
var projxSkillMD string

// selfBinaryPath returns this running binary's absolute path with FORWARD SLASHES — the
// one form that survives everywhere: bash (Git Bash) accepts `C:/Users/.../x.exe`, and
// Windows/Node spawn accept it too, whereas a backslash path is mangled by bash. Falls
// back to the bare name (PATH resolution) only if the executable path can't be resolved.
func selfBinaryPath() string {
	if self, err := os.Executable(); err == nil {
		return filepath.ToSlash(self)
	}
	return "projx-engine"
}

// projxHookCommand is the command every ProjX lifecycle hook runs. Claude Code executes
// hooks through a shell (bash, even on Windows via Git Bash), so the command must be
// BASH-SAFE. A bare `projx-engine` needs the binary on PATH — which a Windows install to
// %LOCALAPPDATA%\projx is NOT by default — so instead we bake this binary's absolute
// forward-slash path as the default, quoted (spaces-safe), with a runtime PROJX_ENGINE_BIN
// override. Result works with NO PATH configuration. isProjxHookCmd detects every variant.
func projxHookCommand() string {
	return `"${PROJX_ENGINE_BIN:-` + selfBinaryPath() + `}" hook`
}

// isProjxHookCmd reports whether a hook command string is a ProjX lifecycle hook. It
// matches any command that runs projx-engine's `hook` subcommand — the current
// PATH-resolved form, an explicit-path form, or a PROJX_ENGINE_BIN override — so
// idempotency and uninstall detect every historical and current variant.
func isProjxHookCmd(cmd string) bool {
	if !strings.Contains(cmd, "hook") {
		return false
	}
	// New form bakes an absolute path but always carries the PROJX_ENGINE_BIN override
	// marker (stable even if the binary is renamed); old forms carry "projx-engine".
	return strings.Contains(cmd, "PROJX_ENGINE_BIN") || strings.Contains(cmd, "projx-engine")
}

// globalFloorOrigin tags the global-scope floor records this command seeds, distinct
// from the project floor's "seed:floor".
const globalFloorOrigin = "seed:global-floor"

// globalFloorConventions are the always-on behaviour rules seeded at GLOBAL scope —
// they travel with the user across every project (adr/projx-bootstrap-skill-idea).
var globalFloorConventions = []store.SeedRec{
	{"working-protocol", "You author and direct; ProjX implements, verifies, then commits. For real build work, say one line about what you're about to do before doing it. Prefer deterministic tools (verify, store, tests) over reasoning whenever a tool can do the job."},
	{"secrets-by-codename", "Never read, edit, or print secret material. Reference secrets only by codename."},
}

// globalFloorGates are the off-limits paths denied everywhere by default, seeded at
// GLOBAL scope so they apply to any directory ProjX touches.
var globalFloorGates = []store.SeedRec{
	{"dotenv files", ".env*"},
	{"private keys", "**/*.key"},
	{"secrets dir", "secret/**"},
	{"ssh material", "**/.ssh/**"},
}

// claudeHomeDir resolves the user's home directory. It prefers $HOME (set on Git Bash,
// macOS, and Linux, and lets tests redirect to a temp dir) and falls back to the OS
// notion of home. This is the parent of ~/.claude.
func claudeHomeDir() (string, error) {
	if h := strings.TrimSpace(os.Getenv("HOME")); h != "" {
		return h, nil
	}
	return os.UserHomeDir()
}

// runGlobalBootstrap implements `projx-engine init --global`. Deterministic + idempotent.
func runGlobalBootstrap() {
	home, err := claudeHomeDir()
	if err != nil {
		die("bootstrap: cannot resolve home dir: %v", err)
	}
	claudeDir := filepath.Join(home, ".claude")
	settingsPath := filepath.Join(claudeDir, "settings.json")

	fmt.Println("projx bootstrap: installing the GLOBAL ProjX floor (idempotent — binary NOT touched)")

	// 1. Merge the lifecycle hook into ~/.claude/settings.json, preserving existing hooks.
	added, skipped, err := mergeGlobalHook(settingsPath)
	if err != nil {
		die("bootstrap: hook merge: %v", err)
	}
	if len(added) > 0 {
		fmt.Printf("  hook: added ProjX entries for %s → %s\n", strings.Join(added, ", "), settingsPath)
	}
	if len(skipped) > 0 {
		fmt.Printf("  hook: already present for %s (left as-is)\n", strings.Join(skipped, ", "))
	}

	// 2. Seed the global-scope floor (conventions + gate rules) if absent.
	seeded, present, err := seedGlobalFloor()
	if err != nil {
		die("bootstrap: seed global floor: %v", err)
	}
	if len(seeded) > 0 {
		fmt.Printf("  floor: seeded %d global record(s): %s\n", len(seeded), strings.Join(seeded, ", "))
	}
	if len(present) > 0 {
		fmt.Printf("  floor: %d global record(s) already present (left as-is)\n", len(present))
	}

	// 3. Install the `projx` skill.
	skillPath, wrote, err := installProjxSkill(claudeDir)
	if err != nil {
		die("bootstrap: install skill: %v", err)
	}
	if wrote {
		fmt.Printf("  skill: installed → %s\n", skillPath)
	} else {
		fmt.Printf("  skill: already up to date → %s\n", skillPath)
	}

	fmt.Println("\nprojx bootstrap: done. The global ProjX hook now loads on every Claude Code session.")
	fmt.Println("  • Make the current dir a ProjX project:  projx-engine --root . init")
	fmt.Println("  • The `projx` skill will bootstrap ProjX automatically on any machine.")
}

// hookSpec describes one lifecycle event's ProjX hook registration.
type hookSpec struct {
	event   string
	matcher string // "" = no matcher (applies to the whole event)
	timeout int
}

// projxHookSpecs is the canonical ProjX hook registration — the five lifecycle events
// and their timeouts. This is the single source the merge writes.
var projxHookSpecs = []hookSpec{
	{"SessionStart", "", 30},
	{"UserPromptSubmit", "", 15},
	{"PreToolUse", "Read|Edit|Write", 10},
	{"PreCompact", "", 15},
	{"Stop", "", 10},
}

// mergeGlobalHook parses ~/.claude/settings.json (creating it if absent), adds the ProjX
// hook entry for each lifecycle event that doesn't already have one, and writes the file
// back — PRESERVING every existing (ProjX or unrelated) hook and top-level key. It never
// duplicates: an event already carrying a "projx-engine hook" command is left untouched.
// Returns the events it added and the events it skipped as already-present.
func mergeGlobalHook(settingsPath string) (added, skipped []string, err error) {
	root := map[string]any{}
	if data, rerr := os.ReadFile(settingsPath); rerr == nil {
		if len(bytes.TrimSpace(data)) > 0 {
			if json.Unmarshal(data, &root) != nil {
				return nil, nil, fmt.Errorf("%s exists but isn't valid JSON — merge the ProjX hooks by hand", settingsPath)
			}
		}
	} else if !os.IsNotExist(rerr) {
		return nil, nil, rerr
	}

	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	want := projxHookCommand()
	changed := false
	for _, s := range projxHookSpecs {
		arr, _ := hooks[s.event].([]any)
		// SELF-HEAL: drop any ProjX hook group whose command differs from the current one
		// (e.g. an old install's broken/stale path), and detect whether the CURRENT command
		// is already present. This makes a re-run of `init --global` REPAIR a stale hook,
		// not just skip it.
		kept, hasCurrent, dropped := pruneStaleProjxGroups(arr, want)
		if hasCurrent && !dropped {
			skipped = append(skipped, s.event)
			continue // already up to date, untouched
		}
		if !hasCurrent {
			group := map[string]any{
				"hooks": []any{
					map[string]any{"type": "command", "command": want, "timeout": s.timeout},
				},
			}
			if s.matcher != "" {
				group["matcher"] = s.matcher
			}
			kept = append(kept, group)
			added = append(added, s.event) // added or refreshed
		}
		hooks[s.event] = kept
		changed = true
	}

	if !changed {
		return added, skipped, nil // nothing to write; existing file untouched
	}

	root["hooks"] = hooks
	out, merr := json.MarshalIndent(root, "", "  ")
	if merr != nil {
		return nil, nil, merr
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(settingsPath, append(out, '\n'), 0o644); err != nil {
		return nil, nil, err
	}
	return added, skipped, nil
}

// projxGroupCommand returns the command of a ProjX hook group's first inner hook ("" if
// the shape is unexpected).
func projxGroupCommand(g any) string {
	if gm, ok := g.(map[string]any); ok {
		if inner, _ := gm["hooks"].([]any); len(inner) > 0 {
			if hm, ok := inner[0].(map[string]any); ok {
				cmd, _ := hm["command"].(string)
				return cmd
			}
		}
	}
	return ""
}

// pruneStaleProjxGroups returns arr with every ProjX hook group whose command differs from
// `want` removed (stale/old-install hooks), reporting whether a group with the CURRENT
// command remained and whether anything was dropped. Non-ProjX groups are always kept.
func pruneStaleProjxGroups(arr []any, want string) (kept []any, hasCurrent, dropped bool) {
	for _, g := range arr {
		if !groupIsProjx(g) {
			kept = append(kept, g)
			continue
		}
		if projxGroupCommand(g) == want {
			hasCurrent = true
			kept = append(kept, g)
		} else {
			dropped = true // stale ProjX hook — drop it so a fresh one replaces it
		}
	}
	return kept, hasCurrent, dropped
}

// hookGroupsHaveProjx reports whether any hook group in an event's array already carries
// a ProjX command (detected by the "projx-engine hook" substring, so a differently-pathed
// install still counts and won't be double-added).
func hookGroupsHaveProjx(groups []any) bool {
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		inner, _ := gm["hooks"].([]any)
		for _, h := range inner {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if cmd, _ := hm["command"].(string); isProjxHookCmd(cmd) {
				return true
			}
		}
	}
	return false
}

// seedGlobalFloor seeds the always-on GLOBAL-scope floor into the per-user (YOURS) store,
// skipping any record that already exists. Returns the keys newly seeded and those already
// present. Idempotent.
func seedGlobalFloor() (seeded, present []string, err error) {
	st, err := openYoursStore()
	if err != nil {
		return nil, nil, err
	}
	defer st.Close()

	put := func(kind store.Kind, r store.SeedRec) {
		id := kind.String() + "/" + seedSlug(r.Key)
		if _, ok := st.Get(id); ok {
			present = append(present, id)
			return
		}
		rec := store.Record{
			ID: id, Kind: kind, Scope: store.ScopeGlobal,
			Key: r.Key, Body: r.Body, Origin: globalFloorOrigin,
		}
		if perr := st.Put(rec); perr != nil {
			err = perr
			return
		}
		seeded = append(seeded, id)
	}

	for _, c := range globalFloorConventions {
		put(store.KConvention, c)
		if err != nil {
			return seeded, present, err
		}
	}
	for _, g := range globalFloorGates {
		put(store.KGateRule, g)
		if err != nil {
			return seeded, present, err
		}
	}
	return seeded, present, nil
}

// openYoursStore opens ONLY the per-user (global + workspace) YOURS store, without
// creating a project .projx. PROJX_YOURS_DIR overrides the location (used by tests and
// custom homes); otherwise it is <UserConfigDir>/projx — the same per-user root openStore
// and secrets use.
func openYoursStore() (*store.SQLite, error) {
	dir := yoursDir()
	if dir == "" {
		return nil, fmt.Errorf("no per-user config dir available for the global store")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return store.Open(filepath.Join(dir, "store.db"))
}

// installProjxSkill writes the embedded `projx` skill to <claudeDir>/skills/projx/SKILL.md.
// Idempotent: if the on-disk content already matches the embedded source, it is left as-is
// and wrote is false. Returns the destination path.
func installProjxSkill(claudeDir string) (path string, wrote bool, err error) {
	dst := filepath.Join(claudeDir, "skills", "projx", "SKILL.md")
	if existing, rerr := os.ReadFile(dst); rerr == nil && string(existing) == projxSkillMD {
		return dst, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return dst, false, err
	}
	if err := os.WriteFile(dst, []byte(projxSkillMD), 0o644); err != nil {
		return dst, false, err
	}
	return dst, true, nil
}
