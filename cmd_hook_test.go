package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
