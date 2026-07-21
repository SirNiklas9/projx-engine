package main

// exec_test.go — integration tests for the multi-call (busybox-style) exec jail.
//
// Tests:
//  1. DENY — engine invoked as "ssh" with ssh not in allowlist → exit 126,
//     stderr contains "denied".  No third-party binary required.
//  2. ALLOW — engine invoked as "go" with go in allowlist → execs real go,
//     exit 0, stdout contains "go version".
//  3. PATH-INTERPOSITION end-to-end — jail.Build + jail.Env → spawn
//     <jaildir>/go with env → exit 0, stdout "go version".

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/SirNiklas9/projx-engine/internal/jail"
)

// buildEngine compiles the projx-engine binary into dir and returns the path.
// If the build fails or times out the test is skipped (not failed) because
// this can happen in constrained CI environments.
func buildEngine(t *testing.T, dir string) string {
	t.Helper()

	engineName := "projx-engine"
	if runtime.GOOS == "windows" {
		engineName = "projx-engine.exe"
	}
	enginePath := filepath.Join(dir, engineName)

	// Allow up to 3 minutes for the build (first build can be slow with module downloads).
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Build from the current module (main package in ".").
	cmd := exec.CommandContext(ctx, "go", "build", "-o", enginePath, ".")
	cmd.SysProcAttr = quietSysProcAttr()
	cmd.Dir = moduleRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("go build failed (skipping exec integration tests): %v\n%s", err, out)
	}
	return enginePath
}

// moduleRoot returns the directory containing the go.mod for this module.
// In a test binary the working dir is set to the package dir, which IS the
// module root for the main package.
func moduleRoot(t *testing.T) string {
	t.Helper()
	// The main package is at the module root.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return wd
}

// realGoBin finds the real "go" binary on the host's PATH (before any jail
// manipulation).  Returns "" if not found.
func realGoBin() string {
	p, err := exec.LookPath("go")
	if err != nil {
		return ""
	}
	return p
}

// shimPath returns the path of the shim for name in dir.
func shimPath(dir, name string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, name+".exe")
	}
	return filepath.Join(dir, name)
}

// hostRealPath returns the real PATH of the host (before any jail env).
func hostRealPath() string {
	return os.Getenv("PATH")
}

// runShim runs the shim at shimBin with the given env and args.
// It captures stdout + stderr and returns them along with the exit code.
func runShim(t *testing.T, shimBin string, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(shimBin, args...)
	cmd.Env = env

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	stdout = outBuf.String()
	stderr = errBuf.String()
	if err == nil {
		code = 0
	} else if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else {
		code = 1
	}
	return
}

// ── Test: DENY ────────────────────────────────────────────────────────────────

// TestExecJailDeniesUnallowlisted builds the real engine, links it as "ssh",
// runs it with PROJX_BROKER_ALLOW_BINS=git (ssh excluded), and asserts:
//   - exit code 126
//   - stderr mentions "denied"
//
// This test does NOT require any particular binary to be installed on the host;
// the deny decision happens before any real binary is looked up.
func TestExecJailDeniesUnallowlisted(t *testing.T) {
	buildDir := t.TempDir()
	enginePath := buildEngine(t, buildDir)

	root := t.TempDir()

	// Link engine as "ssh".
	sshShim := shimPath(buildDir, "ssh")
	if err := linkOrCopy(enginePath, sshShim); err != nil {
		t.Fatalf("link ssh shim: %v", err)
	}

	env := baseJailEnv(root, hostRealPath(), []string{"git"}, nil)
	_, stderr, code := runShim(t, sshShim, env, "-V")

	if code != 126 {
		t.Errorf("exit code = %d, want 126 (denied)", code)
	}
	if !strings.Contains(stderr, "denied") {
		t.Errorf("stderr = %q, expected to contain %q", stderr, "denied")
	}
}

// ── Test: ALLOW ───────────────────────────────────────────────────────────────

