//go:build linux

package confine

import (
	"fmt"
	"os"
	"syscall"

	"github.com/SirNiklas9/projx-engine/internal/secrets"
)

// RunConfinedLaunch applies Landlock confinement using the provided policy and
// then replaces the current process with args[0] via syscall.Exec.
// This means the Landlock domain is inherited by the executed program.
//
// Secret injection strategy (OS-FS tier):
//
// This function runs UNCONFINED — it is exec'd by the engine launcher before
// Landlock is applied. The keyfile is therefore readable at this point. We
// decrypt the secrets store here and append CODENAME=value pairs to the exec
// environment BEFORE calling Apply. Once Apply is called, the Landlock domain
// is active and the keyfile becomes unreadable from inside that domain.
//
// The resulting plaintext lives in the confined process's environment (not
// codename-only as in the cooperative tier), but the LLM never sees it: it
// has no shell access and the agent-context restriction blocks direct
// PROJX_SECRET_* reads. The stronger model — keeping plaintext out of the
// process env entirely using an out-of-container IPC broker — is a future
// refinement and is NOT implemented here.
//
// The function never returns on success. On failure it prints to stderr and
// exits with code 1 (fail-closed: we never run unconfined if confinement was
// requested but failed).
func RunConfinedLaunch(policy Policy, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "projx-engine: confined-launch: no command given")
		os.Exit(1)
	}

	// ── Resolve secrets BEFORE Apply (keyfile still readable here) ───────────
	// Non-fatal: if Open or Resolve fails (e.g. no store, bad key) we skip
	// injection and proceed with the original env. Values are injected last so
	// they override any duplicates already in os.Environ().
	execEnv := injectSecretsIntoEnv(os.Environ())

	c := landlockConfiner{}
	if err := c.Apply(policy); err != nil {
		// Fail closed: do not exec without confinement.
		fmt.Fprintf(os.Stderr, "projx-engine: confined-launch: landlock apply failed: %v\n", err)
		os.Exit(1)
	}

	// Replace this process with the target. Landlock domain is inherited,
	// and the augmented env (with decrypted secret values) travels with it.
	if err := syscall.Exec(args[0], args, execEnv); err != nil {
		fmt.Fprintf(os.Stderr, "projx-engine: confined-launch: exec %q: %v\n", args[0], err)
		os.Exit(1)
	}
	// unreachable
}

// injectSecretsIntoEnv returns base (a copy) with CODENAME=value pairs
// appended for each secret in the store. Later entries override earlier ones
// in os.Exec's env semantics (last-defined wins on Linux).
// Non-fatal: returns base unchanged if the store cannot be opened or resolved.
func injectSecretsIntoEnv(base []string) []string {
	st, err := secrets.Open()
	if err != nil {
		// No store, missing key file, or permission error — skip injection.
		return base
	}
	vals, err := st.Resolve()
	if err != nil {
		return base
	}
	if len(vals) == 0 {
		return base
	}
	env := make([]string, len(base), len(base)+len(vals))
	copy(env, base)
	for codename, val := range vals {
		env = append(env, codename+"="+val)
	}
	return env
}
