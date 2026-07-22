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
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

// lifecycleEvent is the harness-neutral event consumed by ProjX's policy core.
// Harness adapters normalize their payloads into this small common shape.
type lifecycleEvent struct {
	SessionID string `json:"session_id"`
	Event     string `json:"hook_event_name"`
	Cwd       string `json:"cwd"`       // project dir the harness ran the hook in
	Prompt    string `json:"prompt"`    // UserPromptSubmit
	ToolName  string `json:"tool_name"` // PreToolUse — which tool (Edit/Write/Read/…)
	ToolInput struct {
		FilePath string `json:"file_path"` // PreToolUse (Read/Edit/Write)
		Cmd      string `json:"cmd"`
		Patch    string `json:"patch"`
		Input    string `json:"input"`
		Workdir  string `json:"workdir"`
		Command  string `json:"command"` // PreToolUse (Bash) — the shell command line
	} `json:"tool_input"`
}

// decodeLifecycleEvent is the adapter boundary between raw hook JSON and the
// shared lifecycle policy. Field aliases are collapsed here so handleHook does
// not need harness-specific branches.
func decodeLifecycleEvent(input []byte) lifecycleEvent {
	var ev lifecycleEvent
	_ = json.Unmarshal(input, &ev) // partial/garbage input intentionally becomes a no-op event
	ev.ToolInput.Command = firstHookValue(ev.ToolInput.Command, ev.ToolInput.Cmd)
	return ev
}

func isMutatingHookTool(name string) bool {
	n := normalizedHookTool(name)
	return store.IsMutatingTool(name) || n == "apply_patch" || n == "exec_command"
}

func normalizedHookTool(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndexAny(name, ".:/"); i >= 0 {
		name = name[i+1:]
	}
	switch name {
	case "bash", "shell", "exec", "exec_command":
		return "exec_command"
	case "applypatch", "apply_patch":
		return "apply_patch"
	default:
		return name
	}
}

func hookTargetPaths(ev lifecycleEvent) []string {
	var out []string
	if p := strings.TrimSpace(ev.ToolInput.FilePath); p != "" {
		out = append(out, p)
	}
	switch normalizedHookTool(ev.ToolName) {
	case "apply_patch":
		patch := firstHookValue(ev.ToolInput.Patch, ev.ToolInput.Input, ev.ToolInput.Command, ev.ToolInput.Cmd)
		for _, line := range strings.Split(patch, "\n") {
			line = strings.TrimSpace(line)
			for _, prefix := range []string{"*** Add File:", "*** Update File:", "*** Delete File:", "*** Move to:"} {
				if strings.HasPrefix(line, prefix) {
					if p := strings.TrimSpace(strings.TrimPrefix(line, prefix)); p != "" {
						out = append(out, resolveHookPath(ev, p))
					}
				}
			}
		}
	case "exec_command":
		out = append(out, execCommandTargetPaths(ev, firstHookValue(ev.ToolInput.Cmd, ev.ToolInput.Command))...)
		if ev.ToolInput.Workdir != "" {
			out = append(out, filepath.Join(ev.ToolInput.Workdir, "_"))
		}
	}
	return uniqueHookPaths(out)
}

