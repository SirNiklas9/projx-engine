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

	confine "github.com/BananaLabs-OSS/Pulp-ext-confine/core"
	egress "github.com/BananaLabs-OSS/Pulp-ext-egress/core"
	"github.com/SirNiklas9/projx-engine/internal/secrets"
)

func main() {
	// Wire the Landlock hook BEFORE calling egress.Init(). When the engine is
	// re-exec'd as an egress gateway child (NETGW_MODE=child), Init() will call
	// this hook right before syscall.Exec, applying FS confinement inside the
	// already-established network namespace.
	egress.PreExecHook = confine.ApplyLandlockFromEnv

	// egress.Init() MUST be called before any other dispatch. On Linux, if this
	// binary was re-exec'd as a gateway child (NETGW_MODE=child), Init() handles
	// the child lifecycle (netns setup + PreExecHook + syscall.Exec) and exits.
	// On non-Linux platforms this is a no-op.
	egress.Init()

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
	case "verify-loop":
		runVerifyLoopCmd(absRoot, rest)
	case "context":
		runContextCmd(absRoot, rest)
	case "session-suggest":
		runSessionSuggestCmd(absRoot, rest)
	case "map":
		runMapCmd(absRoot, rest)
	case "route":
		runRouteCmd(absRoot, rest)
	case "init":
		runInitCmd(absRoot, rest)
	case "serve":
		runServeCmd(absRoot, rest)
	case "secret":
		runSecretCmd(rest)
	case "agent":
		runAgentCmd(absRoot, rest)
	case "run":
		runRunCmd(absRoot, rest)
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
//
// Extra path grants are conveyed via environment variables set by runAgentCmd:
//   PROJX_ALLOW_READ  — os.PathListSeparator-separated list of extra RO paths
//   PROJX_ALLOW_WRITE — os.PathListSeparator-separated list of extra RW paths
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

	// Extend the policy with any extra path grants passed from runAgentCmd.
	// These are os.PathListSeparator-separated absolute paths; we filter to
	// existing paths (same as DefaultPolicy does for its own entries).
	if extraRead := os.Getenv("PROJX_ALLOW_READ"); extraRead != "" {
		policy.ReadOnly = append(policy.ReadOnly,
			confine.ExistingPaths(strings.Split(extraRead, string(os.PathListSeparator)))...)
	}
	if extraWrite := os.Getenv("PROJX_ALLOW_WRITE"); extraWrite != "" {
		policy.ReadWrite = append(policy.ReadWrite,
			confine.ExistingPaths(strings.Split(extraWrite, string(os.PathListSeparator)))...)
	}

	// Inject secrets into the process environment BEFORE the confinement
	// boundary is applied. RunConfinedLaunch (core) uses os.Environ() for the
	// exec env, so setting env vars here propagates them into the confined child.
	// Non-fatal: if the store cannot be opened, proceed without injection.
	if st, stErr := secrets.Open(); stErr == nil {
		if vals, vErr := st.Resolve(); vErr == nil {
			for codename, val := range vals {
				_ = os.Setenv(codename, val)
			}
		}
	}

	confine.RunConfinedLaunch(policy, agentArgs) // never returns
}

func usage() {
	fmt.Print(`projx-engine — headless engine CLI

Usage:
  projx-engine [--root <dir>] <command> [args]

Real commands:
  init [stacks...] [--force]              ProjX-enable this project: install the Claude Code
                                            connector (hooks + /projx:* slash commands), seed the
                                            store, and index the code map. One command to turn it on.
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
  gate check <path>                       exit 0 if allowed, 2 if denied by a gate rule
  verify                                  check declared vs actual boundaries
  context                                 print the compiled store context (preamble) to stdout
                                            --task "<prompt>"   task-slice the reference knowledge
                                            --session <id>      session-aware delta (lean floor,
                                                                then only new/changed each turn)
                                            --reset             (PreCompact) mark floor lost; next
                                                                turn re-sends protocol+law+slice
  session-suggest --session <id>          (Stop) suggest committing a flagged @remember (exit 2)
                                            if nothing was stored; silent otherwise
  map sync                                parse the project → index every symbol (signature +
                                            doc + file:line anchor) into the store as code-map records
  map list                                show the current code-map records
  route <task>                            print the tier decision (class+source+cmd); no execution
  route pin|floor <tier>                  set a standing pin (hard-lock) or floor (minimum) tier
  route clear pin|floor                   remove a standing routing setting
  route show                              show current pin / floor / keyword signals
  run [--dry-run] <task>                  triage task → deterministic op or agent
                                            --dry-run: print decision, no execution
                                            policy: .projx/routing.json (optional)
  secret set <CODENAME>                   seal a secret (reads value from stdin)
  secret ls                               list sealed secret codenames (no values)
  secret rm <CODENAME>                    delete a sealed secret
  agent run [-- <agent-args>]             launch agent inside sandbox with ambient store context
                                            --allow <bin>         add to exec allowlist
                                            --allow-host <host>   add to egress allowlist
                                            --allow-read <path>   extra FS read grant
                                            --allow-write <path>  extra FS write grant
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
