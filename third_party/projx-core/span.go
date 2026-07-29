package core

// Span is a byte range [Start, End) within a single source file. Every node and
// slot in the model carries one. It is the editor handoff (resolve to file:line
// for "jump to code") and the anchor for surgical edits and round-trip — never
// a line number, which would shift the moment code is added above it.
type Span struct {
	Start int // byte offset, inclusive
	End   int // byte offset, exclusive
}

// Len is the byte length of the span.
func (s Span) Len() int { return s.End - s.Start }

// Empty reports whether the span covers no bytes.
func (s Span) Empty() bool { return s.End <= s.Start }

// Overlaps reports whether two spans share at least one byte. The gate uses this
// to refuse a surgical regen whose target overlaps a redacted region.
func (s Span) Overlaps(o Span) bool { return s.Start < o.End && o.Start < s.End }

// Text slices src to this span. It assumes the span came from the same source;
// out-of-range spans are clamped rather than panicking.
func (s Span) Text(src []byte) string {
	a, b := s.Start, s.End
	if a < 0 {
		a = 0
	}
	if b > len(src) {
		b = len(src)
	}
	if a >= b {
		return ""
	}
	return string(src[a:b])
}
