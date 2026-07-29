package core

// digest.go — the CONTEXT DIGEST: a compact, navigable index of a project's
// declarations. For each top-level symbol it produces name + kind + one-line
// signature + leading doc-comment + a `path:line` anchor, sourced from the symbol
// span and the file bytes. This is the Tier-1 "code map" the context engine injects
// as an INDEX (not full bodies): the agent reads the signature, then JUMPS to the
// anchor instead of grepping. Language-neutral — it reads only the abstract model
// (Symbol.Span) plus the raw bytes, so every registered language gets a digest.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// SymbolDigest is one navigable declaration: identity + a one-line signature, its
// leading doc-comment (collapsed, "" if none), and a `path:line` anchor for "jump
// to code". It is the unit the smart-context engine stores (anchor in the record
// body) and slices per task.
type SymbolDigest struct {
	ID        string  // stable symbol ID (path::Name / path::Recv.Name)
	Name      string  // declared name
	Kind      SymKind // func / method / type / const / var
	Recv      string  // receiver type for methods; "" otherwise
	Signature string  // one-line declaration header (no body)
	Doc       string  // leading doc-comment, collapsed to one line; "" if none
	Anchor    string  // "path:line" — 1-based line of the symbol's first byte
	// Terms are distinctive words from the symbol's BODY — the names it CALLS plus its
	// string literals — so a concept that lives inside a differently-named function
	// (e.g. "webhook" inside a func named Setup) is still findable. Deterministic
	// "auto-seeding": no model, no manual doc. Bounded by digestTermsCap words.
	Terms string
	// Calls are the RAW callee names this symbol's body invokes (deduped, may include
	// a package/receiver qualifier like "pkg.Fn" or "recv.Method" as the parser found
	// them — NOT resolved to symbol IDs). This is the raw material for an approximate,
	// name-matched blast-radius/call-graph; a precise, type-aware call graph is out of
	// scope here (that's a job for a dedicated resolver). Bounded by digestTermsCap.
	Calls []string
}

// digestDocCap bounds a collapsed doc-comment; digestSigCap the signature; digestTermsCap
// the number of distinct body terms — so no single symbol bloats the index.
const (
	digestDocCap   = 160
	digestSigCap   = 200
	digestTermsCap = 40
)

// strLitRe matches string literals across the supported languages: double-quoted,
// backtick (Go raw), and single-quoted (JS/Python). Their CONTENT is a source of
// searchable concept words (route paths, log tags, error text).
var strLitRe = regexp.MustCompile("\"(?:[^\"\\\\]|\\\\.)*\"|`[^`]*`|'(?:[^'\\\\]|\\\\.)*'")

