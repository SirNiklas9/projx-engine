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
	store "github.com/SirNiklas9/projx-store"
)

// cageRequested reports whether this launch should be OS-confined. PROJX_CAGE, when
// set, always wins as an explicit override (on OR off); absent that, it defers to the
// project's DECLARED default (store.CageModeOn — a real, editable setting/cage-mode
// record, seeded "off"). Cage is opt-in, never required.
func cageRequested(st store.Store) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PROJX_CAGE"))) {
	case "1", "on", "true", "yes":
		return true
	case "0", "off", "false", "no":
		return false
	}
	return store.CageModeOn(st)
}

func runAgentCmd(absRoot string, args []string) {
	autoSeed(absRoot) // fresh project? seed floor + detected stack first

	// `agent run [...]` — strip the "run" subcommand word so it doesn't leak into the
	// launched agent's argv (the only subcommand today; the dispatcher passes it through).
	if len(args) > 0 && args[0] == "run" {
		args = args[1:]
	}

	// ── Step 1: parse flags ───────────────────────────────────────────────────
	var allowBins []string  // extra allowlisted exec basenames from --allow
	var allowHosts []string // extra allowlisted net hostnames from --allow-host
	var passthroughArgs []string
	var task string // --task: slices the ambient context to this task (else full dump)

	// Parse flags; everything after "--" is passthrough.
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--":
			passthroughArgs = append(passthroughArgs, args[i+1:]...)
			i = len(args)
		case "--task":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "projx-engine: --task requires an argument")
				os.Exit(1)
			}
			task = args[i]
			i++
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

	// Worker permissions are DATA, read from the store — nothing hardcoded. The
	// safe-list (setting/worker-allow) is the "basic permissions" floor; the
	// full-autonomy override (setting/worker-autonomy) lets the human verbally grant a
	// worker all permissions.
	permSt := openStore(absRoot)
	workerBins := store.WorkerAllowBins(permSt)
	workerFullAuto := store.WorkerFullAutonomy(permSt)
	permSt.Close()

	// The sandbox exec allow-list includes the declared safe-list (toolchains/read
	// utils) so a confined worker can actually build and test — not just git. Explicit
	// --allow flags still extend it.
	effectiveAllowBins := dedupStrings(append(append([]string{"git", "projx-engine"}, workerBins...), allowBins...))

	// Language-aware sandbox (Task #18 — the sandbox half of the language-aware gate):
	// detect the task/repo language and GRANT that language's declared toolchain +
	// net-allow on the fly, so a worker whose toolchain would otherwise be "red"
	// (e.g. a Rust task with no cargo in the allow-list) can actually run. Additive +
	// safe: only the detected stacks' PROFILE-declared tools/hosts are granted — the
	// sandbox is never widened beyond the profile. A Rust repo/task gets cargo +
	// crates.io; a Go repo still gets go; etc.
	langTools, langHosts := profileGrants(detectStacksForTask(absRoot, task))
	if len(langTools) > 0 {
		effectiveAllowBins = dedupStrings(append(effectiveAllowBins, langTools...))
	}
	if len(langHosts) > 0 {
		allowHosts = dedupStrings(append(allowHosts, langHosts...))
	}

	// ── Step 2: retain the real PATH before any jail/PATH change ──────────────
	// Provider invocation is deliberately resolved after the context file exists:
	// each adapter owns how it delivers that context.
	realPath := os.Getenv("PATH")

	// ── Step 3: open the store, compile and write the ambient context ─────────
	// With a --task, slice the contract to the task (law + only relevant records)
	// instead of dumping the whole store (incl. the full code map) into the launch.
	st := openStore(absRoot)
	var preamble string
	if strings.TrimSpace(task) != "" {
		preamble = compileStorePreambleForTask(st, task)
	} else {
		preamble = compileStorePreamble(st)
	}
	st.Close()
	// Per-worker ProjX scope: when this launch is a dispatched step (PROJX_WORKER_ROLE
	// set by the supervisor), prepend the role banner so the worker's injected context
	// is scoped to its step's ROLE + the task-sliced knowledge above — not the full trunk.
	preamble = applyWorkerRole(preamble, workerRoleLabel())

	ctxFile, ctxWriteErr := writeAgentContextText(absRoot, preamble)
	if ctxWriteErr != nil {
		// Non-fatal: warn, but continue. Env-var delivery still carries the context.
		fmt.Fprintf(os.Stderr, "projx-engine: warning: could not write agent-context.md: %v\n", ctxWriteErr)
	}

	launchTask := task
	if len(passthroughArgs) > 0 {
		launchTask = strings.Join(passthroughArgs, " ")
	}
	agentName, agentLeadingArgs := resolveAgentArgv(absRoot, launchTask, renderOpts{
		Model: os.Getenv("PROJX_AGENT_MODEL"), Profile: os.Getenv("PROJX_AGENT_PROFILE"),
		NativeEffort: os.Getenv("PROJX_AGENT_EFFORT"), SystemPromptFile: ctxFile,
	})
	// Each native adapter declares its minimal inference endpoint. This keeps
	// provider egress structured and prevents optional plugin traffic from
	// widening a worker's network policy.
	allowHosts = dedupStrings(append(allowHosts, providerNetworkHosts(agentName)...))
	agentAbsPath := agentName
	if !filepath.IsAbs(agentAbsPath) && !strings.ContainsAny(agentAbsPath, `/\`) {
		if p, err := exec.LookPath(agentAbsPath); err == nil {
			agentAbsPath = p
		} else {
			agentAbsPath = ""
		}
	}
	if agentAbsPath == "" {
		fmt.Fprintln(os.Stderr, "projx-engine: no agent found: set PROJX_AGENT_CMD or install an agent CLI on PATH")
		os.Exit(1)
	}
	if !filepath.IsAbs(agentAbsPath) {
		abs, err := filepath.Abs(agentAbsPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "projx-engine: cannot make agent path absolute: %v\n", err)
			os.Exit(1)
		}
		agentAbsPath = abs
	}
	agentAbsPath = preferProviderRuntime(agentName, agentAbsPath)

	// Claude's equivalent least-privilege policy remains provider-specific. Codex
	// receives its explicit workspace-write policy from its adapter above.
	if isClaudeAgent(agentAbsPath) {
		if workerFullAuto {
			agentLeadingArgs = append([]string{"--dangerously-skip-permissions"}, agentLeadingArgs...)
		} else if len(agentLeadingArgs) > 0 {
			agentLeadingArgs = append(claudeAllowedToolsArgs(workerBins), agentLeadingArgs...)
		}
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
	// The jail intentionally removes the ambient PATH, but provider CLIs can
	// launch sibling runtime helpers (Codex's Windows sandbox helper is one).
	// Preserve only the resolved provider executable directory ahead of the
	// jail shims; never restore the broad parent PATH.
	env = prependProviderRuntimePath(env, agentAbsPath)
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
	// PROJX_ROLE=worker exempts this spawned agent from the trunk-dispatch gate — the
	// trunk is denied file mutation, its workers are not. (Survives the hook's unset of
	// PROJX_AGENT_CONTEXT, which is why this is a separate signal.)
	env = append(env, "PROJX_ROLE=worker")

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
	// Cage is OPT-IN, never required: confine ONLY when requested (PROJX_CAGE override,
	// else the project's declared setting/cage-mode — default off). Default is uncaged
	// (cooperative) so a dispatched worker can launch its agent binary — which lives
	// outside any project jail and needs its own files. A confiner merely being
	// available does not mean we impose it.
	cageSt := openStore(absRoot)
	osConfined := c.Available() && cageRequested(cageSt)
	cageSt.Close()

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
			"'projx-engine store commit --kind doc|adr|convention|declared-structure --key ... --body ... --claim-class ... --evidence ...'. "+
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
		agentArgv := append([]string{agentAbsPath}, agentLeadingArgs...)
		policy := confine.DefaultPolicy(absRoot, jailDir, filepath.Dir(agentAbsPath))

		code, launchErr := c.LaunchConfined(policy, agentArgv, confinedEnv, absRoot)
		if launchErr != nil {
			fmt.Fprintf(os.Stderr, "projx-engine: LaunchConfined failed (failing closed): %v\n", launchErr)
			os.Exit(1)
		}
		os.Exit(code)
	}

	// Cooperative path — direct launch, no OS-FS confinement.
	cmd := exec.Command(agentAbsPath, append([]string{}, agentLeadingArgs...)...)
	cmd.Env = env
	cmd.Dir = absRoot
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = quietSysProcAttr()

	if runErr := cmd.Run(); runErr != nil {
		os.Exit(exitCode(runErr))
	}
	os.Exit(0)
}

func prependProviderRuntimePath(env []string, agentPath string) []string {
	dir := strings.TrimSpace(filepath.Dir(agentPath))
	if dir == "" || dir == "." {
		return env
	}
	for i, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), "PATH=") {
			env[i] = "PATH=" + dir + string(os.PathListSeparator) + kv[len("PATH="):]
			return env
		}
	}
	return append(env, "PATH="+dir)
}
