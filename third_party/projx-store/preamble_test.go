package store

import (
	"strings"
	"testing"
)

// TestAgentPreambleTiered is the primary tiering regression test: full sections
// render verbatim (with a per-record cap), index sections render one line.
func TestAgentPreambleTiered(t *testing.T) {
	m := NewMem()
	mustPut := func(r Record) {
		if err := m.Put(r); err != nil {
			t.Fatalf("seed Put %s: %v", r.ID, err)
		}
	}
	mustPut(Record{ID: "gate-rule/secret", Kind: KGateRule, Scope: ScopeProject, Key: "secret paths", Body: "secret/"})
	mustPut(Record{ID: "convention/naming", Kind: KConvention, Scope: ScopeProject, Key: "naming", Body: "All exported symbols use camelCase. No underscores in public names."})
	mustPut(Record{ID: "adr/db-choice", Kind: KADR, Scope: ScopeProject, Key: "db-choice", Body: "We use SQLite for the project store because it requires no daemon."})
	mustPut(Record{ID: "doc/big-doc", Kind: KDoc, Scope: ScopeProject, Key: "big-doc", Body: strings.Repeat("x ", 2000)})

	p := AgentPreamble(m)

	if !strings.Contains(p, "secret/") {
		t.Error("gate rule body 'secret/' not found in preamble")
	}
	wantConv := "All exported symbols use camelCase. No underscores in public names."
	if !strings.Contains(p, wantConv) {
		t.Errorf("convention body not found\nwant: %q", wantConv)
	}
	if !strings.Contains(p, "big-doc") {
		t.Error("doc key 'big-doc' not found in index")
	}
	if !strings.Contains(p, "store get") {
		t.Error("'store get' reference not found for indexed doc")
	}
	if strings.Contains(p, strings.Repeat("x ", 2000)) {
		t.Error("full 4KB big-doc body present — should be indexed only")
	}
	if !strings.Contains(p, "- [`adr/db-choice`]") {
		t.Error("ADR 'db-choice' not rendered as index line")
	}
	if len(p) >= 4000 {
		t.Errorf("preamble length %d >= 4000 — tiering not reducing tokens", len(p))
	}
}

// TestAgentPreambleFullSectionSizeCap verifies a >cap convention is demoted to
// an index line even though conventions are a "full" section.
func TestAgentPreambleFullSectionSizeCap(t *testing.T) {
	m := NewMem()
	if err := m.Put(Record{ID: "convention/short", Kind: KConvention, Scope: ScopeProject, Key: "short-convention", Body: "A short convention body."}); err != nil {
		t.Fatalf("put: %v", err)
	}
	longBody := strings.Repeat("y ", 1000) // 2000 bytes > cap
	if err := m.Put(Record{ID: "convention/big", Kind: KConvention, Scope: ScopeProject, Key: "big-convention", Body: longBody}); err != nil {
		t.Fatalf("put: %v", err)
	}

	p := AgentPreamble(m)
	if !strings.Contains(p, "A short convention body.") {
		t.Error("short convention body not found — expected full render")
	}
	if strings.Contains(p, longBody) {
		t.Error("oversized convention full body present — size cap not applied")
	}
	if !strings.Contains(p, "- [`convention/big`]") {
		t.Error("oversized convention not rendered as index line")
	}
}

// TestAgentPreambleProtocolText verifies the contract protocol text is present
// and mentions the on-demand index pattern.
func TestAgentPreambleProtocolText(t *testing.T) {
	if !strings.Contains(preambleProtocolText, "INDEX") {
		t.Error("protocol rule 2 does not mention INDEX — agent may not know to fetch full records")
	}
	if !strings.Contains(preambleProtocolText, "store get") {
		t.Error("protocol does not mention 'store get' — on-demand fetch instruction missing")
	}
	// And it must surface through AgentPreamble.
	if !strings.Contains(AgentPreamble(NewMem()), "YOUR CONTRACT") {
		t.Error("AgentPreamble output missing the contract header")
	}
}

// TestAgentPreambleEmptyAndNil verifies a nil/empty store still yields the
// protocol (the agent always knows the rules).
func TestAgentPreambleEmptyAndNil(t *testing.T) {
	p := AgentPreamble(nil)
	if !strings.Contains(p, "ProjX") {
		t.Error("nil store: protocol header not found")
	}
	if !strings.Contains(p, "store unavailable") {
		t.Error("nil store: expected '(store unavailable)' marker")
	}
	p2 := AgentPreamble(NewMem())
	if !strings.Contains(p2, "ProjX") {
		t.Error("empty store: protocol header not found")
	}
	if !strings.Contains(p2, "store is empty") {
		t.Error("empty store: expected '(the store is empty…)' marker")
	}
}

