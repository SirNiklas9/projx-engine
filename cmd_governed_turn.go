package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// governedTurn is deliberately harness-neutral state. The harness still owns
// reasoning and execution; ProjX only remembers which lifecycle obligations a
// turn has acquired and refuses to silently drop them.
type governedTurn struct {
	Prompt       string   `json:"prompt,omitempty"`
	Recalled     bool     `json:"recalled"`
	MutatedRoots []string `json:"mutated_roots,omitempty"`
	MutatedPaths []string `json:"mutated_paths,omitempty"`
}

func governedTurnPath(root, session string) string {
	return filepath.Join(root, ".projx", "governed-turn-"+sanitizeSession(session)+".json")
}

func loadGovernedTurn(root, session string) governedTurn {
	var turn governedTurn
	data, err := os.ReadFile(governedTurnPath(root, session))
	if err == nil {
		_ = json.Unmarshal(data, &turn)
	}
	return turn
}

func saveGovernedTurn(root, session string, turn governedTurn) bool {
	path := governedTurnPath(root, session)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false
	}
	data, err := json.Marshal(turn)
	return err == nil && os.WriteFile(path, data, 0o644) == nil
}

func markGovernedRecall(root, session, prompt string) {
	turn := loadGovernedTurn(root, session)
	turn.Recalled = true
	if strings.TrimSpace(prompt) != "" {
		turn.Prompt = strings.TrimSpace(prompt)
	}
	saveGovernedTurn(root, session, turn)
}

func markGovernedMutation(home, session string, roots, paths []string) bool {
	turn := loadGovernedTurn(home, session)
	turn.MutatedRoots = uniqueSortedStrings(append(turn.MutatedRoots, roots...))
	turn.MutatedPaths = uniqueSortedStrings(append(turn.MutatedPaths, paths...))
	return saveGovernedTurn(home, session, turn)
}

func uniqueSortedStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func mutationRoots(absRoot string, targets []string) []string {
	if len(targets) == 0 {
		if isUserHomeRoot(absRoot) {
			return nil
		}
		return []string{absRoot}
	}
	roots := make([]string, 0, len(targets))
	for _, target := range targets {
		root := targetStoreRoot(absRoot, target)
		if !isUserHomeRoot(root) {
			roots = append(roots, root)
		}
	}
	return uniqueSortedStrings(roots)
}

// isGovernedMutation is narrower than the dispatcher mutation check: shell
// reads must not create false verification obligations, while direct write
// tools and recognizable mutating shell operations must.
func isGovernedMutation(ev lifecycleEvent) bool {
	switch normalizedHookTool(ev.ToolName) {
	case "edit", "write", "apply_patch":
		return true
	case "exec_command":
		cmd := strings.ToLower(strings.TrimSpace(ev.ToolInput.Command))
		for _, signal := range []string{"set-content", "add-content", "new-item", "remove-item", "move-item", "copy-item", "git apply", "git commit", "go fmt", "gofmt", "npm run format", "cargo fmt"} {
			if strings.Contains(cmd, signal) {
				return true
			}
		}
	}
	return false
}

// closeGovernedTurn automatically verifies every project touched by a mutation.
// Stop hooks have a strict harness deadline, so they run only deterministic
// boundaries and drift checks. Behavioral build/test gates remain explicit in
// verify, dispatch, and workflow, where they can run to completion safely.
func closeGovernedTurn(home, session string) (string, bool) {
	turn := loadGovernedTurn(home, session)
	if len(turn.MutatedRoots) == 0 {
		return "", false
	}
	for _, root := range turn.MutatedRoots {
		if isUserHomeRoot(root) {
			continue
		}
		if verifyAll(root, true, false) {
			return "ProjX governed turn: safety verification failed; the turn remains open. Repair the change and stop again to re-verify.", true
		}
	}
	turn.MutatedRoots = nil
	turn.MutatedPaths = nil
	if !saveGovernedTurn(home, session, turn) {
		return "ProjX governed turn: verification passed, but its checkpoint could not be persisted; failing closed.", true
	}
	return "", false
}
