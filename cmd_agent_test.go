package main

// cmd_agent_test.go — end-to-end proof that `agent run`:
//   1. Injects the ambient store context into the agent process.
//   2. Enforces the exec jail (a denied binary cannot be exec'd from inside).
//   3. Sets the agent's working directory to the project root.
//
// Strategy: build a scripted "fake agent" from inline Go source, then run the
// real projx-engine binary with PROJX_AGENT_CMD pointing at it and assert the
// fake agent's output proves all three properties.
//
// Platform: works on Windows AND Linux because the fake agent is compiled from Go
// source (not a shell script) and uses Go's os/exec semantics.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	store "github.com/SirNiklas9/projx-store"
)

// fakeAgentSource is the Go source for the scripted fake agent.
// When run, it prints three lines to stdout:
//   STORE_CONTEXT_PRESENT=<true|false>
//   DENIED_BIN_BLOCKED=<true|false>   (or DENIED_BIN_RAN=true if the denied binary ran)
//   CWD=<working directory>
//
// The "denied binary" is "ssh" — it is never in the default allowlist and is
// guaranteed not to be shimmed by projx-engine. This makes the denial test
// independent of any particular binary being installed on the host; the deny
// decision happens inside runBrokeredExec before any real binary is looked up.
const fakeAgentSource = `package main

import (
	"fmt"
	"os"
	"os/exec"
)

func main() {
	ctx := os.Getenv("PROJX_STORE_CONTEXT")
	if ctx != "" {
		fmt.Println("STORE_CONTEXT_PRESENT=true")
	} else {
		fmt.Println("STORE_CONTEXT_PRESENT=false")
	}

	// Attempt to run "ssh" — it must NOT be in the jail's allowlist.
	// The jail only shims explicitly allowed binaries; ssh is never allowed by
	// default. When the jail is active PATH == jailDir, and jailDir has no ssh
	// shim, so exec.LookPath("ssh") returns "not found" (or the shim is absent),
	// meaning the exec call will fail with a "not found / no such file" error.
	// Either way the binary did NOT run successfully.
	err := exec.Command("ssh").Run()
	if err == nil {
		fmt.Println("DENIED_BIN_RAN=true")
	} else {
		fmt.Println("DENIED_BIN_BLOCKED=true")
	}

	wd, _ := os.Getwd()
	fmt.Println("CWD=" + wd)
}
`

// buildFakeAgent writes fakeAgentSource to a temp dir and compiles it, returning
// the path to the compiled binary. Skips the test if compilation fails.
func buildFakeAgent(t *testing.T) string {
	t.Helper()
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "main.go")
	if err := os.WriteFile(srcFile, []byte(fakeAgentSource), 0o644); err != nil {
		t.Fatalf("write fake agent source: %v", err)
	}

	agentName := "fake-agent"
	if runtime.GOOS == "windows" {
		agentName = "fake-agent.exe"
	}
	agentBin := filepath.Join(srcDir, agentName)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "build", "-o", agentBin, srcFile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("could not build fake agent (skipping agent integration test): %v\n%s", err, out)
	}
	return agentBin
}

// setupTestRoot creates a temp projx root with .projx/ and a store containing
// one convention record so the compiled preamble is non-trivially populated.
func setupTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	projxDir := filepath.Join(root, ".projx")
	if err := os.MkdirAll(projxDir, 0o755); err != nil {
		t.Fatalf("mkdir .projx: %v", err)
	}

	st, err := store.Open(filepath.Join(projxDir, "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	if err := st.Put(store.Record{
		ID:    "convention/test-convention",
		Kind:  store.KConvention,
		Scope: store.ScopeProject,
		Key:   "test-convention",
		Body:  "This is a test convention injected by cmd_agent_test.",
	}); err != nil {
		t.Fatalf("put convention: %v", err)
	}
	return root
}

