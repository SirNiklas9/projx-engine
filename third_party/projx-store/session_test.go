package store

import (
	"strings"
	"testing"
)

// memCheckpoints is an in-memory CheckpointStore for testing the lifecycle without any FS.
type memCheckpoints map[string]Checkpoint

func (m memCheckpoints) Load(s string) Checkpoint { return m[s] }
func (m memCheckpoints) Save(s string, cp Checkpoint) {
	m[s] = cp
}

func seedSessionLife(t *testing.T) *Mem {
	t.Helper()
	m := NewMem()
	put := func(id string, k Kind, key, body string) {
		if err := m.Put(Record{ID: id, Kind: k, Scope: ScopeProject, Key: key, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	put("gate-rule/secrets", KGateRule, "secrets", "secret/**")
	put("convention/naming", KConvention, "naming", "use camelCase")
	put("doc/login", KDoc, "minecraft/login/backend", "JWT auth")
	put("doc/billing", KDoc, "billing/checkout", "stripe flow")
	return m
}

// TestSessionContextLifecycle walks the full per-session lifecycle through the shared
// definition: floor → delta → suppress → reset → refill, against an in-memory store.
func TestSessionContextLifecycleLib(t *testing.T) {
	m := seedSessionLife(t)
	cps := memCheckpoints{}
	const sess = "s1"

	floor := SessionContext(m, cps, sess, "", false, nil)
	if !strings.Contains(floor, "READ BEFORE ACTING") || !strings.Contains(floor, "secret/**") {
		t.Error("floor missing protocol/law")
	}
	if strings.Contains(floor, "minecraft/login/backend") {
		t.Error("floor should not dump reference docs")
	}
	if cps[sess].NeedFloor || len(cps[sess].Seen) != 0 {
		t.Errorf("fresh checkpoint should be empty, got %+v", cps[sess])
	}

	d1 := SessionContext(m, cps, sess, "fix the minecraft login backend", false, nil)
	if !strings.Contains(d1, "minecraft/login/backend") || strings.Contains(d1, "billing/checkout") {
		t.Error("turn1 delta should include login, exclude billing")
	}
	if _, ok := cps[sess].Seen["doc/login"]; !ok {
		t.Error("turn1 should record doc/login as seen")
	}

	d2 := SessionContext(m, cps, sess, "more minecraft login work", false, nil)
	if strings.Contains(d2, "minecraft/login/backend") {
		t.Error("turn2 should suppress the already-seen login doc")
	}
	if !strings.Contains(d2, "secret/**") {
		t.Error("turn2 should still re-assert the law")
	}

	if out := SessionContext(m, cps, sess, "", true, nil); out != "" {
		t.Errorf("reset should inject nothing, got %q", out)
	}
	if !cps[sess].NeedFloor {
		t.Error("reset should set NeedFloor")
	}

	r1 := SessionContext(m, cps, sess, "minecraft login after compaction", false, nil)
	if !strings.Contains(r1, "READ BEFORE ACTING") || !strings.Contains(r1, "minecraft/login/backend") {
		t.Error("refill should restore protocol + the slice")
	}
	if cps[sess].NeedFloor {
		t.Error("refill should clear NeedFloor")
	}
}

// TestSessionSuggestLib proves the Stop suggestion: armed by @remember, silenced by a
// commit or after firing once.
func TestSessionSuggestLib(t *testing.T) {
	m := seedSessionLife(t)
	cps := memCheckpoints{}
	const sess = "s2"

	SessionContext(m, cps, sess, "", false, nil)
	SessionContext(m, cps, sess, "@remember login uses JWT", false, nil)
	if !cps[sess].Flagged {
		t.Fatal("@remember should arm the suggestion")
	}

	msg, block := SessionSuggest(m, cps, sess)
	if !block || !strings.Contains(msg, "@remember") {
		t.Error("uncommitted @remember should nudge")
	}
	if !strings.Contains(msg, "--claim-class") || !strings.Contains(msg, "--evidence") {
		t.Error("@remember nudge omitted durable candidate metadata")
	}
	if _, block2 := SessionSuggest(m, cps, sess); block2 {
		t.Error("suggestion should self-disarm after firing once")
	}

	// Arm again, then commit → no nudge.
	SessionContext(m, cps, sess, "@remember another thing", false, nil)
	if err := m.Put(Record{ID: "doc/new", Kind: KDoc, Scope: ScopeProject, Key: "x/y", Body: "z"}); err != nil {
		t.Fatal(err)
	}
	if _, block := SessionSuggest(m, cps, sess); block {
		t.Error("a commit after @remember should silence the nudge")
	}
}

// TestMultiSessionIsolation is the headline: two sessions share ONE store but keep
// independent delta cursors — session B is not affected by what session A has seen.
func TestMultiSessionIsolation(t *testing.T) {
	m := seedSessionLife(t)
	cps := memCheckpoints{}

	// Session A sees the login doc.
	SessionContext(m, cps, "A", "", false, nil)
	SessionContext(m, cps, "A", "fix minecraft login backend", false, nil)
	if _, ok := cps["A"].Seen["doc/login"]; !ok {
		t.Fatal("A should have seen doc/login")
	}

	// Session B is fresh — it must STILL get the login doc (its own cursor), not be
	// suppressed by A's state.
	SessionContext(m, cps, "B", "", false, nil)
	dB := SessionContext(m, cps, "B", "fix minecraft login backend", false, nil)
	if !strings.Contains(dB, "minecraft/login/backend") {
		t.Error("session B should get the login doc independently of session A")
	}
	if _, ok := cps["B"].Seen["doc/login"]; !ok {
		t.Error("B should have its own seen entry")
	}
}

func TestSessionContextHonorsKnowledgeLifecycle(t *testing.T) {
	defer pinClock(1000)()
	m := NewMem()
	for _, r := range []Record{
		{ID: "doc/current", Kind: KDoc, Scope: ScopeProject, Key: "billing/current", Body: "current truth"},
		{ID: "doc/candidate", Kind: KDoc, Scope: ScopeProject, Key: "billing/candidate", Body: "candidate claim", Status: StatusCandidate},
		{ID: "doc/old", Kind: KDoc, Scope: ScopeProject, Key: "billing/old", Body: "obsolete claim", Status: StatusSuperseded},
		{ID: "doc/stale", Kind: KDoc, Scope: ScopeProject, Key: "billing/stale", Body: "stale assertion body", ReviewAfter: 900},
	} {
		if err := m.Put(r); err != nil {
			t.Fatal(err)
		}
	}
	out := SessionContext(m, memCheckpoints{}, "life", "check billing records", false, nil)
	if !strings.Contains(out, "billing/current") {
		t.Fatal("fresh active record was not injected")
	}
	if strings.Contains(out, "candidate claim") || strings.Contains(out, "obsolete claim") {
		t.Fatalf("non-authoritative record leaked into context:\n%s", out)
	}
	if strings.Contains(out, "stale assertion body") || !strings.Contains(out, "REVIEW REQUIRED") || !strings.Contains(out, "doc/stale") {
		t.Fatalf("stale record should be identified for verification, not asserted:\n%s", out)
	}
}
