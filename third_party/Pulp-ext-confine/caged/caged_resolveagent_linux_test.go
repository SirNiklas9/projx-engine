//go:build linux

package caged

// Red->green REGRESSION HARNESS locking cage fix (2) made 2026-06-25:
// resolveAgentPath in caged.go must (a) turn a BARE name into an ABSOLUTE path
// via the host PATH, and (b) resolve SYMLINKS to the real binary. The latter is
// load-bearing: Landlock follows symlinks at exec time and requires the REAL
// target's directory to be granted; agent installers commonly symlink
// (~/.local/bin/claude -> ~/.local/share/claude/versions/<v>), so without
// EvalSymlinks the FS cage would grant the link dir while the kernel denies exec
// of the ungranted target dir ("permission denied").
//
// Internal test (package caged) because resolveAgentPath is unexported.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveAgentPath_BareNameResolvesToAbsolute proves (a): a bare basename on
// PATH resolves to an absolute path. We create a fake executable in a temp dir,
// prepend that dir to PATH, then resolve the bare name.
func TestResolveAgentPath_BareNameResolvesToAbsolute(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "fakeagent")
	writeExecutable(t, exe)

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := resolveAgentPath("fakeagent")
	if err != nil {
		t.Fatalf("resolveAgentPath(bare): %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("resolveAgentPath returned non-absolute path %q for bare name", got)
	}
	// EvalSymlinks both sides so /tmp -> /private/tmp style differences don't
	// cause a spurious mismatch; exe is not a symlink so this is identity here.
	wantReal, _ := filepath.EvalSymlinks(exe)
	if got != wantReal {
		t.Errorf("resolveAgentPath(bare) = %q, want %q", got, wantReal)
	}
}

// TestResolveAgentPath_ResolvesSymlinkToTarget proves (b): given a symlink that
// points at a real executable, resolveAgentPath returns the TARGET path, not the
// link path. This is the exact ~/.local/bin/claude -> versions/<v> scenario.
func TestResolveAgentPath_ResolvesSymlinkToTarget(t *testing.T) {
	// Real target lives in its own "versions" dir.
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "claude-real")
	writeExecutable(t, target)
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("evalsymlinks target: %v", err)
	}

	// Symlink in a separate "bin" dir, exposed on PATH as a bare name.
	binDir := t.TempDir()
	link := filepath.Join(binDir, "claude")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlink (need privilege/devmode): %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := resolveAgentPath("claude")
	if err != nil {
		t.Fatalf("resolveAgentPath(symlink): %v", err)
	}
	if got == link {
		t.Fatalf("REGRESSION: resolveAgentPath returned the LINK %q, not the symlink target — Landlock would deny exec of the ungranted target dir", link)
	}
	if got != realTarget {
		t.Errorf("resolveAgentPath(symlink) = %q, want resolved target %q", got, realTarget)
	}
	// The grant the FS cage derives is filepath.Dir(got); prove it is the target
	// dir, not the link dir.
	if filepath.Dir(got) == binDir {
		t.Errorf("resolved dir is the link dir %q; FS cage would grant the wrong directory", binDir)
	}
	t.Logf("resolveAgentPath: link %q -> target %q (cage grants dir %q)", link, got, filepath.Dir(got))
}

// TestResolveAgentPath_AbsoluteSymlinkResolved proves resolveAgentPath also
// resolves symlinks when given an ALREADY-ABSOLUTE link path (the PATH lookup is
// skipped but EvalSymlinks must still run).
func TestResolveAgentPath_AbsoluteSymlinkResolved(t *testing.T) {
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "agent-real")
	writeExecutable(t, target)
	realTarget, _ := filepath.EvalSymlinks(target)

	link := filepath.Join(t.TempDir(), "agent-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	got, err := resolveAgentPath(link) // absolute path, is a symlink
	if err != nil {
		t.Fatalf("resolveAgentPath(abs symlink): %v", err)
	}
	if got != realTarget {
		t.Errorf("resolveAgentPath(abs symlink) = %q, want %q", got, realTarget)
	}
}

// writeExecutable creates a minimal executable file (mode 0755) at path.
func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write executable %q: %v", path, err)
	}
}