// TestAgentContextSlices proves task-sliced injection: LAW always present, only
// the relevant reference records included, irrelevant ones (+ canaries) excluded,
// sliced < full, and zero-selector == AgentPreamble (back-compat).
func TestAgentContextSlices(t *testing.T) {
	m := NewMem()
	put := func(id string, k Kind, key, body string) {
		if err := m.Put(Record{ID: id, Kind: k, Scope: ScopeProject, Key: key, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	put("gate-rule/secrets", KGateRule, "secrets", "secret/**")        // LAW
	put("convention/naming", KConvention, "naming", "use camelCase")   // LAW
	put("doc/mc-login", KDoc, "minecraft/login/backend", "JWT auth in internal/auth/login.go")
	put("doc/billing", KDoc, "billing/checkout", "stripe flow")
	put("doc/canary", KDoc, "canary/up", "Up has balloons")

	full := AgentContext(m, Filter{})
	sliced := AgentContext(m, Filter{KeyPrefix: "minecraft/login"})

	if full != AgentPreamble(m) {
		t.Error("AgentContext(Filter{}) must equal AgentPreamble (back-compat)")
	}
	for _, law := range []string{"secret/**", "use camelCase"} {
		if !strings.Contains(sliced, law) {
			t.Errorf("sliced is missing the LAW %q (law must always load)", law)
		}
	}
	if !strings.Contains(sliced, "minecraft/login/backend") {
		t.Error("sliced is missing the relevant minecraft/login doc")
	}
	for _, off := range []string{"billing/checkout", "canary/up", "balloons"} {
		if strings.Contains(sliced, off) {
			t.Errorf("sliced should NOT contain %q (it's outside the task slice)", off)
		}
	}
	if len(sliced) >= len(full) {
		t.Errorf("sliced (%d bytes) should be smaller than full (%d bytes) — the cost win", len(sliced), len(full))
	}
}

// TestAgentContextForTask proves the deterministic v1 selector: a task's tokens
// pull the relevant reference records (law always present), exclude the rest +
// canaries, and a token-less task falls back to the full preamble.
func TestAgentContextForTask(t *testing.T) {
	m := NewMem()
	put := func(id string, k Kind, key, body string) {
		if err := m.Put(Record{ID: id, Kind: k, Scope: ScopeProject, Key: key, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	put("gate-rule/secrets", KGateRule, "secrets", "secret/**")      // LAW
	put("convention/naming", KConvention, "naming", "use camelCase") // LAW
	put("doc/mc-login", KDoc, "minecraft/login/backend", "JWT auth in internal/auth/login.go")
	put("doc/billing", KDoc, "billing/checkout", "stripe flow")
	put("doc/canary", KDoc, "canary/up", "Up has balloons")

	ctx := AgentContextForTask(m, "look at the minecraft login backend")
	for _, law := range []string{"secret/**", "use camelCase"} {
		if !strings.Contains(ctx, law) {
			t.Errorf("task context missing LAW %q", law)
		}
	}
	if !strings.Contains(ctx, "minecraft/login/backend") {
		t.Error("task context missing the relevant minecraft/login doc")
	}
	for _, off := range []string{"billing/checkout", "canary/up", "balloons"} {
		if strings.Contains(ctx, off) {
			t.Errorf("task context should NOT contain %q (outside the task)", off)
		}
	}
	// A task with no significant tokens falls back to the full preamble.
	if AgentContextForTask(m, "the a an of") != AgentPreamble(m) {
		t.Error("token-less task should fall back to the full AgentPreamble")
	}
}

// TestAgentContextFloor proves the lean session-start floor: protocol + LAW in
// full, and NO reference knowledge dumped (it streams in per-task via the delta).
func TestAgentContextFloor(t *testing.T) {
	m := NewMem()
	put := func(id string, k Kind, key, body string) {
		if err := m.Put(Record{ID: id, Kind: k, Scope: ScopeProject, Key: key, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	put("gate-rule/secrets", KGateRule, "secrets", "secret/**")      // LAW
	put("convention/naming", KConvention, "naming", "use camelCase") // LAW
	put("doc/mc-login", KDoc, "minecraft/login/backend", "JWT auth in internal/auth/login.go")
	put("doc/canary", KDoc, "canary/up", "Up has balloons")

	floor := AgentContextFloor(m)
	if !strings.Contains(floor, "READ BEFORE ACTING") {
		t.Error("floor missing the protocol lecture")
	}
	for _, law := range []string{"secret/**", "use camelCase"} {
		if !strings.Contains(floor, law) {
			t.Errorf("floor missing LAW %q", law)
		}
	}
	for _, ref := range []string{"minecraft/login/backend", "doc/mc-login", "canary/up", "balloons"} {
		if strings.Contains(floor, ref) {
			t.Errorf("floor should NOT dump reference knowledge %q (loads per-task)", ref)
		}
	}
	if len(floor) >= len(AgentPreamble(m)) {
		t.Error("lean floor should be smaller than the full preamble")
	}
}

// TestAgentContextDelta proves the per-message delta contract: the LAW always
// re-sends in full, a NEW task-relevant reference record is injected once, an
// UNCHANGED already-seen record is suppressed on the next turn, a CHANGED record
// re-injects, and the returned seen map tracks what has been shown.
func TestAgentContextDelta(t *testing.T) {
	m := NewMem()
	put := func(id string, k Kind, key, body string) {
		if err := m.Put(Record{ID: id, Kind: k, Scope: ScopeProject, Key: key, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	put("gate-rule/secrets", KGateRule, "secrets", "secret/**")      // LAW
	put("convention/naming", KConvention, "naming", "use camelCase") // LAW
	put("doc/mc-login", KDoc, "minecraft/login/backend", "JWT auth in internal/auth/login.go")
	put("doc/billing", KDoc, "billing/checkout", "stripe flow")
	put("doc/canary", KDoc, "canary/up", "Up has balloons")

	// Turn 1: nothing seen yet. The relevant doc is new → injected; law present;
	// the protocol lecture is NOT repeated (delta is leaner than the full preamble).
	d1, seen1 := AgentContextDelta(m, "look at the minecraft login backend", nil)
	for _, law := range []string{"secret/**", "use camelCase"} {
		if !strings.Contains(d1, law) {
			t.Errorf("delta turn1 missing LAW %q", law)
		}
	}
	if !strings.Contains(d1, "minecraft/login/backend") {
		t.Error("delta turn1 missing the new relevant doc")
	}
	for _, off := range []string{"billing/checkout", "canary/up", "balloons"} {
		if strings.Contains(d1, off) {
			t.Errorf("delta turn1 leaked out-of-slice %q", off)
		}
	}
	if strings.Contains(d1, "READ BEFORE ACTING") {
		t.Error("delta should NOT repeat the full protocol lecture each turn")
	}
	if seen1["doc/mc-login"] == 0 {
		t.Error("seen map should record the injected doc/mc-login")
	}

	// Turn 2: same task, doc/mc-login unchanged → SUPPRESSED (already in context),
	// but the LAW still re-sends.
	d2, _ := AgentContextDelta(m, "more on the minecraft login backend", seen1)
	if strings.Contains(d2, "minecraft/login/backend") {
		t.Error("delta turn2 re-sent an unchanged, already-seen doc")
	}
	if !strings.Contains(d2, "secret/**") {
		t.Error("delta turn2 dropped the standing LAW (must always re-send)")
	}

	// Turn 3: the doc CHANGES → it re-injects even though it was seen.
	put("doc/mc-login", KDoc, "minecraft/login/backend", "JWT auth moved to internal/auth/v2.go")
	d3, _ := AgentContextDelta(m, "minecraft login backend again", seen1)
	if !strings.Contains(d3, "internal/auth/v2.go") {
		t.Error("delta turn3 failed to re-inject a CHANGED record")
	}
}

// TestAgentContextForTaskSel proves the v2 selector path: a SelectorFunc is handed the
// candidate reference keys and its chosen subset is what loads (law always present),
// independent of token overlap. A nil selector reverts to v1 token matching.
func TestAgentContextForTaskSel(t *testing.T) {
	m := NewMem()
	put := func(id string, k Kind, key, body string) {
		if err := m.Put(Record{ID: id, Kind: k, Scope: ScopeProject, Key: key, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	put("gate-rule/secrets", KGateRule, "secrets", "secret/**")
	put("convention/naming", KConvention, "naming", "use camelCase")
	put("doc/login", KDoc, "auth/login/backend", "JWT session handling")
	put("doc/billing", KDoc, "billing/checkout", "stripe flow")

	// The selector receives the candidate keys and picks billing — even though the task
	// words ("paragraph...") don't lexically overlap it. This is the semantic win the v1
	// token matcher can't do.
	var gotKeys []string
	sel := func(task string, keys []string) []string {
		gotKeys = keys
		return []string{"billing/checkout"}
	}
	ctx := AgentContextForTaskSel(m, "a long rambling paragraph about money movement", sel, "")

	if len(gotKeys) != 2 {
		t.Errorf("selector should get both reference keys, got %v", gotKeys)
	}
	if !strings.Contains(ctx, "secret/**") || !strings.Contains(ctx, "use camelCase") {
		t.Error("law must always load regardless of selector")
	}
	if !strings.Contains(ctx, "billing/checkout") {
		t.Error("selector-chosen record missing")
	}
	if strings.Contains(ctx, "auth/login/backend") {
		t.Error("non-selected record leaked")
	}

	// Nil selector → v1 token match (a "login" task pulls the login doc, not billing).
	v1 := AgentContextForTaskSel(m, "fix the login backend", nil, "")
	if !strings.Contains(v1, "auth/login/backend") || strings.Contains(v1, "billing/checkout") {
		t.Error("nil selector should use v1 token matching")
	}

	// A selector that returns nothing (model failed) degrades to v1, not starvation:
	// the "login" task still pulls the login doc via token fallback.
	empty := AgentContextForTaskSel(m, "fix the login backend", func(string, []string) []string { return nil }, "")
	if !strings.Contains(empty, "auth/login/backend") {
		t.Error("empty selection should fall back to v1, not starve the agent")
	}
}

// TestReferenceKeys covers the candidate-key gathering for the selector.
func TestReferenceKeys(t *testing.T) {
	m := NewMem()
	_ = m.Put(Record{ID: "g", Kind: KGateRule, Scope: ScopeProject, Key: "secrets", Body: "secret/**"})
	_ = m.Put(Record{ID: "d1", Kind: KDoc, Scope: ScopeProject, Key: "auth/login", Body: "x"})
	_ = m.Put(Record{ID: "d2", Kind: KADR, Scope: ScopeProject, Key: "db/choice", Body: "y"})
	keys := referenceKeys(m)
	// Law (gate) key excluded; reference keys present and sorted.
	want := []string{"auth/login", "db/choice"}
	if len(keys) != 2 || keys[0] != want[0] || keys[1] != want[1] {
		t.Errorf("referenceKeys = %v, want %v (law excluded, sorted)", keys, want)
	}
}

// TestPreambleOneLine covers the summary helper's truncation and edge cases.
func TestPreambleOneLine(t *testing.T) {
	cases := []struct {
		name         string
		input        string
		wantPrefix   string
		wantMaxRunes int
		wantSuffix   string
		wantExact    string
	}{
		{name: "empty body", input: "", wantExact: "(no summary)", wantMaxRunes: 12},
		{name: "single short line", input: "hello world", wantExact: "hello world", wantMaxRunes: 11},
		{name: "multi-line picks first", input: "first line\nsecond line\nthird", wantPrefix: "first line", wantMaxRunes: 121},
		{name: "long line truncated", input: strings.Repeat("a", 200), wantMaxRunes: 121, wantSuffix: "…"},
		{name: "exactly 120 runes — no ellipsis", input: strings.Repeat("b", 120), wantMaxRunes: 120, wantExact: strings.Repeat("b", 120)},
		{name: "121 runes — truncated", input: strings.Repeat("c", 121), wantMaxRunes: 121, wantSuffix: "…"},
		{name: "internal whitespace collapsed", input: "hello   world\nnext", wantExact: "hello world", wantMaxRunes: 11},
		{name: "blank lines before content", input: "\n\n   \nactual content\nignored", wantExact: "actual content", wantMaxRunes: 14},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := preambleOneLine(tc.input)
			if rc := len([]rune(got)); rc > tc.wantMaxRunes {
				t.Errorf("rune count %d > %d: %q", rc, tc.wantMaxRunes, got)
			}
			if tc.wantExact != "" && got != tc.wantExact {
				t.Errorf("got %q, want %q", got, tc.wantExact)
			}
			if tc.wantPrefix != "" && !strings.HasPrefix(got, tc.wantPrefix) {
				t.Errorf("got %q, want prefix %q", got, tc.wantPrefix)
			}
			if tc.wantSuffix != "" && !strings.HasSuffix(got, tc.wantSuffix) {
				t.Errorf("got %q, want suffix %q", got, tc.wantSuffix)
			}
		})
	}
}
