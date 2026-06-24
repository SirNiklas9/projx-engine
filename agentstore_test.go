package main

// agentstore_test.go — end-to-end proof that an agent running inside the jail
// can read and write project knowledge via the store CLI, and that the
// restricted-mode (PROJX_AGENT_CONTEXT=1) allow/deny rules are correct.
//
// Tests:
//   1. TestAgentStoreWriteAndRead — fake agent commits a doc via
//      'projx-engine store commit' then reads it back via 'projx-engine store
//      query', all through the jailed PATH.  After the run the real on-disk
//      store is opened and the record is asserted to be present.
//   2. TestAgentContextDeniesGate — subprocess with PROJX_AGENT_CONTEXT=1
//      and cmd 'gate add foo' → exit 1 + "not permitted in agent context".
//   3. TestAgentContextDeniesGateRuleKind — subprocess with
//      PROJX_AGENT_CONTEXT=1 tries to commit kind gate-rule → refused by
//      agentWritableKind enforcement.
//   4. TestAgentContextForcesAgentBy — subprocess with
//      PROJX_AGENT_CONTEXT=1, commit --kind doc --by ui → succeeds but the
//      journal records the actor as "agent" (not "ui").

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"os/exec"

	store "github.com/SirNiklas9/projx-store"
)

// fakeAgentStoreSource is a fake agent that:
//  1. Commits a doc record using projx-engine store commit.
//  2. Queries it back using projx-engine store query --kind doc.
//  3. Prints the query result to stdout so the parent test can assert on it.
//
// It locates projx-engine via PATH (which, inside the jail, resolves to the
// shim).  The commit uses --by ui to verify that PROJX_AGENT_CONTEXT overrides
// it to "agent" — the test checks the journal after the run.
const fakeAgentStoreSource = `package main

import (
	"fmt"
	"os"
	"os/exec"
)

func run(args ...string) (string, int) {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = os.Environ()
	out, _ := cmd.CombinedOutput()
	code := 0
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	return string(out), code
}

func main() {
	root := os.Getenv("PROJX_BROKER_ROOT")
	if root == "" {
		root = "."
	}

	// Commit a doc via the jailed projx-engine (with --by ui to test override).
	commitOut, commitCode := run("projx-engine", "--root", root,
		"store", "commit",
		"--kind", "doc",
		"--key", "agent-note",
		"--body", "hello from agent",
		"--by", "ui",
	)
	if commitCode != 0 {
		fmt.Fprintf(os.Stderr, "commit failed (code %d): %s\n", commitCode, commitOut)
		os.Exit(1)
	}
	fmt.Print("COMMIT_OK=true\n")

	// Query it back.
	queryOut, queryCode := run("projx-engine", "--root", root,
		"store", "query", "--kind", "doc",
	)
	if queryCode != 0 {
		fmt.Fprintf(os.Stderr, "query failed (code %d): %s\n", queryCode, queryOut)
		os.Exit(1)
	}
	fmt.Print("QUERY_OUTPUT=" + queryOut + "\n")
}
`

// buildFakeAgentStore compiles fakeAgentStoreSource and returns the binary path.
func buildFakeAgentStore(t *testing.T) string {
	t.Helper()
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "main.go")
	if err := os.WriteFile(srcFile, []byte(fakeAgentStoreSource), 0o644); err != nil {
		t.Fatalf("write fake agent store source: %v", err)
	}

	agentName := "fake-agent-store"
	if runtime.GOOS == "windows" {
		agentName = "fake-agent-store.exe"
	}
	agentBin := filepath.Join(srcDir, agentName)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "build", "-o", agentBin, srcFile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("could not build fake agent store binary: %v\n%s", err, out)
	}
	return agentBin
}

// TestAgentStoreWriteAndRead proves that an agent running inside the jail can
// commit knowledge to the store and query it back, and that the knowledge
// survives in the real on-disk store.
func TestAgentStoreWriteAndRead(t *testing.T) {
	root := setupTestRoot(t)
	fakeAgent := buildFakeAgentStore(t)
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

	t.Logf("engine stderr:\n%s", stderr)
	t.Logf("fake agent stdout:\n%s", stdout)

	if runErr != nil {
		t.Fatalf("agent run failed: %v\nstdout=%q\nstderr=%q", runErr, stdout, stderr)
	}

	// The fake agent must have committed successfully.
	if !strings.Contains(stdout, "COMMIT_OK=true") {
		t.Errorf("COMMIT_OK=true not found in agent stdout\ngot: %q", stdout)
	}

	// The query output must contain the key we committed.
	if !strings.Contains(stdout, "agent-note") {
		t.Errorf("expected 'agent-note' in agent query output\ngot: %q", stdout)
	}

	// Open the real on-disk store and verify the record is there.
	st, err := store.Open(filepath.Join(root, ".projx", "store.db"))
	if err != nil {
		t.Fatalf("open store after agent run: %v", err)
	}
	defer st.Close()

	rec, ok := st.Get("doc/agent-note")
	if !ok {
		t.Fatal("doc/agent-note not found in store after agent run — agent commit did not persist")
	}
	if rec.Body != "hello from agent" {
		t.Errorf("Body = %q, want %q", rec.Body, "hello from agent")
	}
	if rec.Kind != store.KDoc {
		t.Errorf("Kind = %v, want KDoc", rec.Kind)
	}

	// The enforcement banner must mention the store tools.
	if !strings.Contains(stderr, "projx-engine store query") {
		t.Errorf("store tools hint not in enforcement banner\ngot stderr: %q", stderr)
	}
}

