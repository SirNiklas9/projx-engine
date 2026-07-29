package core

// Syntax highlighting straight from the tree-sitter grammar's own highlights.scm
// query — the SAME parser that powers the graph also colors the editor, so there
// is no per-language highlighting table anywhere. Whatever grammars the build
// links (selected via the gotreesitter `grammar_subset_*` build tags) are
// highlightable; adding a language to the engine automatically highlights it.

import (
	"unicode/utf16"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// HLToken is one highlighted span in UTF-16 code-unit offsets (what the browser /
// Monaco's getPositionAt consumes directly), plus the tree-sitter capture name
// ("keyword", "string", "function", "comment", …). The frontend maps capture →
// color via a small theme; it needs no language knowledge.
type HLToken struct {
	Start   int    `json:"s"` // UTF-16 code-unit offset (inclusive)
	End     int    `json:"e"` // UTF-16 code-unit offset (exclusive)
	Capture string `json:"c"`
}

// Highlight returns highlight tokens for a file. ok=false means the language is
// not recognized by the linked grammars or has no highlight query — the caller
// should fall back to the editor's built-in (Monaco) highlighting in that case.
func Highlight(path string, src []byte) (tokens []HLToken, ok bool) {
	entry := grammars.DetectLanguage(path) // by filename/extension (NOT DetectLanguageByName, which matches language names)
	if entry == nil || entry.Language == nil || entry.HighlightQuery == "" {
		return nil, false
	}
	hl, err := gts.NewHighlighter(entry.Language(), entry.HighlightQuery)
	if err != nil {
		return nil, false
	}
	units := utf16.Encode([]rune(string(src)))
	ranges := hl.HighlightUTF16(units)
	tokens = make([]HLToken, 0, len(ranges))
	for _, r := range ranges {
		if r.Capture == "" || r.EndCodeUnit <= r.StartCodeUnit {
			continue
		}
		tokens = append(tokens, HLToken{Start: int(r.StartCodeUnit), End: int(r.EndCodeUnit), Capture: r.Capture})
	}
	return tokens, true
}
