package main

// storecontext.go — ambient store injection for agent launch (DELIVERY side).
//
// The CONTRACT PREAMBLE itself is defined ONCE in the shared projx-store library
// (store.AgentPreamble) so the WASM cell (brain) and any native consumer compute
// the IDENTICAL contract by construction — no re-implementation. This file keeps
// only the native delivery concerns: writing the compiled preamble to
// <root>/.projx/agent-context.md (the callers also expose it via
// PROJX_STORE_CONTEXT[_FILE]).
//
// VENDOR-NEUTRAL: the knowledge is present by construction at launch; the agent
// never needs to be taught it at runtime.

import (
	"fmt"
	"os"
	"path/filepath"

	store "github.com/SirNiklas9/projx-store"
)

// agentContextFileName is the on-disk preamble written for every launched agent.
// Lives under .projx so it travels with the project and is trivially git-ignorable.
const agentContextFileName = "agent-context.md"

// compileStorePreamble renders the live project store into the ambient agent
// preamble. It delegates to the single shared definition in projx-store; the
// native engine keeps this thin alias so the existing call sites and the delivery
// helper below read naturally.
func compileStorePreamble(st store.Store) string {
	return store.AgentPreamble(st)
}

// compileStorePreambleForTask renders the TASK-SLICED contract: the law (gate
// rules + conventions) in full plus only the reference records relevant to the
// task. Delegates to the single shared definition in projx-store. Used by
// `context --task` (the per-message UserPromptSubmit hook) so each turn injects
// the least, most-relevant context instead of the whole store. An empty task
// yields the full preamble (so SessionStart still gets the floor).
// (newSelectorFunc returns nil unless PROJX_SMART_CONTEXT is set, so the default path is
// the deterministic v1 token slice with zero model calls; when opted in it selects the
// relevant records semantically via the cheap model.)
func compileStorePreambleForTask(st store.Store, task string) string {
	return store.AgentContextForTaskSel(st, task, contextSelector(st, task), os.Getenv("PROJX_FOCUS"))
}

// writeAgentContextText writes an already-compiled preamble to disk at
// <root>/.projx/agent-context.md, returning the absolute path. Best-effort —
// a write failure must not block launching the agent; the env-var delivery
// carries the context even if the file write fails.
func writeAgentContextText(root, text string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("no project root")
	}
	dir := filepath.Join(root, ".projx")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, agentContextFileName)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
