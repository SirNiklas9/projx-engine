//go:build linux

package confine

import (
	"fmt"
	"os"
	"syscall"
)

// RunConfinedLaunch applies Landlock confinement using the provided policy and
// then replaces the current process with args[0] via syscall.Exec.
// This means the Landlock domain is inherited by the executed program.
//
// The function never returns on success. On failure it prints to stderr and
// exits with code 1 (fail-closed: we never run unconfined if confinement was
// requested but failed).
func RunConfinedLaunch(policy Policy, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "projx-engine: confined-launch: no command given")
		os.Exit(1)
	}

	c := landlockConfiner{}
	if err := c.Apply(policy); err != nil {
		// Fail closed: do not exec without confinement.
		fmt.Fprintf(os.Stderr, "projx-engine: confined-launch: landlock apply failed: %v\n", err)
		os.Exit(1)
	}

	// Replace this process with the target. Landlock domain is inherited.
	if err := syscall.Exec(args[0], args, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "projx-engine: confined-launch: exec %q: %v\n", args[0], err)
		os.Exit(1)
	}
	// unreachable
}