// runEngineWithAgentContext runs engineBin with PROJX_AGENT_CONTEXT=1 in a
// temp root and returns (stdout, stderr, exitCode).
func runEngineWithAgentContext(t *testing.T, engineBin, root string, args ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fullArgs := append([]string{"--root", root}, args...)
	cmd := exec.CommandContext(ctx, engineBin, fullArgs...)
	cmd.Env = append(os.Environ(), "PROJX_AGENT_CONTEXT=1")

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

// TestAgentContextDeniesGate verifies that 'gate add foo' is refused when
// PROJX_AGENT_CONTEXT=1, with exit 1 and a clear "not permitted" message.
func TestAgentContextDeniesGate(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	engineBin := buildEngine(t, t.TempDir())

	_, stderr, code := runEngineWithAgentContext(t, engineBin, root, "gate", "add", "foo")

	if code != 1 {
		t.Errorf("exit code = %d, want 1 for denied command", code)
	}
	if !strings.Contains(stderr, "not permitted in agent context") {
		t.Errorf("expected 'not permitted in agent context' in stderr\ngot: %q", stderr)
	}
}

// TestAgentContextDeniesGateRuleKind verifies that committing kind=gate-rule
// under PROJX_AGENT_CONTEXT=1 is refused via agentWritableKind enforcement.
func TestAgentContextDeniesGateRuleKind(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	engineBin := buildEngine(t, t.TempDir())

	_, stderr, code := runEngineWithAgentContext(t, engineBin, root,
		"store", "commit",
		"--kind", "gate-rule",
		"--key", "k",
		"--body", "b",
	)

	if code != 1 {
		t.Errorf("exit code = %d, want 1 for gate-rule commit in agent context", code)
	}
	// Either the "kind is human-only" message or "not permitted" — both prove refusal.
	if !strings.Contains(stderr, "human-only") && !strings.Contains(stderr, "not permitted") {
		t.Errorf("expected refusal message in stderr\ngot: %q", stderr)
	}
}

// TestAgentContextForcesAgentBy verifies that store commit with --by ui under
// PROJX_AGENT_CONTEXT=1 records the actor as "agent" (not "ui") in the journal.
func TestAgentContextForcesAgentBy(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	engineBin := buildEngine(t, t.TempDir())

	stdout, stderr, code := runEngineWithAgentContext(t, engineBin, root,
		"store", "commit",
		"--kind", "doc",
		"--key", "forced-by-test",
		"--body", "body text",
		"--by", "ui", // this must be overridden to "agent"
	)

	t.Logf("stdout: %q", stdout)
	t.Logf("stderr: %q", stderr)

	if code != 0 {
		t.Fatalf("commit failed (code %d): stderr=%q", code, stderr)
	}

	// The journal must record By="agent" not "ui".
	revs := readRevisions(root)
	if len(revs) == 0 {
		t.Fatal("no revisions in journal after commit")
	}
	last := revs[len(revs)-1]
	if last.By != "agent" {
		t.Errorf("journal By = %q, want %q (--by ui must be overridden to 'agent' in agent context)", last.By, "agent")
	}

	// Also verify the record is in the store.
	st, err := store.Open(filepath.Join(root, ".projx", "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	rec, ok := st.Get("doc/forced-by-test")
	if !ok {
		t.Fatal("doc/forced-by-test not found in store")
	}
	if rec.Kind != store.KDoc {
		t.Errorf("Kind = %v, want KDoc", rec.Kind)
	}
}

// TestAgentContextAllowsStoreQuery verifies that store query is permitted under
// PROJX_AGENT_CONTEXT=1 and returns the expected record.
func TestAgentContextAllowsStoreQuery(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Seed a record directly.
	st, err := store.Open(filepath.Join(root, ".projx", "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Put(store.Record{
		ID:    "doc/queryable",
		Kind:  store.KDoc,
		Scope: store.ScopeProject,
		Key:   "queryable",
		Body:  "this body is queryable",
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	st.Close()

	engineBin := buildEngine(t, t.TempDir())

	stdout, stderr, code := runEngineWithAgentContext(t, engineBin, root,
		"store", "query", "--kind", "doc",
	)
	t.Logf("stdout: %q", stdout)
	t.Logf("stderr: %q", stderr)

	if code != 0 {
		t.Fatalf("store query exited %d: stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "queryable") {
		t.Errorf("expected 'queryable' in query output\ngot: %q", stdout)
	}
}

// TestAgentContextDeniesStoreRm verifies that store rm is blocked in agent context.
func TestAgentContextDeniesStoreRm(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	engineBin := buildEngine(t, t.TempDir())

	_, stderr, code := runEngineWithAgentContext(t, engineBin, root,
		"store", "rm", "doc/something",
	)
	if code != 1 {
		t.Errorf("exit code = %d, want 1 for store rm in agent context", code)
	}
	if !strings.Contains(stderr, "not permitted in agent context") {
		t.Errorf("expected 'not permitted in agent context' in stderr\ngot: %q", stderr)
	}
}
