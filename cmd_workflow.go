package main

// cmd_workflow.go — the WORKFLOW-DICTATION layer over the harness.
//
// dispatch --run FANS OUT: it decomposes one message into independent tasks and
// runs them (in order, but each standalone) with a SINGLE verify gate on the whole
// result. A workflow is the missing layer above that: a DECLARED, ordered set of
// steps — each with its own task, an optional routing tier/role, optional deps, and
// an optional PER-STEP verify gate — that ProjX SEQUENCES deterministically over the
// EXISTING dispatch/agent/verify machinery. ProjX orchestrates; it does not reason
// about what to do next — the manifest already said. (convention/deterministic-first.)
//
// This is the BOUNDED first increment: a small struct + JSON loader + a
// `workflow run <manifest>` command that walks the steps in authored order, routes
// each through the same store-backed decider `dispatch` uses, launches it via the
// same child machinery (runDispatchChild), and — when a step declares verify:true —
// runs verifyloop's conformance check as a GATE, stopping the run on the first
// failure. It reinvents neither dispatch nor verify; it drives them.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/SirNiklas9/projx-engine/internal/routing"
)

// WorkflowManifest is a declarative, ordered set of steps. The file order IS the
// execution order; deps are validated (each must reference an EARLIER step) so the
// authored order is guaranteed to be a valid topological order — no scheduler, no
// reasoning, fully deterministic.
type WorkflowManifest struct {
	Name     string         `json:"name,omitempty"`
	Parallel bool           `json:"parallel,omitempty"`
	Steps    []WorkflowStep `json:"steps"`
}

// WorkflowStep is one node: a task plus how ProjX should route + gate it.
type WorkflowStep struct {
	ID     string   `json:"id"`               // unique handle, referenced by later deps
	Task   string   `json:"task"`             // what the worker is asked to do
	Tier   string   `json:"tier,omitempty"`   // capability-class override; else the store rules route it
	Role   string   `json:"role,omitempty"`   // per-worker scope label; else derived from the routing
	Deps   []string `json:"deps,omitempty"`   // ids that MUST have completed before this step runs
	Writes []string `json:"writes,omitempty"` // repo-relative files/globs this step may mutate
	Verify bool     `json:"verify,omitempty"`

	// Gate SELECTS which post-step gate runs (SHAPE decision #2, formerly deferred):
	//   "conformance" — verifyloop's boundary/drift conformance check (verifyViolations);
	//   "behavioral"  — the build+test behavioral half (verifyAll's behavior gate/verifyCommand);
	//   "both"        — conformance AND behavioral;
	//   "none"        — no gate.
	// Absent ⇒ current behavior is preserved: a step gates iff Verify:true, and that
	// legacy gate is conformance. A failed gate STOPS the workflow (see resolveStepGate).
	Gate string `json:"gate,omitempty"`
}

