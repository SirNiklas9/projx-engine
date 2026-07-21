package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	store "github.com/SirNiklas9/projx-store"
)

// TestHandleHookLifecycle drives every event through handleHook and asserts the
// stdout / stderr / exit-code contract — the Go-native replacement for the .sh hooks.
func TestHandleHookLifecycle(t *testing.T) {
	root := t.TempDir()
	seedSessionStore(t, root) // gate secret/**, convention, doc minecraft/login, doc billing
	const sid = "hk"

	// SessionStart → wrapped lean floor (protocol + law), no reference dump.
	out, _, code := handleHook(root, []byte(`{"session_id":"`+sid+`","hook_event_name":"SessionStart"}`))
	if code != 0 {
		t.Fatalf("SessionStart code = %d, want 0", code)
	}
	if !strings.Contains(out, `source="ProjX"`) {
		t.Error("SessionStart output not wrapped in the project-context frame")
	}
	if !strings.Contains(out, "secret/**") || !strings.Contains(out, "READ BEFORE ACTING") {
		t.Error("SessionStart floor missing law/protocol")
	}
	if strings.Contains(out, "minecraft/login/backend") {
		t.Error("SessionStart floor should not dump reference docs")
	}

	// UserPromptSubmit → wrapped task-sliced delta with the relevant doc, not billing.
	out, _, code = handleHook(root, []byte(`{"session_id":"`+sid+`","hook_event_name":"UserPromptSubmit","prompt":"work on the minecraft login backend"}`))
	if code != 0 || !strings.Contains(out, "minecraft/login/backend") {
		t.Errorf("UserPromptSubmit delta missing the login doc (code=%d)", code)
	}
	if strings.Contains(out, "billing/checkout") {
		t.Error("UserPromptSubmit delta leaked the unrelated billing doc")
	}

	// PreToolUse on an off-limits path → block (exit 2) with a reason on stderr.
	_, errOut, code := handleHook(root, []byte(`{"hook_event_name":"PreToolUse","tool_input":{"file_path":"secret/key.txt"}}`))
	if code != 2 || !strings.Contains(errOut, "off-limits") {
		t.Errorf("PreToolUse on secret path = code %d, stderr %q; want 2 + off-limits", code, errOut)
	}
	// PreToolUse on an allowed path → allow (exit 0).
	if _, _, code := handleHook(root, []byte(`{"hook_event_name":"PreToolUse","tool_input":{"file_path":"internal/auth/login.go"}}`)); code != 0 {
		t.Errorf("PreToolUse on allowed path = code %d, want 0", code)
	}

	// PreCompact → no output, resets the checkpoint (NeedFloor set).
	out, _, code = handleHook(root, []byte(`{"session_id":"`+sid+`","hook_event_name":"PreCompact"}`))
	if code != 0 || out != "" {
		t.Errorf("PreCompact = code %d, out %q; want 0 + empty", code, out)
	}
	if cp := readCheckpoint(t, root, sid); !cp.NeedFloor {
		t.Error("PreCompact did not reset NeedFloor")
	}

	// Stop with no armed @remember → silent.
	if _, _, code := handleHook(root, []byte(`{"session_id":"`+sid+`","hook_event_name":"Stop"}`)); code != 0 {
		t.Errorf("Stop with nothing flagged = code %d, want 0", code)
	}

	// Arm an @remember via a prompt, then Stop with nothing committed → nudge (exit 2).
	handleHook(root, []byte(`{"session_id":"`+sid+`","hook_event_name":"UserPromptSubmit","prompt":"@remember login uses JWT"}`))
	_, errOut, code = handleHook(root, []byte(`{"session_id":"`+sid+`","hook_event_name":"Stop"}`))
	if code != 2 || !strings.Contains(errOut, "@remember") {
		t.Errorf("Stop after uncommitted @remember = code %d, stderr %q; want 2 + nudge", code, errOut)
	}

	// Unknown event → no-op.
	if _, _, code := handleHook(root, []byte(`{"hook_event_name":"Whatever"}`)); code != 0 {
		t.Errorf("unknown event = code %d, want 0", code)
	}
}