func firstHookValue(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func resolveHookPath(ev lifecycleEvent, p string) string {
	p = strings.TrimSpace(p)
	if p == "" || filepath.IsAbs(p) || ev.ToolInput.Workdir == "" {
		return p
	}
	return filepath.Join(ev.ToolInput.Workdir, p)
}

func execCommandTargetPaths(ev lifecycleEvent, cmd string) []string {
	supported := map[string]bool{"cat": true, "type": true, "get-content": true, "rg": true, "grep": true, "touch": true, "mkdir": true, "new-item": true, "rm": true, "remove-item": true, "cp": true, "copy-item": true, "mv": true, "move-item": true, "set-content": true, "add-content": true, "sed": true}
	var out []string
	fields := bashSplit(cmd)
	for i := 0; i < len(fields); {
		op := strings.ToLower(filepath.Base(fields[i]))
		if !supported[op] {
			i++
			continue
		}
		i++
		for i < len(fields) {
			tok := strings.TrimSpace(fields[i])
			low := strings.ToLower(tok)
			if supported[low] {
				break
			}
			i++
			if tok == "" || strings.HasPrefix(tok, "-") || strings.HasPrefix(tok, "$") || strings.Contains(tok, "*") {
				continue
			}
			out = append(out, resolveHookPath(ev, tok))
		}
	}
	return out
}

func uniqueHookPaths(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// runHookCmd reads the hook JSON from stdin, dispatches, and exits with the right code.
func runHookCmd(absRoot string, args []string) {
	// PROJX_AGENT_CONTEXT=1 (restricted mode, set inside a caged agent run) would refuse
	// the engine ops the hooks need; the connector always ran with it unset, so do the same.
	_ = os.Unsetenv("PROJX_AGENT_CONTEXT")
	data, _ := io.ReadAll(os.Stdin)
	// PROJX_CELL_URL set → drive the deployed cell over HTTP; else compute locally.
	var stdout, stderr string
	var code int
	if cellURL := strings.TrimSpace(os.Getenv("PROJX_CELL_URL")); cellURL != "" {
		stdout, stderr, code = handleHookViaCell(cellURL, data)
	} else {
		root := hookRoot(absRoot, data)
		stdout, stderr, code = handleHook(root, data)
		// Status-line breadcrumb (best-effort, never affects the result). Records two
		// facts for `projx-engine statusline`: the last visible ProjX ACTION (ctx/gate)
		// and the actively-touched PROJECT (floating scope — the badge follows what any
		// agent edits/reads, not the static cwd). The crumb lives in the session cwd's
		// project so the statusline command, deriving the same home, finds it.
		home := targetStoreRoot(root, filepath.Join(root, "_"))
		meta := decodeLifecycleEvent(data)
		if meta.SessionID != "" {
			targets := hookTargetPaths(meta)
			switch {
			case meta.Event == "PreToolUse" && code == 2:
				// A block — float the badge to the blocked area (if it's a project) so the
				// human sees WHERE the wall is, at a glance, alongside the red marker.
				if tr := lastTargetRoot(root, targets); tr != "" {
					updateCrumb(home, meta.SessionID, func(c *statusCrumb) { c.A = "gate"; c.R = tr })
				} else {
					updateCrumb(home, meta.SessionID, func(c *statusCrumb) { c.A = "gate" })
				}
			case meta.Event == "PreToolUse" && len(targets) > 0:
				// A file was touched (allowed) → float the scope to its owning project.
				if tr := lastTargetRoot(root, targets); tr != "" {
					updateCrumb(home, meta.SessionID, func(c *statusCrumb) { c.R = tr })
				}
			case meta.Event == "SessionStart":
				// Fresh session: reset the floated scope, note the injected floor.
				n := len(stdout)
				updateCrumb(home, meta.SessionID, func(c *statusCrumb) { c.A = "ctx"; c.N = n; c.R = "" })
			case meta.Event == "UserPromptSubmit" && stdout != "":
				n := len(stdout)
				updateCrumb(home, meta.SessionID, func(c *statusCrumb) { c.A = "ctx"; c.N = n })
			}
		}
	}
	if codexHookRequested(args) {
		stdout = codexHookOutput(hookRoot(absRoot, data), data, stdout)
	}
	if stdout != "" {
		fmt.Print(stdout)
	}
	if stderr != "" {
		fmt.Fprintln(os.Stderr, stderr)
	}
	os.Exit(code)
}

func codexHookRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--codex" {
			return true
		}
	}
	return false
}

func codexHookOutput(absRoot string, input []byte, context string) string {
	ev := decodeLifecycleEvent(input)
	if ev.Event != "SessionStart" {
		return context
	}
	payload := map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":     "SessionStart",
			"additionalContext": context,
		},
	}
	if statusLinkRoot(absRoot) != "" {
		_ = ensureStatusServer(absRoot, []string{"--session", ev.SessionID}, false)
		payload["systemMessage"] = "ProjX live status: http://" + statusDashboardAddr + "/"
	}
	out, _ := json.Marshal(payload)
	return string(out)
}

