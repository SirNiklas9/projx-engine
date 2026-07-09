package main

// cmd_dispatch.go — `projx-engine dispatch [--run] <message>`.
//
// The FAN-OUT pillar. You state WHAT you want in one plain message —
// "rename the config var, then refactor the auth module, then design a caching layer" —
// and the engine, WITHOUT you naming a model or preambling anything:
//   1. DECOMPOSES the message into discrete tasks (store.Decompose — deterministic
//      connector split; a cheap-model splitter escalates when the words don't cleanly
//      separate and a model endpoint is configured);
//   2. ROUTES each task through the very same store-backed decider `run` uses, so the
//      TIER is chosen 100% by your rules (route records + keyword classifier + pin/floor
//      + @overrides), never by the phrasing of the ask;
//   3. prints the PLAN (task → tier), and with --run FANS OUT — launching one agent per
//      task at its own tier, in order.
//
// Default is plan-only (dry): safe to eyeball what would run and on which model before
// spending a token. A single-task message just routes as one task (no fan-out).

import (
	"fmt"
	"os"
	"strings"

	"github.com/SirNiklas9/projx-engine/internal/routing"
	store "github.com/SirNiklas9/projx-store"
)

// dispatchStep is one decomposed task plus the decision the rules made for it.
type dispatchStep struct {
	Task     string
	Decision routing.Decision
}

func runDispatchCmd(absRoot string, args []string) {
	// `dispatch status [id]` — read background run manifests (never pins the trunk).
	if len(args) > 0 && args[0] == "status" {
		runDispatchStatus(absRoot, args[1:])
		return
	}

	run := false
	rest := args[:0]
	for _, a := range args {
		if a == "--run" {
			run = true
		} else {
			rest = append(rest, a)
		}
	}
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "usage: projx-engine dispatch [--run] <message>")
		fmt.Fprintln(os.Stderr, "  decompose a multi-task message and route each task to its own tier")
		fmt.Fprintln(os.Stderr, "  (plan-only by default; --run fans out one agent per task, in order)")
		os.Exit(1)
	}
	message := strings.Join(rest, " ")

	// 1. DECOMPOSE — split ONLY on explicit task delimiters (bullets / numbers / TASK: /
	//    ---). A single-intent spec with no delimiters stays ONE task. The old cheap-model
	//    splitter is intentionally NOT used here: it shredded a cohesive single-file spec
	//    into mis-routed fragments. To fan out, author explicit delimiters.
	tasks := store.Decompose(message)

	// 2. ROUTE each task through the same decider run uses — the rules pick the tier.
	cfg := routing.LoadConfig(absRoot)
	st := openStore(absRoot)
	triage := newTriageFunc(absRoot)
	steps := make([]dispatchStep, 0, len(tasks))
	for _, t := range tasks {
		steps = append(steps, dispatchStep{Task: t, Decision: routing.DecideWithStore(st, t, cfg, triage)})
	}
	st.Close()

	// 3. PLAN — always print what the rules decided.
	printDispatchPlan(message, steps)
	if !run {
		if len(steps) > 1 {
			fmt.Println("\n(plan only — re-run with `dispatch --run` to fan these out)")
		}
		return
	}

	// FAN OUT — DETACHED. The old path ran the steps + verify inline, pinning the
	// trunk (whoever called `dispatch --run`) for the whole run so no new instruction
	// could queue. Now the fan-out runs in a background supervisor: this call writes
	// the run manifest, launches the supervisor, and RETURNS immediately with a
	// dispatch id. Step-by-step progress, the verify gate, and the final outcome are
	// recorded to the manifest (see `dispatch status`) — off the trunk entirely.
	startDetachedDispatch(absRoot, steps, message)
}

// printDispatchPlan shows the decomposition + the per-task tier the rules chose.
func printDispatchPlan(message string, steps []dispatchStep) {
	if len(steps) < 2 {
		fmt.Printf("dispatch: 1 task (no split)\n")
	} else {
		fmt.Printf("dispatch: %d tasks from your message — each routed by your rules:\n", len(steps))
	}
	for i, s := range steps {
		src := s.Decision.Source
		if src == "" {
			src = s.Decision.Kind
		}
		fmt.Printf("  %d. [%-14s] %s\n", i+1, stepTier(s.Decision), s.Task)
		fmt.Printf("       ↳ %s (%s)\n", s.Decision.Reason, src)
	}
}

// stepTier is the display label for where a task lands — the agent class, or the local
// op for deterministic tasks.
func stepTier(d routing.Decision) string {
	if d.Kind == "deterministic" {
		return "local:" + d.Op
	}
	if d.Class == "" {
		return "agent"
	}
	return d.Class
}
