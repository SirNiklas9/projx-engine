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

// runOverrides are ephemeral, highest-precedence provider controls. They are
// deliberately command arguments rather than store writes: one dispatched
// child can use a precise profile without changing Desktop or project defaults.
type runOverrides struct {
	Provider     string
	Model        string
	Effort       string
	NativeEffort string
}

func parseRunArgs(args []string) (dryRun bool, overrides runOverrides, task []string, err error) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			dryRun = true
		case "--provider", "--model", "--effort", "--native-effort":
			if i+1 >= len(args) {
				return false, runOverrides{}, nil, fmt.Errorf("%s requires a value", args[i])
			}
			value := strings.TrimSpace(args[i+1])
			i++
			switch args[i-1] {
			case "--provider":
				overrides.Provider = value
			case "--model":
				overrides.Model = value
			case "--effort":
				overrides.Effort = value
			case "--native-effort":
				overrides.NativeEffort = value
			}
		default:
			task = append(task, args[i])
		}
	}
	return dryRun, overrides, task, nil
}

func (o runOverrides) apply(d *routing.Decision, cfg routing.Config) {
	if o.Provider != "" {
		d.Provider = o.Provider
	}
	if o.Model != "" {
		d.Model = o.Model
	}
	if o.Effort != "" {
		d.Effort = o.Effort
	}
	if o.NativeEffort != "" {
		d.NativeEffort = o.NativeEffort
	}
	if o.Provider != "" || o.Model != "" || o.Effort != "" || o.NativeEffort != "" {
		d.ProviderCmd = "" // structured per-run selection takes precedence over legacy command strings
		requested := routing.Provider{
			Provider: d.Provider, Model: d.Model, Effort: d.Effort, NativeEffort: d.NativeEffort,
		}
		selected, ok := routing.SelectCatalogProfile(cfg.Catalog.Profiles, requested, cfg.Weights[d.Class])
		if ok {
			d.Selection = routing.CatalogSelectionReason(requested, selected)
			d.Provider, d.Model = selected.Provider, selected.Model
			d.Effort, d.NativeEffort = string(selected.Effort), selected.NativeEffort
			d.Availability, d.CatalogSource, d.CatalogUpdatedAt = string(selected.Availability), selected.Source, selected.CheckedAt
		}
		d.Source = firstNonEmpty(d.Source+"+per-run", "per-run")
	}
}

func runRunCmd(absRoot string, args []string) {
	// ── Parse flags ──────────────────────────────────────────────────────────
	dryRun, overrides, rest, parseErr := parseRunArgs(args)
	if parseErr != nil {
		fmt.Fprintf(os.Stderr, "projx-engine run: %v\n", parseErr)
		os.Exit(1)
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
	if d.Kind == "agent" {
		if overrides.Provider != "" && !routing.ProviderEnabled(st, overrides.Provider) {
			st.Close()
			fmt.Fprintf(os.Stderr, "projx-engine run: provider %q is globally disabled\n", overrides.Provider)
			os.Exit(1)
		}
		overrides.apply(&d, cfg)
	}
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
			if cmd != "" {
				fmt.Printf("  provider-cmd: %s\n", cmd)
			} else {
				provider := d.Provider
				if provider == "" {
					provider = "(ambient PROJX_AGENT / claude default)"
				}
				fmt.Printf("  provider: %s\n", provider)
				if d.Profile != "" {
					fmt.Printf("  profile:  %s\n", d.Profile)
				}
				if d.Model != "" {
					fmt.Printf("  model:    %s\n", d.Model)
				}
				if effort := firstNonEmpty(d.NativeEffort, d.Effort); effort != "" {
					fmt.Printf("  effort:   %s\n", effort)
				}
				if d.Selection != "" {
					fmt.Printf("  selection: %s\n", d.Selection)
				}
			}
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
		_ = os.Setenv("PROJX_POLICY_CLASS", d.Class)
		_ = os.Setenv("PROJX_POLICY_PROVIDER", d.Provider)
		_ = os.Setenv("PROJX_POLICY_PROFILE", d.Profile)
		_ = os.Setenv("PROJX_POLICY_MODEL", d.Model)
		_ = os.Setenv("PROJX_AGENT_PROFILE", d.Profile)
		_ = os.Setenv("PROJX_AGENT_EFFORT", firstNonEmpty(d.NativeEffort, d.Effort))
		if d.ProviderCmd != "" {
			providerNote += " (" + d.ProviderCmd + ")"
		}
		fmt.Fprintf(os.Stderr, "projx-engine run: routing to agent — class: %s\n", providerNote)

		// Route-selected provider settings must reach the child launcher. Legacy
		// concrete commands still win; otherwise pass the template-based provider.
		if d.ProviderCmd != "" {
			if err := os.Setenv("PROJX_AGENT_CMD", d.ProviderCmd); err != nil {
				fmt.Fprintf(os.Stderr, "projx-engine run: warning: could not set PROJX_AGENT_CMD: %v\n", err)
			}
			_ = os.Unsetenv("PROJX_AGENT")
			_ = os.Unsetenv("PROJX_AGENT_PROFILE")
			_ = os.Unsetenv("PROJX_AGENT_MODEL")
			_ = os.Setenv("PROJX_POLICY_FALLBACK", "explicit-cmd")
		} else {
			if err := os.Setenv("PROJX_AGENT_CMD", ""); err != nil {
				fmt.Fprintf(os.Stderr, "projx-engine run: warning: could not clear PROJX_AGENT_CMD: %v\n", err)
			}
			if d.Provider != "" {
				if err := os.Setenv("PROJX_AGENT", d.Provider); err != nil {
					fmt.Fprintf(os.Stderr, "projx-engine run: warning: could not set PROJX_AGENT: %v\n", err)
				}
			}
			if d.Profile != "" {
				if err := os.Setenv("PROJX_AGENT_PROFILE", d.Profile); err != nil {
					fmt.Fprintf(os.Stderr, "projx-engine run: warning: could not set PROJX_AGENT_PROFILE: %v\n", err)
				}
			}
			if model := firstNonEmpty(d.Model, d.Profile); model != "" {
				if err := os.Setenv("PROJX_AGENT_MODEL", model); err != nil {
					fmt.Fprintf(os.Stderr, "projx-engine run: warning: could not set PROJX_AGENT_MODEL: %v\n", err)
				}
			}
			_ = os.Setenv("PROJX_POLICY_FALLBACK", "ambient-allowed")
		}

		runAgentCmd(absRoot, []string{"--task", task, "--", task})

	default:
		fmt.Fprintf(os.Stderr, "projx-engine run: internal error: unknown decision kind %q\n", d.Kind)
		os.Exit(1)
	}
}
