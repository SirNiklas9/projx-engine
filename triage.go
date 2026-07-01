package main

// triage.go — the LIVE cheap-model triage behind the decider's TriageFunc seam.
//
// The decider (store.RouteDecide) routes unambiguous tasks for free by rule and only
// asks a model for the ambiguous middle. This file implements that model call — but the
// HOW (which provider, which flags/endpoint) is NOT hardcoded here: it goes through the
// vendor-neutral completer (completion.go), driven by the active integration record. The
// insight (see SMART-CONTEXT-PLAN "The DECIDER"): you don't need opus to know a task
// NEEDS opus — triage is a far smaller problem than the work, so a cheap model does it.
//
// Provider resolution is the integration seam: the store's active integration (declared,
// agnostic), else an env-keyed OpenAI-compatible endpoint, else the default Claude Code
// CLI template — all DATA. newTriageFunc returns nil when no provider is reachable, so
// the decider stays purely deterministic offline. Confidence drives
// escalate-on-uncertainty in RouteDecide (unsure → up a tier, never down).

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

// defaults for the implicit env-keyed endpoint (used only when nothing is declared).
const (
	defaultTriageBaseURL = "https://openrouter.ai/api/v1"
	defaultTriageModel   = "anthropic/claude-haiku-4.5"
)

// newTriageFunc returns a live store.TriageFunc backed by the active integration, or nil
// when no provider is reachable (decider stays deterministic).
func newTriageFunc(absRoot string) store.TriageFunc {
	c, ok := resolveCompleter(absRoot)
	if !ok {
		return nil
	}
	return func(task string) (string, bool) {
		reply, ok := c.complete(triageSystemPrompt+"\n\nClassify this task:\n"+task, cheapModel())
		if !ok {
			return "", false
		}
		return parseTriageReply(reply)
	}
}

// cheapModel is the model override for throwaway completions — empty unless the user set
// PROJX_TRIAGE_MODEL, in which case it overrides the integration's own default model.
func cheapModel() string { return strings.TrimSpace(os.Getenv("PROJX_TRIAGE_MODEL")) }

// resolveTriageBin finds the agent CLI to drive a cli integration: the binary from
// PROJX_AGENT_CMD / PROJX_AGENT, else `claude` on PATH. "" if none is found. It lets the
// user retarget the harness binary without editing the integration template.
func resolveTriageBin() string {
	if cmd := strings.TrimSpace(os.Getenv("PROJX_AGENT_CMD")); cmd != "" {
		if f := strings.Fields(cmd); len(f) > 0 {
			return f[0]
		}
	}
	name := firstNonEmpty(os.Getenv("PROJX_AGENT"), "claude")
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// neutralTriageDir is a scratch cwd with no .claude, so a throwaway model call doesn't
// trigger the project's lifecycle hooks. Falls back to the OS temp dir.
func neutralTriageDir() string {
	d := filepath.Join(os.TempDir(), "projx-triage")
	if os.MkdirAll(d, 0o755) == nil {
		return d
	}
	return os.TempDir()
}

const triageSystemPrompt = `You are a routing triage for a coding assistant. Classify the user's task into exactly one TIER by how much reasoning it needs:
- "cheap-fast": rename/format/list/lookup/typo/trivial one-liners.
- "default": standard coding — implement a feature, write tests, a normal edit.
- "deep-reasoning": architecture, multi-file refactor, debugging a hard bug, design, redesign.
Reply with ONLY compact JSON, no prose: {"tier":"<one of the three>","confident":<true|false>}. Set confident=false if you are genuinely unsure.`

// parseTriageReply delegates to the shared store parser (one definition for every face).
func parseTriageReply(content string) (string, bool) { return store.ParseTierReply(content) }

// firstNonEmpty returns the first non-empty, trimmed string.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
