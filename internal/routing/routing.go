// Package routing implements the deterministic task-triage front door for
// projx-engine run.  No LLM calls; no exec.  Pure policy + keyword matching.
//
// Decision flow:
//  1. DETERMINISTIC-FIRST: if the task clearly maps to an engine op (keyword
//     match), return Kind="deterministic" with an Op.  The caller executes the
//     appropriate handler directly — no agent is launched.
//  2. AGENT: classify the capability-class by keyword (deep-reasoning,
//     cheap-fast, default) and resolve the ProviderCmd from the config.
//
// The routing POLICY (which classes exist, what keywords trigger them) is
// encoded here.  Vendor model names live in the per-project routing.json.
package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

// Decision is the result of a routing decision.
//
//   - Kind         "deterministic" or "agent"
//   - Op           set when Kind=="deterministic" (e.g. "verify", "store log", "store list")
//   - Class        set when Kind=="agent" (e.g. "deep-reasoning", "cheap-fast", "default")
//   - ProviderCmd  the resolved agent command string (empty → use PROJX_AGENT_CMD / claude)
//   - Reason       a short human-readable explanation
type Decision struct {
	Kind        string
	Op          string
	Class       string
	ProviderCmd string
	Reason      string
	// Source is how the class was chosen (override | pin | keyword | triage |
	// triage-escalated | default, +floor) — set for Kind=="agent" via the decider.
	Source string
}

// Provider maps a capability-class name to a concrete agent command string.
// Cmd="" means "use the ambient PROJX_AGENT_CMD / claude default".
type Provider struct {
	Class string `json:"class"`
	Cmd   string `json:"cmd"`
}

// Config holds the routing configuration.  Constructed by DefaultConfig and
// optionally merged with a project-local routing.json by LoadConfig.
type Config struct {
	Providers []Provider `json:"providers"`
}

// DefaultConfig returns the built-in provider table.
// All Cmds default to "" (use the ambient PROJX_AGENT_CMD / claude).
// Users override individual classes in .projx/routing.json.
func DefaultConfig() Config {
	return Config{
		Providers: []Provider{
			{Class: "default", Cmd: ""},
			{Class: "cheap-fast", Cmd: ""},
			{Class: "deep-reasoning", Cmd: ""},
			{Class: "local", Cmd: ""},
		},
	}
}

// LoadConfig merges DefaultConfig with <root>/.projx/routing.json if present.
// Any parse error or missing file is silently ignored (returns defaults).
// Only providers listed in the JSON file are merged; unlisted classes keep
// their default Cmd.
func LoadConfig(root string) Config {
	cfg := DefaultConfig()

	data, err := os.ReadFile(filepath.Join(root, ".projx", "routing.json"))
	if err != nil {
		return cfg // file absent or unreadable — use defaults
	}

	var fileCfg Config
	if err := json.Unmarshal(data, &fileCfg); err != nil {
		return cfg // parse error — use defaults
	}

	// Merge: for each provider in the file, update the matching class in cfg.
	for _, fp := range fileCfg.Providers {
		merged := false
		for i, dp := range cfg.Providers {
			if strings.EqualFold(dp.Class, fp.Class) {
				cfg.Providers[i].Cmd = fp.Cmd
				merged = true
				break
			}
		}
		if !merged {
			// Unknown class in the file — add it.
			cfg.Providers = append(cfg.Providers, fp)
		}
	}
	return cfg
}

// resolveProviderCmd looks up the Cmd for the given capability-class.
// Returns "" if the class is not found or its Cmd is empty.
func resolveProviderCmd(class string, cfg Config) string {
	for _, p := range cfg.Providers {
		if strings.EqualFold(p.Class, class) {
			return p.Cmd
		}
	}
	return ""
}

