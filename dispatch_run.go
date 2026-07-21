package main

// dispatch_run.go — the "trunk never computes" machinery for `dispatch --run`.
//
// The old fan-out ran every step in-process, in order, then blocked on the verify
// gate — so whoever invoked `dispatch --run` (the trunk) was PINNED for the whole
// run and could not accept new instructions. This file makes the run DETACH: the
// foreground call writes a run manifest, spawns a background supervisor, and
// returns immediately with a dispatch id. The supervisor executes each step as
// its own child process (agent steps MUST be children — the agent launcher
// os.Exit()s, so it can't be looped in-process), updates the manifest as it goes,
// runs the verify gate on the result, and records the outcome. `dispatch status`
// and the statusline read the manifests.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// dispatchManifest is the on-disk record of one backgrounded dispatch run.
type dispatchManifest struct {
	ID       string             `json:"id"`
	Message  string             `json:"message"`
	State    string             `json:"state"` // running | done | failed
	Started  time.Time          `json:"started"`
	Finished time.Time          `json:"finished,omitempty"`
	Steps    []dispatchStepStat `json:"steps"`
	Verify   string             `json:"verify,omitempty"` // "" | passed | failed | skipped
	Reported bool               `json:"reported"`         // surfaced to the trunk yet?
	PID      int                `json:"pid,omitempty"`
}

// dispatchStepStat is one step's routing + live status inside a manifest.
//
// The last three fields are populated ONLY for a detached WORKFLOW run (`workflow run
// --detach`), which rides this very manifest rather than forking a parallel format —
// so `dispatch status`, the statusline badge, and the one-shot surface hook treat a
// detached workflow exactly like a detached dispatch. A plain dispatch leaves them
// empty (all omitempty), so its on-disk shape is byte-for-byte unchanged.
type dispatchStepStat struct {
	Task        string `json:"task"`
	Tier        string `json:"tier"`
	Kind        string `json:"kind"` // agent | deterministic
	Op          string `json:"op,omitempty"`
	ProviderCmd string `json:"provider_cmd,omitempty"`
	Role        string `json:"role,omitempty"`    // per-worker ProjX scope: role the worker plays
	State       string `json:"state"`             // pending | running | done | failed
	Mutated     string `json:"mutated,omitempty"` // yes | no | unknown — did the step CHANGE the working tree?

	ID   string   `json:"id,omitempty"`   // workflow: the step's handle (dep target)
	Deps []string `json:"deps,omitempty"` // workflow: ids that must complete first
	Gate string   `json:"gate,omitempty"` // workflow: resolved gate — conformance|behavioral|both ("" = none)
}

// workerScope is the per-worker ProjX scope the supervisor computes for ONE step:
// the ROLE the worker plays plus the TASK that slices its injected store context.
// Each dispatched worker is launched under only this scope — its step's role + the
// task-sliced contract — never the full trunk context. (The context slice itself is
// produced downstream by compileStorePreambleForTask(task); this type is the seam
// the supervisor uses to compute + carry the scope explicitly, per adr/dispatch-run-
// worker-permissions "workers carry ProjX scope".)
type workerScope struct {
	Role string
	Task string
}

// scopeForStep derives the per-worker scope for a routed step: its descriptive role
// plus the task that will slice the worker's context.
func scopeForStep(s dispatchStepStat) workerScope {
	return workerScope{Role: workerRoleForStep(s), Task: s.Task}
}

// workerRoleForStep derives a descriptive role label from the step's routing (its
// tier/kind). This is an OBSERVABILITY/scoping label injected into the worker's
// context so it knows the narrow role it was spawned for; the gate-exemption signal
// stays PROJX_ROLE=worker regardless.
func workerRoleForStep(s dispatchStepStat) string {
	switch {
	case s.Kind == "deterministic":
		if s.Op != "" {
			return "operator:" + s.Op
		}
		return "operator"
	case s.Tier != "" && s.Tier != "agent":
		return s.Tier + " worker"
	default:
		return "worker"
	}
}

func dispatchRunsDir(absRoot string) string { return filepath.Join(absRoot, ".projx", "runs") }
func dispatchManifestPath(absRoot, id string) string {
	return filepath.Join(dispatchRunsDir(absRoot), id+".json")
}
func dispatchLogPath(absRoot, id string) string {
	return filepath.Join(dispatchRunsDir(absRoot), id+".log")
}

