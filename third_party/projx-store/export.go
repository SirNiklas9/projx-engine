package store

import (
	"sort"
	"strings"
)

// exportSection pairs a Kind with the markdown header it renders under, in the
// fixed order Export emits.
type exportSection struct {
	kind   Kind
	header string
}

var exportSections = []exportSection{
	{KADR, "Architecture Decisions"},
	{KDeclaredStructure, "Declared Structure"},
	{KConvention, "Conventions"},
	{KDoc, "Docs"},
	{KHistory, "History"},
}

// Export renders the project store's declared knowledge as an architecture.md-style
// markdown document. It is strictly READ-ONLY: it pulls records via List and never
// writes. Records are grouped into fixed sections (Architecture Decisions, Declared
// Structure, Conventions, Docs, History); empty sections are omitted. Within a
// section records are sorted by Key (then ID) and rendered as "### {Key}" followed
// by the record Body.
func Export(project Store) string {
	var b strings.Builder
	b.WriteString("# Architecture\n\n")
	b.WriteString("_Generated, read-only view of declared knowledge. Do not edit by hand._\n")

	for _, sec := range exportSections {
		records := project.List(OfKind(sec.kind))
		if len(records) == 0 {
			continue
		}
		sort.Slice(records, func(i, j int) bool {
			if records[i].Key != records[j].Key {
				return records[i].Key < records[j].Key
			}
			return records[i].ID < records[j].ID
		})
		b.WriteString("\n## ")
		b.WriteString(sec.header)
		b.WriteString("\n")
		for _, r := range records {
			b.WriteString("\n### ")
			b.WriteString(r.Key)
			b.WriteString("\n\n")
			b.WriteString(r.Body)
			b.WriteString("\n")
		}
	}
	return b.String()
}
