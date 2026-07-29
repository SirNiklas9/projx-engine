package store

// preamble.go — the agent CONTRACT preamble, rendered from the live store.
//
// This is the single, OS-free definition of the ambient agent contract: the
// protocol the agent must follow (read-before-act, commit-on-learn, gate is law)
// followed by the live store contents grouped by kind. It lives here in the
// shared store library so EVERY face computes the identical contract by
// construction — the WASM cell (brain), and any native consumer — rather than
// each re-implementing it. Pure and read-only (List only); never mutates.
//
// Delivery (writing it to a file / env var) is the caller's concern; this
// function only produces the text.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// maxSliceRecords caps how many matched reference records a single section injects, so
// one over-broad task token can never explode the slice on a large store. Overflow is
// summarized as a one-line pointer instead of dumped.
const maxSliceRecords = 12

func freshRecords(recs []Record, now int64) []Record {
	out := recs[:0:0]
	for _, r := range recs {
		if r.FreshAt(now) {
			out = append(out, r)
		}
	}
	return out
}

// reviewDueNotice keeps stale active knowledge visible without presenting its body as
// current truth. The engine can use the IDs to route live-source verification.
func reviewDueNotice(st Store, task string) string {
	if st == nil {
		return ""
	}
	toks := significantTokens(task)
	var due []Record
	for _, sec := range preambleSections {
		for _, r := range RecordsDueForReview(st, Filter{Kind: &sec.kind}, nowMillis()) {
			if len(toks) > 0 {
				hay, matched := strings.ToLower(matchText(r)), false
				for _, tok := range toks {
					if strings.Contains(hay, tok) {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
			}
			due = append(due, r)
		}
	}
	if len(due) == 0 {
		return ""
	}
	sort.Slice(due, func(i, j int) bool { return due[i].ID < due[j].ID })
	var b strings.Builder
	b.WriteString("\n## REVIEW REQUIRED — stale knowledge, do not assert as current\n")
	b.WriteString("_Re-verify these records against a live source before use:_\n")
	for _, r := range due {
		fmt.Fprintf(&b, "- [`%s`] %s — review due at %d\n", r.ID, r.Key, r.ReviewAfter)
	}
	return b.String()
}

// matchText returns the text a task's tokens are matched against for relevance. For
// CODE-MAP records (a JSON body carrying an "anchor") it is the SEMANTIC content —
// signature + doc — NOT the structural `code/<repo>/<path>` key or the anchor path, so a
// task word that happens to be a repo or directory name doesn't match every symbol under
// it. For everything else it's the record's key + body (their human path IS meaningful).
func matchText(r Record) string {
	// Code-map records are the AUTO-generated ones keyed `code/<repo>/<path>/<name>`;
	// match them on their signature + doc, NOT the structural path/anchor. HUMAN records
	// (docs, ADRs, conventions — any other key) match on their key + body, so a doc keyed
	// `billing/stripe/webhook` matches the word "webhook".
	if strings.HasPrefix(r.Key, "code/") {
		var m struct {
			Signature string `json:"signature"`
			Doc       string `json:"doc"`
			Terms     string `json:"terms"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(r.Body)), &m) == nil {
			return m.Signature + " " + m.Doc + " " + m.Terms
		}
	}
	return r.Key + " " + r.Body
}

// TaskSliceOverflows reports whether the deterministic v1 slice for this task would
// exceed the per-section cap in any reference section — i.e. the keyword match was
// AMBIGUOUS (too many hits). Consumers use this to auto-escalate to the semantic v2
// selector ONLY when v1 is too broad: deterministic-when-easy, model-when-needed.
func TaskSliceOverflows(st Store, task string) bool {
	toks := significantTokens(task)
	if st == nil || len(toks) == 0 {
		return false
	}
	for _, sec := range preambleSections {
		if sec.full {
			continue
		}
		n := 0
		for _, r := range freshRecords(dropSettings(st.List(OfKind(sec.kind))), nowMillis()) {
			hay := strings.ToLower(matchText(r))
			for _, t := range toks {
				if strings.Contains(hay, t) {
					n++
					break
				}
			}
			if n > maxSliceRecords {
				return true
			}
		}
	}
	return false
}

// perGroupCap bounds how many slots a single repo/group may take in the FIRST pass, so a
// big repo can't crowd out smaller ones that are the actual target. Leftover slots are
// then filled ignoring the cap, so context is never wasted.
const perGroupCap = 4

// recordGroup returns the repo/group a record belongs to for balancing — the <repo>
// segment of a code-map key (`code/<repo>/...`). Non-code records have no group.
func recordGroup(r Record) string {
	if strings.HasPrefix(r.Key, "code/") {
		rest := r.Key[len("code/"):]
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			return rest[:i]
		}
		return rest
	}
	return ""
}

// rankAndCap scores matched records by how many task tokens they hit (via matchText),
// then selects up to maxSliceRecords with per-repo BALANCING: pass 1 gives each repo at
// most perGroupCap of the top slots (so no repo crowds out the rest), pass 2 fills any
// remaining slots with the best leftovers. Reports how many matched records were dropped.
// Bounds every slice regardless of how many records a token matched, AND spreads it
// across the repos that matched.
// focusBoost outranks any token-hit count so a focused repo's matched records lead.
const focusBoost = 1000

func rankAndCap(recs []Record, toks []string, focus string) (kept []Record, overflow int) {
	focus = strings.ToLower(strings.TrimSpace(focus))
	type scored struct {
		r     Record
		score int
	}
	ss := make([]scored, 0, len(recs))
	for _, r := range recs {
		hay := strings.ToLower(matchText(r))
		n := 0
		for _, t := range toks {
			if strings.Contains(hay, t) {
				n++
			}
		}
		if focus != "" && strings.ToLower(recordGroup(r)) == focus {
			n += focusBoost // records in the focused repo lead the slice
		}
		ss = append(ss, scored{r, n})
	}
	sort.SliceStable(ss, func(i, j int) bool {
		if ss[i].score != ss[j].score {
			return ss[i].score > ss[j].score
		}
		if ss[i].r.UpdatedAt != ss[j].r.UpdatedAt {
			return ss[i].r.UpdatedAt > ss[j].r.UpdatedAt
		}
		return ss[i].r.Key < ss[j].r.Key
	})

	// Per-repo cap only when more than one repo matched (else it's a single focused set).
	groups := map[string]bool{}
	for _, s := range ss {
		if g := recordGroup(s.r); g != "" {
			groups[g] = true
		}
	}
	perGroup := len(ss)
	if len(groups) > 1 {
		perGroup = perGroupCap
	}

	picked := make([]bool, len(ss))
	gc := map[string]int{}
	for i, s := range ss { // pass 1: balanced (the FOCUS repo is exempt from the cap)
		if len(kept) >= maxSliceRecords {
			break
		}
		g := recordGroup(s.r)
		if g != "" && strings.ToLower(g) != focus && gc[g] >= perGroup {
			continue
		}
		kept = append(kept, s.r)
		gc[g]++
		picked[i] = true
	}
	for i, s := range ss { // pass 2: fill leftover slots
		if len(kept) >= maxSliceRecords {
			break
		}
		if !picked[i] {
			kept = append(kept, s.r)
			picked[i] = true
		}
	}
	overflow = len(ss) - len(kept)

	sort.Slice(kept, func(i, j int) bool {
		if kept[i].Key != kept[j].Key {
			return kept[i].Key < kept[j].Key
		}
		return kept[i].ID < kept[j].ID
	})
	return kept, overflow
}

// preambleFullBodyCap is the maximum body length (bytes) for a record in a FULL
// section. Records exceeding this are demoted to index lines even in full
// sections, so one unusually large record cannot blow up the launch context.
const preambleFullBodyCap = 1500

// preambleProtocolText is the fixed, vendor-neutral contract the agent is handed
// at launch: how the store works and the rules it is bound to.
const preambleProtocolText = `You are running inside ProjX. The PROJECT STORE below is your single source of
project knowledge and your binding contract. It replaces README/CLAUDE.md/any
loose .md for what is true about this project. Operate by these rules — they are
not suggestions; ProjX enforces them externally (a gate denies off-limits
actions, and an isolated verify-gate rejects any change that violates the store
before it can land):

1. READ BEFORE ACTING. The store contents below are already loaded — you know
   them now. Before doing anything, check whether the store already declares a
   convention, decision, or boundary that governs it, and follow it.
2. KNOWLEDGE IN = THE STORE. When you need to know something about this project,
   it is in the store (below, or via the store.query tool). Do not rely on or
   author loose .md files for project knowledge — they are not authoritative and
   not read. Some items below are shown as an INDEX (id + one-line summary) to
   save context tokens — to load any item's full content on demand run
   ` + "`projx-engine store get <id>`" + ` (or search with ` + "`store query`" + `);
   do not assume the summary is the whole thing.
3. KNOWLEDGE OUT = store.commit. When you learn, decide, or mark something down
   (a convention, an ADR, a doc, a history entry), commit it to the store via
   the store.commit tool. One commit after another — that IS the project's
   versioned history. Do not write it to a markdown file.
4. OFF-LIMITS IS LAW. The OFF-LIMITS section lists paths you must not read,
   edit, or run against. This is enforced, not requested: attempts are denied,
   and any change touching them is rejected by the verify-gate. Don't try.
5. YOU WORK IN ISOLATION. Your changes do not land directly. ProjX runs your
   diff through projx-verify and the gate; only a clean diff is accepted. Write
   code that conforms to the store and it lands; violate it and it bounces back.`

// preambleSection pairs a Kind with its display header and delivery tier.
// full=true: records are delivered verbatim (with a per-record size cap).
// full=false: section is indexed — one line per record, full body on demand.
type preambleSection struct {
	kind   Kind
	header string
	full   bool
}

var preambleSections = []preambleSection{
	{KGateRule, "OFF-LIMITS — do NOT read, edit, or run against these (this is LAW, enforced)", true},
	{KConvention, "Conventions you MUST follow", true},
	{KADR, "Architecture decisions (ADRs)", false},
	{KDeclaredStructure, "Declared structure / boundary rules", false},
	{KDoc, "Subsystem notes", false},
	{KHistory, "Recent history (most recent decisions/changes)", false},
}

// AgentPreamble renders the FULL contract (everything in the store). It is
// AgentContext with a zero selector — kept for callers that want the whole store.
func AgentPreamble(st Store) string { return AgentContext(st, Filter{}) }

// AgentContext renders the contract with TASK SLICING. The protocol + the LAW
// sections (gate rules, conventions) are ALWAYS included in full (small, binding);
// the reference sections (ADRs, declared structure, docs, history) are narrowed by
// sel.KeyPrefix / sel.Text — typically derived from the task — so only the relevant
// records load ("query, don't dump"). A zero sel includes everything (== the full
// AgentPreamble). Deterministic, read-only; an empty store still yields the
// protocol so the agent always knows the rules.
func AgentContext(st Store, sel Filter) string {
	var b strings.Builder
	b.WriteString("# ProjX project knowledge store — YOUR CONTRACT (read this first)\n\n")
	b.WriteString(preambleProtocolText)
	b.WriteString("\n\n---\n\n")
	b.WriteString("# Current store contents\n")
	b.WriteString("_This is the live store at launch. It is the authoritative project knowledge — not any README or .md file. Treat everything below as already-known context._\n")

	if st == nil {
		b.WriteString("\n_(store unavailable)_\n")
		return b.String()
	}

	wroteAny := false
	for _, sec := range preambleSections {
		var recs []Record
		if sec.full {
			recs = freshRecords(dropSettings(st.List(OfKind(sec.kind))), nowMillis()) // LAW: current only
		} else {
			k := sec.kind // reference section: narrow by the task slice (KeyPrefix/Text)
			recs = freshRecords(dropSettings(st.List(Filter{Kind: &k, KeyPrefix: sel.KeyPrefix, Text: sel.Text})), nowMillis())
		}
		if len(recs) == 0 {
			continue
		}
		sort.Slice(recs, func(i, j int) bool {
			if recs[i].Key != recs[j].Key {
				return recs[i].Key < recs[j].Key
			}
			return recs[i].ID < recs[j].ID
		})
		wroteAny = true
		fmt.Fprintf(&b, "\n## %s\n", sec.header)
		if !sec.full {
			b.WriteString("_(indexed — run `projx-engine store get <id>` to load the full content of any item below when you need it)_\n")
		}
		for _, r := range recs {
			if sec.full && len(r.Body) <= preambleFullBodyCap {
				renderPreambleRecord(&b, sec.kind, r)
			} else {
				renderPreambleIndexRecord(&b, sec.kind, r)
			}
		}
	}
	if !wroteAny {
		b.WriteString("\n_(the store is empty — no knowledge declared yet. Use store.commit to populate it as you learn.)_\n")
	}
	b.WriteString(reviewDueNotice(st, sel.Text))
	return b.String()
}

// SelectorFunc proposes which reference records are relevant to a task. Given the task
// and the candidate reference-record keys (the index), it returns the subset of keys to
// include. A consumer (the engine, backed by a cheap model) injects a real one for v2
// SEMANTIC selection; nil falls back to the v1 DETERMINISTIC token selector (the offline
// floor). This is the "cheap-model-proposed query" the plan layers on top of v1.
type SelectorFunc func(task string, candidateKeys []string) (relevantKeys []string)

// AgentContextForTask is the task-driven entry point used by the launch hooks: it
// renders the law + only the reference records relevant to the task, selected by the v1
// DETERMINISTIC token match (significant tokens vs each record's Key+Body). Equivalent
// to AgentContextForTaskSel with a nil selector.
func AgentContextForTask(st Store, task string) string {
	return AgentContextForTaskSel(st, task, nil, "")
}

// AgentContextForTaskSel renders the task-sliced contract using sel to choose the
// relevant reference records (v2 semantic selection) — or, when sel is nil, the
// deterministic v1 token match. The law (gates+conventions) always loads in full. Falls
// back to the FULL context when there is no selection signal at all (nil sel AND no
// usable tokens), so the agent is never starved of context.
func AgentContextForTaskSel(st Store, task string, sel SelectorFunc, focus string) string {
	toks := significantTokens(task)
	if st == nil || (sel == nil && len(toks) == 0) {
		return AgentPreamble(st) + CaptureHint(task)
	}

	tokenMatch := func(r Record) bool {
		hay := strings.ToLower(matchText(r)) // match on symbol name/sig/doc, not the path
		for _, t := range toks {
			if strings.Contains(hay, t) {
				return true
			}
		}
		return false
	}

	var matchAny func(r Record) bool
	if sel != nil {
		// v2: hand the model the candidate reference keys and use the subset it returns.
		selected := sel(task, referenceKeys(st))
		if len(selected) == 0 && len(toks) > 0 {
			matchAny = tokenMatch // model picked nothing (or failed) → safe v1 fallback
		} else {
			chosen := map[string]bool{}
			for _, k := range selected {
				chosen[strings.TrimSpace(k)] = true
			}
			matchAny = func(r Record) bool { return chosen[strings.TrimSpace(r.Key)] }
		}
	} else {
		matchAny = tokenMatch // v1: deterministic token overlap
	}

	var b strings.Builder
	b.WriteString("# ProjX project knowledge store — YOUR CONTRACT (read this first)\n\n")
	b.WriteString(preambleProtocolText)
	b.WriteString("\n\n---\n\n")
	b.WriteString("# Current store contents (task-sliced)\n")
	b.WriteString("_The law (off-limits + conventions) is shown in full; reference sections are narrowed to this task. Run `projx-engine store get <id>` or `store query` to pull anything not shown._\n")

	wroteAny := false
	for _, sec := range preambleSections {
		recs := freshRecords(dropSettings(st.List(OfKind(sec.kind))), nowMillis())
		overflow := 0
		if !sec.full { // reference: keep task-relevant, then rank+cap so it can't explode
			matched := recs[:0:0]
			for _, r := range recs {
				if matchAny(r) {
					matched = append(matched, r)
				}
			}
			recs, overflow = rankAndCap(matched, toks, focus)
		} else {
			sort.Slice(recs, func(i, j int) bool {
				if recs[i].Key != recs[j].Key {
					return recs[i].Key < recs[j].Key
				}
				return recs[i].ID < recs[j].ID
			})
		}
		if len(recs) == 0 {
			continue
		}
		wroteAny = true
		fmt.Fprintf(&b, "\n## %s\n", sec.header)
		if !sec.full {
			b.WriteString("_(indexed — run `projx-engine store get <id>` to load full content)_\n")
		}
		for _, r := range recs {
			if sec.full && len(r.Body) <= preambleFullBodyCap {
				renderPreambleRecord(&b, sec.kind, r)
			} else {
				renderPreambleIndexRecord(&b, sec.kind, r)
			}
		}
		if overflow > 0 {
			fmt.Fprintf(&b, "- _(+%d more matched this task — run `store query` to load)_\n", overflow)
		}
	}
	if !wroteAny {
		b.WriteString("\n_(no matching knowledge — the store is empty or nothing matched the task)_\n")
	}
	b.WriteString(reviewDueNotice(st, task))
	b.WriteString(CaptureHint(task)) // "" unless the task signals "remember this"
	return b.String()
}

// AgentContextFloor renders the LEAN session-start floor: the full protocol lecture
// plus the LAW (gate rules + conventions) in full — and nothing else. The reference
// knowledge (ADRs, docs, declared structure, history) is deliberately NOT dumped here;
// it loads per-message via AgentContextDelta as a task makes it relevant ("why load
// the full context if you don't have to"). This is the SessionStart counterpart to the
// per-message delta: small + binding at the top of a session, the rest streamed in on
// demand. Pure and read-only; a nil/empty store still yields the protocol.
func AgentContextFloor(st Store) string {
	var b strings.Builder
	b.WriteString("# ProjX project knowledge store — YOUR CONTRACT (read this first)\n\n")
	b.WriteString(preambleProtocolText)
	b.WriteString("\n\n---\n\n")
	b.WriteString("# Standing law (always in force)\n")
	b.WriteString("_The binding floor for this project. Project docs, decisions, and history are NOT dumped here — they load per-task as you work. Run `projx-engine store get <id>` or `store query` to pull any of them._\n")

	if st == nil {
		b.WriteString("\n_(store unavailable)_\n")
		return b.String()
	}
	wroteAny := false
	for _, sec := range preambleSections {
		if !sec.full { // floor carries the LAW only; reference loads on demand
			continue
		}
		recs := freshRecords(dropSettings(st.List(OfKind(sec.kind))), nowMillis())
		if len(recs) == 0 {
			continue
		}
		sort.Slice(recs, func(i, j int) bool {
			if recs[i].Key != recs[j].Key {
				return recs[i].Key < recs[j].Key
			}
			return recs[i].ID < recs[j].ID
		})
		wroteAny = true
		fmt.Fprintf(&b, "\n## %s\n", sec.header)
		for _, r := range recs {
			if len(r.Body) <= preambleFullBodyCap {
				renderPreambleRecord(&b, sec.kind, r)
			} else {
				renderPreambleIndexRecord(&b, sec.kind, r)
			}
		}
	}
	if !wroteAny {
		b.WriteString("\n_(no law declared yet — no off-limits paths or conventions in the store.)_\n")
	}
	b.WriteString(reviewDueNotice(st, ""))
	return b.String()
}

// AgentContextDelta renders the PER-MESSAGE contract for a session that has ALREADY
// been injected the records named in `seen` (recordID -> the UpdatedAt at injection
// time). Unlike AgentContextForTask it does NOT repeat the protocol lecture — the
// agent received it at SessionStart and it is still in context. The LAW (gate rules +
// conventions) is always re-sent in full: it is small, binding, and must not be
// forgotten between turns. Reference records (ADR / doc / declared-structure / history)
// are included ONLY when they (a) match the task slice AND (b) are new or changed
// versus `seen` — so an unchanged record already in the agent's context is not paid
// for again. Returns the rendered text plus the UPDATED seen map (every task-relevant
// record now in the agent's context, by id -> UpdatedAt) for the caller to persist as
// the session checkpoint. A nil `seen` means "fresh session" (everything relevant is
// new). Deterministic and read-only. Equivalent to AgentContextDeltaSel with nil sel.
func AgentContextDelta(st Store, task string, seen map[string]int64) (string, map[string]int64) {
	return AgentContextDeltaSel(st, task, seen, nil, "")
}

// AgentContextDeltaSel is AgentContextDelta with an injectable relevance selector and a
// focus repo: when sel is non-nil it chooses the relevant reference records semantically
// (v2); nil uses the deterministic v1 token match. focus (a repo/group) boosts that
// repo's matched records. An empty/failed selection falls back to v1 so a turn is never
// starved.
func AgentContextDeltaSel(st Store, task string, seen map[string]int64, sel SelectorFunc, focus string) (string, map[string]int64) {
	nowSeen := make(map[string]int64, len(seen))
	for k, v := range seen {
		nowSeen[k] = v
	}
	if st == nil {
		return "# ProjX — session context update\n_(store unavailable)_\n", nowSeen
	}

	toks := significantTokens(task)
	tokenMatch := func(r Record) bool {
		if len(toks) == 0 { // no slicing signal → treat all reference as relevant
			return true
		}
		hay := strings.ToLower(matchText(r)) // match on symbol name/sig/doc, not the path
		for _, t := range toks {
			if strings.Contains(hay, t) {
				return true
			}
		}
		return false
	}
	relevant := tokenMatch
	if sel != nil {
		selected := sel(task, referenceKeys(st))
		if len(selected) > 0 || len(toks) == 0 {
			chosen := map[string]bool{}
			for _, k := range selected {
				chosen[strings.TrimSpace(k)] = true
			}
			relevant = func(r Record) bool { return chosen[strings.TrimSpace(r.Key)] }
		} // else: empty selection with tokens present → keep tokenMatch (v1 fallback)
	}

	var body strings.Builder
	newRefs := 0
	for _, sec := range preambleSections {
		recs := freshRecords(dropSettings(st.List(OfKind(sec.kind))), nowMillis())
		var kept []Record
		overflow := 0
		if sec.full { // LAW: always re-sent in full, never delta-suppressed
			kept = recs
			for _, r := range recs {
				nowSeen[r.ID] = r.UpdatedAt
			}
			sort.Slice(kept, func(i, j int) bool {
				if kept[i].Key != kept[j].Key {
					return kept[i].Key < kept[j].Key
				}
				return kept[i].ID < kept[j].ID
			})
		} else { // reference: task-relevant AND new/changed, then rank+cap
			var cand []Record
			for _, r := range recs {
				if !relevant(r) {
					continue
				}
				if old, ok := seen[r.ID]; ok && old == r.UpdatedAt {
					nowSeen[r.ID] = r.UpdatedAt // unchanged, already shown — keep, skip render
					continue
				}
				cand = append(cand, r) // new or changed & relevant
			}
			kept, overflow = rankAndCap(cand, toks, focus)
			for _, r := range kept { // mark ONLY what we actually inject as seen
				nowSeen[r.ID] = r.UpdatedAt
				newRefs++
			}
		}
		if len(kept) == 0 {
			continue
		}
		fmt.Fprintf(&body, "\n## %s\n", sec.header)
		if !sec.full {
			body.WriteString("_(indexed — run `projx-engine store get <id>` to load full content)_\n")
		}
		for _, r := range kept {
			if sec.full && len(r.Body) <= preambleFullBodyCap {
				renderPreambleRecord(&body, sec.kind, r)
			} else {
				renderPreambleIndexRecord(&body, sec.kind, r)
			}
		}
		if overflow > 0 {
			fmt.Fprintf(&body, "- _(+%d more matched this task — run `store query` to load)_\n", overflow)
		}
	}

	var b strings.Builder
	b.WriteString("# ProjX — session context update (standing law + new knowledge for this task)\n")
	if newRefs == 0 {
		b.WriteString("_The binding law below is unchanged and re-asserted; no NEW project knowledge applies to this task beyond what is already in your context. Run `projx-engine store get <id>` or `store query` to pull anything else._\n")
	} else {
		b.WriteString("_The law (off-limits + conventions) is re-asserted in full; below it is only project knowledge that is NEW or CHANGED for this task — everything else is already in your context. Run `projx-engine store get <id>` to pull anything not shown._\n")
	}
	b.WriteString(body.String())
	b.WriteString(reviewDueNotice(st, task))
	b.WriteString(CaptureHint(task)) // "" unless the task signals "remember this"
	return b.String(), nowSeen
}

// referenceKeys returns the distinct, sorted keys of all NON-law reference records
// (ADR / doc / declared-structure / history) — the candidate index a v2 SelectorFunc
// chooses from. Law sections (gates/conventions) always load in full and are excluded.
func referenceKeys(st Store) []string {
	if st == nil {
		return nil
	}
	seen := map[string]bool{}
	var keys []string
	for _, sec := range preambleSections {
		if sec.full {
			continue
		}
		for _, r := range freshRecords(dropSettings(st.List(OfKind(sec.kind))), nowMillis()) {
			k := strings.TrimSpace(r.Key)
			if k != "" && !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// significantTokens lowercases the task and returns distinct alphanumeric words
// ≥3 chars that aren't common stopwords — the deterministic v1 selector signal.
func significantTokens(task string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range strings.FieldsFunc(strings.ToLower(task), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}) {
		if len(f) < 3 || taskStopWords[f] || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

var taskStopWords = map[string]bool{
	"the": true, "and": true, "for": true, "add": true, "with": true, "that": true,
	"this": true, "need": true, "want": true, "have": true, "how": true, "what": true,
	"where": true, "when": true, "into": true, "from": true, "about": true, "some": true,
	"more": true, "can": true, "you": true, "please": true, "make": true, "new": true,
}

// renderPreambleRecord renders one record into the preamble at full fidelity.
// Gate rules render as bare path patterns (that's their Body); everything else
// renders Key + Body.
func renderPreambleRecord(b *strings.Builder, kind Kind, r Record) {
	key := strings.TrimSpace(r.Key)
	body := strings.TrimSpace(r.Body)
	switch kind {
	case KGateRule:
		b.WriteString("- `" + body + "`")
		if key != "" {
			b.WriteString("  — " + key)
		}
		b.WriteString("\n")
	default:
		if key != "" {
			b.WriteString("\n### " + key + "\n")
		}
		b.WriteString(body + "\n")
	}
}

// renderPreambleIndexRecord renders one record as a single index line:
//
//   - [`<id>`] <key> — <one-line summary>
//
// Gate rules are never index-rendered (they are always short by design).
func renderPreambleIndexRecord(b *strings.Builder, kind Kind, r Record) {
	key := strings.TrimSpace(r.Key)
	summary := preambleOneLine(strings.TrimSpace(r.Body))
	if kind == KGateRule {
		renderPreambleRecord(b, kind, r)
		return
	}
	line := fmt.Sprintf("- [`%s`] %s — %s", r.ID, key, summary)
	if len(r.Body) > preambleFullBodyCap {
		line += fmt.Sprintf("  _(body >%d bytes — run `projx-engine store get %s` for full content)_", preambleFullBodyCap, r.ID)
	}
	b.WriteString(line + "\n")
}

// preambleOneLine returns the first non-empty trimmed line of body, internal
// whitespace collapsed, truncated to 120 runes with a trailing ellipsis if
// longer. An empty body returns "(no summary)".
func preambleOneLine(body string) string {
	if body == "" {
		return "(no summary)"
	}
	line := ""
	for _, l := range strings.Split(body, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			line = l
			break
		}
	}
	if line == "" {
		return "(no summary)"
	}
	line = strings.Join(strings.Fields(line), " ")
	if utf8.RuneCountInString(line) > 120 {
		runes := []rune(line)
		line = string(runes[:120]) + "…"
	}
	return line
}