func TestDecodeLifecycleEventHarnessParity(t *testing.T) {
	claude := decodeLifecycleEvent([]byte(`{
		"session_id":"same", "hook_event_name":"PreToolUse", "cwd":"C:/work",
		"tool_name":"Bash", "tool_input":{"command":"Get-Content docs/plan.md","workdir":"C:/work"}
	}`))
	codex := decodeLifecycleEvent([]byte(`{
		"session_id":"same", "hook_event_name":"PreToolUse", "cwd":"C:/work",
		"tool_name":"exec_command", "tool_input":{"cmd":"Get-Content docs/plan.md","workdir":"C:/work"}
	}`))

	if normalizedHookTool(claude.ToolName) != normalizedHookTool(codex.ToolName) {
		t.Fatalf("tool normalization differs: Claude=%q Codex=%q", normalizedHookTool(claude.ToolName), normalizedHookTool(codex.ToolName))
	}
	if claude.ToolInput.Command != codex.ToolInput.Command {
		t.Fatalf("command normalization differs: Claude=%q Codex=%q", claude.ToolInput.Command, codex.ToolInput.Command)
	}
	claudeTargets := hookTargetPaths(claude)
	codexTargets := hookTargetPaths(codex)
	if len(claudeTargets) != len(codexTargets) {
		t.Fatalf("target normalization differs: Claude=%v Codex=%v", claudeTargets, codexTargets)
	}
	for i := range claudeTargets {
		if claudeTargets[i] != codexTargets[i] {
			t.Fatalf("target normalization differs: Claude=%v Codex=%v", claudeTargets, codexTargets)
		}
	}
}

func TestDecodeLifecycleEventMalformedPayloadIsNoOp(t *testing.T) {
	ev := decodeLifecycleEvent([]byte(`{"hook_event_name":`))
	if ev.Event != "" || ev.SessionID != "" || len(hookTargetPaths(ev)) != 0 {
		t.Fatalf("malformed payload decoded to actionable event: %+v", ev)
	}
	if out, errOut, code := handleHook(t.TempDir(), []byte(`{"hook_event_name":`)); code != 0 || out != "" || errOut != "" {
		t.Fatalf("malformed payload = (%q, %q, %d), want no-op", out, errOut, code)
	}
}

