package routing

import "testing"

// The mutation veto exists because of a real, expensive failure on 2026-07-16.
//
// The deterministic-op arms are bare substring matches. A dispatched EDIT task
// that happened to contain the word "verify" — as an acceptance criterion, e.g.
// "Add the two missing registrations ... VERIFY: run go build" — matched the
// verify arm and was silently downgraded to a read-only boundary check. The op
// dutifully ran the build, printed "verify: behavioral gate PASSED", edited
// NOTHING, and the dispatch reported `done`.
//
// It looked identical to success. Two dispatches were lost that way on payment
// code before the no-op was noticed by reading the file. For a dispatcher whose
// contract is "agents mutate, the trunk verifies the returned diff", a silent
// no-op that reports success is the worst failure available: it defeats the
// verify step by giving it nothing to catch.
func TestMutationTask_IsNeverDowngradedToADeterministicOp(t *testing.T) {
	// Every one of these asks for a CHANGE and also mentions a read-only word.
	// The change is the job; the rest is acceptance criteria.
	tasks := []string{
		"Add the two missing real-builder registrations. VERIFY: run go build ./...",
		"Add SetupIntent support to stripe.go. Verify with go test ./...",
		"BUG FIX in router.go: the gate returns early. Must pass go build and go test",
		"Fix the retry storm, then verify the suite is green",
		"Implement the charge path; check boundaries afterwards",
		"Rewrite the decline handler and verify nothing else changed",
		"Update the migration to add a column, verify the post-condition holds",
		"Remove the duplicated helper — verify the tests still pass",
		"Refactor promoteQueue. Violations should be zero afterwards.",
	}
	for _, task := range tasks {
		d := DecideWithStore(nil, task, DefaultConfig(), nil)
		if d.Kind != "agent" {
			t.Errorf("EDIT task silently became a no-op %s/%s — nothing would be changed and the "+
				"dispatch would report success:\n  %q", d.Kind, d.Op, task)
		}
	}
}

// The veto must not swallow the deterministic ops it sits in front of. These are
// genuinely read-only requests and must still skip the agent entirely.
func TestReadOnlyTask_StillRoutesToItsDeterministicOp(t *testing.T) {
	cases := []struct{ task, wantOp string }{
		{"verify the boundaries", "verify"},
		{"check boundaries", "verify"},
		{"are there any violations?", "verify"},
		{"show me the changelog", "store log"},
		{"what changed recently", "store log"},
		{"list the store", "store list"},
		{"show conventions", "store list"},
	}
	for _, c := range cases {
		d := DecideWithStore(nil, c.task, DefaultConfig(), nil)
		if d.Kind != "deterministic" || d.Op != c.wantOp {
			t.Errorf("read-only task %q = %s/%s, want deterministic/%s — the veto is too greedy and "+
				"is burning an agent on something an op answers for free", c.task, d.Kind, d.Op, c.wantOp)
		}
	}
}

// The veto reads the LEADING clause only. A read-only question that mentions a
// mutation verb in passing ("verify nothing added a new export") is still a
// question, not a change.
func TestVeto_ReadsTheLeadingClauseNotTheWholeBody(t *testing.T) {
	d := DecideWithStore(nil, "verify that nothing added an unexpected export", DefaultConfig(), nil)
	if d.Kind != "deterministic" {
		t.Errorf("a read-only check that merely mentions 'added' was dragged onto the agent path: %s/%s",
			d.Kind, d.Op)
	}
}

// hasWord must match whole words. Substring matching is the exact bug the veto
// exists to prevent, so the veto must not reproduce it.
func TestHasWord_DoesNotMatchSubstrings(t *testing.T) {
	if hasWord("readd the row", "add") {
		t.Error("'readd' matched 'add' — substring matching is the bug being fixed, not the fix")
	}
	if hasWord("padding the struct", "add") {
		t.Error("'padding' matched 'add'")
	}
	if !hasWord("add the row", "add") {
		t.Error("'add the row' should match 'add'")
	}
	if !hasWord("fix: the gate", "fix") {
		t.Error("punctuation should still bound a word")
	}
}
