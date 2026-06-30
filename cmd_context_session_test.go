package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	store "github.com/SirNiklas9/projx-store"
)

// seedSessionStore writes a small mixed store under <root>/.projx/store.db so the
// session-aware context commands have real records to slice/delta against.
func seedSessionStore(t *testing.T, root string) {
	t.Helper()
	st := openStore(root)
	defer st.Close()
	put := func(id string, k store.Kind, key, body string) {
		if err := st.Put(store.Record{ID: id, Kind: k, Scope: store.ScopeProject, Key: key, Body: body}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	put("gate-rule/secrets", store.KGateRule, "secrets", "secret/**")
	put("convention/naming", store.KConvention, "naming", "use camelCase")
	put("doc/mc-login", store.KDoc, "minecraft/login/backend", "JWT auth in internal/auth/login.go")
	put("doc/billing", store.KDoc, "billing/checkout", "stripe flow")
}

func readCheckpoint(t *testing.T, root, session string) sessionCheckpoint {
	t.Helper()
	data, err := os.ReadFile(sessionCheckpointPath(root, session))
	if err != nil {
		t.Fatalf("read checkpoint: %v", err)
	}
	var cp sessionCheckpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		t.Fatalf("unmarshal checkpoint: %v", err)
	}
	return cp
}

// captureStdout runs fn and returns whatever it printed to os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 4096)
		for {
			n, err := r.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if err != nil {
				break
			}
		}
		done <- string(buf)
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

// TestSessionCheckpointSanitize covers filename safety + the default collapse.
func TestSessionCheckpointSanitize(t *testing.T) {
	cases := map[string]string{"": "default", "-": "default", "abc-123_X": "abc-123_X", "a/b:c": "a_b_c"}
	for in, want := range cases {
		if got := sanitizeSession(in); got != want {
			t.Errorf("sanitizeSession(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSessionContextLifecycle walks SessionStart → delta turn 1 → delta turn 2 →
// PreCompact reset → refill, asserting the floor/slice/delta behavior at each step.
func TestSessionContextLifecycle(t *testing.T) {
	root := t.TempDir()
	seedSessionStore(t, root)
	const sess = "sess-1"

	// SessionStart: lean floor — protocol + law, NO reference dump. Fresh checkpoint.
	floor := captureStdout(t, func() { runSessionContext(root, sess, "", false) })
	if !strings.Contains(floor, "READ BEFORE ACTING") || !strings.Contains(floor, "secret/**") || !strings.Contains(floor, "use camelCase") {
		t.Error("SessionStart floor missing protocol/law")
	}
	if strings.Contains(floor, "minecraft/login/backend") || strings.Contains(floor, "billing/checkout") {
		t.Error("SessionStart floor should not dump reference docs")
	}
	if cp := readCheckpoint(t, root, sess); cp.NeedFloor || len(cp.Seen) != 0 {
		t.Errorf("fresh checkpoint should be empty/needFloor=false, got %+v", cp)
	}

	// Turn 1: task about login → delta injects the mc-login index line, not billing,
	// and re-asserts the law. Checkpoint now records the seen doc.
	d1 := captureStdout(t, func() { runSessionContext(root, sess, "fix the minecraft login backend", false) })
	if !strings.Contains(d1, "minecraft/login/backend") {
		t.Error("turn1 delta missing the relevant login doc")
	}
	if strings.Contains(d1, "billing/checkout") {
		t.Error("turn1 delta leaked the out-of-slice billing doc")
	}
	if !strings.Contains(d1, "secret/**") {
		t.Error("turn1 delta dropped the standing law")
	}
	if _, ok := readCheckpoint(t, root, sess).Seen["doc/mc-login"]; !ok {
		t.Error("turn1 should record doc/mc-login as seen")
	}

	// Turn 2: same task, doc unchanged → suppressed; law still present.
	d2 := captureStdout(t, func() { runSessionContext(root, sess, "more minecraft login backend work", false) })
	if strings.Contains(d2, "minecraft/login/backend") {
		t.Error("turn2 re-sent an already-seen, unchanged doc")
	}
	if !strings.Contains(d2, "secret/**") {
		t.Error("turn2 dropped the standing law")
	}

	// PreCompact: reset. Checkpoint marks NeedFloor and clears seen; prints nothing.
	out := captureStdout(t, func() { runSessionContext(root, sess, "", true) })
	if out != "" {
		t.Errorf("PreCompact reset should print nothing, got %q", out)
	}
	if cp := readCheckpoint(t, root, sess); !cp.NeedFloor || len(cp.Seen) != 0 {
		t.Errorf("after reset want NeedFloor=true, empty seen; got %+v", cp)
	}

	// Refill: next task turn re-sends the full floor (protocol back) + the slice, and
	// clears NeedFloor while re-seeding seen.
	r1 := captureStdout(t, func() { runSessionContext(root, sess, "minecraft login backend after compact", false) })
	if !strings.Contains(r1, "READ BEFORE ACTING") || !strings.Contains(r1, "minecraft/login/backend") {
		t.Error("refill should restore protocol + the task slice")
	}
	if cp := readCheckpoint(t, root, sess); cp.NeedFloor {
		t.Error("refill should clear NeedFloor")
	}
}

// TestSessionSuggestRemember proves the Stop suggestion is silent unless an @remember
// went uncommitted: flagged + no commit → exit 2 with a nudge; a commit → silent.
func TestSessionSuggestRemember(t *testing.T) {
	root := t.TempDir()
	seedSessionStore(t, root)
	const sess = "sess-rem"

	// SessionStart, then a turn that flags @remember. The checkpoint should arm.
	runSessionContext(root, sess, "", false)
	runSessionContext(root, sess, "@remember login uses JWT in internal/auth", false)
	cp := readCheckpoint(t, root, sess)
	if !cp.Flagged {
		t.Fatal("an @remember turn should arm the Stop suggestion")
	}

	// No commit yet → suggest (exit 2). We exercise the decision logic directly rather
	// than runSessionSuggestCmd (which calls os.Exit).
	committed := storeMaxUpdatedAt(mustOpen(t, root)) > cp.FlaggedAt
	if committed {
		t.Fatal("nothing committed yet — expected committed=false")
	}

	// Now commit something → the high-water mark rises past FlaggedAt → no suggestion.
	st := openStore(root)
	if err := st.Put(store.Record{ID: "doc/new", Kind: store.KDoc, Scope: store.ScopeProject, Key: "minecraft/login/jwt", Body: "JWT in internal/auth/login.go"}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if !(storeMaxUpdatedAt(mustOpen(t, root)) > cp.FlaggedAt) {
		t.Error("after a commit the store max UpdatedAt should exceed FlaggedAt (no nag)")
	}
}

func mustOpen(t *testing.T, root string) store.Store {
	t.Helper()
	st := openStore(root)
	t.Cleanup(func() { st.Close() })
	return st
}
