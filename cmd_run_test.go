package main

// cmd_run_test.go — integration tests for `projx-engine run --dry-run`.
//
// These tests build the real engine binary and invoke it with --dry-run so no
// agent is actually launched and no exec happens.  They verify:
//
//  1. A design/architecture task routes to agent + deep-reasoning class.
//  2. A verify task routes to deterministic + verify op.
//  3. A history task routes to deterministic + store log op.
//  4. A small/typo task routes to agent + cheap-fast class.
//  5. An unrecognised task routes to agent + default class.
//  6. An empty task prints a usage error.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runDryRun invokes `projx-engine --root <root> run --dry-run <task...>` and
// returns (stdout, stderr, exitCode).
func runDryRun(t *testing.T, engineBin, root string, taskWords ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	args := append([]string{"--root", root, "run", "--dry-run"}, taskWords...)
	cmd := exec.CommandContext(ctx, engineBin, args...)
	cmd.Env = os.Environ()

	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	runErr := cmd.Run()
	code := 0
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = 1
		}
	}
	return outBuf.String(), errBuf.String(), code
}

// mkRunRoot creates a temp dir with .projx/ so LoadConfig works.
func mkRunRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatalf("mkdir .projx: %v", err)
	}
	return root
}

// TestRunDryRunAgentDeepReasoning verifies that an architecture task routes to
// "agent" with class "deep-reasoning".
func TestRunDryRunAgentDeepReasoning(t *testing.T) {
	root := mkRunRoot(t)
	engineBin := buildEngine(t, t.TempDir())

	stdout, _, code := runDryRun(t, engineBin, root, "redesign", "the", "architecture")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "agent") {
		t.Errorf("expected 'agent' in output\ngot: %q", stdout)
	}
	if !strings.Contains(stdout, "deep-reasoning") {
		t.Errorf("expected 'deep-reasoning' in output\ngot: %q", stdout)
	}
}

// TestRunDryRunDeterministicVerify verifies that a verify task routes to
// "deterministic" with op "verify".
func TestRunDryRunDeterministicVerify(t *testing.T) {
	root := mkRunRoot(t)
	engineBin := buildEngine(t, t.TempDir())

	stdout, _, code := runDryRun(t, engineBin, root, "verify", "the", "boundaries")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "deterministic") {
		t.Errorf("expected 'deterministic' in output\ngot: %q", stdout)
	}
	if !strings.Contains(stdout, "verify") {
		t.Errorf("expected 'verify' in output\ngot: %q", stdout)
	}
}

// TestRunDryRunDeterministicHistory verifies that a history task routes to
// "deterministic" with op "store log".
func TestRunDryRunDeterministicHistory(t *testing.T) {
	root := mkRunRoot(t)
	engineBin := buildEngine(t, t.TempDir())

	stdout, _, code := runDryRun(t, engineBin, root, "show", "me", "the", "history")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "deterministic") {
		t.Errorf("expected 'deterministic' in output\ngot: %q", stdout)
	}
	if !strings.Contains(stdout, "store log") {
		t.Errorf("expected 'store log' in output\ngot: %q", stdout)
	}
}

// TestRunDryRunAgentCheapFast verifies that a typo-fix task routes to
// "agent" with class "cheap-fast".
func TestRunDryRunAgentCheapFast(t *testing.T) {
	root := mkRunRoot(t)
	engineBin := buildEngine(t, t.TempDir())

	stdout, _, code := runDryRun(t, engineBin, root, "fix", "this", "typo")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "agent") {
		t.Errorf("expected 'agent' in output\ngot: %q", stdout)
	}
	if !strings.Contains(stdout, "cheap-fast") {
		t.Errorf("expected 'cheap-fast' in output\ngot: %q", stdout)
	}
}

// TestRunDryRunAgentDefault verifies that an unrecognised task routes to
// "agent" with class "default".
func TestRunDryRunAgentDefault(t *testing.T) {
	root := mkRunRoot(t)
	engineBin := buildEngine(t, t.TempDir())

	stdout, _, code := runDryRun(t, engineBin, root, "implement", "feature", "X")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "agent") {
		t.Errorf("expected 'agent' in output\ngot: %q", stdout)
	}
	if !strings.Contains(stdout, "default") {
		t.Errorf("expected 'default' in output\ngot: %q", stdout)
	}
}

// TestRunDryRunEmptyTaskFails verifies that an empty task prints a usage error
// and exits non-zero.
func TestRunDryRunEmptyTaskFails(t *testing.T) {
	root := mkRunRoot(t)
	engineBin := buildEngine(t, t.TempDir())

	// Provide only --dry-run, no task words.
	_, stderr, code := runDryRun(t, engineBin, root /* no task */)
	if code == 0 {
		t.Error("expected non-zero exit for empty task, got 0")
	}
	if !strings.Contains(stderr, "usage") {
		t.Errorf("expected usage error in stderr\ngot: %q", stderr)
	}
}
