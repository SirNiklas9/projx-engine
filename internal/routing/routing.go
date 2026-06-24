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

// Decide returns the routing Decision for a task string.
//
// Rules are intentionally small and obvious.  The heuristic is coarse; a
// future version can use a real classifier.  Ambiguous tasks fall through to
// the agent path — the cost of a mis-classified deterministic route (silently
// not launching an agent) is worse than the cost of spending a token budget.
func Decide(task string, cfg Config) Decision {
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

	// ── 2. AGENT path: capability-class classification ────────────────────────
	class := classifyCapability(task)
	cmd := resolveProviderCmd(class, cfg)

	return Decision{
		Kind:        "agent",
		Class:       class,
		ProviderCmd: cmd,
		Reason:      capabilityReason(class),
	}
}

// classifyCapability maps a task to a capability-class string.
// Priority: deep-reasoning > cheap-fast > default.
func classifyCapability(task string) string {
	if containsAny(task,
		"design", "architect", "architecture", "refactor", "why", "plan",
		"debug", "diagnose", "analyse", "analyze", "redesign", "strategy",
		"tradeoff", "trade-off",
	) {
		return "deep-reasoning"
	}

	if containsAny(task,
		"rename", "typo", "format", "comment", "small", "trivial",
		"one-liner", "oneliner", "quick fix", "quickfix", "spelling",
	) {
		return "cheap-fast"
	}

	return "default"
}

// capabilityReason returns a short human explanation for a capability-class.
func capabilityReason(class string) string {
	switch class {
	case "deep-reasoning":
		return "task involves design/architecture/debugging — using deep-reasoning class"
	case "cheap-fast":
		return "task is a small mechanical change — using cheap-fast class"
	default:
		return "task does not match a specific class — using default class"
	}
}
