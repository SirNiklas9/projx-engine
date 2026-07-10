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
type dispatchStepStat struct {
	Task        string `json:"task"`
	Tier        string `json:"tier"`
	Kind        string `json:"kind"` // agent | deterministic
	Op          string `json:"op,omitempty"`
	ProviderCmd string `json:"provider_cmd,omitempty"`
	State       string `json:"state"` // pending | running | done | failed
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
		m.Steps = append(m.Steps, dispatchStepStat{
			Task:        s.Task,
			Tier:        stepTier(s.Decision),
			Kind:        s.Decision.Kind,
			Op:          s.Decision.Op,
			ProviderCmd: s.Decision.ProviderCmd,
			State:       "pending",
		})
	}
	if err := writeDispatchManifest(absRoot, m); err != nil {
		fmt.Fprintf(os.Stderr, "dispatch: cannot write run manifest: %v\n", err)
		os.Exit(1)
	}

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

		var stepErr error
		if st.Kind == "deterministic" {
			stepErr = runDispatchChild(self, absRoot, deterministicStepArgs(st.Op), "")
		} else {
			agentWork = true
			stepErr = runDispatchChild(self, absRoot, []string{"agent", "run", "--task", st.Task, "--", st.Task}, st.ProviderCmd)
		}
		if stepErr != nil {
			st.State = "failed"
			failed = true
			_ = writeDispatchManifest(absRoot, m)
			fmt.Printf("dispatch %s: step %d FAILED: %v\n", id, i+1, stepErr)
			break
		}
		st.State = "done"
		_ = writeDispatchManifest(absRoot, m)
	}

	// Verify gate on the RESULT — mirrors the old inline gate, now off the trunk.
	switch {
	case failed:
		// leave verify unset; the run already failed at a step
	case agentWork:
		fmt.Printf("\n── dispatch %s: verifying result ──\n", id)
		if err := runDispatchChild(self, absRoot, []string{"verify"}, ""); err != nil {
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
// tier the router chose for this step.
func runDispatchChild(self, absRoot string, argv []string, providerCmd string) error {
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
	cmd.Env = env
	return cmd.Run()
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
		fmt.Fprintf(&b, "- %s: %s — %d/%d steps passed — %q\n",
			m.ID, outcome, done, len(m.Steps), truncateDispatchMsg(m.Message, 60))
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
		printDispatchManifest(m)
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
}

func printDispatchManifest(m *dispatchManifest) {
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
		fmt.Printf("  %d. [%-14s] %-8s %s\n", i+1, s.Tier, s.State, s.Task)
	}
	fmt.Printf("  log: %s\n", dispatchLogPath(mustAbsForLog(m), m.ID))
}

// mustAbsForLog is a tiny shim so printDispatchManifest can render the log path
// without threading absRoot through; the runs dir is always <cwd>/.projx/runs at
// the point status is read.
func mustAbsForLog(_ *dispatchManifest) string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func orDashDispatch(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncateDispatchMsg(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}
