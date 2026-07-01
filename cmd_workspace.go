package main

// cmd_workspace.go — a first-class multi-repo WORKSPACE. A workspace store (one .projx)
// spans several repos; the member repo dirs are DECLARED in .projx/workspace.json so that
// `map sync` and — critically — the SessionStart map-refresh operate over ALL of them
// instead of re-indexing the (empty) workspace root and pruning the whole map. Without
// this, a multi-repo workspace's code-map gets wiped on the first session.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

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
