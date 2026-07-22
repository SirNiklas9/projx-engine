package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestGovernedTurnStopVerifiesAndStagesCandidate(t *testing.T) {
	root := t.TempDir()
	seedSessionStore(t, root)
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
	candidate, ok := st.Get("candidate/governed-turn/" + sid)
	st.Close()
	if !ok || candidate.Status != "candidate" || candidate.Provenance != "gate-verified" {
		t.Fatalf("learn candidate not staged with lifecycle metadata: %+v, ok=%v", candidate, ok)
	}
}

func TestLearnCandidateSurfacesOnNextPrompt(t *testing.T) {
	root := t.TempDir()
	seedSessionStore(t, root)
	turn := governedTurn{Prompt: "change main", MutatedPaths: []string{"main.go"}}
	if !stageLearnCandidate(root, "learn", turn) {
		t.Fatal("stageLearnCandidate failed")
	}
	out, _, code := handleHook(root, []byte(`{"session_id":"learn","hook_event_name":"UserPromptSubmit","prompt":"next"}`))
	if code != 0 || !strings.Contains(out, "candidate, not authority") {
		t.Fatalf("candidate not surfaced: code=%d out=%q", code, out)
	}
}

func TestCrossProjectLearnStagesInEachOwningStore(t *testing.T) {
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

	for root, want := range map[string]string{repoA: pathA, repoB: pathB} {
		st := openStore(root)
		record, ok := st.Get("candidate/governed-turn/cross-learn")
		st.Close()
		if !ok {
			t.Fatalf("candidate missing from owning store %q", root)
		}
		var candidate learnCandidate
		if err := json.Unmarshal([]byte(record.Body), &candidate); err != nil {
			t.Fatalf("decode candidate in %q: %v", root, err)
		}
		if len(candidate.Paths) != 1 || candidate.Paths[0] != want {
			t.Fatalf("candidate paths in %q = %q, want only %q", root, candidate.Paths, want)
		}
	}
	if st := openStore(home); func() bool { defer st.Close(); _, ok := st.Get("candidate/governed-turn/cross-learn"); return ok }() {
		t.Fatal("cross-project candidate was incorrectly staged in the session home store")
	}
}

func TestPendingLearnNoticeFollowsActiveProject(t *testing.T) {
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
	if !stageLearnCandidate(repoB, "project-notice", governedTurn{MutatedPaths: []string{filepath.Join(repoB, "b.go")}}) {
		t.Fatal("stageLearnCandidate failed")
	}

	if notice := pendingLearnNotice(repoB, "project-notice"); !strings.Contains(notice, "candidate, not authority") {
		t.Fatalf("owning project candidate not surfaced: %q", notice)
	}
	if notice := pendingLearnNotice(repoA, "project-notice"); notice != "" {
		t.Fatalf("candidate leaked into a different project's notice: %q", notice)
	}
}