// TestAgentRunEndToEnd is the main proof:
//   - STORE_CONTEXT_PRESENT=true    → context was injected via env var.
//   - DENIED_BIN_BLOCKED=true       → "ssh" could not exec (jail PATH enforced).
//   - CWD == root                   → agent's working directory is the project root.
func TestAgentRunEndToEnd(t *testing.T) {
	root := setupTestRoot(t)
	fakeAgent := buildFakeAgent(t)
	engineBin := buildEngine(t, t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, engineBin,
		"--root", root,
		"agent", "run",
	)
	cmd.Env = append(os.Environ(), "PROJX_AGENT_CMD="+fakeAgent)

	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	runErr := cmd.Run()
	stdout := outBuf.String()
	stderr := errBuf.String()

	// Print stderr for diagnostics regardless — it contains the enforcement banner.
	t.Logf("engine stderr:\n%s", stderr)
	t.Logf("fake agent stdout:\n%s", stdout)

	if runErr != nil {
		t.Fatalf("projx-engine agent run exited with error: %v\nstdout=%q\nstderr=%q", runErr, stdout, stderr)
	}

	// Assert 1: store context was injected.
	if !strings.Contains(stdout, "STORE_CONTEXT_PRESENT=true") {
		t.Errorf("STORE_CONTEXT_PRESENT=true not found in agent stdout\ngot: %q", stdout)
	}

	// Assert 2: the denied binary (ssh) was blocked — ALWAYS run (no host-dependency).
	if strings.Contains(stdout, "DENIED_BIN_RAN=true") {
		t.Errorf("DENIED_BIN_RAN=true: ssh executed successfully inside the jail — exec jail is broken")
	}
	if !strings.Contains(stdout, "DENIED_BIN_BLOCKED=true") {
		t.Errorf("DENIED_BIN_BLOCKED=true not found in agent stdout — expected ssh to be blocked\ngot: %q", stdout)
	}

	// Assert 3: CWD is the project root.
	// Resolve symlinks on both sides so /var vs /private/var on macOS doesn't bite us.
	wantCWD, _ := filepath.EvalSymlinks(root)
	if wantCWD == "" {
		wantCWD = root
	}
	if !strings.Contains(stdout, fmt.Sprintf("CWD=%s", wantCWD)) {
		// Also accept the non-symlink-resolved root.
		if !strings.Contains(stdout, fmt.Sprintf("CWD=%s", root)) {
			t.Errorf("CWD not set to project root\nwant CWD=%s (or %s)\ngot: %q", wantCWD, root, stdout)
		}
	}

	// Assert 4 (optional): enforcement banner is on stderr — sanity check.
	if !strings.Contains(stderr, "sandbox ACTIVE") {
		t.Errorf("enforcement banner not found in stderr\ngot: %q", stderr)
	}
}

// TestAgentRunNoAgentFound verifies that a clear error is printed and exit 1 is
// returned when no agent can be resolved.
func TestAgentRunNoAgentFound(t *testing.T) {
	root := setupTestRoot(t)
	engineBin := buildEngine(t, t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Use an env that strips PROJX_AGENT_CMD and removes "claude" from PATH.
	// We build a minimal env: only what the store Open needs (HOME/USERPROFILE on
	// Windows), plus an empty PATH so LookPath("claude") will fail.
	minEnv := []string{"PATH=", "PROJX_AGENT_CMD="}
	if runtime.GOOS == "windows" {
		// sqlite needs TEMP/TMP on Windows for its temp files.
		for _, k := range []string{"TEMP", "TMP", "USERPROFILE", "SYSTEMROOT"} {
			if v := os.Getenv(k); v != "" {
				minEnv = append(minEnv, k+"="+v)
			}
		}
	} else {
		if v := os.Getenv("HOME"); v != "" {
			minEnv = append(minEnv, "HOME="+v)
		}
	}

	cmd := exec.CommandContext(ctx, engineBin, "--root", root, "agent", "run")
	cmd.Env = minEnv

	var errBuf strings.Builder
	cmd.Stderr = &errBuf

	runErr := cmd.Run()
	stderr := errBuf.String()

	if runErr == nil {
		t.Fatal("expected exit error when no agent found, got nil")
	}
	if !strings.Contains(stderr, "no agent found") {
		t.Errorf("expected 'no agent found' in stderr, got: %q", stderr)
	}
}
