package main

// cmd_agent.go — implements `projx-engine agent run`.
//
// Launches a resolved agent CLI inside the exec-jail with the ambient store
// context injected. On Linux, when a kernel confiner (Landlock) is available,
// the agent is re-exec'd through the __confined-launch subcommand so the OS
// physically denies reads/writes outside the project root. On other platforms
// the jail is cooperative (PATH-interposition only) — the enforcement banner is
// honest about that limitation either way.
//
// Default allowlist: ["git"]. The caller may extend via --allow <bin> flags.
// Shells (bash/sh/cmd/powershell/pwsh) are NEVER in the default allowlist; the
// user must pass --allow explicitly if they truly want one (their call).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/SirNiklas9/projx-engine/internal/confine"
	"github.com/SirNiklas9/projx-engine/internal/jail"
	"github.com/SirNiklas9/projx-engine/internal/secrets"
)

func runAgentCmd(absRoot string, args []string) {
	// ── Step 1: parse flags ───────────────────────────────────────────────────
	var allowBins []string   // extra allowlisted exec basenames from --allow
	var allowHosts []string  // extra allowlisted net hostnames from --allow-host
	var passthroughArgs []string

	// Parse --allow and --allow-host flags; everything after "--" is passthrough.
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--":
			passthroughArgs = append(passthroughArgs, args[i+1:]...)
			i = len(args)
		case "--allow":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "projx-engine: --allow requires an argument")
				os.Exit(1)
			}
			allowBins = append(allowBins, args[i])
			i++
		case "--allow-host":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "projx-engine: --allow-host requires an argument")
				os.Exit(1)
			}
			allowHosts = append(allowHosts, args[i])
			i++
		default:
			passthroughArgs = append(passthroughArgs, args[i])
			i++
		}
	}

	// Build the final allowlist: default ["git", "projx-engine"] PLUS any --allow
	// entries.  projx-engine is always present so the agent can use the store tools
	// (store query / store commit) without being explicitly allowlisted.
	// When the jail shims projx-engine, the multi-call guard (base=="projx-engine")
	// routes it through the normal CLI — NOT through runBrokeredExec — so it never
	// needs to be a "brokered" name.
	// Shells are intentionally absent from the default. If the user explicitly
	// passes --allow powershell that is their call; we do not add shells here.
	// Merge the seeded profile (.projx/cage.json) into the allowlists so a seeded
	// project inherits its profile's egress hosts + tools without repeating
	// --allow/--allow-host. Flags still extend it. Agent-agnostic: the profile —
	// not the agent — supplies these defaults.
	allowHosts, allowBins = mergeAllowlists(loadCageConfig(absRoot), allowHosts, allowBins)

	effectiveAllowBins := dedupStrings(append([]string{"git", "projx-engine"}, allowBins...))

	// ── Step 2: resolve the agent command (BEFORE any jail/PATH change) ───────
	// Capture the real PATH now, before we modify anything.
	realPath := os.Getenv("PATH")

	agentAbsPath := os.Getenv("PROJX_AGENT_CMD")
	if agentAbsPath == "" {
		p, err := exec.LookPath("claude")
		if err == nil {
			agentAbsPath = p
		}
	}
	if agentAbsPath == "" {
		fmt.Fprintln(os.Stderr, "projx-engine: no agent found: set PROJX_AGENT_CMD or install an agent CLI on PATH")
		os.Exit(1)
	}
	// Make the agent path absolute so it survives the jailed env.
	if !filepath.IsAbs(agentAbsPath) {
		abs, err := filepath.Abs(agentAbsPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "projx-engine: cannot make agent path absolute: %v\n", err)
			os.Exit(1)
		}
		agentAbsPath = abs
	}

	// ── Step 3: open the store, compile and write the ambient context ─────────
	st := openStore(absRoot)
	preamble := compileStorePreamble(st)
	st.Close()

	ctxFile, ctxWriteErr := writeAgentContextText(absRoot, preamble)
	if ctxWriteErr != nil {
		// Non-fatal: warn, but continue. Env-var delivery still carries the context.
		fmt.Fprintf(os.Stderr, "projx-engine: warning: could not write agent-context.md: %v\n", ctxWriteErr)
	}

	// ── Step 4: build the jail ────────────────────────────────────────────────
	jailDir := filepath.Join(absRoot, ".projx", "jail")
	// Always rebuild the jail dir to pick up fresh engine binary.
	_ = os.RemoveAll(jailDir)
	if err := os.MkdirAll(jailDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "projx-engine: mkdir jail: %v\n", err)
		os.Exit(1)
	}

	enginePath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "projx-engine: cannot resolve own path: %v\n", err)
		os.Exit(1)
	}
	if err := jail.Build(jailDir, enginePath, effectiveAllowBins); err != nil {
		fmt.Fprintf(os.Stderr, "projx-engine: jail.Build: %v\n", err)
		os.Exit(1)
	}

	// ── Step 5: assemble the jailed environment ───────────────────────────────
	j := &jail.Jail{
		Dir:        jailDir,
		RealPath:   realPath,
		Root:       absRoot,
		AllowBins:  effectiveAllowBins,
		AllowHosts: allowHosts,
	}
	env := j.Env(os.Environ())
	// Inject the ambient store context two ways:
	//   PROJX_STORE_CONTEXT      — the full preamble text (env-var delivery).
	//   PROJX_STORE_CONTEXT_FILE — path to the on-disk file (file delivery).
	env = append(env, "PROJX_STORE_CONTEXT="+preamble)
	if ctxFile != "" {
		env = append(env, "PROJX_STORE_CONTEXT_FILE="+ctxFile)
	}
	// Signal restricted mode: inside this agent invocation the engine CLI only
	// permits reads + agent-safe commits (no gate/secret/agent/run/destructive
	// store ops).  storeCommit will force --by=agent regardless of what the
	// caller passes so agentWritableKind is always enforced.
	env = append(env, "PROJX_AGENT_CONTEXT=1")

	// ── Step 5b: inject secret metadata (codenames only, never values) ────────
	// The agent process receives only the list of codename strings. Plaintext
	// is resolved and injected by the brokered-exec shim at exec time, so the
	// agent's own environment never holds any secret value.
	var secretNames []string
	if sec, secErr := secrets.Open(); secErr == nil {
		secretNames = sec.Names()
	}
	// Propagate the secrets directory so the brokered-exec shim (which runs as a
	// child of the agent) can open the same store. If PROJX_SECRETS_DIR is not
	// set the shim will fall back to UserConfigDir — same machine, same default.
	if secretsDir := os.Getenv("PROJX_SECRETS_DIR"); secretsDir != "" {
		env = append(env, "PROJX_SECRETS_DIR="+secretsDir)
	}
	env = append(env, "PROJX_SECRET_NAMES="+strings.Join(secretNames, ","))

	// ── Step 6: detect the OS-level confiner and print the enforcement banner ──
	// (banner goes to STDERR, not stdout).
	c := confine.Detect()
	osConfined := c.Available()

	hostStr := "deny-all"
	if len(allowHosts) > 0 {
		hostStr = strings.Join(allowHosts, ", ")
	}
	secretNamesStr := "none"
	if len(secretNames) > 0 {
		secretNamesStr = strings.Join(secretNames, ", ")
	}

	// The filesystem clause and the OS-isolation caveat depend on whether a real
	// kernel-level confiner is active. When os-fs is active we promise a real wall
	// (kernel-confined); when cooperative we keep the honest bypass caveat.
	var fsClause, isoClause string
	if osConfined {
		fsClause = fmt.Sprintf("filesystem: kernel-confined to %s (%s). ", absRoot, c.Level())
		isoClause = ""
	} else {
		fsClause = fmt.Sprintf("filesystem: confined to %s. ", absRoot)
		isoClause = "OS-level isolation: NOT enabled (absolute-path bypass possible — see PROJX-ENGINE-SPEC.md). "
	}

	fmt.Fprintf(os.Stderr,
		"projx-engine: sandbox ACTIVE — enforcement level: %s. "+
			"allowed binaries: [%s]. shells/ssh/powershell: BLOCKED (no shim). "+
			"%snetwork: %s. "+
			"%s"+
			"knowledge: read via 'projx-engine store query', write via "+
			"'projx-engine store commit --kind doc|adr|convention|declared-structure --key ... --body ...'. "+
			"gate/secrets are human-only. "+
			"secrets: %d available to commands you run, by env var, by codename: [%s]. you cannot read their values.\n",
		c.Level(),
		strings.Join(effectiveAllowBins, ", "),
		fsClause,
		hostStr,
		isoClause,
		len(secretNames),
		secretNamesStr,
	)

	// ── Step 7: launch the agent ──────────────────────────────────────────────
	// If an OS-level confiner is available, use LaunchConfined which handles
	// the platform-specific mechanism:
	//   Linux   — re-execs through __confined-launch, applies Landlock, syscall.Exec's agent.
	//   Windows — launches the agent inside an AppContainer via CreateProcess.
	//
	// If LaunchConfined returns an error we FAIL CLOSED (never fall back to
	// unconfined launch when a confiner was expected).
	//
	// On platforms without an OS confiner (macOS, etc.) we launch the agent
	// directly — cooperative jail only, honestly reported in the banner above.
	if osConfined {
		// Under OS-FS confinement, secrets are decrypted in the unconfined
		// launcher (RunConfinedLaunch / LaunchConfined) BEFORE the confinement
		// boundary is applied, so the keyfile is still readable at that point.
		// The decrypted CODENAME=value pairs are injected into the confined
		// process's environment, from which child tools inherit them normally.
		//
		// NOTE: The plaintext therefore lives in the confined process's env
		// (not codename-only, as in the cooperative tier). The LLM cannot
		// read it: it has no shell, and the agent-context restriction blocks
		// direct access. The stronger model — an out-of-container IPC broker
		// that never puts plaintext in the process env — is a future refinement.
		if len(secretNames) > 0 {
			fmt.Fprintf(os.Stderr,
				"projx-engine: secrets: %d injected into the confined process environment "+
					"(by codename); the model has no tool to read them "+
					"(no shell, restricted agent-context).\n",
				len(secretNames))
		}

		confinedEnv := append(env, "PROJX_JAIL_DIR="+jailDir)
		agentArgv := append([]string{agentAbsPath}, passthroughArgs...)
		policy := confine.DefaultPolicy(absRoot, jailDir, filepath.Dir(agentAbsPath))

		code, launchErr := c.LaunchConfined(policy, agentArgv, confinedEnv, absRoot)
		if launchErr != nil {
			fmt.Fprintf(os.Stderr, "projx-engine: LaunchConfined failed (failing closed): %v\n", launchErr)
			os.Exit(1)
		}
		os.Exit(code)
	}

	// Cooperative path — direct launch, no OS-FS confinement.
	cmd := exec.Command(agentAbsPath, passthroughArgs...)
	cmd.Env = env
	cmd.Dir = absRoot
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if runErr := cmd.Run(); runErr != nil {
		os.Exit(exitCode(runErr))
	}
	os.Exit(0)
}
