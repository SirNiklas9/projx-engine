package store

// session.go — the per-session context LIFECYCLE, OS-free and shared by every face.
//
// A live conversation needs per-session STATE (what has already been injected) on top
// of the shared project knowledge, so that many concurrent agents can share ONE store
// yet each keep their own "what have I seen" cursor. That state is a Checkpoint, keyed
// by session id; WHERE it is persisted differs per face (native = .projx files, cell =
// pulp.FS), so this file abstracts persistence behind CheckpointStore and keeps the
// lifecycle decision (floor / delta / refill / suggest) here, defined ONCE.

import "strings"

// Checkpoint is the per-session delta state. JSON tags are stable on disk so a file
// written by one face round-trips through another.
type Checkpoint struct {
	// Seen maps recordID -> the UpdatedAt at the time it was last injected, so the delta
	// suppresses records already in the agent's context and re-sends changed ones.
	Seen map[string]int64 `json:"seen"`
	// NeedFloor is set on PreCompact: the next turn must re-send the floor before the slice.
	NeedFloor bool `json:"need_floor"`
	// Flagged records an @remember this session; FlaggedAt is the store's high-water mark
	// at that moment, so Stop can tell whether anything was committed afterward.
	Flagged   bool  `json:"flagged_remember"`
	FlaggedAt int64 `json:"flagged_at"`
	// Focus is the repo/group the session is currently working in — set automatically as
	// the agent edits that repo's files (or explicitly via @focus). It boosts that repo's
	// records in the slice, and "shifts" when work moves to another repo.
	Focus string `json:"focus,omitempty"`
}

// CheckpointStore persists one Checkpoint per session id. Load returns the zero
// Checkpoint for an unknown/corrupt session (a fresh session); neither Load nor Save
// returns an error — losing a checkpoint only costs a little redundant context, never a
// blocked turn.
type CheckpointStore interface {
	Load(session string) Checkpoint
	Save(session string, cp Checkpoint)
}

// SessionContext owns the per-turn lifecycle and returns the text to inject:
//   - reset (PreCompact): mark the floor lost, inject nothing.
//   - task=="" (SessionStart): the lean floor, fresh checkpoint.
//   - otherwise (UserPromptSubmit): the delta — law re-asserted + only new/changed
//     task-relevant records; or, right after a reset, the full floor+slice refill.
//
// sel is the optional v2 semantic selector (nil → deterministic v1). It mutates the
// session's checkpoint via cps.
func SessionContext(st Store, cps CheckpointStore, session, task string, reset bool, sel SelectorFunc) string {
	if reset {
		prev := cps.Load(session) // preserve focus across a compaction
		cps.Save(session, Checkpoint{Seen: map[string]int64{}, NeedFloor: true, Focus: prev.Focus})
		return ""
	}
	if task == "" {
		cps.Save(session, Checkpoint{Seen: map[string]int64{}})
		return AgentContextFloor(st)
	}

	cp := cps.Load(session)
	if cp.Seen == nil {
		cp.Seen = map[string]int64{}
	}
	// An @remember arms the Stop suggestion (record the store high-water mark).
	if CaptureHint(task) != "" && !cp.Flagged {
		cp.Flagged = true
		cp.FlaggedAt = MaxUpdatedAt(st)
	}
	// An explicit @focus <repo> / @unfocus in the message overrides the (auto-tracked) focus.
	cp.Focus = focusFromTask(task, cp.Focus)

	if cp.NeedFloor {
		// Post-compaction refill: full floor + slice, then re-seed seen from the delta.
		out := AgentContextForTaskSel(st, task, sel, cp.Focus)
		_, seen := AgentContextDeltaSel(st, task, nil, sel, cp.Focus)
		cp.NeedFloor = false
		cp.Seen = seen
		cps.Save(session, cp)
		return out
	}

	text, seen := AgentContextDeltaSel(st, task, cp.Seen, sel, cp.Focus)
	cp.Seen = seen
	cps.Save(session, cp)
	return text
}

// focusFromTask applies an explicit focus directive in the message: `@focus <repo>` sets
// it, `@unfocus` clears it; otherwise the current focus (auto-tracked from edits) stands.
func focusFromTask(task, current string) string {
	t := strings.ToLower(task)
	if strings.Contains(t, "@unfocus") {
		return ""
	}
	if i := strings.Index(t, "@focus"); i >= 0 {
		rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t[i+len("@focus"):]), ":"))
		for _, f := range strings.FieldsFunc(rest, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }) {
			return f // first word after @focus
		}
	}
	return current
}

// SessionSuggest is the Stop suggestion: SUGGEST-ONLY, and only when an @remember was
// flagged this session and nothing was committed afterward. Returns the nudge text +
// whether to surface it (block), and disarms the flag so it fires at most once.
func SessionSuggest(st Store, cps CheckpointStore, session string) (msg string, block bool) {
	cp := cps.Load(session)
	if !cp.Flagged {
		return "", false
	}
	committed := MaxUpdatedAt(st) > cp.FlaggedAt
	cp.Flagged = false
	cp.FlaggedAt = 0
	cps.Save(session, cp)
	if committed {
		return "", false
	}
	return SessionSuggestText, true
}

// SessionSuggestText is the @remember nudge surfaced by SessionSuggest / the Stop hook.
const SessionSuggestText = "ProjX: you were asked to @remember something this session, but nothing was " +
	"committed to the project store. If it's worth keeping, commit it now:\n" +
	"    projx-engine store commit --kind doc --key <area>/<feature> --body \"<the durable fact>\" --claim-class <class> --evidence \"<source or verification>\"\n" +
	"Otherwise, briefly note that it wasn't worth storing and you're done."

// MaxUpdatedAt returns the largest UpdatedAt across every record (0 for an empty store)
// — the cheap "has anything been committed since?" signal the Stop suggestion uses.
func MaxUpdatedAt(st Store) int64 {
	if st == nil {
		return 0
	}
	var max int64
	for _, r := range st.List(Filter{IncludeNonAuthoritative: true}) {
		if r.UpdatedAt > max {
			max = r.UpdatedAt
		}
	}
	return max
}
