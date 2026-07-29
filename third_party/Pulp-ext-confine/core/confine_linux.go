//go:build linux

package core

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/landlock-lsm/go-landlock/landlock"
	ll "github.com/landlock-lsm/go-landlock/landlock/syscall"
)

type landlockConfiner struct{}

func (landlockConfiner) Level() string { return "os-fs:landlock" }

func (landlockConfiner) Available() bool {
	v, err := ll.LandlockGetABIVersion()
	return err == nil && v >= 1
}

// Apply applies Landlock filesystem confinement to the current process.
// This is irreversible for the lifetime of the process.
func (landlockConfiner) Apply(p Policy) error {
	rules := make([]landlock.Rule, 0, 4)

	// Augment read-only paths with the DNS resolver config. On WSL/systemd
	// /etc/resolv.conf is a symlink out of /etc (e.g. → /mnt/wsl/resolv.conf or
	// /run/systemd/resolve/stub-resolv.conf), so granting /etc alone leaves the
	// real target unreadable; the confined resolver then falls back to
	// localhost:53 — which is dead inside the egress netns — and ALL DNS fails.
	// Granting the symlink-resolved target lets name resolution work.
	readOnly := append([]string(nil), p.ReadOnly...)
	readOnly = append(readOnly, resolverReadPaths()...)

	if len(readOnly) > 0 {
		roDirs, roFiles := splitDirsFiles(readOnly)
		if len(roDirs) > 0 {
			rules = append(rules, landlock.RODirs(roDirs...))
		}
		if len(roFiles) > 0 {
			rules = append(rules, landlock.ROFiles(roFiles...))
		}
	}

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
		rwDirs, rwFiles := splitDirsFiles(rwPaths)
		if len(rwDirs) > 0 {
			rules = append(rules, landlock.RWDirs(rwDirs...))
		}
		if len(rwFiles) > 0 {
			rules = append(rules, landlock.RWFiles(rwFiles...))
		}
	}

	if len(rules) == 0 {
		return fmt.Errorf("confine: no access rules specified; refusing to apply (would deny all fs access)")
	}

	return landlock.V5.BestEffort().RestrictPaths(rules...)
}

// splitDirsFiles partitions paths into directories and regular files by stat'ing
// each. Landlock requires directory access rights for directories but the
// narrower file access rights for regular files — applying directory rights
// (read_dir/make_dir/remove_dir/...) to a regular file is rejected by the kernel
// with "inconsistent access rights". Paths that don't exist or can't be stat'd
// are skipped (a Landlock rule on a missing path errors the whole ruleset).
func splitDirsFiles(paths []string) (dirs, files []string) {
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			continue // skip missing/unstattable paths
		}
		if fi.IsDir() {
			dirs = append(dirs, p)
		} else {
			files = append(files, p)
		}
	}
	return dirs, files
}

// resolverReadPaths returns the real (symlink-resolved) paths a confined process
// needs to read so its DNS resolver can find the nameserver. /etc/resolv.conf is
// frequently a symlink to a file outside /etc (WSL: /mnt/wsl/resolv.conf;
// systemd-resolved: /run/systemd/resolve/stub-resolv.conf), which a Landlock
// grant on /etc alone does not cover. Without it the Go resolver falls back to
// localhost:53 (unreachable inside the egress netns) and every lookup fails.
func resolverReadPaths() []string {
	var out []string
	if real, err := filepath.EvalSymlinks("/etc/resolv.conf"); err == nil && real != "" {
		out = append(out, real)
	}
	return out
}

// LaunchConfined launches a process confined under the Landlock policy.
//
// On Linux, Landlock must be applied in a fresh single-threaded process before
// exec'ing the target, because RestrictPaths is irreversible. The
// standard pattern (used by projx-engine) is to run a dedicated launcher
// binary that calls Apply then syscall.Exec.
//
// This core library is host-agnostic. Callers that need real Landlock
// enforcement must compile and register a launcher binary (e.g. by embedding
// RunConfinedLaunch in their host CLI) and point PULP_CONFINE_LAUNCHER_BIN
// at that binary. The test suite uses this to compile a dedicated launcher.
//
// If PULP_CONFINE_LAUNCHER_BIN is not set, LaunchConfined runs the process
// directly (no Landlock domain). Available() is still true; this matches the
// cooperative fallback for callers that have not wired a launcher yet.
func (c landlockConfiner) LaunchConfined(policy Policy, argv []string, env []string, dir string) (int, error) {
	if len(argv) == 0 {
		return 0, fmt.Errorf("confine: LaunchConfined: empty argv")
	}

	var cmd *exec.Cmd
	launcherBin := os.Getenv("PULP_CONFINE_LAUNCHER_BIN")
	if launcherBin != "" {
		// launcher protocol: <launcher> <root> <target> [args...]
		launchArgs := make([]string, 0, 1+len(argv))
		launchArgs = append(launchArgs, policy.Root)
		launchArgs = append(launchArgs, argv...)
		cmd = exec.Command(launcherBin, launchArgs...)
	} else {
		// No launcher registered — run without Landlock enforcement.
		cmd = exec.Command(argv[0], argv[1:]...)
	}

	cmd.Env = env
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}

	if runErr := cmd.Run(); runErr != nil {
		if ex, ok := runErr.(*exec.ExitError); ok {
			return ex.ExitCode(), nil
		}
		return 0, fmt.Errorf("confine: LaunchConfined: %w", runErr)
	}
	return 0, nil
}

// RunConfinedLaunch applies Landlock confinement using the policy derived from
// args, then replaces the current process with the target via syscall.Exec.
// The Landlock domain is inherited by the executed program.
//
// Protocol: args[0] = root dir, args[1] = target exe, args[2:] = target args.
//
// This function is called in the CHILD process (via the launcher binary
// compiled by the host or tests). It never returns on success. On failure it
// writes to stderr and exits with code 1 (fail-closed).
func RunConfinedLaunch(policy Policy, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "pulp-ext-confine: confined-launch: no command given")
		os.Exit(1)
	}
	c := landlockConfiner{}
	if err := c.Apply(policy); err != nil {
		fmt.Fprintf(os.Stderr, "pulp-ext-confine: confined-launch: landlock apply failed: %v\n", err)
		os.Exit(1)
	}
	if err := syscall.Exec(args[0], args, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "pulp-ext-confine: confined-launch: exec %q: %v\n", args[0], err)
		os.Exit(1)
	}
}

func platformConfiner() Confiner { return landlockConfiner{} }