func writeDispatchManifest(absRoot string, m *dispatchManifest) error {
	if err := os.MkdirAll(dispatchRunsDir(absRoot), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := dispatchManifestPath(absRoot, m.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dispatchManifestPath(absRoot, m.ID))
}

func readDispatchManifest(absRoot, id string) (*dispatchManifest, error) {
	data, err := os.ReadFile(dispatchManifestPath(absRoot, id))
	if err != nil {
		return nil, err
	}
	var m dispatchManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// listDispatchManifests returns all recorded runs, newest first.
func listDispatchManifests(absRoot string) []*dispatchManifest {
	entries, err := os.ReadDir(dispatchRunsDir(absRoot))
	if err != nil {
		return nil
	}
	var out []*dispatchManifest
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if m, err := readDispatchManifest(absRoot, id); err == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out
}

func newDispatchID() string {
	return fmt.Sprintf("d%s-%04x", time.Now().Format("0102-150405"), os.Getpid()&0xffff)
}

// dispatchSupervisorEnv strips the trunk-only signals so the detached supervisor
// runs as a clean orchestrator (not restricted agent-context, not a gated worker).
func dispatchSupervisorEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, "PROJX_AGENT_CONTEXT=") || strings.HasPrefix(e, "PROJX_ROLE=") {
			continue
		}
		out = append(out, e)
	}
	return out
}

// startDetachedDispatch writes the run manifest for the routed steps, spawns a
// DETACHED supervisor, and RETURNS immediately — the trunk is never pinned. This
// is the whole point: the caller gets a dispatch id and control back.
func startDetachedDispatch(absRoot string, steps []dispatchStep, message string) {
	id := newDispatchID()
	m := &dispatchManifest{
		ID:      id,
		Message: message,
		State:   "running",
		Started: time.Now(),
		Steps:   make([]dispatchStepStat, 0, len(steps)),
	}
	for _, s := range steps {
		stat := dispatchStepStat{
			Task:        s.Task,
			Tier:        stepTier(s.Decision),
			Kind:        s.Decision.Kind,
			Op:          s.Decision.Op,
			ProviderCmd: s.Decision.ProviderCmd,
			State:       "pending",
		}
		stat.Role = workerRoleForStep(stat) // per-worker ProjX scope (role) computed up front
		m.Steps = append(m.Steps, stat)
	}
	if err := writeDispatchManifest(absRoot, m); err != nil {
		fmt.Fprintf(os.Stderr, "dispatch: cannot write run manifest: %v\n", err)
		os.Exit(1)
	}
	registerDispatchRoot(absRoot) // make this project's live runs visible to the cross-project statusline

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatch: cannot resolve own path: %v\n", err)
		os.Exit(1)
	}
	logf, err := os.Create(dispatchLogPath(absRoot, id))
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatch: cannot open run log: %v\n", err)
		os.Exit(1)
	}
	defer logf.Close()

	cmd := exec.Command(self, "--root", absRoot, "__dispatch-run", id)
	cmd.Dir = absRoot
	cmd.Stdin = nil
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Env = dispatchSupervisorEnv(os.Environ())
	cmd.SysProcAttr = detachSysProcAttr()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "dispatch: cannot start supervisor: %v\n", err)
		os.Exit(1)
	}
	m.PID = cmd.Process.Pid
	_ = writeDispatchManifest(absRoot, m)
	// Release, never Wait — waiting would re-pin the trunk, defeating the purpose.
	_ = cmd.Process.Release()

	fmt.Printf("\ndispatch %s started — %d task(s) running in the background. Trunk is free.\n", id, len(steps))
	fmt.Printf("  status: projx-engine dispatch status %s\n", id)
	fmt.Printf("  log:    %s\n", dispatchLogPath(absRoot, id))
}

