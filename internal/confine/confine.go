// Package confine applies OS-level filesystem confinement to the current process.
//
// On Linux this uses Landlock LSM via go-landlock. On other platforms the
// Confiner is available but marks itself as unavailable and Apply is a no-op.
package confine

import (
	"os"
)

// Policy describes what paths the confined process may access.
type Policy struct {
	// Root is the project root directory (always RW).
	Root string
	// ReadOnly is the list of paths granted read-only access.
	ReadOnly []string
	// ReadWrite is the list of paths granted read-write access (in addition to Root).
	ReadWrite []string
}

// Confiner applies OS-level confinement to the current process.
type Confiner interface {
	// Level returns a human-readable description of the confinement mechanism.
	Level() string
	// Available reports whether the mechanism is actually active on this kernel/OS.
	Available() bool
	// Apply applies the policy to the current process. On Linux this is
	// irreversible for the lifetime of the process. On other platforms it is a
	// no-op.
	Apply(p Policy) error
	// LaunchConfined runs argv[0] (with argv[1:] as arguments) confined per
	// policy, with env as the full child environment and dir as the working
	// directory. Stdin/Stdout/Stderr are wired to os.Std*. It returns the
	// child's exit code on success, or a non-nil error if confinement setup or
	// process launch failed. Callers MUST fail closed on error.
	LaunchConfined(policy Policy, argv []string, env []string, dir string) (exitCode int, err error)
}

// Detect returns the best available Confiner for this platform.
func Detect() Confiner {
	return platformConfiner()
}

// DefaultPolicy builds a sensible default policy: root + /tmp are RW,
// standard Linux system dirs are RO. Paths that don't exist are filtered out.
// jailDir and agentDir are added as RO so the process can still read them.
func DefaultPolicy(root, jailDir, agentDir string) Policy {
	rw := filterExisting([]string{root, "/tmp"})
	ro := filterExisting([]string{
		"/usr", "/lib", "/lib64", "/bin", "/sbin",
		"/etc", "/opt", "/proc", "/dev", jailDir, agentDir,
	})
	return Policy{Root: root, ReadOnly: ro, ReadWrite: rw}
}

// ExistingPaths returns only paths that exist on the filesystem, deduplicating
// and removing empty strings. It is exported so tests and callers can build
// custom policies without using DefaultPolicy.
func ExistingPaths(paths []string) []string {
	return filterExisting(paths)
}

// filterExisting is the internal implementation.
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
