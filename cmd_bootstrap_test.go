package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	store "github.com/SirNiklas9/projx-store"
)

// isolateHome points HOME (and the YOURS store) at a temp dir so bootstrap tests NEVER
// touch the real ~/.claude/settings.json or the real per-user store.
func isolateHome(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	// The global floor lands in the YOURS store; pin it inside the temp home too so a
	// real per-user store is never seeded/read.
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(home, "yours"))
	return home
}

// readHooks loads settings.json and returns its "hooks" map for assertions.
func readHooks(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		t.Fatal("settings.json has no hooks map")
	}
	return hooks
}

// TestMergeGlobalHookFreshFile: an absent settings.json gets all five ProjX events, each
// with the right timeout, and PreToolUse covers Claude's shell and file mutation tools.
func TestMergeGlobalHookFreshFile(t *testing.T) {
	home := isolateHome(t)
	path := filepath.Join(home, ".claude", "settings.json")

	added, skipped, err := mergeGlobalHook(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 5 {
		t.Fatalf("added = %v; want all 5 events", added)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped = %v; want none on a fresh file", skipped)
	}

	hooks := readHooks(t, path)
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PreCompact", "Stop"} {
		if _, ok := hooks[ev]; !ok {
			t.Errorf("event %q missing from hooks", ev)
		}
		if !hookGroupsHaveProjx(hooks[ev].([]any)) {
			t.Errorf("event %q has no ProjX command", ev)
		}
	}
	// PreToolUse must carry the matcher.
	pre := hooks["PreToolUse"].([]any)[0].(map[string]any)
	wantMatcher := "Bash|Read|Edit|Write|MultiEdit|NotebookEdit"
	if pre["matcher"] != wantMatcher {
		t.Errorf("PreToolUse matcher = %v; want %s", pre["matcher"], wantMatcher)
	}
	// SessionStart timeout must be 30.
	ss := hooks["SessionStart"].([]any)[0].(map[string]any)
	inner := ss["hooks"].([]any)[0].(map[string]any)
	if int(inner["timeout"].(float64)) != 30 {
		t.Errorf("SessionStart timeout = %v; want 30", inner["timeout"])
	}
}

func TestMergeGlobalHookRepairsMatcherDrift(t *testing.T) {
	home := isolateHome(t)
	path := filepath.Join(home, ".claude", "settings.json")
	if _, _, err := mergeGlobalHook(path); err != nil {
		t.Fatal(err)
	}

	root := map[string]any{}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &root) != nil {
		t.Fatalf("load generated settings: %v", err)
	}
	hooks := root["hooks"].(map[string]any)
	pre := hooks["PreToolUse"].([]any)[0].(map[string]any)
	pre["matcher"] = "Read|Edit|Write" // prior release, same hook command
	data, _ = json.Marshal(root)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	added, _, err := mergeGlobalHook(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0] != "PreToolUse" {
		t.Fatalf("matcher drift refresh = %v; want [PreToolUse]", added)
	}
	pre = readHooks(t, path)["PreToolUse"].([]any)[0].(map[string]any)
	if pre["matcher"] != projxHookSpecs[2].matcher {
		t.Fatalf("matcher was not repaired: %v", pre["matcher"])
	}
}

