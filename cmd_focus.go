package main

// cmd_focus.go — cross-project run visibility primitives + `projx-engine focus`.
//
// The statusline renders ONE LINE PER running background agent, across ALL projects
// (not just the session's cwd). Claude Code's statusLine stdin is session-level only
// (cwd, model, cost — no running-subagent list), so ProjX supplies the multi-project
// signal itself: every detached run REGISTERS its project root in a small global index
// under the per-user yours dir, and the statusline reads that index each render to know
// which projects to scan for dispatch manifests. Cheap by design (a tiny JSON list) so
// it is safe on the statusline hot path.
//
// `focus` is the manual override for which agent renders FAT: by default the agent whose
// project == the current ProjX scope is fattened, but `projx-engine focus <selector>`
// pins a specific one instead (matched by dispatch id, project name, or role). It writes
// a single global focus-state file the statusline reads each render.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// dispatchRootsPath is the global index of project roots that have started a detached
// run. Lives in the per-user yours dir so it is shared across every session/project.
func dispatchRootsPath() string {
	yd := yoursDir()
	if yd == "" {
		return ""
	}
	return filepath.Join(yd, "dispatch-roots.json")
}

type dispatchRootIndex struct {
	Roots []string `json:"roots"`
}

// registerDispatchRoot records a project root in the global index (idempotent). Called
// when a detached dispatch/workflow run is created, so the statusline can later find its
// manifests without walking the filesystem. Best-effort: any error is ignored — failing
// to register only means that project's live line may not appear cross-session, never a
// broken run.
func registerDispatchRoot(root string) {
	p := dispatchRootsPath()
	if p == "" || root == "" {
		return
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	idx := readDispatchRootIndex(p)
	for _, r := range idx.Roots {
		if pathEq(r, abs) {
			return // already present
		}
	}
	idx.Roots = append(idx.Roots, abs)
	writeDispatchRootIndex(p, idx)
}

// dispatchRoots returns the known dispatch project roots, pruning any that no longer
// exist as ProjX projects. It rewrites the index only when it actually pruned something,
// so the common (steady-state) read performs no write — keeping the statusline hot path
// read-only.
func dispatchRoots() []string {
	p := dispatchRootsPath()
	if p == "" {
		return nil
	}
	idx := readDispatchRootIndex(p)
	if len(idx.Roots) == 0 {
		return nil
	}
	kept := idx.Roots[:0:0]
	pruned := false
	for _, r := range idx.Roots {
		if isProjxDir(r) {
			kept = append(kept, r)
		} else {
			pruned = true
		}
	}
	if pruned {
		writeDispatchRootIndex(p, dispatchRootIndex{Roots: kept})
	}
	return kept
}

func readDispatchRootIndex(path string) dispatchRootIndex {
	var idx dispatchRootIndex
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &idx)
	}
	return idx
}

func writeDispatchRootIndex(path string, idx dispatchRootIndex) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if data, err := json.Marshal(idx); err == nil {
		_ = os.WriteFile(path, data, 0o644)
	}
}

// focusPath is the global focus-state file: which agent the statusline should render FAT,
// overriding the default (fatten the agent in the current scope).
func focusPath() string {
	yd := yoursDir()
	if yd == "" {
		return ""
	}
	return filepath.Join(yd, "focus.json")
}

type focusState struct {
	Sel string `json:"sel"`
}

// readFocus returns the current focus selector ("" when none). Best-effort.
func readFocus() string {
	p := focusPath()
	if p == "" {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var f focusState
	if json.Unmarshal(data, &f) != nil {
		return ""
	}
	return strings.TrimSpace(f.Sel)
}

func writeFocus(sel string) error {
	p := focusPath()
	if p == "" {
		return fmt.Errorf("no per-user config dir available for focus state")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, _ := json.Marshal(focusState{Sel: strings.TrimSpace(sel)})
	return os.WriteFile(p, data, 0o644)
}

// runFocusCmd implements `projx-engine focus [<selector> | --clear | --show]`.
//
//	focus                 print the current focus (read-only)
//	focus --show          same
//	focus <selector>      pin the agent matched by <selector> (dispatch id | project | role)
//	focus --clear|off|none|-  clear the pin (back to fat-by-current-scope)
//
// The statusline reads this each render and fattens the matching running agent. Setting
// a focus that matches nothing running is allowed (it simply has no effect until an agent
// matches) — this is a display preference, not an enforcement mode.
func runFocusCmd(_ string, args []string) {
	if len(args) == 0 || (len(args) == 1 && args[0] == "--show") {
		if sel := readFocus(); sel != "" {
			fmt.Printf("focus: %s\n", sel)
		} else {
			fmt.Println("focus: (none — fat line follows current scope)")
		}
		return
	}
	sel := strings.TrimSpace(strings.Join(args, " "))
	switch strings.ToLower(sel) {
	case "--clear", "clear", "off", "none", "-":
		if err := writeFocus(""); err != nil {
			die("focus: %v", err)
		}
		fmt.Println("focus: cleared (fat line follows current scope)")
		return
	}
	if err := writeFocus(sel); err != nil {
		die("focus: %v", err)
	}
	fmt.Printf("focus: %s\n", sel)
}

// normRoot returns a canonical map/compare key for a path: cleaned, and lower-cased on
// Windows (case-insensitive FS) so the same project never splits into two entries.
func normRoot(r string) string {
	c := filepath.Clean(r)
	if runtime.GOOS == "windows" {
		return strings.ToLower(c)
	}
	return c
}

// pathEq compares two paths for equality using the same normalization as normRoot, so
// trailing-slash / separator / case differences don't split the same project.
func pathEq(a, b string) bool { return normRoot(a) == normRoot(b) }
