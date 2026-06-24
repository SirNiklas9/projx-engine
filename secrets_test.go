package main

// secrets_test.go — security tests for the secrets-by-codename subsystem.
//
// TestSecretSealRoundTrip          — seal/reopen/resolve round-trip; store.json
//                                    must not contain the plaintext.
// TestSecretNamesNeverLeakValues   — Names() returns codenames only.
// TestSecretInjectedIntoBrokeredChild — plaintext reaches brokered child env.
// TestSecretNotInAgentEnv          — agent process env has codenames, not values.

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
	"github.com/SirNiklas9/projx-engine/internal/secrets"
)

// ── TestSecretSealRoundTrip ──────────────────────────────────────────────────

func TestSecretSealRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PROJX_SECRETS_DIR", dir)

	st, err := secrets.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Set("TOK", "s3cr3t-value"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Reopen — simulates a fresh process.
	st2, err := secrets.Open()
	if err != nil {
		t.Fatalf("Open (reopen): %v", err)
	}
	vals, err := st2.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if vals["TOK"] != "s3cr3t-value" {
		t.Errorf("Resolve[TOK] = %q, want %q", vals["TOK"], "s3cr3t-value")
	}
	names := st2.Names()
	if len(names) != 1 || names[0] != "TOK" {
		t.Errorf("Names() = %v, want [TOK]", names)
	}

	// KEY ASSERTION: store.json must not contain the plaintext.
	raw, err := os.ReadFile(filepath.Join(dir, "store.json"))
	if err != nil {
		t.Fatalf("read store.json: %v", err)
	}
	if bytes.Contains(raw, []byte("s3cr3t-value")) {
		t.Errorf("store.json contains plaintext %q — sealing is broken\nfile contents:\n%s",
			"s3cr3t-value", raw)
	}
}

// ── TestSecretNamesNeverLeakValues ────────────────────────────────────────────

func TestSecretNamesNeverLeakValues(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PROJX_SECRETS_DIR", dir)

	st, err := secrets.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Set("API_KEY", "plaintext-value-do-not-leak"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	for _, n := range st.Names() {
		if strings.Contains(n, "plaintext-value-do-not-leak") {
			t.Errorf("Names() entry %q contains the plaintext value", n)
		}
	}
	if got := st.Names(); len(got) != 1 || got[0] != "API_KEY" {
		t.Errorf("Names() = %v, want [API_KEY]", got)
	}
}

// ── TestSecretInjectedIntoBrokeredChild ───────────────────────────────────────

// fakeSecretToolSource writes os.Getenv("TOK") to the file path in argv[1].
const fakeSecretToolSource = `package main
import "os"
func main() {
	_ = os.WriteFile(os.Args[1], []byte(os.Getenv("TOK")), 0o644)
}
`

func buildSmallBin(t *testing.T, name, src string) string {
	t.Helper()
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "main.go")
	if err := os.WriteFile(srcFile, []byte(src), 0o644); err != nil {
		t.Fatalf("write %s source: %v", name, err)
	}
	binName := name
	if runtime.GOOS == "windows" {
		binName = name + ".exe"
	}
	bin := filepath.Join(srcDir, binName)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, srcFile).CombinedOutput()
	if err != nil {
		t.Skipf("could not build %s (skipping): %v\n%s", name, err, out)
	}
	return bin
}

