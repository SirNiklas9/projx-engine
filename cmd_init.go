package main

// cmd_init.go — `projx-engine init` : the one-command on-ramp.
//
// It ProjX-enables a project in a single step:
//   1. writes the Claude Code connector into <root>/.claude (lifecycle hooks +
//      namespaced /projx:* slash commands), from templates EMBEDDED in this binary
//      (so an installed engine needs no repo checkout);
//   2. seeds the store floor + the detected language stack (idempotent — never
//      clobbers an already-populated store);
//   3. indexes the code map (`map sync`);
//   4. checks the engine is on PATH and reports next steps.
//
// Re-runnable: existing hooks/commands are refreshed; an existing settings.json is
// NOT overwritten unless --force is given (so a project's own hooks are preserved).

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

// connectorFS embeds the connector templates (hooks, settings, slash commands).
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

// installMCPConfig registers ProjX's own MCP server (store_query/route/gate_check/
// impact/store_commit) — the agent-agnostic pull surface, additive to the hooks.
func installMCPConfig(absRoot string) string {
	msg, added := mergeMCPServer(absRoot, "projx", map[string]any{"command": "projx-engine", "args": []string{"mcp"}})
	if added {
		return msg + " (any MCP agent: store_query/route/gate_check/impact/store_commit)"
	}
	return msg
}

func runInitCmd(absRoot string, args []string) {
	force := false
	var stacks []string
	for _, a := range args {
		if a == "--force" {
			force = true
			continue
		}
		stacks = append(stacks, strings.ToLower(strings.TrimSpace(a)))
	}

	// 1. Write the connector into <root>/.claude from the embedded templates.
	written, skipped, err := installConnector(absRoot, force)
	if err != nil {
		die("init: install connector: %v", err)
	}
	fmt.Printf("init: connector installed → %s (%d file(s) written", filepath.Join(absRoot, ".claude"), written)
	if skipped != "" {
		fmt.Printf("; %s)\n", skipped)
	} else {
		fmt.Println(")")
	}

	// 1b. Register the ProjX MCP server in <root>/.mcp.json — the portable, agent-
	// AGNOSTIC MCP config, so Claude Code / Cursor / Codex / Cline all get the store
	// tools (store_query/route/gate_check/store_commit). Additive to the hooks (which
	// still do push + enforce); merges, never clobbers other servers.
	if msg := installMCPConfig(absRoot); msg != "" {
		fmt.Println("init: " + msg)
	}

	// 2. Seed the store (floor + stacks), only if the project has no knowledge yet.
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
		fmt.Println("init: store already has knowledge — left as-is (no re-seed)")
	}
	st.Close()

	// 2a. If CodeGraph is already installed (NEVER auto-installed by ProjX), wire it up
	// too: build its index, register its MCP server, declare the preference as a real,
	// editable store convention. Silent no-op when it isn't present. Runs AFTER floor
	// seeding — it writes a project-scoped record itself, and running it before the
	// "is the store empty" check above would make a truly fresh project look non-empty
	// and silently skip the floor seed entirely on any machine with CodeGraph installed.
	for _, line := range wireCodeGraph(absRoot) {
		fmt.Println("init: " + line)
	}

	// 2b. Bake a declared seed file if the project ships one (projx.seed.toml /
	// .projx/seed.toml) — so cloning a repo + `init` reproduces its whole rule-set.
	if p := seedPathArg(absRoot, nil); fileExists(p) {
		applySeedFile(absRoot, p)
	}

	// 3. Index the code map.
	runMapSync(absRoot, nil)

	// 4. PATH check + next steps.
	reportInitNextSteps()
}

// installConnector writes every embedded connector file into <root>/.claude, returning
// the count written and a note about anything skipped. Shell scripts are written
// executable; an existing settings.json is preserved unless force is set.
func installConnector(absRoot string, force bool) (written int, skipped string, err error) {
	walkErr := fs.WalkDir(connectorFS, connectorRoot, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(p, connectorRoot+"/")
		dst := filepath.Join(absRoot, ".claude", filepath.FromSlash(rel))

		// Preserve a project's own settings.json unless --force.
		if rel == "settings.json" && !force {
			if _, statErr := os.Stat(dst); statErr == nil {
				skipped = "settings.json kept (merge hooks by hand or rerun with --force)"
				return nil
			}
		}

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
	return written, skipped, walkErr
}

// reportInitNextSteps checks the engine is reachable on PATH and prints what to do next.
func reportInitNextSteps() {
	onPath := false
	if _, lookErr := exec.LookPath("projx-engine"); lookErr == nil {
		onPath = true
	}
	fmt.Println("\ninit: ready. Open Claude Code in this project — the connector loads automatically.")
	fmt.Println("  • SessionStart injects the lean floor; each message injects a task-sliced delta")
	fmt.Println("  • /projx:remember <fact>   save knowledge   • /projx:store   show the store")
	fmt.Println("  • /projx:route <task>      see the tier     • /projx:gate    list off-limits paths")
	if !onPath {
		self, _ := os.Executable()
		fmt.Printf("\ninit: NOTE — `projx-engine` is not on your PATH. Hooks expect it there, or set\n")
		fmt.Printf("      PROJX_ENGINE_BIN to this binary: %s\n", self)
	}
}
