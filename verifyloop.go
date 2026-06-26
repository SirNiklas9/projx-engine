package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	core "github.com/SirNiklas9/projx-core"
	verify "github.com/SirNiklas9/projx-verify"
)

// verifyViolations runs the declared-structure conformance check and returns the
// violations as formatted strings (empty slice = clean). It is the programmatic
// twin of runVerifyCmd — no printing, no os.Exit — so the verify-loop can use it
// as the deterministic checker.
func verifyViolations(absRoot string) ([]string, error) {
	st := openStore(absRoot)
	defer st.Close()

	proj, _, err := core.ParseDir(absRoot)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	rules := verify.RulesFromStore(st)
	vs := verify.Check(rules, proj)

	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, fmt.Sprintf("%s -> %s violates rule %s->%s (%s)",
			v.Edge.From, v.Edge.To, v.Rule.From, v.Rule.To, v.Rule.Note))
	}
	return out, nil
}

// LoopResult reports the outcome of a verify-loop run.
type LoopResult struct {
	Iterations int
	Clean      bool
	Remaining  []string // violations still present when not Clean
}

// VerifyLoop drives the binding triad's third leg: act -> verify -> repair. It
// runs the agent on the task, checks conformance, and re-runs the agent with the
// violations fed back until the check is clean or maxIters is reached. runAgent
// and check are injected, so the loop is agent-agnostic (any worker) and unit
// testable without a real agent — and the *check* (not the agent's word) is what
// decides success, which is what makes "it did what I asked" a guarantee, not a
// suggestion.
func VerifyLoop(task string, maxIters int, runAgent func(taskWithFeedback string) error, check func() ([]string, error)) (LoopResult, error) {
	if maxIters < 1 {
		maxIters = 1
	}
	var res LoopResult
	feedback := ""
	for res.Iterations < maxIters {
		res.Iterations++
		if err := runAgent(task + feedback); err != nil {
			return res, fmt.Errorf("agent run %d: %w", res.Iterations, err)
		}
		violations, err := check()
		if err != nil {
			return res, fmt.Errorf("verify %d: %w", res.Iterations, err)
		}
		if len(violations) == 0 {
			res.Clean = true
			res.Remaining = nil
			return res, nil
		}
		res.Remaining = violations
		feedback = "\n\nThe previous attempt left these conformance violations — fix ONLY these:\n- " +
			strings.Join(violations, "\n- ")
	}
	return res, nil // reached maxIters with violations remaining (Clean=false)
}

// runVerifyLoopCmd implements `projx-engine verify-loop [--max N] -- <task>`:
// run the configured agent on the task, verify, re-feed any conformance
// violations, and repeat until clean or N iterations. The CHECK decides success,
// not the agent's claim — the binding-triad's third leg.
func runVerifyLoopCmd(absRoot string, args []string) {
	maxIters := 3
	caged := false
	var taskParts []string
	for i := 0; i < len(args); {
		switch args[i] {
		case "--caged":
			caged = true
			i++
		case "--max":
			i++
			if i >= len(args) {
				die("verify-loop: --max requires an argument")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				die("verify-loop: --max must be a positive integer")
			}
			maxIters = n
			i++
		case "--":
			taskParts = append(taskParts, args[i+1:]...)
			i = len(args)
		default:
			taskParts = append(taskParts, args[i])
			i++
		}
	}
	task := strings.TrimSpace(strings.Join(taskParts, " "))
	if task == "" {
		die("usage: verify-loop [--max N] [--caged] -- <task>")
	}

	res, err := VerifyLoop(task, maxIters,
		func(t string) error {
			if caged {
				return runAgentCaged(absRoot, t) // opt-in, Linux-only
			}
			return runAgentHeadless(absRoot, t) // cross-platform default
		},
		func() ([]string, error) { return verifyViolations(absRoot) },
	)
	if err != nil {
		die("verify-loop: %v", err)
	}
	if res.Clean {
		fmt.Printf("verify-loop: CLEAN after %d iteration(s)\n", res.Iterations)
		return
	}
	fmt.Printf("verify-loop: gave up after %d iteration(s); %d violation(s) remain:\n", res.Iterations, len(res.Remaining))
	for _, v := range res.Remaining {
		fmt.Printf("  violation: %s\n", v)
	}
	os.Exit(1)
}

// runAgentHeadless invokes the configured agent non-interactively on the task.
// PROJX_AGENT_CMD (set by routing per task class, or the profile) carries the
// agent and any print-mode flag; the bare "claude" fallback adds -p. The agent
// works in absRoot; the verify-loop checks the result. Agent-agnostic: the
// command, not this code, knows how each agent runs headless. (Caged headless
// runs via RunCagedAgent are the next integration.)
// runAgentHeadless runs the agent UNCAGED — the cross-platform default. Per
// feedback-cage-optional-not-required, caging is opt-in (--caged → runAgentCaged).
// The agent command is resolved agnostically via resolveAgentArgv (agents.go).
func runAgentHeadless(absRoot, task string) error {
	name, argv, env := agentLaunch(absRoot, task)
	cmd := exec.Command(name, argv...)
	cmd.Dir = absRoot
	cmd.Env = append(os.Environ(), kvSlice(env)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