// loadWorkflowManifest reads + validates a workflow manifest. Validation is strict
// and deterministic: unique non-empty ids, non-empty tasks, and every dep must point
// at a step declared EARLIER in the file. That last rule makes the authored order a
// valid execution order by construction (a cycle or forward-ref is a load error).
func loadWorkflowManifest(path string) (*WorkflowManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m WorkflowManifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	// A manifest is one declaration, not a JSON stream. Rejecting a second value
	// prevents accidentally accepting a valid first object while ignoring trailing
	// policy supplied by a generator or a hand edit.
	var trailing any
	if err := dec.Decode(&trailing); err == nil {
		return nil, fmt.Errorf("parse manifest: unexpected trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if len(m.Steps) == 0 {
		return nil, fmt.Errorf("manifest has no steps")
	}
	seen := map[string]bool{}
	for i, s := range m.Steps {
		if strings.TrimSpace(s.ID) == "" {
			return nil, fmt.Errorf("step %d: missing id", i+1)
		}
		if seen[s.ID] {
			return nil, fmt.Errorf("step %q: duplicate id", s.ID)
		}
		if strings.TrimSpace(s.Task) == "" {
			return nil, fmt.Errorf("step %q: missing task", s.ID)
		}
		if m.Parallel && len(s.Writes) == 0 {
			return nil, fmt.Errorf("step %q: parallel workflow requires a non-empty writes declaration", s.ID)
		}
		for _, p := range s.Writes {
			if err := validateWorkflowWrite(p); err != nil {
				return nil, fmt.Errorf("step %q: invalid writes entry %q: %w", s.ID, p, err)
			}
		}
		for _, d := range s.Deps {
			if !seen[d] {
				return nil, fmt.Errorf("step %q: dep %q is not an earlier step", s.ID, d)
			}
		}
		switch strings.ToLower(strings.TrimSpace(s.Gate)) {
		case "", "conformance", "behavioral", "both", "none":
			// ok
		default:
			return nil, fmt.Errorf("step %q: invalid gate %q (want conformance|behavioral|both|none)", s.ID, s.Gate)
		}
		seen[s.ID] = true
	}
	if m.Parallel {
		if _, err := workflowBatches(&m); err != nil {
			return nil, err
		}
	}
	return &m, nil
}

// providerCmdForClass resolves the concrete agent command for a capability-class from
// the routing config. Config.Providers is exported, so a pinned tier is honored
// without reaching into the routing package's unexported resolver. "" ⇒ ambient default.
func providerCmdForClass(cfg routing.Config, class string) string {
	for _, p := range cfg.Providers {
		if strings.EqualFold(p.Class, class) {
			return p.Cmd
		}
	}
	return ""
}

// runWorkflowCmd implements `workflow run [--dry-run] <manifest.json>`.
func runWorkflowCmd(absRoot string, args []string) {
	if len(args) == 0 || args[0] != "run" {
		fmt.Fprintln(os.Stderr, "usage: projx-engine workflow run [--dry-run] [--detach] <manifest.json>")
		fmt.Fprintln(os.Stderr, "  sequence a declared, ordered set of steps over dispatch + verify")
		fmt.Fprintln(os.Stderr, "  --detach runs it in the background (surfaced by `dispatch status`), like dispatch --run")
		os.Exit(1)
	}
	rest := args[1:]
	dry := false
	detach := false
	var path string
	for _, a := range rest {
		switch {
		case a == "--dry-run":
			dry = true
		case a == "--detach":
			detach = true
		case path == "":
			path = a
		}
	}
	if path == "" {
		die("workflow run: manifest path required")
	}

	autoSeed(absRoot)
	m, err := loadWorkflowManifest(path)
	if err != nil {
		die("workflow: %v", err)
	}

	// Route every step up front through the SAME store-backed decider dispatch uses,
	// so the plan is fully known (and printable) before anything executes.
	cfg := routing.LoadConfig(absRoot)
	st := openStore(absRoot)
	triage := newTriageFunc(absRoot)
	decisions := make([]routing.Decision, len(m.Steps))
	for i, s := range m.Steps {
		d := routing.DecideWithStore(st, s.Task, cfg, triage)
		if strings.TrimSpace(s.Tier) != "" { // authored tier override → agent at that class
			d.Kind = "agent"
			d.Class = s.Tier
			d.ProviderCmd = providerCmdForClass(cfg, s.Tier)
			d.Source = "workflow-tier"
			d.Reason = "tier pinned by workflow manifest"
		}
		decisions[i] = d
	}
	st.Close()

	printWorkflowPlan(m, decisions)
	if dry {
		fmt.Println("\n(dry-run — re-run without --dry-run to execute)")
		return
	}

	// SHAPE decision #1 (formerly deferred): --detach runs the workflow in the
	// background the SAME way `dispatch --run` does — it writes a run manifest under
	// .projx/runs and spawns a detached supervisor, then RETURNS. Foreground stays the
	// DEFAULT (no flag = the sequential, trunk-pinning behavior below, unchanged).
	if detach {
		startDetachedWorkflow(absRoot, m, decisions)
		return
	}

	self, _ := os.Executable()
	batches, err := workflowBatches(m)
	if err != nil {
		die("workflow: %v", err)
	}
	for _, batch := range batches {
		var before workflowTreeSnapshot
		if m.Parallel {
			before, err = captureWorkflowTree(absRoot)
			if err != nil {
				die("workflow: cannot establish parallel mutation baseline: %v", err)
			}
		}
		errs := make([]error, len(batch))
		var wg sync.WaitGroup
		for bi, i := range batch {
			s, d := m.Steps[i], decisions[i]
			fmt.Printf("\n── workflow %s: step %d/%d [%s] %s\n", workflowName(m), i+1, len(m.Steps), workflowTierLabel(d), s.ID)
			wg.Add(1)
			go func(pos int, s WorkflowStep, d routing.Decision) {
				defer wg.Done()
				errs[pos] = runWorkflowChild(self, absRoot, s, d, m.Parallel)
			}(bi, s, d)
		}
		wg.Wait()
		if m.Parallel {
			after, snapErr := captureWorkflowTree(absRoot)
			if snapErr != nil {
				die("workflow: cannot verify parallel mutations: %v", snapErr)
			}
			steps := make([]WorkflowStep, 0, len(batch))
			for _, i := range batch {
				steps = append(steps, m.Steps[i])
			}
			if mutationErr := workflowChangedPathsAllowed(changedWorkflowPaths(before, after), steps); mutationErr != nil {
				die("workflow: parallel write-set violation: %v", mutationErr)
			}
		}
		for bi, i := range batch {
			s := m.Steps[i]
			if errs[bi] != nil {
				die("workflow: step %q failed: %v", s.ID, errs[bi])
			}
			if gate := resolveStepGate(s); gate != "" {
				fmt.Printf("── workflow %s: step %q %s gate ──\n", workflowName(m), s.ID, gate)
				if runWorkflowGate(absRoot, gate) {
					die("workflow: step %q %s GATE FAILED", s.ID, gate)
				}
				fmt.Printf("── workflow %s: step %q gate PASSED\n", workflowName(m), s.ID)
			}
		}
	}

	fmt.Printf("\nworkflow %s: DONE — %d/%d steps completed\n", workflowName(m), len(m.Steps), len(m.Steps))
}

func runWorkflowChild(self, absRoot string, s WorkflowStep, d routing.Decision, parallel bool) error {
	if d.Kind == "deterministic" {
		return runDispatchChild(self, absRoot, deterministicStepArgs(d.Op), "", nil)
	}
	role := strings.TrimSpace(s.Role)
	if role == "" {
		role = workerRoleForStep(dispatchStepStat{Tier: d.Class, Kind: d.Kind, Op: d.Op})
	}
	env := workflowChildEnv(s, role, parallel)
	return runDispatchChild(self, absRoot, []string{"agent", "run", "--task", s.Task, "--", s.Task}, d.ProviderCmd, env)
}

func workflowChildEnv(s WorkflowStep, role string, parallel bool) []string {
	env := []string{"PROJX_WORKER_ROLE=" + role}
	if parallel {
		env = append(env, parallelWorkerEnv+"=1", "PROJX_WORKER_WRITES="+strings.Join(s.Writes, string(os.PathListSeparator)))
	}
	return env
}

func printWorkflowPlan(m *WorkflowManifest, decisions []routing.Decision) {
	fmt.Printf("workflow %s: %d step(s) — sequenced in authored order:\n", workflowName(m), len(m.Steps))
	for i, s := range m.Steps {
		gate := ""
		if g := resolveStepGate(s); g != "" {
			gate = "  ⟨" + g + " gate⟩"
		}
		dep := ""
		if len(s.Deps) > 0 {
			dep = "  (after " + strings.Join(s.Deps, ",") + ")"
		}
		fmt.Printf("  %d. [%-14s] %s%s%s\n", i+1, workflowTierLabel(decisions[i]), s.ID, dep, gate)
		fmt.Printf("       ↳ %s\n", s.Task)
	}
}

// workflowTierLabel mirrors stepTier's display but for a routed workflow step.
func workflowTierLabel(d routing.Decision) string {
	if d.Kind == "deterministic" {
		return "local:" + d.Op
	}
	if d.Class == "" {
		return "agent"
	}
	return d.Class
}

func workflowName(m *WorkflowManifest) string {
	if strings.TrimSpace(m.Name) != "" {
		return m.Name
	}
	return "(unnamed)"
}

// resolveStepGate maps a step to the CONCRETE gate kind to run after it:
// "conformance" | "behavioral" | "both", or "" for no gate. It preserves the
// pre-SHAPE-#2 default exactly: when the gate field is ABSENT, a step gates iff
// Verify:true, and that legacy gate is conformance. An explicit gate field wins over
// Verify (including gate:"none", which turns the gate off). Values are validated at
// load, so the default arm here is defensive only.
func resolveStepGate(s WorkflowStep) string {
	switch strings.ToLower(strings.TrimSpace(s.Gate)) {
	case "none":
		return ""
	case "conformance", "behavioral", "both":
		return strings.ToLower(strings.TrimSpace(s.Gate))
	case "":
		if s.Verify {
			return "conformance" // legacy verify:true ⇒ conformance (unchanged behavior)
		}
		return ""
	default:
		return ""
	}
}

// runWorkflowGate runs the selected gate kind and reports whether it FAILED. It reuses
// the SAME two checks the rest of the engine gates on — verifyloop's conformance check
// (verifyViolations) and verify's behavioral build+test half (verifyAll behavior-only)
// — never a parallel checker. Shared by the foreground loop and the detached supervisor.
func runWorkflowGate(absRoot, gate string) (failed bool) {
	switch gate {
	case "conformance":
		return gateConformance(absRoot)
	case "behavioral":
		// verifyAll(noBuild=false, behaviorOnly=true) runs ONLY the behavioral half.
		return verifyAll(absRoot, false, true)
	case "both":
		// Evaluate BOTH (don't short-circuit) so the operator sees every failure.
		c := gateConformance(absRoot)
		b := verifyAll(absRoot, false, true)
		return c || b
	default:
		return false
	}
}

// gateConformance runs the boundary/drift conformance check and prints any violations.
// It is silent on success (the caller prints the single PASSED summary line).
func gateConformance(absRoot string) (failed bool) {
	violations, err := verifyViolations(absRoot)
	if err != nil {
		fmt.Printf("workflow: conformance gate errored: %v\n", err)
		return true
	}
	if len(violations) > 0 {
		fmt.Printf("workflow: conformance gate — %d violation(s):\n", len(violations))
		for _, v := range violations {
			fmt.Printf("  violation: %s\n", v)
		}
		return true
	}
	return false
}

// startDetachedWorkflow is SHAPE decision #1's detach path. It mirrors
// startDetachedDispatch: it writes a run manifest under .projx/runs, spawns a DETACHED
// supervisor (`__workflow-run <id>`), and RETURNS immediately with a run id so the
// trunk is never pinned. Crucially it rides the SAME dispatchManifest — extended with
// each step's id/deps/resolved-gate — so `dispatch status`, the statusline badge, and
// the one-shot surface hook treat a detached workflow exactly like a detached dispatch,
// with no parallel manifest format.
func startDetachedWorkflow(absRoot string, m *WorkflowManifest, decisions []routing.Decision) {
	id := newDispatchID()
	dm := &dispatchManifest{
		ID:       id,
		Message:  "workflow: " + workflowName(m),
		State:    "running",
		Started:  time.Now(),
		Steps:    make([]dispatchStepStat, 0, len(m.Steps)),
		Parallel: m.Parallel,
	}
	for i, s := range m.Steps {
		d := decisions[i]
		stat := dispatchStepStat{
			Task:        s.Task,
			Tier:        workflowTierLabel(d),
			Kind:        d.Kind,
			Op:          d.Op,
			ProviderCmd: d.ProviderCmd,
			State:       "pending",
			ID:          s.ID,
			Deps:        s.Deps,
			Gate:        resolveStepGate(s),
			Writes:      s.Writes,
		}
		role := strings.TrimSpace(s.Role)
		if stat.Kind != "deterministic" && role == "" {
			role = workerRoleForStep(dispatchStepStat{Tier: d.Class, Kind: d.Kind, Op: d.Op})
		}
		stat.Role = role
		dm.Steps = append(dm.Steps, stat)
	}
	if err := writeDispatchManifest(absRoot, dm); err != nil {
		fmt.Fprintf(os.Stderr, "workflow: cannot write run manifest: %v\n", err)
		os.Exit(1)
	}
	registerDispatchRoot(absRoot) // make this project's live runs visible to the cross-project statusline

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "workflow: cannot resolve own path: %v\n", err)
		os.Exit(1)
	}
	logf, err := os.Create(dispatchLogPath(absRoot, id))
	if err != nil {
		fmt.Fprintf(os.Stderr, "workflow: cannot open run log: %v\n", err)
		os.Exit(1)
	}
	defer logf.Close()

	cmd := exec.Command(self, "--root", absRoot, "__workflow-run", id)
	cmd.Dir = absRoot
	cmd.Stdin = nil
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Env = dispatchSupervisorEnv(os.Environ())
	cmd.SysProcAttr = detachSysProcAttr()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "workflow: cannot start supervisor: %v\n", err)
		os.Exit(1)
	}
	dm.PID = cmd.Process.Pid
	_ = writeDispatchManifest(absRoot, dm)
	_ = cmd.Process.Release() // Release, never Wait — waiting would re-pin the trunk.

	fmt.Printf("\nworkflow %s started (%s) — %d step(s) running in the background. Trunk is free.\n",
		workflowName(m), id, len(m.Steps))
	fmt.Printf("  status: projx-engine dispatch status %s\n", id)
	fmt.Printf("  log:    %s\n", dispatchLogPath(absRoot, id))
}