// runDispatchSupervise is the detached background worker (`__dispatch-run <id>`).
func runDispatchSupervise(absRoot string, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: projx-engine __dispatch-run <id>")
		os.Exit(1)
	}
	id := args[0]
	m, err := readDispatchManifest(absRoot, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatch-run: cannot load manifest %s: %v\n", id, err)
		os.Exit(1)
	}
	m.PID = os.Getpid()
	_ = writeDispatchManifest(absRoot, m)

	self, _ := os.Executable()
	agentWork := false
	failed := false
	for i := range m.Steps {
		st := &m.Steps[i]
		st.State = "running"
		_ = writeDispatchManifest(absRoot, m)
		fmt.Printf("\n── dispatch %s: step %d/%d [%s] %s\n", id, i+1, len(m.Steps), st.Tier, st.Task)

		// GROUND TRUTH, before/after: whether the repo changed is decided by git, not by
		// the step's own account of itself. Captured for BOTH kinds — a deterministic op
		// that quietly no-ops is precisely the failure this detects.
		beforeGit, beforeOK := gitFingerprint(absRoot)

		var stepErr error
		if st.Kind == "deterministic" {
			stepErr = runDispatchChild(self, absRoot, deterministicStepArgs(st.Op), "", nil)
		} else {
			agentWork = true
			// Per-worker ProjX scope: the child is launched with --task <this step> so
			// its injected store context is SLICED to this step (compileStorePreambleForTask),
			// and PROJX_WORKER_ROLE carries the step's role so the worker knows the narrow
			// job it was spawned for — not the whole trunk context.
			sc := scopeForStep(*st)
			stepErr = runDispatchChild(self, absRoot,
				[]string{"agent", "run", "--task", sc.Task, "--", sc.Task},
				st.ProviderCmd,
				[]string{"PROJX_WORKER_ROLE=" + sc.Role})
		}
		// Record the verdict even for a FAILED step: a step that half-wrote the tree before
		// dying is worth knowing about, and it costs one git call either way.
		afterGit, afterOK := gitFingerprint(absRoot)
		st.Mutated = mutationBetween(beforeGit, beforeOK, afterGit, afterOK)

		if stepErr != nil {
			st.State = "failed"
			failed = true
			_ = writeDispatchManifest(absRoot, m)
			fmt.Printf("dispatch %s: step %d FAILED: %v\n", id, i+1, stepErr)
			break
		}
		st.State = "done"
		_ = writeDispatchManifest(absRoot, m)
		if silentAgentStep(*st) {
			// Loud at the moment it happens, not just in the final tally — this line is
			// what makes a future misroute self-announcing in the run log.
			fmt.Printf("dispatch %s: ⚠ step %d ran as an AGENT but changed NOTHING — no-op misroute or refusal; read the output above.\n", id, i+1)
		} else if stepChangedNothing(*st) {
			fmt.Printf("dispatch %s: step %d changed nothing (%s op).\n", id, i+1, orDashDispatch(st.Op))
		}
	}

	// Verify gate on the RESULT — mirrors the old inline gate, now off the trunk.
	switch {
	case failed:
		// leave verify unset; the run already failed at a step
	case agentWork:
		fmt.Printf("\n── dispatch %s: verifying result ──\n", id)
		if err := runDispatchChild(self, absRoot, []string{"verify"}, "", nil); err != nil {
			m.Verify = "failed"
			failed = true
		} else {
			m.Verify = "passed"
		}
	default:
		m.Verify = "skipped"
	}

	m.Finished = time.Now()
	if failed {
		m.State = "failed"
	} else {
		m.State = "done"
	}
	_ = writeDispatchManifest(absRoot, m)
	fmt.Printf("\ndispatch %s: %s (verify: %s)\n", id, m.State, orDashDispatch(m.Verify))
}

// deterministicStepArgs maps a routed deterministic op to its CLI argv.
func deterministicStepArgs(op string) []string {
	switch op {
	case "verify":
		return []string{"verify"}
	case "store log":
		return []string{"store", "log"}
	case "store list":
		return []string{"store", "list"}
	}
	return []string{"status"} // harmless fallback for an unknown op
}

// runDispatchChild runs one projx-engine subcommand as a child and waits for it.
// providerCmd, when set, becomes PROJX_AGENT_CMD so the agent launcher uses the
// tier the router chose for this step. extraEnv carries the per-worker scope (e.g.
// PROJX_WORKER_ROLE) so each child is launched under only its step's scope.
func runDispatchChild(self, absRoot string, argv []string, providerCmd string, extraEnv []string) error {
	full := append([]string{"--root", absRoot}, argv...)
	cmd := exec.Command(self, full...)
	cmd.Dir = absRoot
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	env := os.Environ()
	if providerCmd != "" {
		env = append(env, "PROJX_AGENT_CMD="+providerCmd)
	}
	env = append(env, extraEnv...)
	cmd.Env = env
	return cmd.Run()
}

// Mutation values recorded on a step: did the repo actually CHANGE?
const (
	mutatedYes     = "yes"
	mutatedNo      = "no"
	mutatedUnknown = "unknown"
)

