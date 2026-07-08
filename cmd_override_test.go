package main

import (
	"os"
	"path/filepath"
	"testing"

	store "github.com/SirNiklas9/projx-store"
)

// seedGate commits a project-scope gate rule (deny pattern) into root's store.
func seedGateRule(t *testing.T, root, key, pattern string) {
	t.Helper()
	st := openStore(root)
	defer st.Close()
	if err := st.Put(store.Record{
		ID: "gate-rule/" + key, Kind: store.KGateRule,
		Scope: store.ScopeProject, Key: key, Body: pattern,
	}); err != nil {
		t.Fatalf("seed gate %q: %v", key, err)
	}
}

// TestBashGateBlocksSecretRead covers item C: a Bash command that names an
// off-limits path (e.g. `cat .env`) must be blocked, closing the hole where a
// shell command carried no file_path and slipped past the gate entirely.
func TestBashGateBlocksSecretRead(t *testing.T) {
	t.Setenv("PROJX_YOURS_DIR", t.TempDir()) // isolate from the real machine's global store
	root := t.TempDir()
	seedGateRule(t, root, "dotenv", ".env*")
	seedGateRule(t, root, "secrets-dir", "secret/**")

	blocked := []string{"cat .env", "cat ./.env.local", "grep x secret/keys.txt", "cp .env /tmp/x"}
	for _, cmd := range blocked {
		_, _, code := handleHook(root, []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":`+jsonStr(cmd)+`}}`))
		if code != 2 {
			t.Errorf("command %q: got exit %d, want 2 (blocked)", cmd, code)
		}
	}

	allowed := []string{"ls -la", "go test ./...", "git status", "echo hello"}
	for _, cmd := range allowed {
		if _, _, code := handleHook(root, []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":`+jsonStr(cmd)+`}}`)); code != 0 {
			t.Errorf("benign command %q: got exit %d, want 0 (allowed)", cmd, code)
		}
	}
}

// TestDispatcherModeSoftOverride covers item B: dispatcher-mode is a SOFT rule —
// denied by default, but a reasoned override grant lets exactly N actions proceed,
// after which it denies again.
func TestDispatcherModeSoftOverride(t *testing.T) {
	t.Setenv("PROJX_YOURS_DIR", t.TempDir())
	root := t.TempDir()
	// dispatcher-mode ON (a setting gate-rule, not a deny glob).
	st := openStore(root)
	_ = st.Put(store.Record{ID: "gate-rule/setting/dispatcher-mode", Kind: store.KGateRule,
		Scope: store.ScopeProject, Key: "setting/dispatcher-mode", Body: "on"})
	st.Close()

	edit := []byte(`{"hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":"main.go"}}`)

	if _, _, code := handleHook(root, edit); code != 2 {
		t.Fatalf("edit under dispatcher-mode: got %d, want 2 (soft deny)", code)
	}

	// Grant two uses.
	g := loadOverrideGrants(root)
	g.Rules["dispatcher-mode"] = overrideGrant{Rule: "dispatcher-mode", Reason: "test", Uses: 2}
	if err := saveOverrideGrants(root, g); err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 2; i++ {
		if _, _, code := handleHook(root, edit); code != 0 {
			t.Errorf("edit #%d with grant: got %d, want 0 (allowed)", i, code)
		}
	}
	if _, _, code := handleHook(root, edit); code != 2 {
		t.Errorf("edit after grant exhausted: got %d, want 2 (deny again)", code)
	}
}

// TestDispatcherModeHardForbidsOverride covers the data-driven retier: a project that
// marks dispatcher-mode HARD (Record.Enforcement=hard) forbids the override entirely —
// a grant is ignored and the edit stays blocked.
func TestDispatcherModeHardForbidsOverride(t *testing.T) {
	t.Setenv("PROJX_YOURS_DIR", t.TempDir())
	root := t.TempDir()
	st := openStore(root)
	_ = st.Put(store.Record{ID: "gate-rule/setting/dispatcher-mode", Kind: store.KGateRule,
		Scope: store.ScopeProject, Key: "setting/dispatcher-mode", Body: "on",
		Enforcement: store.EnforcementHard})
	st.Close()

	// Grant an override — it must be ineffective against a HARD rule.
	g := loadOverrideGrants(root)
	g.Rules["dispatcher-mode"] = overrideGrant{Rule: "dispatcher-mode", Reason: "x", Uses: 5}
	_ = saveOverrideGrants(root, g)

	edit := []byte(`{"hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":"main.go"}}`)
	if _, _, code := handleHook(root, edit); code != 2 {
		t.Fatalf("hard dispatcher-mode with a grant: got %d, want 2 (still blocked)", code)
	}
}

// TestOverrideAuthorityDelegation covers the core rule: the AI cannot self-authorize
// an override. An AI-initiated `override` (or an attempt to flip the delegation flag)
// via the Bash tool is blocked until the human delegates by setting the flag on.
func TestOverrideAuthorityDelegation(t *testing.T) {
	t.Setenv("PROJX_YOURS_DIR", t.TempDir())
	root := t.TempDir()

	runOverride := []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"projx-engine override dispatcher-mode --reason x"}}`)
	flipFlag := []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"projx-engine store commit --kind gate-rule --key setting/override-authority --body on"}}`)

	// Not delegated → both blocked.
	if _, _, code := handleHook(root, runOverride); code != 2 {
		t.Errorf("AI override without delegation: got %d, want 2 (blocked)", code)
	}
	if _, _, code := handleHook(root, flipFlag); code != 2 {
		t.Errorf("AI flipping delegation flag: got %d, want 2 (blocked)", code)
	}

	// Human delegates (direct store write = out-of-band, no hook).
	st := openStore(root)
	_ = st.Put(store.Record{ID: "gate-rule/setting/override-authority", Kind: store.KGateRule,
		Scope: store.ScopeProject, Key: store.SettingOverrideAuthority, Body: "on"})
	st.Close()

	// Now the AI-initiated override passes the authority guard.
	if _, _, code := handleHook(root, runOverride); code != 0 {
		t.Errorf("AI override after delegation: got %d, want 0 (allowed)", code)
	}
}

