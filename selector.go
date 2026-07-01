package main

// selector.go — v2 SEMANTIC context selection behind store.SelectorFunc.
//
// v1 context slicing (significantTokens) matches a task's words against record keys —
// cheap, deterministic, offline, but LITERAL: a rambling paragraph that never says
// "billing" won't pull the billing doc even if it's about money movement. v2 hands the
// candidate record keys + the task to the SAME cheap model the decider uses and lets it
// PROPOSE the relevant subset.
//
// OPT-IN: per-message selection means a model call per prompt, so it is OFF unless
// PROJX_SMART_CONTEXT is set. When off, newSelectorFunc returns nil and the store uses
// the v1 token floor. Uses the harness agent CLI by default (no key), HTTP if a triage
// key is set — the same transport choice as triage.go. Any failure → nil → v1 fallback.

import (
	"os"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

const selectorInstruction = `You select which knowledge areas are relevant to a coding task. You are given a list of KEYS (slash-separated knowledge paths) and a TASK. Reply with ONLY a compact JSON array of the keys — copied EXACTLY from the list — that are relevant to the task. Use [] if none are relevant. No prose, no new keys.`

// newSelectorFunc returns the FORCED v2 selector — non-nil only when PROJX_SMART_CONTEXT
// is set AND a cheap model is available (a model call on every message). For the common
// case use contextSelector, which auto-escalates only when the deterministic slice is
// ambiguous.
func newSelectorFunc(st store.Store) store.SelectorFunc {
	if strings.TrimSpace(os.Getenv("PROJX_SMART_CONTEXT")) == "" {
		return nil // not forced
	}
	return rawSelectorFunc(st)
}

// rawSelectorFunc is the model-backed semantic selector with no opt-in gate — nil only
// when no provider integration is reachable. It routes through the SAME completer as
// triage/decompose, so it follows the active integration (no vendor coupling).
func rawSelectorFunc(st store.Store) store.SelectorFunc {
	c, ok := newCompleter(st)
	if !ok {
		return nil
	}
	return func(task string, keys []string) []string {
		if len(keys) == 0 {
			return nil
		}
		prompt := selectorInstruction + "\n\nKEYS:\n" + strings.Join(keys, "\n") + "\n\nTASK:\n" + task
		reply, ok := c.complete(prompt, cheapModel())
		if !ok {
			return nil // model failed → store degrades to v1
		}
		return parseSelectedKeys(reply, keys)
	}
}

// contextSelector decides which slicing to use for a task, cheapest-first:
//   - PROJX_SMART_CONTEXT set → force the semantic selector (v2) always;
//   - otherwise, a cheap model is available AND the deterministic v1 slice would OVERFLOW
//     (ambiguous keywords) → auto-escalate to v2 for THIS task only;
//   - otherwise nil → free deterministic v1.
//
// This spends a model call only where keyword matching is too broad — the routing
// philosophy applied to context.
func contextSelector(st store.Store, task string) store.SelectorFunc {
	if s := newSelectorFunc(st); s != nil {
		return s // forced
	}
	if store.TaskSliceOverflows(st, task) {
		return rawSelectorFunc(st) // auto-escalate: v1 was ambiguous
	}
	return nil
}

// parseSelectedKeys delegates to the shared store parser (one definition for every face).
func parseSelectedKeys(reply string, candidates []string) []string {
	return store.ParseSelectedKeys(reply, candidates)
}