func lastTargetRoot(absRoot string, targets []string) string {
	for i := len(targets) - 1; i >= 0; i-- {
		if tr := targetStoreRoot(absRoot, targets[i]); isProjxDir(tr) {
			return tr
		}
	}
	return ""
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
	ev := decodeLifecycleEvent(input)
	sid := ev.SessionID
	if sid == "" {
		sid = "default"
	}

	// A spawned worker (PROJX_ROLE=worker) gets an executor directive prepended so it
	// does the task directly instead of obeying the trunk's "dispatch, don't mutate" law.
	// The directive text is a real, editable store record (store.WorkerDirectiveText),
	// not a hardcoded constant — fetched fresh so a `store commit` change takes effect
	// immediately, no recompile.
	frame := func(ctx string) string {
		if ctx != "" && os.Getenv("PROJX_ROLE") == "worker" {
			wst, err := openStoreExistingSafe(absRoot)
			if err != nil {
				return ctx
			}
			wd := store.WorkerDirectiveText(wst)
			wst.Close()
			return wd + ctx
		}
		return ctx
	}

	switch ev.Event {
	case "SessionStart":
		// Refresh the code map (silently), then inject the lean floor. Codex owns
		// dashboard presentation in its separate systemMessage hook.
		_, _, _, _ = syncMap(absRoot, nil)
		ctx := reconciliationPrompt(absRoot) + buildSessionContext(absRoot, sid, "", false)
		markGovernedRecall(absRoot, sid, "")
		if disp := surfaceFinishedDispatches(absRoot); disp != "" {
			ctx = disp + "\n" + ctx
		}
		if ctx != "" {
			return wrapProjectContext(frame(withOverrideNotice(absRoot, ctx))), "", 0
		}
		return "", "", 0

	case "UserPromptSubmit":
		// FLOATING context: inject the ACTIVE project's knowledge, not just the cwd's.
		// The active project is resolved from the prompt (an explicit path) or from what
		// any agent has been touching this session — so working on Sessions pulls the
		// Sessions store, working on Evolution pulls Evolution. openStore composes the
		// global floor over whichever project, so law is injected either way.
		root := activeContextRoot(absRoot, sid, ev.Prompt)
		ctx := reconciliationPrompt(root) + buildSessionContext(root, sid, ev.Prompt, false)
		markGovernedRecall(absRoot, sid, ev.Prompt)
		ctx = pendingLearnNotice(root, sid) + ctx
		// NEXT-PROMPT SURFACE: any detached dispatch that finished but hasn't been
		// reported gets a concise summary prepended here (and flipped Reported=true), so
		// the background result reaches Nick on his next turn without polling.
		if disp := surfaceFinishedDispatches(absRoot); disp != "" {
			ctx = disp + "\n" + ctx
		}
		if ctx != "" {
			return wrapProjectContext(frame(withOverrideNotice(root, ctx))), "", 0
		}
		return "", "", 0

	case "PreToolUse":
		targets := hookTargetPaths(ev)
		if err := enforceParallelWorkerLease(absRoot, ev, targets); err != nil {
			return "", err.Error(), 2
		}
		path := ev.ToolInput.FilePath
		if path == "" && len(targets) > 0 {
			path = targets[0]
		}
		// TARGET-based scope (adr/scope-resolution-is-target-based): the rules that apply
		// are the ones of the project CONTAINING the file being touched, not the process
		// cwd. Walk up from the target path to its owning .projx; fall back to cwd only
		// when there is no file_path (a session-level tool). The global floor still fires
		// because openStore always composes the per-user store over this project.
		storeRoot := targetStoreRoot(absRoot, path)

		// Trunk-dispatch gate: in dispatcher-mode the TRUNK does not mutate files —
		// every change is routed to a spawned tier-agent. A projx-spawned worker
		// (PROJX_ROLE=worker) is exempt. This is a policy gate, NOT the cage. Off unless
		// the setting/dispatcher-mode record is affirmative, so it never blocks by default.
		// Resolved from the TARGET's store: editing a file in a repo without dispatcher-mode
		// is allowed even when cwd is a repo that has it on.
		// FAIL CLOSED (doc/enforcement-follow-override-plan A): the gate opens the store
		// non-fatally. If it can't open, we BLOCK (exit 2) instead of crashing with exit 1
		// — which Claude Code treats as non-blocking, i.e. silently fail-open. The safety
		// floor must deny when it cannot prove the action is allowed.
		st, err := openStoreExistingSafe(storeRoot)
		if err != nil {
			return "", fmt.Sprintf("ProjX gate: store unavailable (%v) — failing closed, action blocked.", err), 2
		}
		defer st.Close()
		if cp, err := refreshReconciliation(storeRoot, false); err == nil {
			if msg, blocked := reconciliationBlocksTargets(st, cp.Issues, targets); blocked {
				return "", msg, 2
			}
		}

		// Override authority is DELEGATED, never self-granted. The AI reaches the engine
		// through this tool hook; a human runs it in their own terminal (which does not).
		// So: block any AI-initiated attempt to run `override` OR to flip the delegation
		// flag itself, UNLESS the human has already delegated (setting/override-authority
		// on). This is what stops the AI from writing its own permission slip.
		if bashAttemptsSelfAuthorize(ev.ToolInput.Command) && !store.OverrideAuthorityOn(st) {
			return "", "ProjX: override authority is not delegated. The AI may REQUEST an override but cannot grant its own. Ask the human to authorize — they delegate with `projx-engine store commit --kind gate-rule --key setting/override-authority --body on`, or run the `override` themselves. (This block is by design.)", 2
		}

		if isMutatingHookTool(ev.ToolName) && os.Getenv("PROJX_ROLE") != "worker" {
			if store.DispatcherModeOn(st) {
				// Tier is DATA: soft = overridable with a logged reason (B); a project may
				// retier dispatcher-mode to hard (store record Enforcement=hard) to forbid
				// the override entirely. On a consumed grant we fall through to the hard
				// floor below (an override past dispatcher-mode never bypasses the gate).
				soft := store.IsSoftRule(st, "dispatcher-mode")
				allowed := false
				if soft {
					_, allowed = consumeOverride(storeRoot, "dispatcher-mode")
				}
				if !allowed {
					hint := "ask the human to authorize an override (they delegate with `store commit --kind gate-rule --key setting/override-authority --body on`, then `projx-engine override dispatcher-mode --reason \"<why>\"` proceeds once)."
					if !soft {
						hint = "this rule is HARD here — it cannot be overridden."
					}
					return "", "ProjX dispatcher-mode: the trunk dispatches, it does not edit. Route this to a tier-agent — `projx-engine dispatch --run \"<task>\"` — or " + hint, 2
				}
			}
		}

		// HARD floor: off-limits gate patterns, matched two ways —
		//   (1) the tool's file_path (Read/Edit/Write), and
		//   (2) paths named inside a Bash command line (so `cat .env` can't slip past — C).
		// Neither is overridable: secrets/off-limits are a wall, not a soft rule.
		if pat, denied := gateDeniedPath(st, gateRelPath(storeRoot, absRoot, path)); path != "" && denied {
			return "", fmt.Sprintf("ProjX gate: %q is off-limits by gate rule %q.", path, pat), 2
		}
		for _, target := range targets {
			targetRoot := targetStoreRoot(absRoot, target)
			if targetRoot == storeRoot {
				if pat, denied := gateDeniedPath(st, gateRelPath(targetRoot, absRoot, target)); denied {
					return "", fmt.Sprintf("ProjX gate: %q is off-limits by gate rule %q.", target, pat), 2
				}
				continue
			}
			targetStore, err := openStoreExistingSafe(targetRoot)
			if err != nil {
				return "", fmt.Sprintf("ProjX gate: store unavailable (%v) - failing closed, action blocked.", err), 2
			}
			pat, denied := gateDeniedPath(targetStore, gateRelPath(targetRoot, absRoot, target))
			targetStore.Close()
			if denied {
				return "", fmt.Sprintf("ProjX gate: %q is off-limits by gate rule %q.", target, pat), 2
			}
		}
		if cmd := strings.TrimSpace(ev.ToolInput.Command); cmd != "" {
			if tok, pat, denied := bashHitsGate(st, storeRoot, absRoot, cmd); denied {
				return "", fmt.Sprintf("ProjX gate: command references %q, off-limits by gate rule %q. Reading/printing secret material is denied.", tok, pat), 2
			}
		}
		if path == "" && len(targets) == 0 {
			return "", "", 0 // a matched tool with no file_path → allow (gate already cleared)
		}
		// Auto-focus: touching a member repo's file focuses the session there, so the
		// next turn's slice leads with that repo — and it SHIFTS when you edit another.
		focusPath := path
		if len(targets) > 0 {
			focusPath = targets[len(targets)-1]
		}
		if repo := repoOfPath(absRoot, focusPath); repo != "" {
			cps := osCheckpoints{absRoot}
			if cp := cps.Load(sid); cp.Focus != repo {
				cp.Focus = repo
				cps.Save(sid, cp)
			}
		}
		if isGovernedMutation(ev) {
			if !markGovernedMutation(absRoot, sid, mutationRoots(absRoot, targets), targets) {
				return "", "ProjX governed turn: checkpoint state is unavailable; failing closed before mutation.", 2
			}
		}
		return "", "", 0

	case "PreCompact":
		_ = buildSessionContext(absRoot, sid, "", true) // reset checkpoint; inject nothing
		return "", "", 0

	case "Stop":
		if msg, block := closeGovernedTurn(absRoot, sid); block {
			return "", msg, 2
		}
		if msg, block := sessionSuggest(absRoot, sid); block {
			return "", msg, 2
		}
		return "", "", 0

	default:
		return "", "", 0 // unknown event → no-op
	}
}

// withOverrideNotice prepends a short banner listing recent soft-rule overrides so
// every deviation the agent made surfaces to the human the next session — the
// "overrides are never silent" half of doc/enforcement-follow-override-plan.
func withOverrideNotice(absRoot, ctx string) string {
	ovs := recentOverrides(absRoot, 5)
	if len(ovs) == 0 {
		return ctx
	}
	var b strings.Builder
	b.WriteString("## Recent rule overrides (soft rules bypassed with a logged reason)\n")
	for _, o := range ovs {
		b.WriteString("- " + o + "\n")
	}
	b.WriteString("\n")
	return b.String() + ctx
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