func TestSecretInjectedIntoBrokeredChild(t *testing.T) {
	secretsDir := t.TempDir()
	buildDir := t.TempDir()
	jailDir := t.TempDir()
	root := t.TempDir()

	// Seal secret directly (no CLI round-trip needed here).
	t.Setenv("PROJX_SECRETS_DIR", secretsDir)
	st, err := secrets.Open()
	if err != nil {
		t.Fatalf("Open secrets: %v", err)
	}
	if err := st.Set("TOK", "THEVALUE"); err != nil {
		t.Fatalf("Set TOK: %v", err)
	}

	enginePath := buildEngine(t, buildDir)
	toolBin := buildSmallBin(t, "fake-secret-tool", fakeSecretToolSource)

	if err := jail.Build(jailDir, enginePath, []string{"fake-secret-tool"}); err != nil {
		t.Fatalf("jail.Build: %v", err)
	}

	outFile := filepath.Join(t.TempDir(), "tok-out.txt")

	shimName := "fake-secret-tool"
	if runtime.GOOS == "windows" {
		shimName = "fake-secret-tool.exe"
	}
	shimBin := filepath.Join(jailDir, shimName)

	// PROJX_REAL_PATH must include the directory containing the real tool binary.
	realPath := filepath.Dir(toolBin)
	if existing := os.Getenv("PATH"); existing != "" {
		realPath = realPath + string(os.PathListSeparator) + existing
	}

	env := append(os.Environ(),
		"PROJX_REAL_PATH="+realPath,
		"PROJX_BROKER_ROOT="+root,
		"PROJX_BROKER_ALLOW_BINS=fake-secret-tool",
		"PROJX_BROKER_ALLOW_HOSTS=",
		"PROJX_SECRETS_DIR="+secretsDir,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, shimBin, outFile)
	cmd.Env = env
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("shim run: %v\nstderr: %s", err, errBuf.String())
	}

	got, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	if string(got) != "THEVALUE" {
		t.Errorf("brokered child got TOK=%q, want %q (injection failed)", string(got), "THEVALUE")
	}
}

// ── TestSecretNotInAgentEnv ───────────────────────────────────────────────────

const agentSecretCheckSource = `package main
import (
	"fmt"
	"os"
)
func main() {
	fmt.Println("TOK_VALUE=" + os.Getenv("TOK"))
	fmt.Println("SECRET_NAMES=" + os.Getenv("PROJX_SECRET_NAMES"))
}
`

func TestSecretNotInAgentEnv(t *testing.T) {
	secretsDir := t.TempDir()
	buildDir := t.TempDir()
	root := setupTestRoot(t)

	t.Setenv("PROJX_SECRETS_DIR", secretsDir)
	st, err := secrets.Open()
	if err != nil {
		t.Fatalf("Open secrets: %v", err)
	}
	if err := st.Set("TOK", "SECRETVAL"); err != nil {
		t.Fatalf("Set TOK: %v", err)
	}

	enginePath := buildEngine(t, buildDir)
	agentBin := buildSmallBin(t, "fake-agent-seccheck", agentSecretCheckSource)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, enginePath, "--root", root, "agent", "run")
	cmd.Env = append(os.Environ(),
		"PROJX_AGENT_CMD="+agentBin,
		"PROJX_SECRETS_DIR="+secretsDir,
	)

	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		t.Fatalf("agent run: %v\nstdout: %s\nstderr: %s", err, outBuf.String(), errBuf.String())
	}

	stdout := outBuf.String()
	t.Logf("fake agent stdout:\n%s", stdout)
	t.Logf("engine stderr:\n%s", errBuf.String())

	// SECURITY ASSERTION 1: TOK value must be empty in the agent's environment.
	tokLeaked := false
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "TOK_VALUE=") {
			val := strings.TrimPrefix(line, "TOK_VALUE=")
			if val != "" {
				tokLeaked = true
				t.Errorf("SECURITY FAILURE: agent env contains TOK=%q — plaintext leaked to agent", val)
			}
			break
		}
	}
	if tokLeaked {
		t.Logf("full stdout: %q", stdout)
	}

	// SECURITY ASSERTION 2: PROJX_SECRET_NAMES must contain "TOK".
	namesFound := false
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "SECRET_NAMES=") {
			if strings.Contains(line, "TOK") {
				namesFound = true
			}
			break
		}
	}
	if !namesFound {
		t.Errorf("PROJX_SECRET_NAMES did not contain TOK in agent stdout:\n%s", stdout)
	}
}