// TestMergeGlobalHookPreservesExisting: a pre-existing UNRELATED hook and top-level key
// survive the merge, and the ProjX entries are added alongside.
func TestMergeGlobalHookPreservesExisting(t *testing.T) {
	home := isolateHome(t)
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// A settings.json with an unrelated top-level key AND an unrelated Stop hook.
	pre := map[string]any{
		"model": "opus",
		"hooks": map[string]any{
			"Stop": []any{
				map[string]any{
					"hooks": []any{
						map[string]any{"type": "command", "command": "my-own-linter", "timeout": 5},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(pre, "", "  ")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := mergeGlobalHook(path); err != nil {
		t.Fatal(err)
	}

	var root map[string]any
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	// Unrelated top-level key preserved.
	if root["model"] != "opus" {
		t.Errorf("unrelated top-level key lost: model = %v", root["model"])
	}
	hooks := root["hooks"].(map[string]any)
	// The unrelated Stop hook must still be there, plus the ProjX one appended.
	stop := hooks["Stop"].([]any)
	if len(stop) != 2 {
		t.Fatalf("Stop groups = %d; want 2 (unrelated linter + ProjX)", len(stop))
	}
	foundLinter := false
	for _, g := range stop {
		inner := g.(map[string]any)["hooks"].([]any)
		for _, h := range inner {
			if h.(map[string]any)["command"] == "my-own-linter" {
				foundLinter = true
			}
		}
	}
	if !foundLinter {
		t.Error("pre-existing my-own-linter Stop hook was clobbered")
	}
	if !hookGroupsHaveProjx(stop) {
		t.Error("ProjX Stop hook was not added alongside the existing one")
	}
	// The other four events got added too.
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PreCompact"} {
		if !hookGroupsHaveProjx(hooks[ev].([]any)) {
			t.Errorf("event %q missing ProjX hook after merge", ev)
		}
	}
}

// TestMergeGlobalHookIdempotent: re-running the merge adds nothing and does not duplicate
// any ProjX entry.
func TestMergeGlobalHookIdempotent(t *testing.T) {
	home := isolateHome(t)
	path := filepath.Join(home, ".claude", "settings.json")

	if _, _, err := mergeGlobalHook(path); err != nil {
		t.Fatal(err)
	}
	added, skipped, err := mergeGlobalHook(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 0 {
		t.Errorf("second run added %v; want nothing (idempotent)", added)
	}
	if len(skipped) != 5 {
		t.Errorf("second run skipped %v; want all 5 already-present", skipped)
	}

	// No event may carry more than one ProjX command.
	hooks := readHooks(t, path)
	for ev, v := range hooks {
		count := 0
		for _, g := range v.([]any) {
			inner, _ := g.(map[string]any)["hooks"].([]any)
			for _, h := range inner {
				if cmd, _ := h.(map[string]any)["command"].(string); isProjxHookCmd(cmd) {
					count++
				}
			}
		}
		if count > 1 {
			t.Errorf("event %q has %d ProjX commands after re-run; want 1 (no duplication)", ev, count)
		}
	}
}

// TestMergeGlobalHookInvalidJSON: a malformed settings.json is reported, not clobbered.
func TestMergeGlobalHookInvalidJSON(t *testing.T) {
	home := isolateHome(t)
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mergeGlobalHook(path); err == nil {
		t.Error("expected an error on invalid JSON, got nil")
	}
	// The file must be left exactly as-is.
	got, _ := os.ReadFile(path)
	if string(got) != "{ not json" {
		t.Error("invalid settings.json was modified")
	}
}

// TestSeedGlobalFloor: the global floor lands at GLOBAL scope, and a second run seeds
// nothing (idempotent), reporting the records as already-present.
func TestSeedGlobalFloor(t *testing.T) {
	isolateHome(t)

	seeded, present, err := seedGlobalFloor()
	if err != nil {
		t.Fatal(err)
	}
	want := len(globalFloorConventions) + len(globalFloorGates)
	if len(seeded) != want {
		t.Fatalf("first seed wrote %d record(s); want %d", len(seeded), want)
	}
	if len(present) != 0 {
		t.Fatalf("first seed reported %v already-present; want none", present)
	}

	// Records exist at GLOBAL scope in the YOURS store.
	st, err := openYoursStore()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"convention/working-protocol", "convention/secrets-by-codename",
		"gate-rule/dotenv-files", "gate-rule/private-keys", "gate-rule/secrets-dir", "gate-rule/ssh-material"} {
		rec, ok := st.Get(id)
		if !ok {
			t.Errorf("global floor record %q missing", id)
			continue
		}
		if rec.Scope != store.ScopeGlobal {
			t.Errorf("record %q scope = %v; want global", id, rec.Scope)
		}
	}
	st.Close()

	// Second run: idempotent — nothing new, all present.
	seeded2, present2, err := seedGlobalFloor()
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded2) != 0 {
		t.Errorf("second seed wrote %v; want nothing (idempotent)", seeded2)
	}
	if len(present2) != want {
		t.Errorf("second seed reported %d already-present; want %d", len(present2), want)
	}
}

// TestInstallProjxSkill: the embedded skill lands on disk and re-install is idempotent.
func TestInstallProjxSkill(t *testing.T) {
	home := isolateHome(t)
	claudeDir := filepath.Join(home, ".claude")

	path, wrote, err := installProjxSkill(claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Error("first install reported no write")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != projxSkillMD {
		t.Error("installed skill content does not match the embedded source")
	}
	// Re-install with identical content is a no-op.
	_, wrote2, err := installProjxSkill(claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	if wrote2 {
		t.Error("second install rewrote an up-to-date skill; want no-op")
	}
}
