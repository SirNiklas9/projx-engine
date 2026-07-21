package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	store "github.com/SirNiklas9/projx-store"
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

type learnCandidate struct {
	Status     string   `json:"status"`
	SessionID  string   `json:"session_id"`
	Prompt     string   `json:"prompt,omitempty"`
	Paths      []string `json:"paths,omitempty"`
	VerifiedAt string   `json:"verified_at"`
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

func pendingLearnNotice(root, session string) string {
	st, err := openStoreSafe(root)
	if err != nil {
		return ""
	}
	defer st.Close()
	id := "candidate/governed-turn/" + sanitizeSession(session)
	if _, ok := st.Get(id); !ok {
		return ""
	}
	return "## ProjX LEARN checkpoint (candidate, not authority)\n" +
		"The preceding governed turn passed verification and has staged evidence as `" + id + "`. " +
		"Classify it now: commit only genuinely durable knowledge to the ProjX store; leave transient observations out.\n\n"
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
		return []string{absRoot}
	}
	roots := make([]string, 0, len(targets))
	for _, target := range targets {
		roots = append(roots, targetStoreRoot(absRoot, target))
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

func stageLearnCandidate(root, session string, turn governedTurn) bool {
	candidate := learnCandidate{
		Status: "candidate", SessionID: session, Prompt: turn.Prompt,
		Paths: turn.MutatedPaths, VerifiedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(candidate)
	if err != nil {
		return false
	}
	st, err := openStoreSafe(root)
	if err != nil {
		return false
	}
	defer st.Close()
	now := time.Now().UTC()
	record := store.Record{
		ID: "candidate/governed-turn/" + sanitizeSession(session), Kind: store.KHistory,
		Scope: store.ScopeProject, Key: "governed-turn/" + sanitizeSession(session), Body: string(data),
		Status: store.StatusCandidate, Provenance: "gate-verified", ClaimClass: "turn-observation",
		VerifiedAt: now.UnixMilli(), Verifier: "projx-engine verify", Evidence: strings.Join(turn.MutatedPaths, ", "),
	}
	return st.Put(record) == nil
}

// closeGovernedTurn automatically verifies every project touched by a mutation.
// Passing verification stages evidence for later AI classification; it never
// writes an authoritative store record by itself.
func closeGovernedTurn(home, session string) (string, bool) {
	turn := loadGovernedTurn(home, session)
	if len(turn.MutatedRoots) == 0 {
		return "", false
	}
	for _, root := range turn.MutatedRoots {
		if verifyAll(root, false, false) {
			return "ProjX governed turn: verification failed; the turn remains open. Repair the change and stop again to re-verify.", true
		}
	}
	if !stageLearnCandidate(home, session, turn) {
		return "ProjX governed turn: verification passed, but the learn candidate could not be staged; failing closed.", true
	}
	turn.MutatedRoots = nil
	turn.MutatedPaths = nil
	if !saveGovernedTurn(home, session, turn) {
		return "ProjX governed turn: verification passed, but its checkpoint could not be persisted; failing closed.", true
	}
	return "", false
}
