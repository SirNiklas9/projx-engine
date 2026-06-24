//go:build !linux

package confine

import (
	"fmt"
	"os"
)

// RunConfinedLaunch is not supported on non-Linux platforms. It prints an
// error and exits with code 1.
func RunConfinedLaunch(policy Policy, args []string) {
	fmt.Fprintln(os.Stderr, "projx-engine: confined-launch: not supported on this OS")
	os.Exit(1)
}
