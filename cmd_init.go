package main

// cmd_init.go — `projx-engine init` : the one-command on-ramp.
//
// It ProjX-enables a project in a single step:
//   1. installs the Claude Code connector into <root>/.claude — the namespaced
//      /projx:* slash commands ONLY. It does NOT write a per-project settings.json:
//      the ProjX lifecycle hook is installed ONCE, GLOBALLY, in ~/.claude/settings.json
//      and does all injection; a per-project hook would DOUBLE-inject (adr/global-hook-
//      single-injection). An existing project settings.json is left untouched.
//   2. registers the ProjX MCP server in <root>/.mcp.json (agent-agnostic pull surface);
//   3. seeds the store — the universal floor + detected stack (only when the store is
//      empty), then a DETERMINISTIC smart seed (recipes / off-limits / architecture),
//      idempotent so re-running never duplicates (adr/seed-beefup-smart-init);
//   4. indexes the code map (`map sync`);
//   5. checks the engine is on PATH and reports next steps.
//
// Re-runnable: slash commands are refreshed; the smart seed and floor seed skip records
// that already exist, so nothing is clobbered or duplicated.

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

// connectorFS embeds the connector templates (settings reference + slash commands).
// `all:` includes dotfiles (.claude) which a bare embed pattern would skip.
//
//go:embed all:claude-connector/.claude
var connectorFS embed.FS

// connectorRoot is the embedded path prefix to strip when writing into <root>/.claude.
const connectorRoot = "claude-connector/.claude"

// mergeMCPServer writes/merges ONE server entry into <root>/.mcp.json (the portable
// MCP config any MCP agent reads). Merges into an existing file so a project's own
// (and other ProjX-registered) servers are preserved; creates the file when absent.
// added reports whether it was newly written (false = already present, left as-is).
func mergeMCPServer(absRoot, name string, def map[string]any) (msg string, added bool) {
	path := filepath.Join(absRoot, ".mcp.json")
	cfg := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(data, &cfg) != nil {
			return fmt.Sprintf(".mcp.json exists but isn't valid JSON — add the %q server by hand", name), false
		}
	}
	servers, _ := cfg["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	if _, exists := servers[name]; exists {
		return fmt.Sprintf("MCP server %q already registered in .mcp.json", name), false
	}
	servers[name] = def
	cfg["mcpServers"] = servers
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "could not encode .mcp.json: " + err.Error(), false
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return "could not write .mcp.json: " + err.Error(), false
	}
	return fmt.Sprintf("MCP server %q registered → .mcp.json", name), true
}

// mcpBinaryPath resolves the projx-engine path for the MCP `command`: PROJX_ENGINE_BIN if
// set, else this binary's absolute forward-slash path (selfBinaryPath). The MCP server is
// spawned WITHOUT a shell, so unlike the hook there is no ${} expansion — the path is baked
// at init. Shares selfBinaryPath with the hook so hook and MCP never point at different bins.
func mcpBinaryPath() string {
	if b := strings.TrimSpace(os.Getenv("PROJX_ENGINE_BIN")); b != "" {
		return filepath.ToSlash(b)
	}
	return selfBinaryPath()
}

// installMCPConfig registers ProjX's own MCP server (store_query/route/gate_check/
// impact/store_commit) — the agent-agnostic pull surface, additive to the hooks.
func installMCPConfig(absRoot string) string {
	// The MCP server is spawned directly by the agent (no shell), so a bare "projx-engine"
	// needs the binary on PATH — which a Windows install to %LOCALAPPDATA%\projx is NOT.
	// Use the resolved absolute path (PROJX_ENGINE_BIN override, else this binary), the SAME
	// path routine the hook uses, so the two can never disagree (see cmd_bootstrap.go).
	msg, added := mergeMCPServer(absRoot, "projx", map[string]any{"command": mcpBinaryPath(), "args": []string{"mcp"}})
	if added {
		return msg + " (any MCP agent: store_query/route/gate_check/impact/store_commit)"
	}
	return msg
}

