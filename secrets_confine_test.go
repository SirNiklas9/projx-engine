package main

// secrets_confine_test.go — proves that secrets are injected into the confined
// agent environment when OS-FS confinement (Landlock / AppContainer) is active.
//
// Strategy:
//  1. Create a temp secrets store with one entry: TESTSEC=secretval.
//  2. Build a fake agent that reads os.Getenv("TESTSEC") and writes
//     "SEC:OK:<value>" or "SEC:MISSING" to <root>/secresult.txt, PLUS also
//     attempts to read a file OUTSIDE the root and writes OUTSIDE:DENIED or
//     OUTSIDE:OK to the same file (two lines, semicolon-separated for simplicity).
//  3. Run `projx-engine --root <root> agent run` with PROJX_SECRETS_DIR set
//     and PROJX_AGENT_CMD pointing at the fake agent.
//  4. When confine.Detect().Available():
//     a. Assert the secret VALUE reached the confined agent (SEC:OK:secretval).
//     b. Assert the outside-root read was still DENIED (confinement still holds).
//     When not available: skip assertions but still verify the file was written.
//
// Both assertions in the same test prove: inject-before-confine works AND the
// confinement wall is not weakened by the injection.

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

	"github.com/SirNiklas9/projx-engine/internal/confine"
	"github.com/SirNiklas9/projx-engine/internal/secrets"
)

// secretAgentSource is the fake agent source. It reads TESTSEC from env,
// also reads PROJX_OUTSIDE_PATH and tries to read that file, then writes
// two result tokens separated by "|" to PROJX_RESULT_PATH.
const secretAgentSource = `package main

import (
	"fmt"
	"os"
)

func main() {
	val := os.Getenv("TESTSEC")
	var secToken string
	if val == "" {
		secToken = "SEC:MISSING"
	} else {
		secToken = "SEC:OK:" + val
	}

	outsidePath := os.Getenv("PROJX_OUTSIDE_PATH")
	var outsideToken string
	if outsidePath == "" {
		outsideToken = "OUTSIDE:SKIP"
	} else {
		_, err := os.ReadFile(outsidePath)
		if err != nil {
			outsideToken = "OUTSIDE:DENIED"
		} else {
			outsideToken = "OUTSIDE:OK"
		}
	}

	result := secToken + "|" + outsideToken
	fmt.Println(result)

	resultPath := os.Getenv("PROJX_RESULT_PATH")
	if resultPath != "" {
		_ = os.WriteFile(resultPath, []byte(result+"\n"), 0o644)
	}
}
`

// buildSecretFakeAgent compiles the secret-reading fake agent. CGO disabled for
// minimal filesystem deps (so Landlock policy does not need to grant libc paths).
func buildSecretFakeAgent(t *testing.T) string {
	t.Helper()

	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "main.go")
	if err := os.WriteFile(srcFile, []byte(secretAgentSource), 0o644); err != nil {
		t.Fatalf("write secret agent source: %v", err)
	}

	name := "secret-agent"
	if runtime.GOOS == "windows" {
		name = "secret-agent.exe"
	}
	bin := filepath.Join(srcDir, name)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, srcFile)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not build secret fake agent (skipping): %v\n%s", err, out)
	}
	return bin
}

// setupSecretsDir creates a temp secrets store containing TESTSEC=secretval
// and returns the dir path and the expected plaintext value.
func setupSecretsDir(t *testing.T) (dir, value string) {
	t.Helper()

	dir = t.TempDir()
	// Point secrets.Open() at this dir via env var — we do this by setting
	// the env on the subprocess, but we need to create the store here using
	// the same path.
	t.Setenv("PROJX_SECRETS_DIR", dir)

	st, err := secrets.Open()
	if err != nil {
		t.Fatalf("setup secrets store: %v", err)
	}
	value = "secretval"
	if err := st.Set("TESTSEC", value); err != nil {
		t.Fatalf("secrets.Set: %v", err)
	}
	return dir, value
}

// outsideSecretPath creates a file OUTSIDE the root that the confined agent
// should not be able to read. Returns the path and registers cleanup.
func outsideSecretPath(t *testing.T) string {
	t.Helper()
	var path string
	if runtime.GOOS == "windows" {
		tmpDir := os.Getenv("TEMP")
		if tmpDir == "" {
			tmpDir = os.Getenv("TMP")
		}
		if tmpDir == "" {
			t.Skip("no TEMP dir on Windows — skipping outside-denial check")
		}
		path = filepath.Join(tmpDir, fmt.Sprintf("projx-sec-outside-%d.txt", os.Getpid()))
	} else {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			t.Skip("no home dir — skipping outside-denial check")
		}
		path = filepath.Join(home, fmt.Sprintf(".projx-sec-confine-test-%d", os.Getpid()))
	}
	if err := os.WriteFile(path, []byte("OUTSIDE_SECRET_DATA\n"), 0o644); err != nil {
		t.Skipf("cannot write outside file %q: %v", path, err)
	}
	t.Cleanup(func() { os.Remove(path) })
	return path
}