// runWorkflowSupervise is the detached background supervisor (`__workflow-run <id>`).
// It mirrors runDispatchSupervise but walks WORKFLOW semantics: it honors per-step deps
// and the per-step SELECTABLE gate (a failed gate STOPS the run), updating the same
// .projx/runs manifest as it goes so the trunk-side surfaces stay live.
func runWorkflowSupervise(absRoot string, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: projx-engine __workflow-run <id>")
		os.Exit(1)
	}
	id := args[0]
	m, err := readDispatchManifest(absRoot, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "workflow-run: cannot load manifest %s: %v\n", id, err)
		os.Exit(1)
	}
	m.PID = os.Getpid()
	_ = writeDispatchManifest(absRoot, m)

	self, _ := os.Executable()
	if detachedWorkflowParallel(m) {
		runParallelWorkflowSupervise(absRoot, id, self, m)
		return
	}
	done := map[string]bool{}
	failed := false
	gateRan := false
	for i := range m.Steps {
		st := &m.Steps[i]

		// Deps are pre-validated to be earlier steps; refuse to start a step whose
		// dependency never completed (e.g. an earlier gate stopped the run).
		blocked := false
		for _, dep := range st.Deps {
			if !done[dep] {
				fmt.Printf("workflow %s: step %q blocked — dep %q did not complete\n", id, st.ID, dep)
				blocked = true
				break
			}
		}
		if blocked {
			st.State = "failed"
			failed = true
			_ = writeDispatchManifest(absRoot, m)
			break
		}

		st.State = "running"
		_ = writeDispatchManifest(absRoot, m)
		fmt.Printf("\n── workflow %s: step %d/%d [%s] %s\n", id, i+1, len(m.Steps), st.Tier, st.ID)

		var stepErr error
		if st.Kind == "deterministic" {
			stepErr = runDispatchChild(self, absRoot, deterministicStepArgs(st.Op), "", nil)
		} else {
			role := st.Role
			if role == "" {
				role = workerRoleForStep(*st)
			}
			stepErr = runDispatchChild(self, absRoot,
				[]string{"agent", "run", "--task", st.Task, "--", st.Task},
				st.ProviderCmd,
				[]string{"PROJX_WORKER_ROLE=" + role})
		}
		if stepErr != nil {
			st.State = "failed"
			failed = true
			_ = writeDispatchManifest(absRoot, m)
			fmt.Printf("workflow %s: step %q failed: %v\n", id, st.ID, stepErr)
			break
		}

		// Per-step SELECTABLE gate — a failed gate stops the run.
		if st.Gate != "" {
			gateRan = true
			fmt.Printf("── workflow %s: step %q %s gate ──\n", id, st.ID, st.Gate)
			if runWorkflowGate(absRoot, st.Gate) {
				st.State = "failed"
				failed = true
				m.Verify = "failed"
				_ = writeDispatchManifest(absRoot, m)
				fmt.Printf("workflow %s: step %q %s GATE FAILED — remaining steps NOT run.\n", id, st.ID, st.Gate)
				break
			}
			fmt.Printf("── workflow %s: step %q gate PASSED\n", id, st.ID)
		}

		st.State = "done"
		done[st.ID] = true
		_ = writeDispatchManifest(absRoot, m)
	}

	// Reflect the run's gate outcome into the shared manifest's verify field so
	// `dispatch status` / the surface hook read a meaningful result.
	if !failed {
		if gateRan {
			m.Verify = "passed"
		} else {
			m.Verify = "skipped"
		}
	}
	m.Finished = time.Now()
	if failed {
		m.State = "failed"
	} else {
		m.State = "done"
	}
	_ = writeDispatchManifest(absRoot, m)
	fmt.Printf("\nworkflow %s: %s (gate: %s)\n", id, m.State, orDashDispatch(m.Verify))
}