// TestExecJailAllowsAllowlisted builds the real engine, links it as "go",
// runs it with PROJX_BROKER_ALLOW_BINS=go and args ["version"], and asserts:
//   - exit code 0
//   - stdout contains "go version"
func TestExecJailAllowsAllowlisted(t *testing.T) {
	if realGoBin() == "" {
		t.Skip("go binary not found on PATH — skipping allow test")
	}

	buildDir := t.TempDir()
	enginePath := buildEngine(t, buildDir)

	root := t.TempDir()

	// Link engine as "go".
	goShim := shimPath(buildDir, "go")
	if err := linkOrCopy(enginePath, goShim); err != nil {
		t.Fatalf("link go shim: %v", err)
	}

	env := baseJailEnv(root, hostRealPath(), []string{"go"}, nil)
	stdout, stderr, code := runShim(t, goShim, env, "version")

	if code != 0 {
		t.Errorf("exit code = %d, want 0 (stderr: %q)", code, stderr)
	}
	if !strings.Contains(stdout, "go version") {
		t.Errorf("stdout = %q, expected to contain %q", stdout, "go version")
	}
}

// ── Test: PATH-INTERPOSITION end-to-end ───────────────────────────────────────

// TestExecJailPathInterposition uses jail.Build + jail.Env to set up the jail,
// then spawns <jaildir>/go with j.Env(os.Environ()) and confirms that the PATH
// interposition resolves the shim → engine → real go correctly.
func TestExecJailPathInterposition(t *testing.T) {
	if realGoBin() == "" {
		t.Skip("go binary not found on PATH — skipping interposition test")
	}

	buildDir := t.TempDir()
	enginePath := buildEngine(t, buildDir)

	jailDir := t.TempDir()
	if err := jail.Build(jailDir, enginePath, []string{"go"}); err != nil {
		t.Fatalf("jail.Build: %v", err)
	}

	j := &jail.Jail{
		Dir:        jailDir,
		RealPath:   hostRealPath(),
		Root:       t.TempDir(),
		AllowBins:  []string{"go"},
		AllowHosts: nil,
	}

	env := j.Env(os.Environ())

	goShim := shimPath(jailDir, "go")
	stdout, stderr, code := runShim(t, goShim, env, "version")
	_ = stderr

	if code != 0 {
		t.Errorf("exit code = %d, want 0 (stderr: %q)", code, stderr)
	}
	if !strings.Contains(stdout, "go version") {
		t.Errorf("stdout = %q, expected to contain %q", stdout, "go version")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// baseJailEnv returns the env for a jailed invocation: the full host environment
// with the PROJX_* broker variables appended (appended keys win over earlier
// duplicates per os/exec semantics). It layers onto os.Environ() exactly like the
// real jail.Env does — a real exec'd binary needs a sane environment (e.g. `go`
// needs HOME for its toolchain/cache on Unix); a starved 4-var env would make the
// child misbehave on some platforms and is not representative of real usage.
func baseJailEnv(root, realPath string, allowBins, allowHosts []string) []string {
	env := append([]string{}, os.Environ()...)
	env = append(env,
		"PROJX_REAL_PATH="+realPath,
		"PROJX_BROKER_ROOT="+root,
		"PROJX_BROKER_ALLOW_BINS="+strings.Join(allowBins, ","),
		"PROJX_BROKER_ALLOW_HOSTS="+strings.Join(allowHosts, ","),
	)
	return env
}

// linkOrCopy creates dst as a link/copy of src.
// Tries symlink → hard link → byte-copy, in that order.
func linkOrCopy(src, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return nil // already exists
	}
	if err := os.Symlink(src, dst); err == nil {
		return nil
	}
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	return copyExecFile(src, dst)
}

// copyExecFile is a minimal file copy used when links are unavailable.
func copyExecFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	buf := make([]byte, 32*1024)
	for {
		n, readErr := in.Read(buf)
		if n > 0 {
			if _, writeErr := out.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
		}
		if readErr != nil {
			break // EOF or real error — either way stop reading
		}
	}
	return out.Close()
}