// gitOutput runs one read-only git command in absRoot and returns its trimmed
// stdout. ok=false means git is absent, absRoot is not a repo, or git errored —
// every caller treats that as "unknown", never as a failure.
func gitOutput(absRoot string, args ...string) (string, bool) {
	cmd := exec.Command("git", args...)
	cmd.Dir = absRoot
	cmd.Stdin = nil
	out, err := cmd.Output() // stderr discarded on purpose: a non-repo is not an error here
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// gitFingerprint captures the repo's state as a comparable string: HEAD's sha plus
// the porcelain working-tree status. Both halves matter — status alone would call a
// step that COMMITTED its work "no changes", and HEAD alone would miss uncommitted
// edits. Returns ok=false when there is no git to ask (see gitOutput).
//
// This is GROUND TRUTH: it is what the repo looks like, not what the step claims it
// did. It works identically for agent steps and deterministic ops, which is the whole
// reason the mutation signal is taken from git rather than from the step's own report.
func gitFingerprint(absRoot string) (string, bool) {
	if _, ok := gitOutput(absRoot, "rev-parse", "--is-inside-work-tree"); !ok {
		return "", false
	}
	// HEAD is absent (and rev-parse fails) in a repo with no commits yet — that is a
	// legitimate repo, so tolerate the miss and let the status half carry the signal.
	head, _ := gitOutput(absRoot, "rev-parse", "HEAD")
	status, ok := gitOutput(absRoot, "status", "--porcelain")
	if !ok {
		return "", false
	}
	return head + "\x00" + dropEngineStateLines(status), true
}

// dropEngineStateLines removes the engine's OWN bookkeeping from a porcelain status.
// The supervisor rewrites .projx/runs/<id>.json on every step transition, so without
// this every step would fingerprint as "mutated" purely because dispatch was watching
// itself. This repo gitignores .projx/ (so the lines never appear), but a project that
// does not must not get a false "yes" — the filter makes the signal independent of
// whether .projx happens to be ignored.
func dropEngineStateLines(status string) string {
	if status == "" {
		return ""
	}
	var keep []string
	for _, ln := range strings.Split(status, "\n") {
		// Porcelain v1 is "XY <path>"; a rename is "R  <old> -> <new>". Path starts at 3.
		p := ln
		if len(ln) > 3 {
			p = ln[3:]
		}
		p = strings.TrimPrefix(strings.TrimSpace(p), "\"")
		if strings.HasPrefix(p, ".projx/") || p == ".projx" {
			continue
		}
		keep = append(keep, ln)
	}
	return strings.Join(keep, "\n")
}

// mutationBetween turns two fingerprints into the recorded mutation value. Unknown is
// contagious on purpose: if either side could not be read we say so rather than guess,
// because a wrong "no" is exactly the silent lie this whole change exists to kill.
func mutationBetween(before string, beforeOK bool, after string, afterOK bool) string {
	if !beforeOK || !afterOK {
		return mutatedUnknown
	}
	if before == after {
		return mutatedNo
	}
	return mutatedYes
}

// stepChangedNothing reports a step that PROVABLY did not touch the repo. Unknown is
// deliberately excluded — we only make the claim when git actually told us.
func stepChangedNothing(s dispatchStepStat) bool { return s.Mutated == mutatedNo }

// silentAgentStep is the loud case: routing sent this step to an AGENT — the entire
// point of which was to change code — and the repo came back byte-identical. That is
// either a no-op misroute (the Jul 16 incident: a task keyword-swallowed into a
// read-only op, reported as success) or an agent refusal. Both need a human to look;
// neither is an engine error, which is why this is a LABEL + WARNING and not a failed
// state. See stepOutcomeLabel.
func silentAgentStep(s dispatchStepStat) bool {
	return s.Kind == "agent" && s.State == "done" && stepChangedNothing(s)
}

// stepOutcomeLabel renders a step's state at its HONEST strength. A bare "done" reads
// as "the work happened" — so a step that changed nothing must not get one. Unknown
// keeps the plain label: we have no evidence either way, and the manifest field records
// the uncertainty for anyone who looks.
func stepOutcomeLabel(s dispatchStepStat) string {
	if s.State != "done" || !stepChangedNothing(s) {
		return s.State
	}
	if s.Kind == "agent" {
		return "done (NO CHANGES)"
	}
	return "done (no changes)"
}

// silentAgentSteps returns the 1-based indexes of every agent step that changed nothing.
func silentAgentSteps(m *dispatchManifest) []int {
	var out []int
	for i, s := range m.Steps {
		if silentAgentStep(s) {
			out = append(out, i+1)
		}
	}
	return out
}

// surfaceFinishedDispatches builds a short summary of background dispatch runs that
// have FINISHED (done|failed) but not yet been surfaced (Reported=false), then flips
// them to Reported=true so each finished run reaches the human EXACTLY ONCE via the
// lifecycle hook — no polling. Returns "" when there is nothing new to report. This is
// the "next-prompt surface": the hook prepends this to the injected session context.
func surfaceFinishedDispatches(absRoot string) string {
	runs := listDispatchManifests(absRoot)
	var pending []*dispatchManifest
	for _, m := range runs {
		if m.Reported {
			continue
		}
		if m.State == "done" || m.State == "failed" {
			pending = append(pending, m)
		}
	}
	if len(pending) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Background dispatch finished (surfaced once)\n")
	for _, m := range pending {
		done := 0
		for _, s := range m.Steps {
			if s.State == "done" {
				done++
			}
		}
		outcome := m.State
		if m.Verify != "" {
			outcome += ", verify " + m.Verify
		}
		// "N/N steps passed" is the sentence that made the Jul 16 no-ops look like wins;
		// qualify it the moment git says some of those steps changed nothing.
		quiet := 0
		for _, s := range m.Steps {
			if s.State == "done" && stepChangedNothing(s) {
				quiet++
			}
		}
		tally := fmt.Sprintf("%d/%d steps passed", done, len(m.Steps))
		if quiet > 0 {
			tally += fmt.Sprintf(" (%d changed nothing)", quiet)
		}
		fmt.Fprintf(&b, "- %s: %s — %s — %q\n",
			m.ID, outcome, tally, truncateDispatchMsg(m.Message, 60))
		if n := silentAgentSteps(m); len(n) > 0 {
			fmt.Fprintf(&b, "  ⚠ step(s) %s ran as an AGENT and changed NOTHING — no-op misroute or refusal. Needs a human.\n",
				joinInts(n))
		}
		fmt.Fprintf(&b, "  full output: %s\n", dispatchLogPath(absRoot, m.ID))
	}
	// Mark reported AFTER composing so a write failure does not drop the summary; a
	// failed flip just means it surfaces again next turn (at-least-once), never lost.
	for _, m := range pending {
		m.Reported = true
		_ = writeDispatchManifest(absRoot, m)
	}
	return b.String()
}

// runDispatchStatus implements `dispatch status [id]`.
func runDispatchStatus(absRoot string, args []string) {
	if len(args) > 0 {
		m, err := readDispatchManifest(absRoot, args[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "dispatch: no run %q: %v\n", args[0], err)
			os.Exit(1)
		}
		printDispatchManifest(absRoot, m)
		return
	}
	runs := listDispatchManifests(absRoot)
	if len(runs) == 0 {
		fmt.Println("dispatch: no background runs recorded.")
		return
	}
	fmt.Printf("%-18s %-8s %-8s %-7s %s\n", "ID", "STATE", "VERIFY", "STEPS", "MESSAGE")
	for _, m := range runs {
		done := 0
		for _, s := range m.Steps {
			if s.State == "done" {
				done++
			}
		}
		fmt.Printf("%-18s %-8s %-8s %d/%-5d %s\n",
			m.ID, m.State, orDashDispatch(m.Verify), done, len(m.Steps), truncateDispatchMsg(m.Message, 46))
	}
	// The table's "done" column cannot say this, so say it under the table rather than
	// let a silent agent step hide behind a healthy-looking row.
	for _, m := range runs {
		if n := silentAgentSteps(m); len(n) > 0 {
			fmt.Printf("⚠ %s: %d agent step(s) changed nothing — `dispatch status %s`\n", m.ID, len(n), m.ID)
		}
	}
}

func printDispatchManifest(absRoot string, m *dispatchManifest) {
	fmt.Printf("dispatch %s — %s\n", m.ID, m.State)
	fmt.Printf("  message:  %s\n", m.Message)
	fmt.Printf("  started:  %s\n", m.Started.Format(time.RFC3339))
	if !m.Finished.IsZero() {
		fmt.Printf("  finished: %s\n", m.Finished.Format(time.RFC3339))
	}
	if m.Verify != "" {
		fmt.Printf("  verify:   %s\n", m.Verify)
	}
	for i, s := range m.Steps {
		fmt.Printf("  %d. [%-14s] %-17s %s\n", i+1, s.Tier, stepOutcomeLabel(s), s.Task)
	}
	// An agent step that changed nothing gets its own line, not a parenthetical — the
	// label alone is easy to skim past, and this is the case that cost real debugging.
	for _, n := range silentAgentSteps(m) {
		fmt.Printf("  ⚠ step %d was routed to an AGENT but left the repo byte-identical.\n", n)
		fmt.Printf("    Either it no-op'd (misroute) or it refused. Neither is visible from \"done\" — check the log.\n")
	}
	fmt.Printf("  log: %s\n", dispatchLogPath(absRoot, m.ID))
}

func orDashDispatch(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// joinInts renders step numbers for a warning line: "2" / "2, 4".
func joinInts(ns []int) string {
	parts := make([]string, 0, len(ns))
	for _, n := range ns {
		parts = append(parts, fmt.Sprintf("%d", n))
	}
	return strings.Join(parts, ", ")
}

func truncateDispatchMsg(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}