// containsAny returns true if s (lowercased) contains any of the given tokens.
func containsAny(s string, tokens ...string) bool {
	lower := strings.ToLower(s)
	for _, t := range tokens {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}

// Decide is the store-free back-compat entry point: deterministic-op triage, then
// the decider with no store (built-in classifier, no pin/floor) and no model triage.
func Decide(task string, cfg Config) Decision {
	return DecideWithStore(nil, task, cfg, nil)
}

// DecideWithStore returns the routing Decision for a task. It first does the
// deterministic-OP triage (verify / store log / store list — handled with no agent at
// all), and otherwise hands the capability-tier choice to the store-backed DECIDER
// (store.RouteDecide): per-message @-override > standing pin/floor > keyword classifier
// > cheap model triage > default. Pass triage=nil for deterministic-only routing.
func DecideWithStore(s store.Store, task string, cfg Config, triage store.TriageFunc) Decision {
	// ── 0. MUTATION VETO ─────────────────────────────────────────────────────
	// A task that asks for a CHANGE must never fall into the deterministic-op
	// triage below, whatever else it happens to mention.
	//
	// Why this exists: the arms below are bare substring matches, so a perfectly
	// ordinary edit task — "Add the two missing registrations ... VERIFY: run go
	// build" — matched on the word "verify" and was silently downgraded to a
	// boundary check. The op then ran the build, reported "verify: behavioral
	// gate PASSED", edited NOTHING, and the dispatch reported `done`. It looked
	// exactly like success. That is the worst possible failure for a dispatcher
	// whose whole contract is "agents mutate, the trunk verifies the diff":
	// nothing mutated, and the report said otherwise.
	//
	// Deterministic ops are read-only by construction, so they can only ever be
	// the right answer for a read-only request. When a task says BOTH "change
	// this" and "verify it", the change is the job and the verify is an
	// acceptance criterion — routing to the criterion and skipping the job
	// inverts the request.
	if isMutationTask(task) {
		return decideAgent(s, task, cfg, triage)
	}

	// ── 1. DETERMINISTIC-FIRST triage ────────────────────────────────────────
	// Each arm maps a set of obvious keywords to an engine op.  Keywords are
	// checked in priority order; first match wins.

	if containsAny(task, "verify", "check boundaries", "check boundary", "violations") {
		return Decision{
			Kind:   "deterministic",
			Op:     "verify",
			Reason: "task clearly requests a boundary/rule check — routing to verify op (no agent needed)",
		}
	}

	if containsAny(task, "history", "changelog", "what changed", "show changes", "store log", "show log") {
		return Decision{
			Kind:   "deterministic",
			Op:     "store log",
			Reason: "task requests project history — routing to store log op (no agent needed)",
		}
	}

	if containsAny(task,
		"list the store", "what's in the store", "whats in the store",
		"show conventions", "list conventions", "show store", "list store",
	) {
		return Decision{
			Kind:   "deterministic",
			Op:     "store list",
			Reason: "task requests a store listing — routing to store list op (no agent needed)",
		}
	}

	// ── 2. AGENT path: the DECIDER (precedence ladder) picks the tier ─────────
	return decideAgent(s, task, cfg, triage)
}

// decideAgent runs the DECIDER (precedence ladder) and resolves the provider
// command. Split out of DecideWithStore so the mutation veto can reach the agent
// path without duplicating the ladder — one definition, so route/run/dispatch
// keep agreeing with each other.
//
// The risk-floor (correctness-critical → deep-reasoning) is applied inside
// store.RouteDecide, so route/run/dispatch all get it consistently.
func decideAgent(s store.Store, task string, cfg Config, triage store.TriageFunc) Decision {
	rd := store.RouteDecide(s, task, triage)
	cmd := rd.Cmd // store KRoute tier-map wins if set…
	if cmd == "" {
		cmd = resolveProviderCmd(rd.Class, cfg) // …else the routing.json provider.
	}
	return Decision{
		Kind:        "agent",
		Class:       rd.Class,
		ProviderCmd: cmd,
		Reason:      rd.Reason,
		Source:      rd.Source,
	}
}

// mutationVerbs are the openers of a task that asks for a CHANGE. Matched as
// whole words against the task's LEADING clause, not anywhere in the body: a
// read-only request like "verify nothing added a new export" mentions "add" but
// asks for nothing to change, while "Add the missing registration" leads with it.
//
// Deliberately conservative. A false positive costs an agent run on something an
// op could have answered — cheap, visible, correctable. A false negative silently
// turns a code change into a no-op that reports success, which is what happened
// on 2026-07-16 and cost real debugging time on payment code. Prefer the cheap
// failure.
var mutationVerbs = []string{
	"add", "insert", "append", "create", "write", "implement",
	"fix", "change", "edit", "update", "modify", "patch",
	"remove", "delete", "drop", "rename", "move",
	"refactor", "rewrite", "replace", "wire", "register",
	"bug fix", "bugfix",
}

// isMutationTask reports whether the task's opening asks for a change.
//
// Only the leading clause is considered — up to the first sentence break — so an
// acceptance criterion further down ("... then verify with go build") cannot flip
// the decision, and a genuinely read-only task that merely mentions a verb in
// passing is not dragged onto the agent path.
func isMutationTask(task string) bool {
	lead := strings.ToLower(strings.TrimSpace(task))
	// The lead clause: whichever sentence break comes first.
	for _, brk := range []string{".", ":", ";", "\n", " — ", " - "} {
		if i := strings.Index(lead, brk); i > 0 && i < len(lead) {
			lead = lead[:i]
		}
	}
	for _, v := range mutationVerbs {
		if hasWord(lead, v) {
			return true
		}
	}
	return false
}

// hasWord reports whether s contains tok as a WHOLE word. Substring matching is
// what created the bug this guard exists for ("verify" inside a sentence), so
// the guard itself must not repeat it — "readd" must not match "add".
func hasWord(s, tok string) bool {
	i := 0
	for {
		j := strings.Index(s[i:], tok)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(tok)
		beforeOK := start == 0 || !isWordByte(s[start-1])
		afterOK := end == len(s) || !isWordByte(s[end])
		if beforeOK && afterOK {
			return true
		}
		i = start + 1
		if i >= len(s) {
			return false
		}
	}
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
