package main

// cmd_workspace.go — a first-class multi-repo WORKSPACE. A workspace store (one .projx)
// spans several repos; the member repo dirs are DECLARED in .projx/workspace.json so that
// `map sync` and — critically — the SessionStart map-refresh operate over ALL of them
// instead of re-indexing the (empty) workspace root and pruning the whole map. Without
// this, a multi-repo workspace's code-map gets wiped on the first session.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

// runWorkspaceInit implements `projx-engine --root . init --workspace`: it creates the
// .projx-workspace marker + store at absRoot so every repo UNDER this folder composes its
// workspace-scoped records (on top of that repo's project store and the global floor).
// Idempotent — re-running just re-opens the store and re-asserts the marker record.
func runWorkspaceInit(absRoot string) {
	marker := filepath.Join(absRoot, ".projx-workspace")
	dbPath := filepath.Join(marker, "store.db")

	existed := false
	if _, err := os.Stat(dbPath); err == nil {
		existed = true
	}
	if err := os.MkdirAll(marker, 0o755); err != nil {
		die("workspace: mkdir %q: %v", marker, err)
	}
	ws, err := store.Open(dbPath) // creates + migrates the workspace store
	if err != nil {
		die("workspace: open store: %v", err)
	}
	defer ws.Close()

	// Seed a workspace-scope marker record: confirms composition works and keeps
	// `store list --scope workspace` non-empty (the thing that was previously unverifiable).
	rec := store.Record{
		ID: "doc/workspace-root", Kind: store.KDoc, Scope: store.ScopeWorkspace, Key: "workspace/root",
		Body: "Workspace root: " + absRoot + ". Every repo under this folder composes these " +
			"workspace-scoped records on top of its own project store + the global floor.",
		Origin: "seed:workspace",
	}
	if _, ok := ws.Get(rec.ID); !ok {
		_ = ws.Put(rec)
	}

	if existed {
		fmt.Printf("workspace: already initialized → %s\n", marker)
	} else {
		fmt.Printf("workspace: created → %s\n", marker)
	}
	if members := childProjxRepos(absRoot); len(members) > 0 {
		fmt.Printf("  member repos under this root: %s\n", strings.Join(members, ", "))
	}
	fmt.Println("  • workspace-scope records now compose into every repo under this folder")
	fmt.Println("  • add shared knowledge:  projx-engine --root <repo> store commit --scope workspace --kind convention --key <key> --body \"…\"")
	fmt.Println("  • confirm:               projx-engine --root <repo> store list --scope workspace")
}

// childProjxRepos returns the immediate subdirectories of absRoot that are ProjX projects
// (have a .projx dir) — the repos that will compose this workspace.
func childProjxRepos(absRoot string) []string {
	var out []string
	entries, _ := os.ReadDir(absRoot)
	for _, e := range entries {
		if e.IsDir() {
			if fi, err := os.Stat(filepath.Join(absRoot, e.Name(), ".projx")); err == nil && fi.IsDir() {
				out = append(out, e.Name())
			}
		}
	}
	return out
}

// workspaceRepoNames returns the basenames of the declared member repos (the focus set).
func workspaceRepoNames(absRoot string) map[string]bool {
	names := map[string]bool{}
	for _, r := range loadWorkspaceSrcs(absRoot) {
		names[filepath.Base(r)] = true
	}
	return names
}

// repoOfPath returns the member repo a tool's file_path belongs to (its first path
// segment relative to the workspace root), or "" if it isn't under a declared repo. This
// is what lets the session's FOCUS auto-track the repo you're editing.
func repoOfPath(absRoot, p string) string {
	p = filepath.ToSlash(strings.TrimSpace(p))
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		if rel, err := filepath.Rel(absRoot, p); err == nil {
			p = filepath.ToSlash(rel)
		}
	}
	p = strings.TrimPrefix(p, "./")
	seg := p
	if i := strings.IndexByte(p, '/'); i > 0 {
		seg = p[:i]
	}
	if workspaceRepoNames(absRoot)[seg] {
		return seg
	}
	return ""
}

// workspaceFile is where a workspace records its member repo source dirs.
func workspaceFile(absRoot string) string {
	return filepath.Join(absRoot, ".projx", "workspace.json")
}

type workspaceConfig struct {
	Repos []string `json:"repos"` // absolute repo source dirs indexed into this store
}

// loadWorkspaceSrcs returns the declared member repo dirs ("" list if none / unreadable).
func loadWorkspaceSrcs(absRoot string) []string {
	data, err := os.ReadFile(workspaceFile(absRoot))
	if err != nil {
		return nil
	}
	var w workspaceConfig
	if json.Unmarshal(data, &w) != nil {
		return nil
	}
	return w.Repos
}

// saveWorkspaceSrcs records the member repo dirs (resolved to absolute paths) so future
// syncs and the SessionStart refresh index the same set. Best-effort.
func saveWorkspaceSrcs(absRoot string, srcs []string) {
	abs := make([]string, 0, len(srcs))
	for _, s := range srcs {
		if a, err := filepath.Abs(s); err == nil {
			abs = append(abs, a)
		}
	}
	if len(abs) == 0 {
		return
	}
	dir := filepath.Join(absRoot, ".projx")
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	if data, err := json.MarshalIndent(workspaceConfig{Repos: abs}, "", "  "); err == nil {
		_ = os.WriteFile(workspaceFile(absRoot), data, 0o644)
	}
}
