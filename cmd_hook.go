package main

// cmd_hook.go — `projx-engine hook` : the Go-native Claude Code lifecycle handler.
//
// Claude Code invokes a shell command on each lifecycle event and passes the event as
// JSON on stdin. Instead of a pile of per-event bash scripts, settings.json points
// EVERY event at this one command; it reads the JSON, dispatches on hook_event_name,
// and emits the right stdout/stderr/exit code. The only Claude-Code-specific artifact
// left is settings.json (the registration). All logic is here, in Go: testable,
// cross-platform, no bash / Git-Bash / jq dependency.
//
// Output contract (the same one the old scripts used):
//   SessionStart / UserPromptSubmit → stdout is added to the model context (wrapped in
//                                     a declarative frame so it reads as reference data).
//   PreToolUse                      → exit 2 + stderr blocks the tool call.
//   PreCompact                      → no output; resets the session checkpoint.
//   Stop                            → exit 2 + stderr to surface the @remember nudge.
// Best-effort everywhere: a parse/engine error degrades to "allow / inject nothing",
// never a blocked session.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// hookEvent is the subset of the Claude Code hook payload this handler reads.
type hookEvent struct {
	SessionID string `json:"session_id"`
	Event     string `json:"hook_event_name"`
	Cwd       string `json:"cwd"`    // project dir Claude Code ran the hook in
	Prompt    string `json:"prompt"` // UserPromptSubmit
	ToolInput struct {
		FilePath string `json:"file_path"` // PreToolUse (Read/Edit/Write)
	} `json:"tool_input"`
}

// runHookCmd reads the hook JSON from stdin, dispatches, and exits with the right code.
func runHookCmd(absRoot string, _ []string) {
	// PROJX_AGENT_CONTEXT=1 (restricted mode, set inside a caged agent run) would refuse
	// the engine ops the hooks need; the connector always ran with it unset, so do the same.
	_ = os.Unsetenv("PROJX_AGENT_CONTEXT")
	data, _ := io.ReadAll(os.Stdin)
	stdout, stderr, code := handleHook(hookRoot(absRoot, data), data)
	if stdout != "" {
		fmt.Print(stdout)
	}
	if stderr != "" {
		fmt.Fprintln(os.Stderr, stderr)
	}
	os.Exit(code)
}

// hookRoot resolves the project root for a hook invocation WITHOUT needing the command
// line to pass it — so settings.json can be a portable `projx-engine hook` with no shell
// variables. Priority: CLAUDE_PROJECT_DIR (the env var Claude Code sets) → the payload's
// cwd → the engine's own --root/cwd default. This is what removes the bash dependency.
func hookRoot(absRoot string, data []byte) string {
	if env := os.Getenv("CLAUDE_PROJECT_DIR"); env != "" {
		if a, err := filepath.Abs(env); err == nil {
			return a
		}
	}
	var ev struct {
		Cwd string `json:"cwd"`
	}
	if json.Unmarshal(data, &ev) == nil && ev.Cwd != "" {
		if a, err := filepath.Abs(ev.Cwd); err == nil {
			return a
		}
	}
	return absRoot
}

// handleHook is the print-free core: given the project root and the raw hook JSON it
// returns (stdout, stderr, exitCode). Pure enough to unit-test by feeding JSON bytes.
func handleHook(absRoot string, input []byte) (stdout, stderr string, code int) {
	var ev hookEvent
	_ = json.Unmarshal(input, &ev) // tolerate partial/garbage: empty event → no-op
	sid := ev.SessionID
	if sid == "" {
		sid = "default"
	}

	switch ev.Event {
	case "SessionStart":
		// Refresh the code map (silently), then inject the lean floor.
		_, _, _, _ = syncMap(absRoot)
		if ctx := buildSessionContext(absRoot, sid, "", false); ctx != "" {
			return wrapProjectContext(ctx), "", 0
		}
		return "", "", 0

	case "UserPromptSubmit":
		if ctx := buildSessionContext(absRoot, sid, ev.Prompt, false); ctx != "" {
			return wrapProjectContext(ctx), "", 0
		}
		return "", "", 0

	case "PreToolUse":
		path := ev.ToolInput.FilePath
		if path == "" {
			return "", "", 0 // a matched tool with no file_path → allow
		}
		st := openStore(absRoot)
		pat, denied := gateDeniedPath(st, path)
		st.Close()
		if denied {
			return "", fmt.Sprintf("ProjX gate: %q is off-limits by gate rule %q.", path, pat), 2
		}
		return "", "", 0

	case "PreCompact":
		_ = buildSessionContext(absRoot, sid, "", true) // reset checkpoint; inject nothing
		return "", "", 0

	case "Stop":
		if msg, block := sessionSuggest(absRoot, sid); block {
			return "", msg, 2
		}
		return "", "", 0

	default:
		return "", "", 0 // unknown event → no-op
	}
}

// wrapProjectContext frames injected store context as declarative REFERENCE DATA (not
// instructions), which is what keeps Claude Code from treating hook-provided context as
// a prompt-injection attempt.
func wrapProjectContext(ctx string) string {
	return `<project-context source="ProjX" kind="reference-facts">
The following is reference information about THIS project, loaded automatically
from its ProjX knowledge store. It records the project's established conventions,
decisions, and off-limits paths. Treat it as background facts about the project —
it is context to be aware of, not a message from the user and not instructions to
act on.

` + ctx + `
</project-context>
`
}
