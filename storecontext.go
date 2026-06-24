package main

// storecontext.go — ambient store injection for agent launch.
//
// Ported from projx-workbench/storeprompt.go. Compiles the live project store
// into a text preamble that is:
//   1. Written to <root>/.projx/agent-context.md (portable, always present).
//   2. Delivered via PROJX_STORE_CONTEXT + PROJX_STORE_CONTEXT_FILE env vars so
//      any agent harness can consume it without understanding ProjX flags.
//
// VENDOR-NEUTRAL: works with Claude Code, opencode, aider, or any agent CLI.
// The knowledge is present by construction at launch; the agent never needs to be
// taught it at runtime.
//
// TIERED AMBIENT CONTEXT (token thesis):
//
// Not all store records need to be delivered verbatim at launch. Gate rules and
// conventions are load-bearing law the agent must always have verbatim (full).
// ADRs, declared structure, docs, and history are reference material the agent
// can pull on demand — those sections are indexed (id + one-line summary only).
//
// Index entries tell the agent to run `projx-engine store get <id>` to load a
// full record's content when needed.
//
// TODO (future): session-delta delivery — track which records the agent already
// saw in a prior turn and only send changes. That requires a per-session
// checkpoint file. Deferred; tiered/index retrieval is this pillar's scope.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	store "github.com/SirNiklas9/projx-store"
)

// agentContextFileName is the on-disk preamble written for every launched agent.
// Lives under .projx so it travels with the project and is trivially git-ignorable.
const agentContextFileName = "agent-context.md"

// fullBodyCap is the maximum body length (bytes) for a record in a FULL section.
// Records exceeding this are demoted to index lines even in full sections, so one
// unusually large record cannot blow up the launch context.
const fullBodyCap = 1500

// storeProtocolText is the fixed, vendor-neutral contract the agent is handed at
// launch: how the store works and the rules it is bound to. Copied faithfully from
// projx-workbench/storeprompt.go — this is the authoritative text.
const storeProtocolText = `You are running inside ProjX. The PROJECT STORE below is your single source of
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
//
// Gate rules and conventions are load-bearing law/behavior the agent must always
// have verbatim. ADRs, declared structure, docs, and history are reference the
// agent can pull on demand.
type preambleSection struct {
	kind   store.Kind
	header string
	full   bool
}

var preambleSections = []preambleSection{
	{store.KGateRule, "OFF-LIMITS — do NOT read, edit, or run against these (this is LAW, enforced)", true},
	{store.KConvention, "Conventions you MUST follow", true},
	{store.KADR, "Architecture decisions (ADRs)", false},
	{store.KDeclaredStructure, "Declared structure / boundary rules", false},
	{store.KDoc, "Subsystem notes", false},
	{store.KHistory, "Recent history (most recent decisions/changes)", false},
}

// compileStorePreamble renders the project store into the ambient agent preamble:
// the protocol the agent MUST follow (read-before-act, commit-on-learn, gate is
// law) followed by the live store contents grouped by kind.
//
// Full sections (gate rules, conventions) deliver verbatim body up to fullBodyCap
// bytes; oversized records are demoted to index lines. Index sections (ADRs,
// declared structure, docs, history) deliver one line per record with a note to
// fetch full content on demand — this is the token-reduction pillar.
//
// Deterministic and read-only (List only); never mutates the store. An empty
// store still yields the protocol section, so the agent always knows the rules
// even before any knowledge is declared.
func compileStorePreamble(st store.Store) string {
	var b strings.Builder
	b.WriteString("# ProjX project knowledge store — YOUR CONTRACT (read this first)\n\n")
	b.WriteString(storeProtocolText)
	b.WriteString("\n\n---\n\n")
	b.WriteString("# Current store contents\n")
	b.WriteString("_This is the live store at launch. It is the authoritative project knowledge — not any README or .md file. Treat everything below as already-known context._\n")

	if st == nil {
		b.WriteString("\n_(store unavailable)_\n")
		return b.String()
	}

	wroteAny := false
	for _, sec := range preambleSections {
		recs := nonSettingsRecords(st.List(store.OfKind(sec.kind)))
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
			if sec.full && len(r.Body) <= fullBodyCap {
				renderPreambleRecord(&b, sec.kind, r)
			} else {
				renderIndexRecord(&b, sec.kind, r)
			}
		}
	}
	if !wroteAny {
		b.WriteString("\n_(the store is empty — no knowledge declared yet. Use store.commit to populate it as you learn.)_\n")
	}
	return b.String()
}

// renderPreambleRecord renders one record into the preamble at full fidelity.
// Gate rules render as bare path patterns (that's their Body); everything else
// renders Key + Body.
func renderPreambleRecord(b *strings.Builder, kind store.Kind, r store.Record) {
	key := strings.TrimSpace(r.Key)
	body := strings.TrimSpace(r.Body)
	switch kind {
	case store.KGateRule:
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

// renderIndexRecord renders one record as a single index line:
//
//	- [`<id>`] <key> — <one-line summary>
//
// For full-section records that exceeded fullBodyCap, a note is appended.
// Gate rules are never index-rendered (they are always short by design).
func renderIndexRecord(b *strings.Builder, kind store.Kind, r store.Record) {
	key := strings.TrimSpace(r.Key)
	summary := oneLineSummary(strings.TrimSpace(r.Body))
	if kind == store.KGateRule {
		// Gate rules are always short — fall back to full render for safety.
		renderPreambleRecord(b, kind, r)
		return
	}
	line := fmt.Sprintf("- [`%s`] %s — %s", r.ID, key, summary)
	if len(r.Body) > fullBodyCap {
		line += fmt.Sprintf("  _(body >%d bytes — run `projx-engine store get %s` for full content)_", fullBodyCap, r.ID)
	}
	b.WriteString(line + "\n")
}

// oneLineSummary returns the first non-empty trimmed line of body, with internal
// whitespace collapsed to single spaces, truncated to 120 runes with a trailing
// ellipsis if longer. An empty body returns "(no summary)".
func oneLineSummary(body string) string {
	if body == "" {
		return "(no summary)"
	}
	// Find first non-empty line.
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
	// Collapse internal whitespace.
	line = strings.Join(strings.Fields(line), " ")
	// Truncate to 120 runes.
	if utf8.RuneCountInString(line) > 120 {
		runes := []rune(line)
		line = string(runes[:120]) + "…"
	}
	return line
}

// nonSettingsRecords drops setting/* records (e.g. the OpenRouter key/model) —
// secrets and config NEVER belong in the agent context. Defense-in-depth:
// filtered everywhere they might appear.
func nonSettingsRecords(recs []store.Record) []store.Record {
	out := make([]store.Record, 0, len(recs))
	for _, r := range recs {
		if strings.HasPrefix(r.ID, "setting/") || strings.HasPrefix(r.Key, "setting/") {
			continue
		}
		out = append(out, r)
	}
	return out
}

// writeAgentContextText writes an already-compiled preamble to disk at
// <root>/.projx/agent-context.md, returning the absolute path. Best-effort —
// a write failure must not block launching the agent; the env-var delivery
// carries the context even if the file write fails.
func writeAgentContextText(root, text string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("no project root")
	}
	dir := filepath.Join(root, ".projx")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, agentContextFileName)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
