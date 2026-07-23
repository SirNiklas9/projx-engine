package main

// cmd_workspace.go - a first-class multi-repo WORKSPACE. A workspace store
// (one .projx-workspace) spans several repos; the member repo dirs are
// declared in .projx/workspace.json so that `map sync` and, critically, the
// SessionStart map-refresh operate over all of them instead of re-indexing the
// workspace root and pruning the whole map.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

const workspaceFloorOrigin = "seed:workspace-floor"

var workspaceFloorConventions = []store.SeedRec{
	{
		Key:  "workspace-shared-rules-live-here",
		Body: "Put rules and facts that should apply across sibling repos in this workspace store. Keep repo-specific architecture and implementation truth in each project's store.",
	},
	{
		Key:  "workspace-before-project-duplication",
		Body: "When the same convention or gate should apply to multiple repos under this workspace, declare it once at workspace scope instead of duplicating it into each project.",
	},
	{
		Key:  "scope-placement",
		Body: "Scope placement rule: global is for how you work everywhere, workspace is for rules shared by sibling repos on this machine, and project is for repo-specific architecture, implementation truth, and local gates.",
	},
	{
		Key:  "workspace-shared-gates-belong-here",
		Body: "If a deny rule should protect multiple repos in this workspace, commit it once at workspace scope instead of repeating it in each project. Project gates are for repo-specific boundaries only.",
	},
	{
		Key:  "workspace-routing-defaults",
		Body: "Routing defaults that should feel the same across sibling repos belong at workspace scope. Override at project scope only when one repo truly needs a different tier or workflow.",
	},
	{
		Key:  "workspace-commit-durable-cross-repo-facts",
		Body: "When you learn a durable fact that affects more than one repo under this workspace, commit it here so every member project inherits it instead of re-discovering it.",
	},
}

var workspaceFloorDocs = []store.SeedRec{
	{
		Key:  "workspace/root",
		Body: "Workspace root placeholder.",
	},
	{
		Key:  "workspace/member-repos",
		Body: "Member repos under this workspace inherit global + workspace knowledge and add their own project truth on top.",
	},
}

// runWorkspaceInit implements `projx-engine --root . init --workspace`: it
// creates the .projx-workspace marker + store at absRoot so every repo under
// this folder composes its workspace-scoped records on top of the global floor
// and beneath each project's own store. Idempotent - re-running just re-opens
// the store and re-asserts the workspace floor.
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
	ws, err := store.Open(dbPath)
	if err != nil {
		die("workspace: open store: %v", err)
	}
	defer ws.Close()

	seeded, present, err := seedWorkspaceFloor(ws, absRoot)
	if err != nil {
		die("workspace: seed floor: %v", err)
	}

	if existed {
		fmt.Printf("workspace: already initialized -> %s\n", marker)
	} else {
		fmt.Printf("workspace: created -> %s\n", marker)
	}
	if len(seeded) > 0 {
		fmt.Printf("  floor: seeded %d workspace record(s): %s\n", len(seeded), strings.Join(seeded, ", "))
	}
	if len(present) > 0 {
		fmt.Printf("  floor: %d workspace record(s) already present (left as-is)\n", len(present))
	}
	if members := childProjxRepos(absRoot); len(members) > 0 {
		fmt.Printf("  member repos under this root: %s\n", strings.Join(members, ", "))
	}
	fmt.Println("  - workspace-scope records now compose into every repo under this folder")
	fmt.Println("  - add shared knowledge:  projx-engine --root <repo> store commit --scope workspace --kind convention --key <key> --body \"...\"")
	fmt.Println("  - confirm:               projx-engine --root <repo> store list --scope workspace")
}

func seedWorkspaceFloor(ws *store.SQLite, absRoot string) (seeded, present []string, err error) {
	put := func(kind store.Kind, key, body string) {
		id := kind.String() + "/" + seedSlug(key)
		if _, ok := ws.Get(id); ok {
			present = append(present, id)
			return
		}
		rec := store.Record{
			ID:     id,
			Kind:   kind,
			Scope:  store.ScopeWorkspace,
			Key:    key,
			Body:   body,
			Origin: workspaceFloorOrigin,
		}
		if perr := ws.Put(rec); perr != nil {
			err = perr
			return
		}
		seeded = append(seeded, id)
	}

	for _, c := range workspaceFloorConventions {
		put(store.KConvention, c.Key, c.Body)
		if err != nil {
			return seeded, present, err
		}
	}

	memberRepos := childProjxRepos(absRoot)
	for _, d := range workspaceFloorDocs {
		body := d.Body
		switch d.Key {
		case "workspace/root":
			body = "Workspace root: " + absRoot + ". Every repo under this folder composes these workspace-scoped records on top of its own project store + the global floor."
		case "workspace/member-repos":
			if len(memberRepos) > 0 {
				body = "Member repos currently detected under this workspace root: " + strings.Join(memberRepos, ", ") + ". Commit cross-repo rules here so each member project composes them automatically."
			} else {
				body = "No member repos were detected when this workspace floor was seeded. As repos are added under this root, commit shared rules here so each member project composes them automatically."
			}
		}
		put(store.KDoc, d.Key, body)
		if err != nil {
			return seeded, present, err
		}
	}
	return seeded, present, nil
}

// childProjxRepos returns the immediate subdirectories of absRoot that are
// ProjX projects (have a .projx dir) - the repos that will compose this
// workspace.
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

// workspaceRepoNames returns the basenames of the declared member repos (the
// focus set).
func workspaceRepoNames(absRoot string) map[string]bool {
	names := map[string]bool{}
	for _, r := range loadWorkspaceSrcs(absRoot) {
		names[filepath.Base(r)] = true
	}
	return names
}

// repoOfPath returns the member repo a tool's file_path belongs to (its first
// path segment relative to the workspace root), or "" if it isn't under a
// declared repo.
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
	Repos []string `json:"repos"`
}

// loadWorkspaceSrcs returns the declared member repo dirs ("" list if none /
// unreadable).
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

// saveWorkspaceSrcs records the member repo dirs (resolved to absolute paths)
// so future syncs and the SessionStart refresh index the same set. Best-effort.
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
