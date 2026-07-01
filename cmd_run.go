package main

// cmd_run.go — implements `projx-engine run <task>`.
//
// This is the ROUTING pillar (v1 skeleton).  The engine decides — deterministically,
// without the user stating it each time — whether a task needs an AI agent or can
// be handled by a local deterministic engine op.
//
// Routing is performed by internal/routing, which uses a keyword-based policy table
// and a local provider config (.projx/routing.json).  No LLM is called to decide.
//
// Execution:
//   Kind=="deterministic" → calls the existing handler (runVerifyCmd / runStoreCmd).
//   Kind=="agent"         → calls runAgentCmd with the task as a passthrough arg.
//
// Flags:
//   --dry-run   print the decision and return; no handler is executed.

import (
	"fmt"
	"os"
	"strings"

	"github.com/SirNiklas9/projx-engine/internal/routing"
)

func runRunCmd(absRoot string, args []string) {
	// ── Parse flags ──────────────────────────────────────────────────────────
	dryRun := false
	rest := args[:0]
	for _, a := range args {
		if a == "--dry-run" {
			dryRun = true
		} else {
			rest = append(rest, a)
		}
	}

	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "usage: projx-engine run [--dry-run] <task>")
		fmt.Fprintln(os.Stderr, "  --dry-run  print the routing decision without executing anything")
		os.Exit(1)
	}

	task := strings.Join(rest, " ")

	// ── Routing decision ─────────────────────────────────────────────────────
	// The store-backed decider honors standing pin/floor + @-overrides + the
	// project's own keyword signals, and consults the cheap haiku triage for the
	// ambiguous middle (newTriageFunc is nil when no triage endpoint is configured,
	// keeping routing fully deterministic offline).
	cfg := routing.LoadConfig(absRoot)
	st := openStore(absRoot)
	d := routing.DecideWithStore(st, task, cfg, newTriageFunc(absRoot))
	st.Close()

	// ── Dry-run: print decision and return ───────────────────────────────────
	if dryRun {
		fmt.Printf("routing decision:\n")
		fmt.Printf("  kind:   %s\n", d.Kind)
		if d.Kind == "deterministic" {
			fmt.Printf("  op:     %s\n", d.Op)
		} else {
			fmt.Printf("  class:  %s\n", d.Class)
			if d.Source != "" {
				fmt.Printf("  source: %s\n", d.Source)
			}
			cmd := d.ProviderCmd
			if cmd == "" {
				cmd = "(use PROJX_AGENT_CMD or claude)"
			}
			fmt.Printf("  provider-cmd: %s\n", cmd)
		}
		fmt.Printf("  reason: %s\n", d.Reason)
		return
	}

	// ── Execute ──────────────────────────────────────────────────────────────
	switch d.Kind {
	case "deterministic":
		switch d.Op {
		case "verify":
			fmt.Fprintf(os.Stderr, "projx-engine run: routing to deterministic op %q (no agent token spent)\n", d.Op)
			runVerifyCmd(absRoot, nil)
		case "store log":
			fmt.Fprintf(os.Stderr, "projx-engine run: routing to deterministic op %q (no agent token spent)\n", d.Op)
			runStoreCmd(absRoot, []string{"log"})
		case "store list":
			fmt.Fprintf(os.Stderr, "projx-engine run: routing to deterministic op %q (no agent token spent)\n", d.Op)
			runStoreCmd(absRoot, []string{"list"})
		default:
			// Unknown deterministic op — should not happen unless Decide has a bug.
			fmt.Fprintf(os.Stderr, "projx-engine run: internal error: unknown deterministic op %q\n", d.Op)
			os.Exit(1)
		}

	case "agent":
		providerNote := d.Class
		if d.ProviderCmd != "" {
			providerNote += " (" + d.ProviderCmd + ")"
		}
		fmt.Fprintf(os.Stderr, "projx-engine run: routing to agent — class: %s\n", providerNote)

		// Override the agent command when a non-empty provider cmd was resolved.
		if d.ProviderCmd != "" {
			if err := os.Setenv("PROJX_AGENT_CMD", d.ProviderCmd); err != nil {
				fmt.Fprintf(os.Stderr, "projx-engine run: warning: could not set PROJX_AGENT_CMD: %v\n", err)
			}
		}

		// Pass the task twice: --task slices the ambient context to it, and the
		// "--" passthrough hands it to the agent as the prompt (see cmd_agent.go).
		runAgentCmd(absRoot, []string{"--task", task, "--", task})

	default:
		fmt.Fprintf(os.Stderr, "projx-engine run: internal error: unknown decision kind %q\n", d.Kind)
		os.Exit(1)
	}
}
