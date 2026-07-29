// Package caged is the composition layer for spawn.confine: it performs the
// FULL "composed caged launch" — jail PATH + secret env injection + network
// egress confinement + filesystem confinement, applied to a single native
// child process launch — that previously lived in the projx-engine native
// monolith (runAgentCmd + doComposedLaunch).
//
// It is deliberately a separate package from Pulp-ext-confine/core so that
// core stays free of the (heavy, Linux-only) gVisor/netns egress dependency:
// callers who only need bare filesystem confinement import core; callers who
// need the whole composed wall import caged.
//
// The single entrypoint is RunCaged(policy). On Linux it routes the launch
// through Pulp-ext-egress/core's netns gateway and applies Landlock in the
// re-exec'd child via the egress PreExecHook seam — the load-bearing ordering
// guarantee that Landlock is applied in the same thread immediately before
// execve. On Windows it uses the AppContainer confiner (with internetClient
// granted when NetAllow is set) plus a cooperative egress-proxy hint. On other
// platforms it falls back to a cooperative launch.
package caged

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BananaLabs-OSS/Pulp-ext-confine/core"
)

// CagedPolicy fully describes a composed caged launch.
type CagedPolicy struct {
	// Argv is the native child to launch (argv[0] is the executable path).
	Argv []string
	// Root is the project root — always granted read-write.
	Root string
	// ReadOnly lists extra read-only paths.
	ReadOnly []string
	// ReadWrite lists extra read-write paths (in addition to Root).
	ReadWrite []string
	// NetAllow lists DNS names the child may reach. When non-empty on Linux the
	// child is launched inside an egress-filtering network namespace; when empty
	// the child gets a deny-all netns (no egress) on Linux. On Windows a non-empty
	// NetAllow grants the AppContainer internetClient capability.
	NetAllow []string
	// NetOnMiss, when set, makes the egress wall runtime-mutable: a connection to
	// a DNS name not in NetAllow is routed to this hook (typically a grants.Broker
	// decision driven by an approver). Returning true permits the name for this
	// resolution. It is consulted on every miss and never cached here, so revoking
	// the underlying grant re-blocks the name on its next resolution. Kept as a
	// plain func so this package needs no dependency on the grants store. Linux only.
	NetOnMiss func(name string) bool
	// JailBins lists executable basenames to expose to the child on a restricted
	// PATH. A jail directory is built containing symlinks to the real binaries
	// resolved from the host PATH; the child's PATH is set to ONLY that directory
	// so it can exec only the allowlisted binaries.
	JailBins []string
	// Secrets maps codename → secret reference. The VALUES are injected into the
	// child process environment only (as CODENAME=value); they are NEVER returned
	// in CagedResult. The map value is treated as the literal secret value to
	// inject (the caller resolves references before calling RunCaged).
	Secrets map[string]string
	// Env overlays extra environment variables onto the host environment for the
	// child. Applied before secret injection.
	Env map[string]string
	// Dir is the child's working directory.
	Dir string
}

// AuditEvent is a single structured record of a confinement decision made
// while composing/launching the caged child. It is returned to the caller so
// a cell can log/inspect what walls were applied — without ever exposing secret
// values.
type AuditEvent struct {
	Kind   string `msgpack:"kind"`             // e.g. "fs", "net", "jail", "secret", "launch"
	Detail string `msgpack:"detail"`           // human-readable description (no secret values)
	Denied bool   `msgpack:"denied,omitempty"` // true when this records a denial
}

// CagedResult is the outcome of a composed caged launch.
type CagedResult struct {
	ExitCode    int          `msgpack:"exit_code"`
	Error       string       `msgpack:"error,omitempty"`
	AuditEvents []AuditEvent `msgpack:"audit_events,omitempty"`
}

// RunCaged performs the full composed caged launch described by policy and
// blocks until the child exits. It NEVER returns secret values in CagedResult.
//
// Platform behaviour:
//
//	Linux   — egress netns (malicious-proof) + Landlock FS via PreExecHook.
//	Windows — AppContainer FS confinement (+ internetClient when NetAllow set).
//	Other   — cooperative launch (no OS-level wall).
//
// It is implemented per-platform (caged_linux.go / caged_other.go); this file
// holds the shared types and helpers those implementations use.
func RunCaged(policy CagedPolicy) (CagedResult, error) {
	// Resolve a bare agent name (argv[0], e.g. "claude") to an absolute path.
	// The final launch uses syscall.Exec on Linux (which does NOT search PATH)
	// and CreateProcess on Windows; the jail pins the child PATH to the jail dir
	// for SUBPROCESSES only, so neither resolves the initial agent binary from a
	// bare name — "claude" would fail with ENOENT / exit -1. We resolve it
	// against the host PATH here. buildCorePolicy then grants the resolved
	// binary's directory read-only (filepath.Dir of the resolved argv[0]) so the
	// FS cage permits loading it. An already-absolute argv[0] is used unchanged.
	if len(policy.Argv) > 0 {
		if resolved, err := resolveAgentPath(policy.Argv[0]); err == nil {
			policy.Argv[0] = resolved
		}
	}
	return runCaged(policy)
}

