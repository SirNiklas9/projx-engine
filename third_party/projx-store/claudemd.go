package store

import "strings"

// CLAUDE.md managed-block rendering — the ONE definition shared by the engine
// (which owns the store and writes CLAUDE.md) and the Workbench cell. ProjX
// maintains a MANAGED BLOCK between the markers, rendered from the store; content
// outside the markers is the user's and is never touched.

const (
	ClaudeBegin = "<!-- PROJX:BEGIN — managed by ProjX from the project store; edits inside are overwritten -->"
	ClaudeEnd   = "<!-- PROJX:END -->"
)

// ManagedBlock renders the full managed block (markers + body) from the store.
func ManagedBlock(s Store) string {
	return ClaudeBegin + "\n" + renderManagedBody(s) + ClaudeEnd
}

// SpliceManagedBlock replaces the managed block inside existing with block,
// preserving any user content around it. An empty document gets a minimal header.
func SpliceManagedBlock(existing, block string) string {
	if strings.TrimSpace(existing) == "" {
		return "# CLAUDE.md\n\n" + block + "\n"
	}
	bi := strings.Index(existing, ClaudeBegin)
	ei := strings.Index(existing, ClaudeEnd)
	if bi >= 0 && ei > bi {
		return existing[:bi] + block + existing[ei+len(ClaudeEnd):]
	}
	sep := "\n\n"
	if strings.HasSuffix(existing, "\n") {
		sep = "\n"
	}
	return existing + sep + block + "\n"
}

func renderManagedBody(st Store) string {
	var b strings.Builder
	b.WriteString("## Project rules — managed by ProjX\n\n")
	b.WriteString("_Generated from the ProjX store. Edit gate rules / conventions in the Store pane (or via the engine), not here — this block is overwritten._\n")

	gate := dropSettings(st.List(OfKind(KGateRule)))
	b.WriteString("\n### Off-limits — do NOT read or edit\n")
	if len(gate) == 0 {
		b.WriteString("- _(no gate rules declared — nothing is currently off-limits)_\n")
	} else {
		for _, r := range gate {
			b.WriteString("- `" + strings.TrimSpace(r.Body) + "`")
			if k := strings.TrimSpace(r.Key); k != "" {
				b.WriteString("  — " + k)
			}
			b.WriteString("\n")
		}
	}

	if conv := dropSettings(st.List(OfKind(KConvention))); len(conv) > 0 {
		b.WriteString("\n### Conventions to follow\n")
		for _, r := range conv {
			line := mdOneLine(r.Body)
			if k := strings.TrimSpace(r.Key); k != "" {
				line = "**" + k + "** — " + line
			}
			b.WriteString("- " + line + "\n")
		}
	}

	if ds := dropSettings(st.List(OfKind(KDeclaredStructure))); len(ds) > 0 {
		b.WriteString("\n### Architecture (declared subsystems)\n")
		for _, r := range ds {
			name := strings.TrimPrefix(strings.TrimSpace(r.Key), "module:")
			b.WriteString("- **" + name + "** — " + mdOneLine(r.Body) + "\n")
		}
	}

	if docs := dropSettings(st.List(OfKind(KDoc))); len(docs) > 0 {
		b.WriteString("\n### Subsystem notes\n")
		for _, r := range docs {
			t := strings.TrimSpace(r.Key)
			if t == "" {
				t = "note"
			}
			b.WriteString("- **" + t + "** — " + mdOneLine(r.Body) + "\n")
		}
	}

	if adr := dropSettings(st.List(OfKind(KADR))); len(adr) > 0 {
		b.WriteString("\n### Decisions (ADRs)\n")
		for _, r := range adr {
			t := strings.TrimSpace(r.Key)
			if t == "" {
				t = "decision"
			}
			b.WriteString("- **" + t + "** — " + mdOneLine(r.Body) + "\n")
		}
	}
	return b.String()
}

// dropSettings removes setting/* records (config/secrets NEVER belong in rendered
// output) AND deduplicates by content: a rule with the same kind+body appearing in more
// than one scope (e.g. the secrets/off-limits floor seeded at BOTH global and project
// scope) is rendered ONCE. Without this, composing scopes injects the same gate rule /
// convention twice every turn (context bloat + two places that can drift). Dedup is by
// (kind, trimmed body); empty-body records are never collapsed together.
func dropSettings(recs []Record) []Record {
	out := make([]Record, 0, len(recs))
	seen := map[string]bool{}
	for _, r := range recs {
		if strings.HasPrefix(r.ID, "setting/") || strings.HasPrefix(r.Key, "setting/") {
			continue
		}
		if body := strings.TrimSpace(r.Body); body != "" {
			key := r.Kind.String() + "\x00" + body
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		out = append(out, r)
	}
	return out
}

func mdOneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	if len(s) > 220 {
		s = s[:220] + "…"
	}
	return s
}
