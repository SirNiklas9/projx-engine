package main

// cmd_codegraph.go — OPTIONAL integration with CodeGraph (github.com/colbymchenry/
// codegraph), an independent, more mature call-graph indexer: 24 languages, precise
// (type-aware) call resolution, a live file watcher. ProjX's own `impact` command is
// a self-contained, approximate, name-matched substitute that always works (no Node,
// runs in the cage); CodeGraph is the precise UPGRADE when it happens to be installed.
//
// NEVER auto-installed — ProjX only wires it up if the `codegraph` binary is already
// on PATH. When found: builds its index (`codegraph init`, idempotent), registers its
// MCP server in .mcp.json (additive, alongside "projx"), and declares a PREFERENCE as
// a real, editable store convention — not a silent behavior change. Absent CodeGraph,
// none of this runs and native `impact` is the only call-graph tool, as today.

import (
	"os"
	"os/exec"
	"path/filepath"

	store "github.com/SirNiklas9/projx-store"
)

// codegraphPreferenceKey is the declared convention telling an agent to prefer
// CodeGraph's tools over ProjX's native `impact` when both are present. Origin
// "detect:codegraph" (not "seed:floor") — it is added ONLY on detection, and a project
// without CodeGraph never sees it. `store rm convention/prefer-codegraph-call-graph`
// removes the preference without uninstalling anything.
const codegraphPreferenceKey = "prefer-codegraph-call-graph"

const codegraphPreferenceBody = "CodeGraph (github.com/colbymchenry/codegraph) is installed and registered as an MCP " +
	"server ('codegraph') in .mcp.json. PREFER its tools for call-graph / blast-radius / \"who calls X\" / cross-" +
	"language queries — it resolves types precisely and covers more languages than ProjX's own index. ProjX's " +
	"native `impact <symbol>` tool (self-contained, no Node, works caged) remains the fallback for these questions " +
	"when CodeGraph tools are unavailable or CodeGraph isn't installed elsewhere."

// codegraphAvailable reports whether the codegraph CLI is already on PATH. ProjX
// never installs it — this only gates whether we WIRE UP something already there.
func codegraphAvailable() bool {
	_, err := exec.LookPath("codegraph")
	return err == nil
}

// ensureCodeGraphIndex runs `codegraph init` once per project (idempotent — we skip
// our own call if .codegraph/ already exists; codegraph's own init is safe to re-run
// too, but there's no need). Best-effort: a failure here just means we skip wiring it
// up this run, never fatal to `projx-engine init`.
func ensureCodeGraphIndex(absRoot string) error {
	if _, err := os.Stat(filepath.Join(absRoot, ".codegraph")); err == nil {
		return nil
	}
	cmd := exec.Command("codegraph", "init")
	cmd.Dir = absRoot
	_, err := cmd.CombinedOutput()
	return err
}

// wireCodeGraph detects + wires CodeGraph into this project if (and only if) it's
// already installed on the machine: builds its index, registers its MCP server, and
// declares the preference convention. Returns status lines to print (empty if
// CodeGraph isn't present — silent no-op, not an error).
func wireCodeGraph(absRoot string) []string {
	if !codegraphAvailable() {
		return nil
	}
	var lines []string
	if err := ensureCodeGraphIndex(absRoot); err != nil {
		return []string{"codegraph detected on PATH but `codegraph init` failed (" + err.Error() + ") — skipping"}
	}
	if msg, added := mergeMCPServer(absRoot, "codegraph", map[string]any{
		"type": "stdio", "command": "codegraph", "args": []string{"serve", "--mcp"},
	}); msg != "" {
		lines = append(lines, msg)
		_ = added
	}
	st := openStore(absRoot)
	rec := store.Record{
		ID: store.KConvention.String() + "/" + slug(codegraphPreferenceKey), Kind: store.KConvention,
		Scope: store.ScopeProject, Key: codegraphPreferenceKey, Body: codegraphPreferenceBody, Origin: "detect:codegraph",
	}
	if err := st.Put(rec); err == nil {
		lines = append(lines, "codegraph preference declared → "+rec.ID+" (edit or `store rm` to change/remove)")
	}
	st.Close()
	return lines
}
