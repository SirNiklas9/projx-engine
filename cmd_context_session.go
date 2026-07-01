package main

// cmd_context_session.go — SESSION-AWARE context delivery (native face).
//
// The per-session context LIFECYCLE (floor / delta / refill / suggest) is defined ONCE
// in the shared projx-store library (store.SessionContext / store.SessionSuggest). This
// file is only the NATIVE binding: it persists the per-session Checkpoint as a JSON file
// under <root>/.projx/agent-seen-<session>.json (osCheckpoints implements
// store.CheckpointStore) and exposes the CLI/hook entry points. The WASM cell binds the
// same lifecycle to pulp.FS instead — same logic, different hands.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

// sessionCheckpointPath returns <root>/.projx/agent-seen-<sanitized-session>.json.
// A blank or "-" session collapses to a single shared "default" checkpoint.
func sessionCheckpointPath(absRoot, session string) string {
	return filepath.Join(absRoot, ".projx", "agent-seen-"+sanitizeSession(session)+".json")
}

// sanitizeSession maps a session id to a safe filename component (alnum/-/_ kept,
// everything else → '_'); empty/"-" → "default".
func sanitizeSession(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" {
		return "default"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// osCheckpoints is the native store.CheckpointStore: one JSON file per session under
// <root>/.projx. Best-effort — a missing/corrupt file is a fresh session, a failed write
// only costs a little redundant context, never a blocked turn.
type osCheckpoints struct{ root string }

func (o osCheckpoints) Load(session string) store.Checkpoint {
	cp := store.Checkpoint{Seen: map[string]int64{}}
	if data, err := os.ReadFile(sessionCheckpointPath(o.root, session)); err == nil {
		_ = json.Unmarshal(data, &cp)
		if cp.Seen == nil {
			cp.Seen = map[string]int64{}
		}
	}
	return cp
}

func (o osCheckpoints) Save(session string, cp store.Checkpoint) {
	path := sessionCheckpointPath(o.root, session)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if data, err := json.Marshal(cp); err == nil {
		_ = os.WriteFile(path, data, 0o644)
	}
}

// runSessionContext is the CLI face of the session-aware `context` (dispatched from
// runContextCmd when --session is present): it prints what buildSessionContext returns.
func runSessionContext(absRoot, session, task string, reset bool) {
	if out := buildSessionContext(absRoot, session, task, reset); out != "" {
		fmt.Print(out)
	}
}

// buildSessionContext delegates the lifecycle to the shared store definition, bound to
// the native file-backed checkpoint store and the (opt-in) v2 selector. Returns the text
// to inject ("" for a PreCompact reset).
func buildSessionContext(absRoot, session, task string, reset bool) string {
	st := openStore(absRoot)
	defer st.Close()
	return store.SessionContext(st, osCheckpoints{absRoot}, session, task, reset, contextSelector(st, task))
}

// runSessionSuggestCmd is the CLI face of the Stop suggestion (`session-suggest`).
func runSessionSuggestCmd(absRoot string, args []string) {
	msg, block := sessionSuggest(absRoot, parseStrFlag(args, "--session"))
	if block {
		fmt.Println(msg)
		os.Exit(2)
	}
	os.Exit(0)
}

// sessionSuggest delegates the Stop suggestion to the shared definition.
func sessionSuggest(absRoot, session string) (msg string, block bool) {
	st := openStore(absRoot)
	defer st.Close()
	return store.SessionSuggest(st, osCheckpoints{absRoot}, session)
}