func detachedWorkflowParallel(m *dispatchManifest) bool {
	if !m.Parallel {
		return false
	}
	for _, st := range m.Steps {
		if len(st.Writes) == 0 {
			return false
		}
	}
	return true
}

func runParallelWorkflowSupervise(absRoot, id, self string, m *dispatchManifest) {
	plan := &WorkflowManifest{Parallel: true, Steps: make([]WorkflowStep, len(m.Steps))}
	for i, st := range m.Steps {
		plan.Steps[i] = WorkflowStep{ID: st.ID, Task: st.Task, Deps: st.Deps, Writes: st.Writes}
	}
	batches, err := workflowBatches(plan)
	failed, gateRan := err != nil, false
	if err != nil {
		fmt.Printf("workflow %s: schedule failed: %v\n", id, err)
	}
	for _, batch := range batches {
		if failed {
			break
		}
		before, snapErr := captureWorkflowTree(absRoot)
		if snapErr != nil {
			fmt.Printf("workflow %s: cannot establish parallel mutation baseline: %v\n", id, snapErr)
			failed = true
			break
		}
		errs := make([]error, len(batch))
		var wg sync.WaitGroup
		for bi, i := range batch {
			m.Steps[i].State = "running"
			fmt.Printf("\n── workflow %s: step %d/%d [%s] %s\n", id, i+1, len(m.Steps), m.Steps[i].Tier, m.Steps[i].ID)
			wg.Add(1)
			go func(pos int, st dispatchStepStat) {
				defer wg.Done()
				errs[pos] = runDetachedParallelChild(self, absRoot, st)
			}(bi, m.Steps[i])
		}
		_ = writeDispatchManifest(absRoot, m)
		wg.Wait()
		mutationViolation := false
		after, snapErr := captureWorkflowTree(absRoot)
		if snapErr != nil {
			fmt.Printf("workflow %s: cannot verify parallel mutations: %v\n", id, snapErr)
			failed = true
			mutationViolation = true
		} else {
			steps := make([]WorkflowStep, 0, len(batch))
			for _, i := range batch {
				steps = append(steps, plan.Steps[i])
			}
			if mutationErr := workflowChangedPathsAllowed(changedWorkflowPaths(before, after), steps); mutationErr != nil {
				fmt.Printf("workflow %s: parallel write-set violation: %v\n", id, mutationErr)
				failed = true
				mutationViolation = true
			}
		}
		for bi, i := range batch {
			st := &m.Steps[i]
			if errs[bi] != nil {
				st.State = "failed"
				failed = true
				fmt.Printf("workflow %s: step %q failed: %v\n", id, st.ID, errs[bi])
				continue
			}
			if mutationViolation {
				st.State = "failed"
				continue
			}
			if st.Gate != "" {
				gateRan = true
				fmt.Printf("── workflow %s: step %q %s gate ──\n", id, st.ID, st.Gate)
				if runWorkflowGate(absRoot, st.Gate) {
					st.State = "failed"
					failed = true
					m.Verify = "failed"
					continue
				}
				fmt.Printf("── workflow %s: step %q gate PASSED\n", id, st.ID)
			}
			if st.State != "failed" {
				st.State = "done"
			}
		}
		_ = writeDispatchManifest(absRoot, m)
	}
	if !failed {
		if gateRan {
			m.Verify = "passed"
		} else {
			m.Verify = "skipped"
		}
	}
	m.Finished = time.Now()
	if failed {
		m.State = "failed"
	} else {
		m.State = "done"
	}
	_ = writeDispatchManifest(absRoot, m)
	fmt.Printf("\nworkflow %s: %s (gate: %s)\n", id, m.State, orDashDispatch(m.Verify))
}

