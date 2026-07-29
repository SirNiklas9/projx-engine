package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGovernedTurnRecordsMutationObligation(t *testing.T) {
	root := t.TempDir()
	seedSessionStore(t, root)

	input := []byte(`{"session_id":"strict","hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"main.go"}}`)
	_, errOut, code := handleHook(root, input)
	if code != 0 {
		t.Fatalf("mutation with available governed store = code %d, stderr %q", code, errOut)
	}

	handleHook(root, []byte(`{"session_id":"strict","hook_event_name":"UserPromptSubmit","prompt":"change main"}`))
	if _, errOut, code = handleHook(root, input); code != 0 {
		t.Fatalf("mutation after recall = code %d, stderr %q; want allowed", code, errOut)
	}
	turn := loadGovernedTurn(root, "strict")
	if len(turn.MutatedRoots) != 1 || len(turn.MutatedPaths) != 1 {
		t.Fatalf("mutation obligation not recorded: %+v", turn)
	}
}

func TestGovernedTurnStopVerifiesWithoutStagingKnowledge(t *testing.T) {
	root := t.TempDir()
	seedSessionStore(t, root)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/governed\n\ngo 1.25.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() { missingSymbol() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const sid = "close"
	handleHook(root, []byte(`{"session_id":"`+sid+`","hook_event_name":"SessionStart"}`))
	_, errOut, code := handleHook(root, []byte(`{"session_id":"`+sid+`","hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":"main.go"}}`))
	if code != 0 {
		t.Fatalf("mutation = code %d, stderr %q", code, errOut)
	}

	_, errOut, code = handleHook(root, []byte(`{"session_id":"`+sid+`","hook_event_name":"Stop"}`))
	if code != 0 {
		t.Fatalf("Stop after verifiable mutation = code %d, stderr %q", code, errOut)
	}
	if turn := loadGovernedTurn(root, sid); len(turn.MutatedRoots) != 0 {
		t.Fatalf("verified mutation obligation was not cleared: %+v", turn)
	}
	st := openStore(root)
	_, ok := st.Get("candidate/governed-turn/" + sid)
	st.Close()
	if ok {
		t.Fatal("verified turn was incorrectly persisted as durable knowledge")
	}
}

func TestCrossProjectGovernedTurnVerifiesWithoutStagingKnowledge(t *testing.T) {
	t.Setenv("PROJX_YOURS_DIR", t.TempDir())
	home := t.TempDir()
	repoA := filepath.Join(home, "repo-a")
	repoB := filepath.Join(home, "repo-b")
	for _, root := range []string{repoA, repoB} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		seedSessionStore(t, root)
	}
	pathA := filepath.Join(repoA, "a.go")
	pathB := filepath.Join(repoB, "b.go")
	turn := governedTurn{
		Prompt:       "change both projects",
		Recalled:     true,
		MutatedRoots: []string{repoA, repoB},
		MutatedPaths: []string{pathA, pathB},
	}
	if !saveGovernedTurn(home, "cross-learn", turn) {
		t.Fatal("saveGovernedTurn failed")
	}
	if msg, blocked := closeGovernedTurn(home, "cross-learn"); blocked {
		t.Fatalf("closeGovernedTurn blocked: %s", msg)
	}

	if turn := loadGovernedTurn(home, "cross-learn"); len(turn.MutatedRoots) != 0 || len(turn.MutatedPaths) != 0 {
		t.Fatalf("verified cross-project obligations were not cleared: %+v", turn)
	}
	for _, root := range []string{repoA, repoB, home} {
		st := openStore(root)
		_, ok := st.Get("candidate/governed-turn/cross-learn")
		st.Close()
		if ok {
			t.Fatalf("verified turn was incorrectly persisted in %q", root)
		}
	}
}

func TestMutationRootsSkipUserHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "yours"))
	st := openStore(home)
	st.Close()

	if roots := mutationRoots(home, nil); len(roots) != 0 {
		t.Fatalf("session-level home roots = %q, want none", roots)
	}
	target := filepath.Join(home, "notes.txt")
	if roots := mutationRoots(home, []string{target}); len(roots) != 0 {
		t.Fatalf("target-derived home roots = %q, want none", roots)
	}
}

func TestCloseGovernedTurnSkipsLegacyUserHomeRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "yours"))
	st := openStore(home)
	st.Close()

	turn := governedTurn{
		Recalled:     true,
		MutatedRoots: []string{home},
		MutatedPaths: []string{filepath.Join(home, "legacy.txt")},
	}
	if !saveGovernedTurn(home, "legacy-home", turn) {
		t.Fatal("saveGovernedTurn failed")
	}
	if msg, blocked := closeGovernedTurn(home, "legacy-home"); blocked {
		t.Fatalf("legacy home obligation blocked: %s", msg)
	}
	turn = loadGovernedTurn(home, "legacy-home")
	if len(turn.MutatedRoots) != 0 || len(turn.MutatedPaths) != 0 {
		t.Fatalf("legacy home obligation was not cleared: %+v", turn)
	}
}