// resolveAgentPath turns a bare agent name into an absolute path via the host
// PATH, then resolves symlinks to the real binary. The latter is load-bearing
// for the FS cage: Landlock follows symlinks at exec time and requires the REAL
// target's directory to be granted + executable. Agent installers commonly
// symlink (e.g. ~/.local/bin/claude → ~/.local/share/claude/versions/<v>), so
// without EvalSymlinks the policy would grant the link's dir while the kernel
// denies exec of the ungranted target dir ("permission denied").
func resolveAgentPath(name string) (string, error) {
	p := name
	if !filepath.IsAbs(p) {
		resolved, err := execLookPath(name)
		if err != nil {
			return "", err
		}
		p = resolved
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	return p, nil
}

// buildCorePolicy derives a core.Policy (the FS/confine view) from a CagedPolicy.
// It mirrors projx-engine's DefaultPolicy(root, jailDir, agentDir) +
// extra-grants composition: system dirs RO, root+/tmp RW, plus the jail dir and
// the agent's own directory RO so the child can exec them.
func buildCorePolicy(policy CagedPolicy, jailDir string) core.Policy {
	agentDir := ""
	if len(policy.Argv) > 0 {
		agentDir = filepath.Dir(policy.Argv[0])
	}
	p := core.DefaultPolicy(policy.Root, jailDir, agentDir)
	p.NetAllow = policy.NetAllow
	p.ReadOnly = append(p.ReadOnly, core.ExistingPaths(policy.ReadOnly)...)
	p.ReadWrite = append(p.ReadWrite, core.ExistingPaths(policy.ReadWrite)...)
	return p
}

// composeChildEnv builds the child environment: host env, overlaid with
// policy.Env, then with PATH pinned to the jail dir (when one was built), then
// with secret values injected. Returns the env slice and the audit events that
// describe what was injected (codenames only — never values).
func composeChildEnv(policy CagedPolicy, jailDir string, audit *[]AuditEvent) []string {
	env := os.Environ()

	// Overlay caller-supplied env.
	for k, v := range policy.Env {
		env = setEnv(env, k, v)
	}

	// Pin PATH to the jail dir so only allowlisted bins are reachable.
	if jailDir != "" {
		env = setEnv(env, "PATH", jailDir)
		*audit = append(*audit, AuditEvent{
			Kind:   "jail",
			Detail: "PATH restricted to jail dir; allowed bins: " + strings.Join(policy.JailBins, ", "),
		})
	}

	// Inject secret values (codename → value) into the child env only.
	for codename, value := range policy.Secrets {
		env = setEnv(env, codename, value)
		*audit = append(*audit, AuditEvent{
			Kind:   "secret",
			Detail: "injected secret into child env by codename: " + codename,
		})
	}

	return env
}

// buildJail materialises a restricted-PATH directory containing symlinks (or
// copies) to the real binaries named in policy.JailBins, resolved from the host
// PATH. Returns the jail dir path ("" if no bins requested) and a cleanup func.
func buildJail(policy CagedPolicy, audit *[]AuditEvent) (jailDir string, cleanup func(), err error) {
	cleanup = func() {}
	if len(policy.JailBins) == 0 {
		return "", cleanup, nil
	}

	dir, mkErr := os.MkdirTemp("", "pulp-caged-jail-")
	if mkErr != nil {
		return "", cleanup, mkErr
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	for _, bin := range policy.JailBins {
		real := lookPathBin(bin)
		if real == "" {
			*audit = append(*audit, AuditEvent{
				Kind:   "jail",
				Detail: "requested bin not found on host PATH: " + bin,
				Denied: true,
			})
			continue
		}
		shim := bin
		if runtime.GOOS == "windows" {
			ext := strings.ToLower(filepath.Ext(shim))
			if ext == ".exe" || ext == ".cmd" || ext == ".bat" || ext == ".com" {
				shim = shim[:len(shim)-len(ext)]
			}
			shim += ".exe"
		}
		dst := filepath.Join(dir, shim)
		if linkErr := linkOrCopy(real, dst); linkErr != nil {
			cleanup()
			return "", func() {}, linkErr
		}
	}
	return dir, cleanup, nil
}

// ── helpers ────────────────────────────────────────────────────────────────

// setEnv returns env with key set to value, removing any prior entry for key
// (case-insensitive so Windows env works correctly).
func setEnv(env []string, key, value string) []string {
	out := env[:0:0]
	upperKey := strings.ToUpper(key) + "="
	for _, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), upperKey) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, key+"="+value)
}

// lookPathBin resolves a binary basename against the host PATH, returning its
// absolute path or "" if not found.
func lookPathBin(name string) string {
	// Use the OS-native lookup (handles .exe resolution on Windows).
	if p, err := execLookPath(name); err == nil {
		return p
	}
	return ""
}
