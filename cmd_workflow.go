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
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/SirNiklas9/projx-engine/internal/routing"
)

// WorkflowManifest is a declarative, ordered set of steps. The file order IS the
// execution order; deps are validated (each must reference an EARLIER step) so the
// authored order is guaranteed to be a valid topological order — no scheduler, no
// reasoning, fully deterministic.
type WorkflowManifest struct {
	Name  string         `json:"name,omitempty"`
	Steps []WorkflowStep `json:"steps"`
}

// WorkflowStep is one node: a task plus how ProjX should route + gate it.
type WorkflowStep struct {
	ID     string   `json:"id"`             // unique handle, referenced by later deps
	Task   string   `json:"task"`           // what the worker is asked to do
	Tier   string   `json:"tier,omitempty"` // capability-class override; else the store rules route it
	Role   string   `json:"role,omitempty"` // per-worker scope label; else derived from the routing
	Deps   []string `json:"deps,omitempty"` // ids that MUST have completed before this step runs
	Verify bool     `json:"verify,omitempty"`
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
	if err := json.Unmarshal(data, &m); err != nil {
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
		for _, d := range s.Deps {
			if !seen[d] {
				return nil, fmt.Errorf("step %q: dep %q is not an earlier step", s.ID, d)
			}
		}
		seen[s.ID] = true
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
		fmt.Fprintln(os.Stderr, "usage: projx-engine workflow run [--dry-run] <manifest.json>")
		fmt.Fprintln(os.Stderr, "  sequence a declared, ordered set of steps over dispatch + verify")
		os.Exit(1)
	}
	rest := args[1:]
	dry := false
	var path string
	for _, a := range rest {
		switch {
		case a == "--dry-run":
			dry = true
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

	self, _ := os.Executable()
	done := map[string]bool{}
	for i := range m.Steps {
		s := m.Steps[i]
		d := decisions[i]

		// Deps are pre-validated to be earlier steps; this guard makes a gate-aborted
		// run refuse to start a step whose dependency never completed.
		for _, dep := range s.Deps {
			if !done[dep] {
				die("workflow: step %q blocked — dep %q did not complete", s.ID, dep)
			}
		}

		role := strings.TrimSpace(s.Role)
		fmt.Printf("\n── workflow %s: step %d/%d [%s] %s\n",
			workflowName(m), i+1, len(m.Steps), workflowTierLabel(d), s.ID)

		var stepErr error
		if d.Kind == "deterministic" {
			stepErr = runDispatchChild(self, absRoot, deterministicStepArgs(d.Op), "", nil)
		} else {
			if role == "" {
				role = workerRoleForStep(dispatchStepStat{Tier: d.Class, Kind: d.Kind, Op: d.Op})
			}
			stepErr = runDispatchChild(self, absRoot,
				[]string{"agent", "run", "--task", s.Task, "--", s.Task},
				d.ProviderCmd,
				[]string{"PROJX_WORKER_ROLE=" + role})
		}
		if stepErr != nil {
			die("workflow: step %q failed: %v", s.ID, stepErr)
		}

		// PER-STEP verify GATE — reuse verifyloop's conformance check. A failed gate
		// STOPS the workflow here (the whole point of a per-step gate vs one final one).
		if s.Verify {
			fmt.Printf("── workflow %s: step %q verify gate ──\n", workflowName(m), s.ID)
			violations, vErr := verifyViolations(absRoot)
			if vErr != nil {
				die("workflow: step %q verify errored: %v", s.ID, vErr)
			}
			if len(violations) > 0 {
				fmt.Printf("workflow: step %q GATE FAILED — %d violation(s):\n", s.ID, len(violations))
				for _, v := range violations {
					fmt.Printf("  violation: %s\n", v)
				}
				fmt.Printf("workflow: stopped at step %d/%d (%q). Remaining steps NOT run.\n",
					i+1, len(m.Steps), s.ID)
				os.Exit(1)
			}
			fmt.Printf("── workflow %s: step %q gate PASSED\n", workflowName(m), s.ID)
		}

		done[s.ID] = true
	}

	fmt.Printf("\nworkflow %s: DONE — %d/%d steps completed\n", workflowName(m), len(m.Steps), len(m.Steps))
}

func printWorkflowPlan(m *WorkflowManifest, decisions []routing.Decision) {
	fmt.Printf("workflow %s: %d step(s) — sequenced in authored order:\n", workflowName(m), len(m.Steps))
	for i, s := range m.Steps {
		gate := ""
		if s.Verify {
			gate = "  ⟨verify gate⟩"
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