// TestSecretInjectedUnderConfinement proves that a secret (TESTSEC=secretval)
// set in the store is decrypted by the launcher BEFORE confinement is applied
// and reaches the confined agent process via environment injection.
//
// It also proves that OS-FS confinement still holds: the agent cannot read
// a file OUTSIDE the project root even though secrets were injected.
func TestSecretInjectedUnderConfinement(t *testing.T) {
	c := confine.Detect()
	t.Logf("confiner: level=%q available=%v", c.Level(), c.Available())

	root := setupTestRoot(t)
	secretsDir, wantVal := setupSecretsDir(t)
	fakeAgent := buildSecretFakeAgent(t)
	engineBin := buildEngine(t, t.TempDir())
	outsidePath := outsideSecretPath(t)

	resultPath := filepath.Join(root, "secresult.txt")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, engineBin,
		"--root", root,
		"agent", "run",
	)
	cmd.Env = append(os.Environ(),
		"PROJX_AGENT_CMD="+fakeAgent,
		"PROJX_SECRETS_DIR="+secretsDir,
		"PROJX_OUTSIDE_PATH="+outsidePath,
		"PROJX_RESULT_PATH="+resultPath,
		"PROJX_CAGE=1", // asserts CONFINED behavior; cage is opt-in now
	)

	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	runErr := cmd.Run()
	stdout := outBuf.String()
	stderr := errBuf.String()

	t.Logf("engine stderr:\n%s", stderr)
	t.Logf("agent stdout:\n%s", stdout)

	if runErr != nil {
		t.Fatalf("projx-engine agent run failed: %v\nstdout=%q\nstderr=%q", runErr, stdout, stderr)
	}

	// Read the result file written by the confined agent inside the root.
	resultData, readErr := os.ReadFile(resultPath)
	if readErr != nil {
		t.Fatalf("could not read agent result file %q: %v\n(engine stderr: %s)", resultPath, readErr, stderr)
	}
	result := strings.TrimSpace(string(resultData))
	t.Logf("agent result: %q", result)

	tokens := strings.SplitN(result, "|", 2)
	secToken := tokens[0]
	outsideToken := ""
	if len(tokens) == 2 {
		outsideToken = tokens[1]
	}

	if !c.Available() {
		// Cooperative platform: we can verify secret injection works (the launcher
		// injects before launch on all paths) but we cannot assert FS denial.
		t.Logf("SKIP confinement-denial assertion: not available on this platform")
		// The secret should still be injected (cooperative path also reads the store
		// in runAgentCmd and passes it through env). Verify at least the file ran.
		if secToken != "SEC:OK:"+wantVal && secToken != "SEC:MISSING" {
			t.Errorf("unexpected sec token: %q", secToken)
		}
		return
	}

	// Confinement IS active — assert both properties.

	// 1. Secret VALUE reached the confined agent.
	if secToken != "SEC:OK:"+wantVal {
		t.Errorf("SECRET INJECTION FAILED under OS-FS confinement\n"+
			"  want sec token: %q\n"+
			"  got:            %q\n"+
			"  full result:    %q\n"+
			"  engine stderr:  %s",
			"SEC:OK:"+wantVal, secToken, result, stderr)
	}

	// 2. Outside-root file was still kernel-denied (confinement still holds).
	if outsideToken != "OUTSIDE:DENIED" && outsideToken != "OUTSIDE:SKIP" {
		t.Errorf("CONFINEMENT WALL BROKEN: outside-root file was NOT denied\n"+
			"  outside token: %q\n"+
			"  full result:   %q\n"+
			"  This means secret injection weakened the %s wall.",
			outsideToken, result, c.Level())
	}
	if strings.Contains(result, "OUTSIDE_SECRET_DATA") {
		t.Errorf("LEAK: outside file content escaped confinement; result: %q", result)
	}

	// 3. INFO line on stderr (not the old warning).
	if !strings.Contains(stderr, "injected into the confined process environment") {
		t.Errorf("expected INFO injection message on stderr; got: %q", stderr)
	}
	if strings.Contains(stderr, "unavailable under OS-FS") {
		t.Errorf("old 'unavailable' warning still present in stderr; got: %q", stderr)
	}
}
