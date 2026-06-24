//go:build linux

package confine

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/landlock-lsm/go-landlock/landlock"
	ll "github.com/landlock-lsm/go-landlock/landlock/syscall"
)

type landlockConfiner struct{}

func (landlockConfiner) Level() string { return "os-fs:landlock" }

// Available probes whether Landlock is usable on this kernel without applying
// any restriction. It uses the LandlockGetABIVersion syscall (which is a pure
// query that does not affect the calling process).
func (landlockConfiner) Available() bool {
	v, err := ll.LandlockGetABIVersion()
	return err == nil && v >= 1
}

// Apply applies Landlock filesystem confinement to the current process.
// This call is irreversible for the lifetime of the process.
//
// The policy must have at least one path (RO or RW); an empty rule set would
// deny all filesystem access, which is not useful. Apply returns an error if
// no paths are provided.
func (landlockConfiner) Apply(p Policy) error {
	rules := make([]landlock.Rule, 0, 2)

	if len(p.ReadOnly) > 0 {
		rules = append(rules, landlock.RODirs(p.ReadOnly...))
	}

	// Collect RW paths: always include Root.
	rwPaths := make([]string, 0, len(p.ReadWrite)+1)
	seenRoot := false
	for _, r := range p.ReadWrite {
		if r == p.Root {
			seenRoot = true
		}
		rwPaths = append(rwPaths, r)
	}
	if p.Root != "" && !seenRoot {
		rwPaths = append([]string{p.Root}, rwPaths...)
	}
	if len(rwPaths) > 0 {
		rules = append(rules, landlock.RWDirs(rwPaths...))
	}

	if len(rules) == 0 {
		return fmt.Errorf("confine: no access rules specified; refusing to apply (would deny all fs access)")
	}

	return landlock.V5.BestEffort().RestrictPaths(rules...)
}

// LaunchConfined re-execs the current process through the
// __confined-launch subcommand, which applies Landlock and then
// syscall.Exec's argv[0]. This preserves the proven Landlock behaviour
// (domain applied in a fresh single-threaded process, then inherited
// across execve into the agent).
//
// The PROJX_JAIL_DIR env var is forwarded if present in env, so the
// confined launcher can include jailDir in the policy.
func (landlockConfiner) LaunchConfined(policy Policy, argv []string, env []string, dir string) (int, error) {
	if len(argv) == 0 {
		return 0, fmt.Errorf("confine: LaunchConfined: empty argv")
	}
	selfExe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("confine: LaunchConfined: cannot resolve own exe: %w", err)
	}

	// Protocol: projx-engine __confined-launch <root> <agentPath> [agentArgs...]
	launchArgs := make([]string, 0, 3+len(argv))
	launchArgs = append(launchArgs, "__confined-launch", policy.Root)
	launchArgs = append(launchArgs, argv...)

	cmd := exec.Command(selfExe, launchArgs...)
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

func platformConfiner() Confiner { return landlockConfiner{} }
