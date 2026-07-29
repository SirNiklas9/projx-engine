package core

import "testing"

// TestFileDigest proves the digest extracts a one-line signature, the leading
// doc-comment, and a path:line anchor for funcs, methods, types, and consts.
func TestFileDigest(t *testing.T) {
	src := []byte(`package x

import "fmt"

// Adder sums two ints.
// It is pure.
func Adder(a, b int) int {
	return a + b
}

// Point is a 2-D point.
type Point struct {
	X, Y int
}

// Dist returns the L1 distance from the origin.
func (p Point) Dist() int {
	return p.X + p.Y
}

const Pi = 3
`)
	f, err := Parse("geo/x.go", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	dg := FileDigest(f, src)

	byName := map[string]SymbolDigest{}
	for _, d := range dg {
		byName[d.Name] = d
	}

	adder, ok := byName["Adder"]
	if !ok {
		t.Fatal("Adder not in digest")
	}
	if adder.Signature != "func Adder(a, b int) int" {
		t.Errorf("Adder signature = %q", adder.Signature)
	}
	if adder.Doc != "Adder sums two ints. It is pure." {
		t.Errorf("Adder doc = %q", adder.Doc)
	}
	if adder.Anchor != "geo/x.go:7" {
		t.Errorf("Adder anchor = %q, want geo/x.go:7", adder.Anchor)
	}

	pt, ok := byName["Point"]
	if !ok {
		t.Fatal("Point not in digest")
	}
	// Signature is the raw span header (language-neutral); the `type` keyword lives
	// in the Kind field, so the render layer composes "<kind> <signature>".
	if pt.Signature != "Point struct" {
		t.Errorf("Point signature = %q", pt.Signature)
	}
	if pt.Kind != SymType {
		t.Errorf("Point kind = %v, want type", pt.Kind)
	}
	if pt.Anchor != "geo/x.go:12" {
		t.Errorf("Point anchor = %q, want geo/x.go:12", pt.Anchor)
	}

	dist, ok := byName["Dist"]
	if !ok {
		t.Fatal("Dist (method) not in digest")
	}
	if dist.Kind != SymMethod || dist.Recv != "Point" {
		t.Errorf("Dist kind/recv = %v/%q, want method/Point", dist.Kind, dist.Recv)
	}
	if dist.Signature != "func (p Point) Dist() int" {
		t.Errorf("Dist signature = %q", dist.Signature)
	}
	if dist.Doc != "Dist returns the L1 distance from the origin." {
		t.Errorf("Dist doc = %q", dist.Doc)
	}

	pi, ok := byName["Pi"]
	if !ok {
		t.Fatal("Pi (const) not in digest")
	}
	if pi.Doc != "" {
		t.Errorf("Pi should have no doc, got %q", pi.Doc)
	}
	if pi.Signature != "Pi = 3" {
		t.Errorf("Pi signature = %q", pi.Signature)
	}
	if pi.Anchor != "geo/x.go:21" {
		t.Errorf("Pi anchor = %q, want geo/x.go:21", pi.Anchor)
	}
}

// TestDigestHelpers covers the line/signature/doc primitives directly.
func TestDigestHelpers(t *testing.T) {
	src := []byte("a\nbb\nccc\n")
	if got := lineAt(src, 0); got != 1 {
		t.Errorf("lineAt(0)=%d want 1", got)
	}
	if got := lineAt(src, 2); got != 2 { // first byte of "bb"
		t.Errorf("lineAt(2)=%d want 2", got)
	}
	if got := lineAt(src, 5); got != 3 { // first byte of "ccc"
		t.Errorf("lineAt(5)=%d want 3", got)
	}
	if got := collapseWS("  a\t b\n c  "); got != "a b c" {
		t.Errorf("collapseWS=%q", got)
	}
	if got := stripCommentMarker("// hello"); got != "hello" {
		t.Errorf("stripCommentMarker //=%q", got)
	}
	if got := stripCommentMarker("# hello"); got != "hello" {
		t.Errorf("stripCommentMarker #=%q", got)
	}
	if got := truncRunes("abcdef", 3); got != "abc…" {
		t.Errorf("truncRunes=%q", got)
	}
}
