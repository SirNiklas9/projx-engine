package main

import (
	"strings"
	"testing"

	store "github.com/SirNiklas9/projx-store"
)

// seedTieredStore returns a Mem store seeded with:
//   - one KGateRule  (Body="secret/")
//   - one KConvention (Key="naming", short Body)
//   - one KADR        (Key="db-choice", short Body)
//   - one KDoc        (Key="big-doc", Body ~4KB)
func seedTieredStore(t *testing.T) *store.Mem {
	t.Helper()
	m := store.NewMem()
	mustPut := func(r store.Record) {
		if err := m.Put(r); err != nil {
			t.Fatalf("seed Put %s: %v", r.ID, err)
		}
	}
	mustPut(store.Record{
		ID:    "gate-rule/secret",
		Kind:  store.KGateRule,
		Scope: store.ScopeProject,
		Key:   "secret paths",
		Body:  "secret/",
	})
	mustPut(store.Record{
		ID:    "convention/naming",
		Kind:  store.KConvention,
		Scope: store.ScopeProject,
		Key:   "naming",
		Body:  "All exported symbols use camelCase. No underscores in public names.",
	})
	mustPut(store.Record{
		ID:    "adr/db-choice",
		Kind:  store.KADR,
		Scope: store.ScopeProject,
		Key:   "db-choice",
		Body:  "We use SQLite for the project store because it requires no daemon.",
	})
	mustPut(store.Record{
		ID:    "doc/big-doc",
		Kind:  store.KDoc,
		Scope: store.ScopeProject,
		Key:   "big-doc",
		Body:  strings.Repeat("x ", 2000), // ~4 000 bytes
	})
	return m
}

// TestCompileStorePreambleTiered is the primary tiering regression test.
func TestCompileStorePreambleTiered(t *testing.T) {
	m := seedTieredStore(t)
	preamble := compileStorePreamble(m)

	// Full sections: gate rule body "secret/" must be present verbatim.
	if !strings.Contains(preamble, "secret/") {
		t.Error("gate rule body 'secret/' not found in preamble")
	}

	// Full sections: convention body must be present verbatim.
	wantConvBody := "All exported symbols use camelCase. No underscores in public names."
	if !strings.Contains(preamble, wantConvBody) {
		t.Errorf("convention body not found in preamble\nwant substring: %q", wantConvBody)
	}

	// Index sections: doc key "big-doc" must appear in the index.
	if !strings.Contains(preamble, "big-doc") {
		t.Error("doc key 'big-doc' not found in preamble index")
	}

	// Index sections: doc must include a "store get" reference.
	if !strings.Contains(preamble, "store get") {
		t.Error("'store get' reference not found in preamble for indexed doc")
	}

	// Index sections: the full 4KB body string must NOT appear.
	bigBody := strings.Repeat("x ", 2000)
	if strings.Contains(preamble, bigBody) {
		t.Error("full 4KB big-doc body is present in preamble — it should be indexed only")
	}

	// Index sections: ADR "db-choice" must appear as an index line.
	if !strings.Contains(preamble, "db-choice") {
		t.Error("ADR key 'db-choice' not found in preamble index")
	}
	// Its full body should NOT appear verbatim as a ### section heading in the ADR block.
	// (The body is short but the section is index-tier, so it goes through renderIndexRecord.)
	// We check by asserting "db-choice" appears in a "- [" index line format.
	if !strings.Contains(preamble, "- [`adr/db-choice`]") {
		t.Error("ADR 'db-choice' not rendered as index line '- [`adr/db-choice`]'")
	}

	// Token win: total preamble must be far smaller than the sum of all bodies.
	// big-doc alone is ~4000 bytes; the full-dump would exceed that significantly.
	// With indexing the preamble should stay under 2000 bytes of store content
	// (protocol text is fixed overhead; we check total preamble < 4000).
	if len(preamble) >= 4000 {
		t.Errorf("preamble length %d >= 4000 — tiering is not reducing token cost (big-doc body may be leaking)", len(preamble))
	}
}

// Note: the one-line-summary helper and the protocol text moved into the shared
// projx-store library; their unit tests live in projx-store/preamble_test.go.
// compileStorePreamble is now a thin alias over store.AgentPreamble, exercised by
// the preamble tests below.

// TestFullSectionSizeCap verifies that a KConvention with a >1500-byte Body is
// demoted to an index line even though conventions are a "full" section.
func TestFullSectionSizeCap(t *testing.T) {
	m := store.NewMem()
	// Short convention — should render full.
	if err := m.Put(store.Record{
		ID:    "convention/short",
		Kind:  store.KConvention,
		Scope: store.ScopeProject,
		Key:   "short-convention",
		Body:  "A short convention body.",
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	// Oversized convention — must be demoted to index.
	longBody := strings.Repeat("y ", 1000) // 2000 bytes > 1500
	if err := m.Put(store.Record{
		ID:    "convention/big",
		Kind:  store.KConvention,
		Scope: store.ScopeProject,
		Key:   "big-convention",
		Body:  longBody,
	}); err != nil {
		t.Fatalf("put: %v", err)
	}

	preamble := compileStorePreamble(m)

	// Short convention must be fully rendered (its body appears verbatim).
	if !strings.Contains(preamble, "A short convention body.") {
		t.Error("short convention body not found — expected full render")
	}

	// Oversized convention must NOT have its full body in the preamble.
	if strings.Contains(preamble, longBody) {
		t.Error("oversized convention full body is present — size cap not applied")
	}

	// Oversized convention must appear as an index line.
	if !strings.Contains(preamble, "- [`convention/big`]") {
		t.Error("oversized convention not rendered as index line '- [`convention/big`]'")
	}

	// Oversized convention index line must mention the size-cap note.
	if !strings.Contains(preamble, "store get") {
		t.Error("'store get' reference missing from oversized convention index line")
	}
}

// TestEmptyStoreStillYieldsProtocol verifies that a nil or empty store still
// returns the protocol text (the agent always knows the rules).
func TestEmptyStoreStillYieldsProtocol(t *testing.T) {
	// nil store
	p := compileStorePreamble(nil)
	if !strings.Contains(p, "ProjX") {
		t.Error("nil store: protocol header not found")
	}
	if !strings.Contains(p, "store unavailable") {
		t.Error("nil store: expected '(store unavailable)' marker")
	}

	// empty store
	p2 := compileStorePreamble(store.NewMem())
	if !strings.Contains(p2, "ProjX") {
		t.Error("empty store: protocol header not found")
	}
	if !strings.Contains(p2, "store is empty") {
		t.Error("empty store: expected '(the store is empty…)' marker")
	}
}
