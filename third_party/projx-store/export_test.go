package store

import (
	"strings"
	"testing"
)

func TestExport(t *testing.T) {
	s := NewMem()
	must := func(r Record) {
		if err := s.Put(r); err != nil {
			t.Fatal(err)
		}
	}
	must(Record{ID: "1", Kind: KADR, Scope: ScopeProject, Key: "adr-001", Body: "Use SQLite."})
	must(Record{ID: "2", Kind: KADR, Scope: ScopeProject, Key: "adr-002", Body: "Interface-first."})
	must(Record{ID: "3", Kind: KConvention, Scope: ScopeProject, Key: "wrap-errors", Body: "Wrap with %w."})

	out := Export(s)

	for _, want := range []string{
		"# Architecture",
		"## Architecture Decisions",
		"### adr-001",
		"### adr-002",
		"## Conventions",
		"### wrap-errors",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Export output missing %q\n--- output ---\n%s", want, out)
		}
	}

	// Empty sections are omitted (no records of these kinds were declared).
	for _, absent := range []string{"## Docs", "## History", "## Declared Structure"} {
		if strings.Contains(out, absent) {
			t.Errorf("Export output should omit empty section %q", absent)
		}
	}
}
