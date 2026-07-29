//go:build linux

package core

// Red->green REGRESSION HARNESS locking the cage fixes made 2026-06-25:
//
//	(1) Landlock file-vs-dir grant split — a Policy granting a regular FILE
//	    (e.g. ~/.claude.json) must apply via ROFiles/RWFiles, NOT via the
//	    directory access rights, which the kernel rejects with "inconsistent
//	    access rights". A directory must still use RODirs/RWDirs.
//	(3) resolverReadPaths must return the EvalSymlinks target of
//	    /etc/resolv.conf so a confined resolver can read it.
//
// These tests live in package core (not core_test) because splitDirsFiles and
// resolverReadPaths are unexported. The Apply-level proof compiles a helper that
// calls Apply with a file grant in a fresh subprocess (Apply is irreversible).

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSplitDirsFiles_PartitionsRegularFilesFromDirs is the unit-level lock on
// fix (1): a regular file must land in the files bucket and a directory in the
// dirs bucket. If a future refactor sent a regular file through the dir bucket
// (RODirs/RWDirs), the kernel would reject the ruleset with "inconsistent
// access rights" — exactly the 2026-06-25 bug. Missing paths must be dropped.
func TestSplitDirsFiles_PartitionsRegularFilesFromDirs(t *testing.T) {
	tmp := t.TempDir()

	// A regular file standing in for ~/.claude.json.
	file := filepath.Join(tmp, ".claude.json")
	if err := os.WriteFile(file, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	// A subdirectory.
	dir := filepath.Join(tmp, "subdir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	missing := filepath.Join(tmp, "does-not-exist")

	dirs, files := splitDirsFiles([]string{file, dir, missing, tmp})

	// The regular file must be in files, never in dirs.
	if !contains(files, file) {
		t.Errorf("regular file %q not partitioned into files bucket: files=%v", file, files)
	}
	if contains(dirs, file) {
		t.Errorf("regular file %q wrongly placed in dirs bucket (would cause 'inconsistent access rights'): dirs=%v", file, dirs)
	}
	// The directories must be in dirs, never in files.
	if !contains(dirs, dir) || !contains(dirs, tmp) {
		t.Errorf("directories not partitioned into dirs bucket: dirs=%v", dirs)
	}
	if contains(files, dir) || contains(files, tmp) {
		t.Errorf("directory wrongly placed in files bucket: files=%v", files)
	}
	// Missing paths must be dropped entirely (a Landlock rule on a missing path
	// errors the whole ruleset).
	if contains(dirs, missing) || contains(files, missing) {
		t.Errorf("missing path %q was not dropped: dirs=%v files=%v", missing, dirs, files)
	}
}

// TestApply_FileGrantNoInconsistentAccessRights is the Apply-level lock on fix
// (1): granting a regular FILE (RO and RW) through a real landlockConfiner.Apply
// must NOT fail with "inconsistent access rights". Apply is irreversible for the
// process lifetime, so we run it in a freshly-compiled helper subprocess that
// exits 0 on success and prints the error on failure.
//
// Before the fix, putting a regular file in ReadOnly/ReadWrite routed it through
// RODirs/RWDirs and BestEffort().RestrictPaths returned the kernel's
// "inconsistent access rights" error — caught here.
func TestApply_FileGrantNoInconsistentAccessRights(t *testing.T) {
	if !(landlockConfiner{}).Available() {
		t.Skip("Landlock not available on this kernel")
	}

	root := t.TempDir()
	roFile := filepath.Join(root, ".claude.json") // file standing in for ~/.claude.json
	if err := os.WriteFile(roFile, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write ro file: %v", err)
	}
	rwFile := filepath.Join(root, "rw.json")
	if err := os.WriteFile(rwFile, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write rw file: %v", err)
	}

	helper := buildApplyHelper(t)

	cmd := exec.Command(helper)
	cmd.Env = append(os.Environ(),
		"APPLY_ROOT="+root,
		"APPLY_RO_FILE="+roFile,
		"APPLY_RW_FILE="+rwFile,
	)
	out, err := cmd.CombinedOutput()
	t.Logf("apply helper output: %q", strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("Apply with a file grant failed (regression of file-vs-dir split): %v\n%s", err, out)
	}
	if strings.Contains(string(out), "inconsistent access rights") {
		t.Fatalf("REGRESSION: Apply produced 'inconsistent access rights' for a file grant:\n%s", out)
	}
	if !strings.Contains(string(out), "APPLY_OK") {
		t.Fatalf("helper did not confirm APPLY_OK:\n%s", out)
	}
}

// buildApplyHelper compiles a helper that builds a Policy with a regular FILE in
// both ReadOnly and ReadWrite, calls landlockConfiner.Apply via the exported
// ApplyLandlockFromEnv path (which routes through the same splitDirsFiles), and
// exits 0 printing APPLY_OK, or 1 printing the error. It imports the local
// module via a replace directive (GOWORK=off + path replace, never go.work).
func buildApplyHelper(t *testing.T) string {
	t.Helper()

	src := `package main

import (
	"fmt"
	"os"

	"github.com/BananaLabs-OSS/Pulp-ext-confine/core"
)

func main() {
	// ApplyLandlockFromEnv reconstructs a Policy from PROJX_CONFINE_* and applies
	// it via the same Apply()/splitDirsFiles path used in production. We feed it a
	// regular FILE in both RO and RW so a file-vs-dir regression surfaces as
	// "inconsistent access rights".
	os.Setenv("PROJX_CONFINE_ROOT", os.Getenv("APPLY_ROOT"))
	os.Setenv("PROJX_CONFINE_RO", os.Getenv("APPLY_RO_FILE"))
	os.Setenv("PROJX_CONFINE_RW", os.Getenv("APPLY_RW_FILE"))
	if err := core.ApplyLandlockFromEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "APPLY_FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("APPLY_OK")
}
`
	moduleRoot := findModuleRootR(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write apply helper src: %v", err)
	}
	gomod := "module pulp-apply-helper\n\ngo 1.25\n\nrequire github.com/BananaLabs-OSS/Pulp-ext-confine v0.0.0\n\nreplace github.com/BananaLabs-OSS/Pulp-ext-confine => " + filepath.ToSlash(moduleRoot) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatalf("write apply helper go.mod: %v", err)
	}
	bin := filepath.Join(dir, "apply-helper")
	cmd := exec.Command("go", "build", "-mod=mod", "-o", bin, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build apply helper: %v\n%s", err, out)
	}
	return bin
}

// TestResolverReadPaths_ReturnsEvalSymlinksTarget is the lock on fix (3): when
// /etc/resolv.conf is a symlink (WSL: -> /mnt/wsl/resolv.conf; systemd-resolved:
// -> /run/systemd/resolve/stub-resolv.conf), resolverReadPaths must return the
// EvalSymlinks TARGET, not the link path. A Landlock grant on /etc alone does
// not cover the out-of-/etc target, so a confined resolver would fall back to
// localhost:53 (dead inside the egress netns) and all DNS would fail.
func TestResolverReadPaths_ReturnsEvalSymlinksTarget(t *testing.T) {
	got := resolverReadPaths()

	real, err := filepath.EvalSymlinks("/etc/resolv.conf")
	if err != nil || real == "" {
		// No resolv.conf present (rare in CI containers): resolverReadPaths must
		// then return nothing, never a bogus path.
		if len(got) != 0 {
			t.Fatalf("no resolvable /etc/resolv.conf but resolverReadPaths returned %v", got)
		}
		t.Skip("/etc/resolv.conf not resolvable on this host; nothing to lock")
	}

	if len(got) != 1 {
		t.Fatalf("resolverReadPaths returned %v, want exactly [%s]", got, real)
	}
	if got[0] != real {
		t.Errorf("resolverReadPaths returned link path, not EvalSymlinks target: got %q want %q", got[0], real)
	}
	// Explicitly prove it is the symlink TARGET when /etc/resolv.conf IS a symlink.
	if fi, lerr := os.Lstat("/etc/resolv.conf"); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
		if got[0] == "/etc/resolv.conf" {
			t.Errorf("resolv.conf is a symlink but resolverReadPaths returned the link path itself; symlink not resolved")
		}
		t.Logf("resolv.conf is a symlink -> resolved target %q (correctly granted)", got[0])
	} else {
		t.Logf("resolv.conf is a regular file; resolverReadPaths returned %q", got[0])
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// findModuleRootR walks up to the directory whose go.mod declares this module.
func findModuleRootR(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		data, readErr := os.ReadFile(filepath.Join(dir, "go.mod"))
		if readErr == nil && strings.Contains(string(data), "github.com/BananaLabs-OSS/Pulp-ext-confine") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find Pulp-ext-confine module root")
		}
		dir = parent
	}
}