// FileDigest builds the digest for one parsed file given its source bytes. Pure and
// deterministic; symbols are returned in source order (by span start). src MUST be
// the same bytes the File was parsed from (spans index into it).
func FileDigest(f *File, src []byte) []SymbolDigest {
	if f == nil {
		return nil
	}
	out := make([]SymbolDigest, 0, len(f.Symbols))
	for _, s := range f.Symbols {
		out = append(out, SymbolDigest{
			ID:        s.ID,
			Name:      s.Name,
			Kind:      s.Kind,
			Recv:      s.Recv,
			Signature: signatureOf(s, src),
			Doc:       docAbove(src, s.Span.Start),
			Anchor:    f.Path + ":" + itoa(lineAt(src, s.Span.Start)),
			Terms:     bodyTerms(s, src),
			Calls:     dedupCalls(s.Calls),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Anchor < out[j].Anchor })
	return out
}

// DigestDir parses every supported file under root and returns the whole project's
// digest. It re-reads each file's bytes (ParseDir does not retain them) so spans
// resolve to signatures/anchors. Unreadable/​unparsable files are skipped, their
// paths returned — a bad file never sinks the digest.
func DigestDir(root string) (digests []SymbolDigest, skipped []string, err error) {
	proj, skipped, err := ParseDir(root)
	if err != nil {
		return nil, skipped, err
	}
	for _, f := range proj.Files {
		src, re := os.ReadFile(filepath.Join(root, filepath.FromSlash(f.Path)))
		if re != nil {
			skipped = append(skipped, f.Path)
			continue
		}
		digests = append(digests, FileDigest(f, src)...)
	}
	return digests, skipped, nil
}

// bodyTerms returns distinctive words from a symbol's body: the names it CALLS (already
// parsed into Symbol.Calls) plus the contents of its string literals, distilled to
// distinct lowercase alphanumeric words of length >=3, capped at digestTermsCap. This is
// the deterministic Level-1 auto-seed: it makes a concept findable even when it only
// appears inside a differently-named function.
func bodyTerms(s Symbol, src []byte) string {
	var raw strings.Builder
	for _, c := range s.Calls {
		raw.WriteString(c)
		raw.WriteByte(' ')
	}
	if s.Body != nil { // only funcs/methods have a body span worth scanning
		for _, lit := range strLitRe.FindAllString(s.Span.Text(src), -1) {
			if len(lit) >= 2 {
				raw.WriteString(lit[1 : len(lit)-1]) // strip the quotes
				raw.WriteByte(' ')
			}
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(raw.String()), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}) {
		if len(w) < 3 || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
		if len(out) >= digestTermsCap {
			break
		}
	}
	return strings.Join(out, " ")
}

// dedupCalls returns the distinct, non-empty raw callee names in first-seen order,
// capped at digestTermsCap so a god-function's call list can't bloat the index.
func dedupCalls(calls []string) []string {
	if len(calls) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, c := range calls {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
		if len(out) >= digestTermsCap {
			break
		}
	}
	return out
}

// signatureOf extracts a one-line declaration header: the symbol's span text up to
// the first body opener ('{') or line break, whitespace collapsed. For a func that
// is `func (r R) Name(args) ret`; for a type, `type T struct`; for a const/var, the
// declaration line. Bounded by digestSigCap.
func signatureOf(s Symbol, src []byte) string {
	text := s.Span.Text(src)
	if i := strings.IndexByte(text, '{'); i >= 0 {
		text = text[:i]
	} else if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	return truncRunes(collapseWS(text), digestSigCap)
}

// docAbove returns the contiguous run of comment lines immediately preceding the
// line that byte offset `start` falls on, collapsed into a single line with markers
// stripped. "" when there is no leading comment. Recognizes line comments (// and #)
// and block-comment continuations (* / /* ... */) so it works across languages.
func docAbove(src []byte, start int) string {
	if start > len(src) {
		start = len(src)
	}
	// Walk to the start of the symbol's own line.
	ls := start
	for ls > 0 && src[ls-1] != '\n' {
		ls--
	}
	// Collect preceding comment lines, moving upward.
	var rev []string
	pos := ls
	for pos > 0 {
		nl := pos - 1 // index of the '\n' that ends the previous line
		bs := nl
		for bs > 0 && src[bs-1] != '\n' {
			bs--
		}
		line := strings.TrimSpace(string(src[bs:nl]))
		if !isCommentLine(line) {
			break
		}
		rev = append(rev, stripCommentMarker(line))
		pos = bs
	}
	if len(rev) == 0 {
		return ""
	}
	// Reverse into source order, drop empties, collapse, truncate.
	parts := make([]string, 0, len(rev))
	for i := len(rev) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(rev[i]); t != "" {
			parts = append(parts, t)
		}
	}
	return truncRunes(collapseWS(strings.Join(parts, " ")), digestDocCap)
}

// isCommentLine reports whether a trimmed line is a comment (line or block form).
func isCommentLine(line string) bool {
	switch {
	case strings.HasPrefix(line, "//"), strings.HasPrefix(line, "#"),
		strings.HasPrefix(line, "/*"), strings.HasPrefix(line, "*"):
		return true
	default:
		return false
	}
}

// stripCommentMarker removes leading/trailing comment punctuation from one line.
func stripCommentMarker(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimSuffix(line, "*/")
	for _, p := range []string{"///", "//", "/*", "*", "#"} {
		if strings.HasPrefix(line, p) {
			line = line[len(p):]
			break
		}
	}
	return strings.TrimSpace(line)
}

// collapseWS replaces every run of whitespace with a single space and trims ends.
func collapseWS(s string) string { return strings.Join(strings.Fields(s), " ") }

// truncRunes truncates s to at most n runes, appending an ellipsis when cut.
func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// lineAt returns the 1-based line number that byte offset off falls on.
func lineAt(src []byte, off int) int {
	if off > len(src) {
		off = len(src)
	}
	line := 1
	for i := 0; i < off; i++ {
		if src[i] == '\n' {
			line++
		}
	}
	return line
}

// itoa is a tiny dependency-free int->string for anchors.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
