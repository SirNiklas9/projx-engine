package main

// cmd_exec.go — brokered-exec handler for the multi-call (busybox-style) dispatch.
//
// When projx-engine is invoked under a name OTHER than "projx-engine" AND the
// environment variable PROJX_REAL_PATH is set (meaning a jail launched us), this
// file's logic takes over instead of the normal CLI dispatch.
//
// Flow:
//  1. Parse the broker policy from env vars injected by the jail.
//  2. Ask RestrictiveBroker whether the requested binary is allowed.
//  3. If denied → stderr + exit 126.
//  4. If allowed → resolve the real binary from PROJX_REAL_PATH (NOT from the
//     live, jailed PATH which would loop back to the shim).
//  5. Exec the real binary, propagating its exit code.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/SirNiklas9/projx-engine/internal/broker"
	"github.com/SirNiklas9/projx-engine/internal/secrets"
)

// runBrokeredExec is called when the binary is invoked as a shim name.
// bin is the trimmed, lowercased basename (no .exe); args is os.Args[1:].
// This function never returns — it always calls os.Exit.
func runBrokeredExec(bin string, args []string) {
	allowBinsEnv := os.Getenv("PROJX_BROKER_ALLOW_BINS")
	root := os.Getenv("PROJX_BROKER_ROOT")
	allowHostsEnv := os.Getenv("PROJX_BROKER_ALLOW_HOSTS")
	realPath := os.Getenv("PROJX_REAL_PATH")

	var allowBins []string
	for _, s := range strings.Split(allowBinsEnv, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			allowBins = append(allowBins, s)
		}
	}

	var allowHosts []string
	for _, s := range strings.Split(allowHostsEnv, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			allowHosts = append(allowHosts, s)
		}
	}

	if root == "" {
		fmt.Fprintf(os.Stderr, "projx-engine: PROJX_BROKER_ROOT is not set\n")
		os.Exit(1)
	}

	b, err := broker.NewRestrictiveBroker(allowBins, root, allowHosts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "projx-engine: broker init failed: %v\n", err)
		os.Exit(1)
	}

	d := b.Check(broker.Action{Kind: "exec", Target: bin})
	if !d.Allow {
		fmt.Fprintf(os.Stderr, "projx-engine: denied exec %q: %s\n", bin, d.Reason)
		os.Exit(126)
	}

	// Resolve the real binary on the original (non-jailed) PATH.
	real, err := lookInPath(bin, realPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "projx-engine: %q not found on real PATH\n", bin)
		os.Exit(127)
	}

	// Execute the real binary.
	cmd := exec.Command(real, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Inherit the full environment (including PATH which the jail has already set
	// to the jail dir — but that is fine because we have already resolved the
	// real binary above; the child's subsequent execs are NOT our concern here).
	cmd.Env = os.Environ()

	// Inject sealed secrets into the child's environment by codename.
	// The child (the real tool) receives codename=plaintext. The agent's own
	// process never held these values — it only carried the codename list.
	if sec, secErr := secrets.Open(); secErr == nil {
		if vals, resolveErr := sec.Resolve(); resolveErr == nil {
			for codename, val := range vals {
				cmd.Env = append(cmd.Env, codename+"="+val)
			}
		} else {
			fmt.Fprintf(os.Stderr, "projx-engine: warning: secrets resolve: %v\n", resolveErr)
		}
	} else if os.Getenv("PROJX_SECRETS_DIR") != "" {
		// Only warn when the caller explicitly pointed at a secrets dir.
		fmt.Fprintf(os.Stderr, "projx-engine: warning: secrets open: %v\n", secErr)
	}

	if runErr := cmd.Run(); runErr != nil {
		os.Exit(exitCode(runErr))
	}
	os.Exit(0)
}

// lookInPath searches each directory in the OS path-list-style pathEnv for bin.
// On Windows it also tries bin with each PATHEXT extension if bin has no
// extension of its own.  It does NOT use exec.LookPath because that would
// search the live os.Getenv("PATH"), which is the jailed PATH.
func lookInPath(bin, pathEnv string) (string, error) {
	dirs := filepath.SplitList(pathEnv)

	// On Windows, determine the list of extensions to try.
	var exts []string
	if runtime.GOOS == "windows" {
		// If bin already carries an executable extension, try it as-is first.
		binExt := strings.ToUpper(filepath.Ext(bin))
		execExts := map[string]bool{
			".EXE": true, ".CMD": true, ".BAT": true, ".COM": true,
		}
		if execExts[binExt] {
			exts = []string{""} // keep the name as-is
		} else {
			// Try common extensions from PATHEXT (fall back to defaults).
			pathext := os.Getenv("PATHEXT")
			if pathext == "" {
				pathext = ".EXE;.CMD;.BAT;.COM"
			}
			for _, e := range strings.Split(pathext, ";") {
				e = strings.TrimSpace(e)
				if e != "" {
					exts = append(exts, e)
				}
			}
		}
	} else {
		exts = []string{""}
	}

	for _, dir := range dirs {
		for _, ext := range exts {
			candidate := filepath.Join(dir, bin+ext)
			info, err := os.Stat(candidate)
			if err != nil {
				continue
			}
			if info.IsDir() {
				continue
			}
			return candidate, nil
		}
	}
	return "", fmt.Errorf("not found")
}

// exitCode extracts the process exit code from a cmd.Run error.
// Returns 0 on nil (success), the ExitError code if available, or 1 otherwise.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return 1
}