// TestHookRootResolution proves the root is resolved from CLAUDE_PROJECT_DIR or the
// payload cwd, so settings.json needs no shell variables.
func TestHookRootResolution(t *testing.T) {
	envDir := t.TempDir()
	t.Setenv("CLAUDE_PROJECT_DIR", envDir)
	if got := hookRoot("/fallback", nil); got != mustAbs(t, envDir) {
		t.Errorf("CLAUDE_PROJECT_DIR not honored: got %q", got)
	}
	os.Unsetenv("CLAUDE_PROJECT_DIR")
	cwdDir := t.TempDir()
	got := hookRoot("/fallback", []byte(`{"cwd":"`+filepath.ToSlash(cwdDir)+`"}`))
	if got != mustAbs(t, cwdDir) {
		t.Errorf("payload cwd not honored: got %q want %q", got, mustAbs(t, cwdDir))
	}
	if got := hookRoot("/fallback", []byte(`{}`)); got != "/fallback" {
		t.Errorf("fallback root not used: got %q", got)
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestPreToolUseTargetBasedScope proves scope resolution FOLLOWS the target file, not the
// cwd (adr/scope-resolution-is-target-based). Layout: a ROOT store with dispatcher-mode ON
// and a CHILD repo store (root/child/.projx) with it OFF. Running the hook with cwd=root:
//   - editing a file in the child (no dispatcher-mode) is ALLOWED, and
//   - editing a file at the root (dispatcher-mode ON) is BLOCKED.
//
// Plus: the child's own off-limits glob still blocks, and a GLOBAL-floor gate still fires
// on the child target — no gate is weakened by making resolution target-based.
func TestPreToolUseTargetBasedScope(t *testing.T) {
	yoursDir := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", yoursDir) // isolate the global (yours) store from the real machine
	os.Unsetenv("PROJX_ROLE")

	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}

	// Global floor: a secret glob in the per-user (yours) store — must fire for ANY target.
	gst := openStore(root)
	if err := gst.Put(store.Record{ID: "gate/global-pem", Kind: store.KGateRule, Scope: store.ScopeGlobal, Key: "global-pem", Body: "**/*.pem"}); err != nil {
		t.Fatalf("seed global gate: %v", err)
	}
	gst.Close()

	// Root project store: dispatcher-mode ON.
	rst := openStore(root)
	if err := rst.Put(store.Record{ID: "gate-rule/setting-dispatcher-mode", Kind: store.KGateRule, Scope: store.ScopeProject, Key: store.SettingDispatcherMode, Body: "on"}); err != nil {
		t.Fatalf("seed root dispatcher-mode: %v", err)
	}
	rst.Close()

	// Child project store: NO dispatcher-mode, but its own off-limits glob.
	cst := openStore(child)
	if err := cst.Put(store.Record{ID: "gate-rule/secrets-dir", Kind: store.KGateRule, Scope: store.ScopeProject, Key: "secrets-dir", Body: "secret/**"}); err != nil {
		t.Fatalf("seed child gate: %v", err)
	}
	cst.Close()

	edit := func(fp string) (string, int) {
		_, errOut, code := handleHook(root, []byte(`{"hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":`+jsonStr(fp)+`}}`))
		return errOut, code
	}

	// Edit inside the child (dispatcher-mode OFF there) → ALLOWED even though cwd=root has it ON.
	if _, code := edit(filepath.Join(child, "src", "app.go")); code != 0 {
		t.Errorf("edit to child path should be allowed (code 0), got %d", code)
	}
	// Edit at the root (dispatcher-mode ON) → BLOCKED.
	if errOut, code := edit(filepath.Join(root, "main.go")); code != 2 || !strings.Contains(errOut, "dispatcher-mode") {
		t.Errorf("edit to root path should be dispatcher-blocked (code 2), got code %d stderr %q", code, errOut)
	}
	// Child's OWN off-limits glob still blocks a child secret path.
	if errOut, code := edit(filepath.Join(child, "secret", "key.txt")); code != 2 || !strings.Contains(errOut, "off-limits") {
		t.Errorf("child secret path should be off-limits (code 2), got code %d stderr %q", code, errOut)
	}
	// The GLOBAL floor still fires for a child target (a .pem there is denied).
	if errOut, code := edit(filepath.Join(child, "certs", "server.pem")); code != 2 || !strings.Contains(errOut, "off-limits") {
		t.Errorf("global-floor gate should still fire on child target (code 2), got code %d stderr %q", code, errOut)
	}
}

func TestCodexTargetsFloatAcrossProjects(t *testing.T) {
	t.Setenv("PROJX_YOURS_DIR", t.TempDir())
	root := t.TempDir()
	repoA := filepath.Join(root, "repo-a")
	repoB := filepath.Join(root, "repo-b")
	for _, repo := range []string{repoA, repoB} {
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		st := openStore(repo)
		st.Close()
	}

	patchJSON := []byte(`{"session_id":"float","hook_event_name":"PreToolUse","tool_name":"functions.apply_patch","tool_input":{"patch":"*** Begin Patch\n*** Update File: ` + filepath.ToSlash(filepath.Join(repoA, "a.go")) + `\n*** Move to: ` + filepath.ToSlash(filepath.Join(repoB, "b.go")) + `\n*** End Patch"}}`)
	var patchEvent lifecycleEvent
	if err := json.Unmarshal(patchJSON, &patchEvent); err != nil {
		t.Fatal(err)
	}
	targets := hookTargetPaths(patchEvent)
	if len(targets) != 2 {
		t.Fatalf("patch targets = %q, want two", targets)
	}
	if got := lastTargetRoot(root, targets); got != repoB {
		t.Fatalf("patch winner = %q, want %q", got, repoB)
	}
	if _, _, code := handleHook(root, patchJSON); code != 0 {
		t.Fatalf("cross-project patch blocked: %d", code)
	}

	home := repoA
	updateCrumb(home, "float", func(c *statusCrumb) { c.R = lastTargetRoot(root, targets) })
	if got := activeContextRoot(home, "float", "continue"); got != repoB {
		t.Fatalf("context did not float after patch: got %q want %q", got, repoB)
	}

	execJSON := []byte(`{"session_id":"float","hook_event_name":"PreToolUse","tool_name":"exec_command","tool_input":{"cmd":"Get-Content src/app.go","workdir":` + jsonStr(repoA) + `}}`)
	var execEvent lifecycleEvent
	if err := json.Unmarshal(execJSON, &execEvent); err != nil {
		t.Fatal(err)
	}
	execTargets := hookTargetPaths(execEvent)
	if got := lastTargetRoot(root, execTargets); got != repoA {
		t.Fatalf("exec winner = %q, want %q (targets %q)", got, repoA, execTargets)
	}
	updateCrumb(home, "float", func(c *statusCrumb) { c.R = lastTargetRoot(root, execTargets) })
	if got := activeContextRoot(home, "float", "continue"); got != repoA {
		t.Fatalf("context did not float back after exec_command: got %q want %q", got, repoA)
	}
}

func TestCodexPatchChecksEveryProjectGate(t *testing.T) {
	t.Setenv("PROJX_YOURS_DIR", t.TempDir())
	root := t.TempDir()
	repoA := filepath.Join(root, "repo-a")
	repoB := filepath.Join(root, "repo-b")
	for _, repo := range []string{repoA, repoB} {
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	a := openStore(repoA)
	a.Close()
	b := openStore(repoB)
	if err := b.Put(store.Record{ID: "gate-rule/secret", Kind: store.KGateRule, Scope: store.ScopeProject, Key: "secret", Body: "secret/**"}); err != nil {
		t.Fatal(err)
	}
	b.Close()
	input := []byte(`{"hook_event_name":"PreToolUse","tool_name":"apply_patch","tool_input":{"input":"*** Begin Patch\n*** Update File: ` + filepath.ToSlash(filepath.Join(repoA, "ok.go")) + `\n*** Update File: ` + filepath.ToSlash(filepath.Join(repoB, "secret", "key.go")) + `\n*** End Patch"}}`)
	_, errOut, code := handleHook(root, input)
	if code != 2 || !strings.Contains(errOut, "off-limits") {
		t.Fatalf("second project gate = code %d stderr %q", code, errOut)
	}
}

// jsonStr quotes a filesystem path into a valid JSON string literal (escapes backslashes
// so Windows paths round-trip through the hook payload).
func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestParallelWorkerWriteLeaseEnforcedAcrossTools(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(parallelWorkerEnv, "1")
	t.Setenv("PROJX_WORKER_WRITES", "internal/a/**")
	inside := filepath.Join(root, "internal", "a", "ok.go")
	outside := filepath.Join(root, "internal", "b", "no.go")
	for name, input := range map[string][]byte{
		"edit allowed":  []byte(`{"hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":` + jsonStr(inside) + `}}`),
		"patch blocked": []byte(`{"hook_event_name":"PreToolUse","tool_name":"apply_patch","tool_input":{"input":"*** Begin Patch\n*** Update File: ` + filepath.ToSlash(outside) + `\n*** End Patch"}}`),
		"shell blocked": []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"Set-Content ` + filepath.ToSlash(outside) + ` x"}}`),
	} {
		_, stderr, code := handleHook(root, input)
		if name == "edit allowed" && code != 0 {
			t.Fatalf("%s: code=%d stderr=%q", name, code, stderr)
		}
		if name != "edit allowed" && (code != 2 || !strings.Contains(stderr, "outside its declared write lease")) {
			t.Fatalf("%s: code=%d stderr=%q", name, code, stderr)
		}
	}
}

func TestParallelWorkerWriteLeaseFailsClosed(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(parallelWorkerEnv, "1")
	t.Setenv("PROJX_WORKER_WRITES", "")
	input := []byte(`{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"x.go"}}`)
	_, stderr, code := handleHook(root, input)
	if code != 2 || !strings.Contains(stderr, "no write lease") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestParallelWorkerInvalidWriteLeaseFailsClosed(t *testing.T) {
	root := t.TempDir()
	t.Setenv(parallelWorkerEnv, "1")
	t.Setenv("PROJX_WORKER_WRITES", "../outside/**")
	ev := decodeLifecycleEvent([]byte(`{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"x.go"}}`))
	err := enforceParallelWorkerLease(root, ev, hookTargetPaths(ev))
	if err == nil || !strings.Contains(err.Error(), "invalid write lease") {
		t.Fatalf("got %v", err)
	}
}
