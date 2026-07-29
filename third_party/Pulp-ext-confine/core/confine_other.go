//go:build !linux && !windows

package core

import (
	"fmt"
	"os"
	"os/exec"
)

type cooperativeConfiner struct{}

func (cooperativeConfiner) Level() string        { return "cooperative" }
func (cooperativeConfiner) Available() bool      { return false }
func (cooperativeConfiner) Apply(p Policy) error { return nil }

func (cooperativeConfiner) LaunchConfined(policy Policy, argv []string, env []string, dir string) (int, error) {
	if len(argv) == 0 {
		return 0, fmt.Errorf("confine: LaunchConfined: empty argv")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if runErr := cmd.Run(); runErr != nil {
		if ex, ok := runErr.(*exec.ExitError); ok {
			return ex.ExitCode(), nil
		}
		return 0, fmt.Errorf("confine: LaunchConfined: run: %w", runErr)
	}
	return 0, nil
}

// RunConfinedLaunch on non-Linux/non-Windows simply execs the command
// without OS-level restriction (cooperative mode). Available() is false
// on this platform so callers should not normally reach this path.
func RunConfinedLaunch(policy Policy, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "pulp-ext-confine: confined-launch: no command given")
		os.Exit(1)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if ex, ok := err.(*exec.ExitError); ok {
			os.Exit(ex.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "pulp-ext-confine: confined-launch: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func platformConfiner() Confiner { return cooperativeConfiner{} }
