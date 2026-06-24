package main

// cmd_agent_confine_test.go — engine-level OS-confinement denial proof.
//
// When confine.Detect().Available() is true (Landlock on Linux, AppContainer
// on Windows), running `projx-engine agent run` with a fake agent that tries
// to read a file OUTSIDE the project root MUST result in a kernel-denied read.
// If confinement is not available on this platform (cooperative), the denial
// assertion is skipped — the test still builds and runs the fake agent to
// exercise the code path, but cannot make the strong guarantee.
//
// Platform-specific outside-file location:
//   Windows — %TEMP%\projx-confine-outside-<pid>.txt  (not under root, not granted)
//   Linux   — $HOME/.projx-confine-test-outside-<pid> (not under root or /tmp)

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"context"

	"github.com/SirNiklas9/projx-engine/internal/confine"
)

// confinedFakeAgentSource is the fake agent that:
//   1. Reads a file OUTSIDE the root (path from env PROJX_OUTSIDE_PATH).
//   2. Writes "OUTSIDE:OK" or "OUTSIDE:DENIED" to <root>/agentresult.txt.
//
// The result file is INSIDE the root so the parent can read it. Stdout is
// also written for debugging.
const confinedFakeAgentSource = `package main

import (
	"fmt"
	"os"
)

func main() {
	outsidePath := os.Getenv("PROJX_OUTSIDE_PATH")
	resultPath  := os.Getenv("PROJX_RESULT_PATH")

	result := "OUTSIDE:DENIED"
	_, err := os.ReadFile(outsidePath)
	if err == nil {
		result = "OUTSIDE:OK"
	}

	fmt.Println(result)

	if resultPath != "" {
		_ = os.WriteFile(resultPath, []byte(result+"\n"), 0o644)
	}
}
`

// buildConfinedFakeAgent compiles the confined fake agent binary. Returns the
// path to the binary. Static (CGO_ENABLED=0) to minimise filesystem deps.
func buildConfinedFakeAgent(t *testing.T) string {
	t.Helper()

	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "main.go")
	if err := os.WriteFile(srcFile, []byte(confinedFakeAgentSource), 0o644); err != nil {
		t.Fatalf("write confined fake agent source: %v", err)
	}

	agentName := "confined-agent"
	if runtime.GOOS == "windows" {
		agentName = "confined-agent.exe"
	}
	agentBin := filepath.Join(srcDir, agentName)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "build", "-o", agentBin, srcFile)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("could not build confined fake agent (skipping): %v\n%s", err, out)
	}
	return agentBin
}

// outsideFilePath returns a path OUTSIDE the root that should be denied if
// confinement is active. It creates the file with a known secret body and
// registers cleanup.
func outsideFilePath(t *testing.T) string {
	t.Helper()

	var outsidePath string
	if runtime.GOOS == "windows" {
		tmpDir := os.Getenv("TEMP")
		if tmpDir == "" {
			tmpDir = os.Getenv("TMP")
		}
		if tmpDir == "" {
			t.Skip("no TEMP dir on Windows")
		}
		outsidePath = filepath.Join(tmpDir, fmt.Sprintf("projx-confine-outside-%d.txt", os.Getpid()))
	} else {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			t.Skip("no home dir")
		}
		outsidePath = filepath.Join(home, fmt.Sprintf(".projx-confine-test-outside-%d", os.Getpid()))
	}

	if err := os.WriteFile(outsidePath, []byte("OUTSIDE_SECRET\n"), 0o644); err != nil {
		t.Skipf("cannot write outside file %q: %v", outsidePath, err)
	}
	t.Cleanup(func() { os.Remove(outsidePath) })
	return outsidePath
}

// TestAgentConfinementDenialEndToEnd is the engine-level denial proof.
//
//   - When confine.Detect().Available() == true (Linux/Windows): asserts
//     that the fake agent wrote OUTSIDE:DENIED (kernel denied the read).
//   - When Available() == false (cooperative platforms): runs the agent but
//     does NOT assert denial — the test passes as "checked code path" only.
func TestAgentConfinementDenialEndToEnd(t *testing.T) {
	c := confine.Detect()
	t.Logf("confiner: level=%q available=%v", c.Level(), c.Available())

	root := setupTestRoot(t) // reuse helper from cmd_agent_test.go
	fakeAgent := buildConfinedFakeAgent(t)
	engineBin := buildEngine(t, t.TempDir())
	outsidePath := outsideFilePath(t)

	// The result file is written INSIDE the root by the fake agent.
	resultPath := filepath.Join(root, "agentresult.txt")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, engineBin,
		"--root", root,
		"agent", "run",
	)
	cmd.Env = append(os.Environ(),
		"PROJX_AGENT_CMD="+fakeAgent,
		"PROJX_OUTSIDE_PATH="+outsidePath,
		"PROJX_RESULT_PATH="+resultPath,
	)

	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	runErr := cmd.Run()
	stdout := outBuf.String()
	stderr := errBuf.String()

	t.Logf("engine stderr:\n%s", stderr)
	t.Logf("fake agent stdout:\n%s", stdout)

	if runErr != nil {
		t.Fatalf("projx-engine agent run exited with error: %v\nstdout=%q\nstderr=%q", runErr, stdout, stderr)
	}

	// Read the result file written by the agent inside the root.
	resultData, readErr := os.ReadFile(resultPath)
	if readErr != nil {
		t.Fatalf("could not read agent result file %q: %v", resultPath, readErr)
	}
	result := strings.TrimSpace(string(resultData))
	t.Logf("agent result: %q", result)

	if !c.Available() {
		t.Logf("SKIP denial assertion: confinement not available on this platform (cooperative)")
		// Still verify the file was written (code path exercise).
		if result != "OUTSIDE:OK" && result != "OUTSIDE:DENIED" {
			t.Errorf("unexpected result from confined agent: %q", result)
		}
		return
	}

	// Confinement IS active — assert kernel denial.
	if result != "OUTSIDE:DENIED" {
		t.Errorf("DENIAL PROOF FAILED: outside-root file was NOT denied by the kernel\n"+
			"agent result: %q\n"+
			"This means the %s wall did NOT hold.", result, c.Level())
	}
	if strings.Contains(result, "OUTSIDE_SECRET") {
		t.Errorf("LEAK: the outside-file content escaped confinement")
	}
}