func TestBashAttemptsSelfAuthorize(t *testing.T) {
	yes := []string{
		"projx-engine override dispatcher-mode --reason x",
		"projx-engine store commit --kind gate-rule --key setting/override-authority --body on",
		"PROJX_SESSION=1 projx-engine override commit-style --reason y",
	}
	no := []string{"ls -la", "go test ./...", "echo override is a word", "git commit -m 'override'"}
	for _, c := range yes {
		if !bashAttemptsSelfAuthorize(c) {
			t.Errorf("expected %q to be a self-authorize attempt", c)
		}
	}
	for _, c := range no {
		if bashAttemptsSelfAuthorize(c) {
			t.Errorf("did NOT expect %q to be flagged", c)
		}
	}
}

// TestConsumeOverride unit-tests the grant lifecycle directly.
func TestConsumeOverride(t *testing.T) {
	root := t.TempDir()

	if _, ok := consumeOverride(root, "dispatcher-mode"); ok {
		t.Fatal("no grant should mean no override")
	}

	g := overrideGrants{Rules: map[string]overrideGrant{
		"dispatcher-mode": {Rule: "dispatcher-mode", Reason: "why", Uses: 1},
	}}
	if err := saveOverrideGrants(root, g); err != nil {
		t.Fatal(err)
	}
	if reason, ok := consumeOverride(root, "dispatcher-mode"); !ok || reason != "why" {
		t.Fatalf("first consume: ok=%v reason=%q, want true/why", ok, reason)
	}
	if _, ok := consumeOverride(root, "dispatcher-mode"); ok {
		t.Fatal("one-shot grant should be gone after one use")
	}

	// Expired grant is not honored.
	g = overrideGrants{Rules: map[string]overrideGrant{
		"dispatcher-mode": {Rule: "dispatcher-mode", Uses: 5, Expiry: 1},
	}}
	_ = saveOverrideGrants(root, g)
	old := nowUnixMilli
	nowUnixMilli = func() int64 { return 1000 }
	defer func() { nowUnixMilli = old }()
	if _, ok := consumeOverride(root, "dispatcher-mode"); ok {
		t.Fatal("expired grant must not be honored")
	}
}

// TestOverrideAuditLog verifies the audit trail is written and surfaced.
func TestOverrideAuditLog(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	appendOverrideLog(root, "dispatcher-mode", "shipping hotfix", "2026-07-08T00:00:00Z")
	appendOverrideLog(root, "commit-style", "wip", "2026-07-08T00:01:00Z")

	recent := recentOverrides(root, 5)
	if len(recent) != 2 {
		t.Fatalf("got %d recent overrides, want 2", len(recent))
	}
	// Newest first.
	if got := recent[0]; got == "" || got[:12] != "commit-style" {
		t.Errorf("newest override = %q, want commit-style first", got)
	}
}