func runDetachedParallelChild(self, absRoot string, st dispatchStepStat) error {
	if st.Kind == "deterministic" {
		return runDispatchChild(self, absRoot, deterministicStepArgs(st.Op), "", nil)
	}
	role := st.Role
	if role == "" {
		role = workerRoleForStep(st)
	}
	env := []string{"PROJX_WORKER_ROLE=" + role, parallelWorkerEnv + "=1", "PROJX_WORKER_WRITES=" + strings.Join(st.Writes, string(os.PathListSeparator))}
	return runDispatchChild(self, absRoot, []string{"agent", "run", "--task", st.Task, "--", st.Task}, st.ProviderCmd, env)
}

// DELIBERATELY DEFERRED — held for Nick to direct the shape (see adr/workflow-dictation-
// layer). SHAPE #1 (detach), #2 (selectable gate), and #4 (dependency-aware parallel
// waves with declared write sets) are IMPLEMENTED above; these three remain open:
//   3. NO PER-STEP REPAIR — a failed gate STOPS; it does not verify-loop-repair the step.
//      Open: fold VerifyLoop in so a gated step self-repairs up to N iters before failing.
//   5. tier override HARD-SETS the class; it does not re-run store pin/floor/risk-floor.
//      Open: should a manifest tier be a floor (minimum) rather than a hard pin?
//   6. No workflow manifest is persisted to the store (it lives as a file).
//      Open: first-class `workflow/<name>` store records?
