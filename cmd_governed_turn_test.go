package main

import (
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
