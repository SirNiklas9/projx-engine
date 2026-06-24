package main

// projx-engine: standalone headless CLI — milestone 1 (extraction skeleton).
// Reuses projx-store, projx-core, projx-verify.
// Every write is journaled (.projx/store-history.jsonl) and undoable.
// Gate rules and recipes are human-only; agents may only write convention/adr/doc/declared-structure.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SirNiklas9/projx-engine/internal/confine"
)

func main() {
	// Multi-call (busybox-style) dispatch — MUST be the very first thing in main.
	// If the binary was invoked under a name other than "projx-engine" AND the
	// jail has set PROJX_REAL_PATH, this is a shim invocation: route through the
	// brokered-exec handler instead of the normal CLI.
	// The PROJX_REAL_PATH guard ensures that:
	//   • Normal CLI use (invoked as "projx-engine") never triggers this path.
	//   • "go test" binaries (named "*.test") never trigger this path.
	//   • Only a jail-launched copy (which always has PROJX_REAL_PATH set) does.
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".exe")
	if base != "projx-engine" && os.Getenv("PROJX_REAL_PATH") != "" {
		runBrokeredExec(base, os.Args[1:]) // never returns (calls os.Exit)
		return
	}

	args := os.Args[1:]

	// __confined-launch must be checked before any other dispatch (including
	// --root extraction) because it replaces this process via syscall.Exec and
	// the policy is encoded in the remaining positional args.
	//
	// Protocol: projx-engine __confined-launch <root> <agentPath> [agentArgs...]
	//
	// On non-Linux platforms this subcommand prints an error and exits 1.
	if len(args) > 0 && args[0] == "__confined-launch" {
		runConfinedLaunchCmd(args[1:]) // never returns
		return
	}

	// Extract global --root flag before subcommand dispatch.
	root := "."
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--root" {
			root = args[i+1]
			args = append(args[:i], args[i+2:]...)
			break
		}
	}

	if len(args) == 0 {
		usage()
		os.Exit(0)
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		die("%v", err)
	}

	cmd, rest := args[0], args[1:]

	// Agent-context restricted mode: when PROJX_AGENT_CONTEXT=1, only a narrow
	// allow-set of read + agent-commit operations is permitted.  Everything that
	// could escalate (gate, secret, agent-launch, run, destructive store ops) is
	// denied here, before any subcommand logic executes.
	if os.Getenv("PROJX_AGENT_CONTEXT") == "1" {
		enforceAgentContext(cmd, rest)
	}

	switch cmd {
	case "store":
		runStoreCmd(absRoot, rest)
	case "gate":
		runGateCmd(absRoot, rest)
	case "verify":
		runVerifyCmd(absRoot, rest)
	case "secret":
		runSecretCmd(rest)
	case "agent":
		runAgentCmd(absRoot, rest)
	case "run":
		runRunCmd(rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(1)
	}
}

// runConfinedLaunchCmd implements `projx-engine __confined-launch <root> <agent> [args...]`.
//
// It builds a DefaultPolicy from <root> and then calls confine.RunConfinedLaunch,
// which applies Landlock and replaces this process via syscall.Exec.
// On non-Linux this always exits 1 (fail-closed).
func runConfinedLaunchCmd(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "projx-engine: __confined-launch requires <root> <agent> [args...]")
		os.Exit(1)
	}
	root := args[0]
	agentArgs := args[1:] // agentArgs[0] is the agent binary path

	// jailDir comes from the environment so the caller can pass it without
	// adding more positional args.
	jailDir := os.Getenv("PROJX_JAIL_DIR")
	agentDir := ""
	if len(agentArgs) > 0 {
		agentDir = filepath.Dir(agentArgs[0])
	}

	policy := confine.DefaultPolicy(root, jailDir, agentDir)
	confine.RunConfinedLaunch(policy, agentArgs) // never returns
}

func usage() {
	fmt.Print(`projx-engine — headless engine CLI (milestone 1: extraction skeleton)

Usage:
  projx-engine [--root <dir>] <command> [args]

Real commands:
  store get <id>                          get a record by id
  store list [--kind <name>] [--scope ..]  list records
  store commit --kind .. --key .. --body ..  write a record
  store rm <id>                           remove a record (journaled)
  store log                               show store history (seq numbers shown)
  store undo                              undo last store change
  store revert <seq>                      git-revert: invert a prior revision
  store cherry-pick <seq>                 re-apply a prior revision's effect
  store checkout <seq>                    read-only view of store state at seq
  gate add <pattern>                      add a gate rule
  gate list                               list gate rules
  gate rm <id-or-pattern>                 remove a gate rule
  verify                                  check declared vs actual boundaries

Stubbed (not yet):
  secret set <CODENAME>
  secret ls
  agent run [prompt]
  run <task>
`)
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "projx-engine: "+format+"\n", a...)
	os.Exit(1)
}

// enforceAgentContext is called when PROJX_AGENT_CONTEXT=1.  It inspects cmd
// and (for store) the first element of rest to decide whether the invocation is
// permitted.  Denied commands print a message to stderr and exit 1.
//
// ALLOW (reads + agent-safe commits):
//   store get, store list, store query, store log, store checkout
//   store commit  — permitted, but --by is forced to "agent" inside storeCommit
//
// DENY (escalation / deletion / history-rewrite):
//   gate (any subcommand), secret, agent, run
//   store rm, store undo, store revert, store cherry-pick
func enforceAgentContext(cmd string, rest []string) {
	deny := func(label string) {
		fmt.Fprintf(os.Stderr, "projx-engine: %q is not permitted in agent context\n", label)
		os.Exit(1)
	}

	// Default-DENY: only known-safe commands pass; everything else (gate, secret,
	// agent, run, and any unknown/future command) is refused.
	switch cmd {
	case "store":
		if len(rest) == 0 {
			// No subcommand — usage will handle it; let through.
			return
		}
		sub := rest[0]
		switch sub {
		case "get", "list", "query", "log", "checkout", "commit":
			// Allowed.  commit's --by override is handled inside storeCommit.
		default:
			// rm, undo, revert, cherry-pick, and anything unknown.
			deny("store " + sub)
		}
	case "verify":
		// Read-only check — allowed.
	default:
		// gate, secret, agent, run, and anything else.
		deny(cmd)
	}
}
