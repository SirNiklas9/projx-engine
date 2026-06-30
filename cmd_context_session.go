package main

// cmd_context_session.go — SESSION-AWARE context delivery (step 5: delta / refill).
//
// The stateless `context [--task]` command (cmd_context.go) prints either the full
// floor or a task slice every call. The session-aware variants here add a per-session
// CHECKPOINT so that, across a live conversation, the engine sends the LEAST new
// context each turn:
//
//   SessionStart   → `context --session <id>`            → lean floor (protocol + law),
//                                                           fresh checkpoint.
//   UserPromptSubmit → `context --session <id> --task t` → law re-asserted + only the
//                                                           NEW/CHANGED task-relevant
//                                                           reference records (delta).
//   PreCompact     → `context --session <id> --reset`    → mark "floor lost"; the next
//                                                           turn re-sends protocol+law+slice.
//   Stop           → `session-suggest --session <id>`    → SUGGEST-ONLY: if the user
//                                                           said @remember but nothing was
//                                                           committed, nudge once (exit 2).
//
// The checkpoint lives at <root>/.projx/agent-seen-<session>.json (travels with the
// project, gitignored under .projx). All delta logic that decides WHAT to send is the
// shared, OS-free projx-store library (AgentContextFloor / AgentContextDelta); this
// file only adds the native per-session state (read/write the checkpoint JSON).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

// sessionCheckpoint is the per-session delta state persisted between hook calls.
type sessionCheckpoint struct {
	Session string `json:"session"`
	// Seen maps recordID -> the UpdatedAt at the time it was last injected, so the
	// delta can suppress records already in the agent's context and re-send changed ones.
	Seen map[string]int64 `json:"seen"`
	// NeedFloor is set by PreCompact: the agent's context is about to be compacted, so
	// the next turn must re-send the floor (protocol + law) before the task slice.
	NeedFloor bool `json:"need_floor"`
	// Flagged records that a turn this session contained an @remember/capture request,
	// and FlaggedAt is the store's max UpdatedAt at that moment — so Stop can tell
	// whether anything was actually committed afterward (suggest only if not).
	Flagged   bool  `json:"flagged_remember"`
	FlaggedAt int64 `json:"flagged_at"`
}

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

// loadCheckpoint reads the session checkpoint; a missing/corrupt file yields a zero
// checkpoint (treated as a fresh session), never an error — a delta hook must never
// brick the conversation.
func loadCheckpoint(path, session string) sessionCheckpoint {
	cp := sessionCheckpoint{Session: session, Seen: map[string]int64{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return cp
	}
	_ = json.Unmarshal(data, &cp) // best-effort; partial/garbage → zero-ish cp
	if cp.Seen == nil {
		cp.Seen = map[string]int64{}
	}
	return cp
}

// saveCheckpoint writes the checkpoint JSON best-effort. A write failure is non-fatal
// (the next turn simply re-sends more than strictly necessary), so errors are swallowed
// after a best-effort mkdir.
func saveCheckpoint(path string, cp sessionCheckpoint) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if data, err := json.Marshal(cp); err == nil {
		_ = os.WriteFile(path, data, 0o644)
	}
}

// storeMaxUpdatedAt returns the largest UpdatedAt across every record in the store
// (0 for an empty store) — the cheap "has anything been committed since?" signal used
// by the Stop suggestion.
func storeMaxUpdatedAt(st store.Store) int64 {
	var max int64
	for _, r := range st.List(store.Filter{}) {
		if r.UpdatedAt > max {
			max = r.UpdatedAt
		}
	}
	return max
}

// runSessionContext implements the session-aware forms of `context` (dispatched from
// runContextCmd when --session is present). It owns the checkpoint lifecycle and
// delegates the actual context rendering to the shared projx-store library.
func runSessionContext(absRoot, session, task string, reset bool) {
	path := sessionCheckpointPath(absRoot, session)
	st := openStore(absRoot)
	defer st.Close()

	// PreCompact: the agent's context is about to be compacted. Don't print context
	// (PreCompact stdout isn't injected); just record that the floor must be re-sent
	// and clear the seen set so the next turn re-streams the task-relevant reference.
	if reset {
		saveCheckpoint(path, sessionCheckpoint{Session: session, Seen: map[string]int64{}, NeedFloor: true})
		return
	}

	// SessionStart (no task): lean floor + a fresh checkpoint. Reference knowledge is
	// NOT dumped here — it streams in per-task via the delta below.
	if task == "" {
		fmt.Print(store.AgentContextFloor(st))
		saveCheckpoint(path, sessionCheckpoint{Session: session, Seen: map[string]int64{}})
		return
	}

	// UserPromptSubmit (task present): delta turn.
	cp := loadCheckpoint(path, session)

	// A capture request (@remember / "document this") arms the Stop suggestion: record
	// that it was asked and the store's high-water mark so Stop can see if a commit landed.
	if storeWantsCapture(task) && !cp.Flagged {
		cp.Flagged = true
		cp.FlaggedAt = storeMaxUpdatedAt(st)
	}

	if cp.NeedFloor {
		// Post-compaction refill: re-send the full floor + task slice (protocol + law +
		// the relevant reference index), then re-seed `seen` from the delta selection so
		// subsequent turns suppress what we just restored.
		fmt.Print(compileStorePreambleForTask(st, task))
		_, seen := store.AgentContextDelta(st, task, nil)
		cp.NeedFloor = false
		cp.Seen = seen
		saveCheckpoint(path, cp)
		return
	}

	text, seen := store.AgentContextDelta(st, task, cp.Seen)
	fmt.Print(text)
	cp.Seen = seen
	saveCheckpoint(path, cp)
}

// runSessionSuggestCmd implements `session-suggest --session <id>` — the Stop hook.
//
// SUGGEST-ONLY by design (the user's "don't over-commit" rule): it stays silent and
// exits 0 UNLESS the user explicitly flagged an @remember this session and NOTHING was
// committed to the store afterward. In that one case it prints a single nudge to stdout
// and exits 2 so the connector can surface it as a Stop reason — then clears the flag so
// it never nags twice. It never writes to the store itself.
func runSessionSuggestCmd(absRoot string, args []string) {
	session := parseStrFlag(args, "--session")
	path := sessionCheckpointPath(absRoot, session)
	cp := loadCheckpoint(path, session)
	if !cp.Flagged {
		os.Exit(0) // nothing was flagged for capture — never nag
	}

	st := openStore(absRoot)
	defer st.Close()
	committed := storeMaxUpdatedAt(st) > cp.FlaggedAt

	// Disarm regardless of outcome so the suggestion fires at most once per request.
	cp.Flagged = false
	cp.FlaggedAt = 0
	saveCheckpoint(path, cp)

	if committed {
		os.Exit(0) // a commit landed after the @remember — nothing to suggest
	}
	fmt.Println("ProjX: you were asked to @remember something this session, but nothing was " +
		"committed to the project store. If it's worth keeping, commit it now:\n" +
		"    projx-engine store commit --kind doc --key <area>/<feature> --body \"<the fact>\"\n" +
		"Otherwise, briefly note that it wasn't worth storing and you're done.")
	os.Exit(2)
}

// storeWantsCapture reports whether a task signals capture intent. It mirrors the
// store library's detection by checking whether CaptureHint would fire.
func storeWantsCapture(task string) bool {
	return store.CaptureHint(task) != ""
}