func runInitCmd(absRoot string, args []string) {
	var stacks []string
	for _, a := range args {
		if a == "--global" {
			// The one-time, per-machine ProjX bootstrap (global hook + floor + skill),
			// distinct from ProjX-enabling a project. It ignores --root by design.
			runGlobalBootstrap()
			return
		}
		if a == "--workspace" {
			// Make --root a multi-repo WORKSPACE: a .projx-workspace marker + store whose
			// records compose into every repo beneath it. Distinct from a project init.
			runWorkspaceInit(absRoot)
			return
		}
		if a == "--force" {
			continue // retained for compatibility; the connector no longer writes a hook file
		}
		stacks = append(stacks, strings.ToLower(strings.TrimSpace(a)))
	}

	// 1. Install the connector's /projx:* slash commands into <root>/.claude. The
	// lifecycle hooks are NOT installed per-project — the single global hook in
	// ~/.claude/settings.json does all injection (a per-project hook would double-inject).
	written, note, err := installConnector(absRoot)
	if err != nil {
		die("init: install connector: %v", err)
	}
	fmt.Printf("init: slash commands installed → %s (%d file(s) written)\n", filepath.Join(absRoot, ".claude"), written)
	if note != "" {
		fmt.Println("init: " + note)
	}

	// 1b. Register the ProjX MCP server in <root>/.mcp.json — the portable, agent-
	// AGNOSTIC MCP config, so Claude Code / Cursor / Codex / Cline all get the store
	// tools (store_query/route/gate_check/store_commit). Additive; merges, never clobbers.
	if msg := installMCPConfig(absRoot); msg != "" {
		fmt.Println("init: " + msg)
	}

	// 2. Seed the store. The floor + stack profiles seed only into a FRESH store (never
	// clobber declared knowledge); the smart seed then enriches idempotently.
	st := openStore(absRoot)
	empty := len(st.List(store.InScope(store.ScopeProject))) == 0
	if empty {
		names := stacks
		if len(names) == 0 {
			names = detectStacks(absRoot)
		}
		if n, serr := Seed(st, absRoot, names); serr != nil {
			st.Close()
			die("init: seed: %v", serr)
		} else {
			fmt.Printf("init: seeded floor%s (%d records)\n", stackSuffix(names), n)
		}
	} else {
		fmt.Println("init: store already has knowledge — floor left as-is (no re-seed)")
	}

	// 2a. Smart seed — scan the project and DETERMINISTICALLY seed build/test/run recipes,
	// the off-limits gate floor, and a high-level architecture summary. Idempotent: it
	// skips any record that already exists, so re-running init never duplicates.
	if n, notes := smartSeed(st, absRoot); n > 0 {
		fmt.Printf("init: smart-seeded %d record(s) — %s\n", n, strings.Join(notes, ", "))
	}
	st.Close()

	// 2b. If CodeGraph is already installed (NEVER auto-installed by ProjX), wire it up
	// too: build its index, register its MCP server, declare the preference as a real,
	// editable store convention. Silent no-op when it isn't present.
	for _, line := range wireCodeGraph(absRoot) {
		fmt.Println("init: " + line)
	}

	// 2c. Bake a declared seed file if the project ships one (projx.seed.toml /
	// .projx/seed.toml) — so cloning a repo + `init` reproduces its whole rule-set.
	if p := seedPathArg(absRoot, nil); fileExists(p) {
		applySeedFile(absRoot, p)
	}

	// 3. Index the code map.
	runMapSync(absRoot, nil)

	// 4. PATH check + next steps.
	reportInitNextSteps()
}

// installConnector writes the embedded connector's slash-command files into <root>/.claude
// and returns the count written plus an optional note. It deliberately SKIPS the connector's
// settings.json: the ProjX lifecycle hook lives once in the GLOBAL ~/.claude/settings.json
// and does all injection, so writing a per-project hook file would double-inject. An existing
// project settings.json is left exactly as the user has it.
func installConnector(absRoot string) (written int, note string, err error) {
	walkErr := fs.WalkDir(connectorFS, connectorRoot, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(p, connectorRoot+"/")

		// Never install a per-project settings.json — the global hook owns injection.
		if rel == "settings.json" {
			if _, statErr := os.Stat(filepath.Join(absRoot, ".claude", "settings.json")); statErr == nil {
				note = "existing .claude/settings.json left as-is (ProjX hooks are global now; a per-project hook would double-inject)"
			}
			return nil
		}

		dst := filepath.Join(absRoot, ".claude", filepath.FromSlash(rel))
		data, rerr := connectorFS.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if mkerr := os.MkdirAll(filepath.Dir(dst), 0o755); mkerr != nil {
			return mkerr
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(rel, ".sh") {
			mode = 0o755
		}
		if werr := os.WriteFile(dst, data, mode); werr != nil {
			return werr
		}
		written++
		return nil
	})
	return written, note, walkErr
}

// reportInitNextSteps checks the engine is reachable on PATH and prints what to do next.
func reportInitNextSteps() {
	onPath := false
	if _, lookErr := exec.LookPath("projx-engine"); lookErr == nil {
		onPath = true
	}
	fmt.Println("\ninit: ready. Open Claude Code in this project — the GLOBAL ProjX hook loads the store automatically.")
	fmt.Println("  • SessionStart injects the lean floor; each message injects a task-sliced delta")
	fmt.Println("  • /projx:remember <fact>   save knowledge   • /projx:store   show the store")
	fmt.Println("  • /projx:route <task>      see the tier     • /projx:gate    list off-limits paths")
	if !onPath {
		self, _ := os.Executable()
		fmt.Printf("\ninit: NOTE — the ProjX hook runs `projx-engine hook` resolved from your PATH, but\n")
		fmt.Printf("      projx-engine isn't on PATH yet, so the hook will fail until you fix that.\n")
		fmt.Printf("      Add its directory to PATH (on Windows: the install dir to your User PATH),\n")
		fmt.Printf("      or set PROJX_ENGINE_BIN=%s\n", self)
	}
}
