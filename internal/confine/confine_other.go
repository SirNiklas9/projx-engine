//go:build !linux && !windows

package confine

import (
	"os"
	"os/exec"
	"fmt"
)

type cooperativeConfiner struct{}

func (cooperativeConfiner) Level() string        { return "cooperative" }
func (cooperativeConfiner) Available() bool      { return false }
func (cooperativeConfiner) Apply(p Policy) error { return nil }

// LaunchConfined runs argv[0] directly without any OS-level confinement.
// Available() is false on this platform so callers should not reach this path
// under normal operation, but it is provided for completeness.
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

func platformConfiner() Confiner { return cooperativeConfiner{} }
