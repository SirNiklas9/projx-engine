// Package core provides OS-level filesystem confinement primitives for
// Pulp-ext-confine. It is pure Go and has no wazero dependency, so the native
// engine can import it directly without pulling in the Pulp capability layer.
//
// On Linux, Landlock LSM (go-landlock) is used. On Windows, an AppContainer
// child process is created. On other platforms, Available() returns false and
// LaunchConfined runs the process without OS-level restriction (cooperative
// mode).
//
// Key difference from projx-engine/internal/confine: the secrets-injection
// code has been removed (that is a projx-engine concern), and NetAllow has
// been added to Policy for future network-egress control.
package core

import (
	"os"
	"path/filepath"
	"runtime"
)

// Policy describes the filesystem access the confined process is permitted.
type Policy struct {
	// Root is the project root directory (always granted read-write access).
	Root string
	// ReadOnly is the list of paths granted read-only access.
	ReadOnly []string
	// ReadWrite is the list of additional paths granted read-write access.
	ReadWrite []string
	// NetAllow lists network destinations the process may reach. At the core
	// layer this field is passed through to callers; enforcement is applied by
	// the caged composition layer (egress netns on Linux, internetClient
	// capability on Windows). Kernel-level per-host filtering (Landlock net
	// v4+ / WFP on Windows) is reserved for a future improvement.
	NetAllow []string
}

// Confiner applies OS-level confinement.
type Confiner interface {
	// Level returns a human-readable name for the confinement mechanism.
	Level() string
	// Available reports whether the mechanism is active on this kernel/OS.
	Available() bool
	// Apply restricts the current process to the policy. On Linux this is
	// irreversible. On Windows it is a no-op (confinement is at child-spawn
	// time). On other platforms it is always a no-op.
	Apply(p Policy) error
	// LaunchConfined starts argv[0] confined per policy. env is the full child
	// environment; dir is the working directory. It waits for the child to
	// finish and returns its exit code. Callers MUST fail closed on error.
	LaunchConfined(policy Policy, argv []string, env []string, dir string) (exitCode int, err error)
}

// Detect returns the best available Confiner for the current platform.
func Detect() Confiner { return platformConfiner() }

// DefaultPolicy builds a sensible default policy. Paths that do not exist are
// filtered; jailDir and agentDir are added read-only so the process can exec them.
//
// The defaults are OS-specific. On Windows the AppContainer must be granted ONLY
// what it needs — the project root (rw), the jail dir, and the agent's directory
// IF it is an absolute path. It must NOT add the Linux system dirs, "/tmp" (which
// resolves to C:\tmp), or a relative agentDir like "." (which resolves to the
// host's working directory): on Windows these grants are applied by a RECURSIVE
// icacls (OI)(CI) pass, so granting C:\tmp or the host cwd stalls the launch on a
// huge tree. (Ancestor TRAVERSE grants — non-recursive — are added separately by
// the Windows confiner so path resolution still works.)
func DefaultPolicy(root, jailDir, agentDir string) Policy {
	if runtime.GOOS == "windows" {
		ro := []string{jailDir}
		if filepath.IsAbs(agentDir) {
			ro = append(ro, agentDir)
		}
		return Policy{Root: root, ReadOnly: filterExisting(ro), ReadWrite: filterExisting([]string{root})}
	}
	// Linux (Landlock) / other (cooperative): root + /tmp read-write; standard
	// system dirs read-only so the process can exec/read them.
	rw := filterExisting([]string{root, "/tmp"})
	ro := filterExisting([]string{
		"/usr", "/lib", "/lib64", "/bin", "/sbin",
		"/etc", "/opt", "/proc", "/dev", jailDir, agentDir,
	})
	return Policy{Root: root, ReadOnly: ro, ReadWrite: rw}
}

// ExistingPaths returns only the paths from the slice that exist on disk,
// deduplicating and removing empty strings.
func ExistingPaths(paths []string) []string { return filterExisting(paths) }

func filterExisting(paths []string) []string {
	out := paths[:0:0]
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
			seen[p] = true
		}
	}
	return out
}
