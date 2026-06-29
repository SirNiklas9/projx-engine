package main

import (
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	store "github.com/SirNiklas9/projx-store"
)

// normGatePath normalizes a path/pattern for gate matching: backslashes→/, strip
// leading "./" and "/", drop a trailing "/". Mirrors the normalization used by
// projx-context's gate so "./secret/x", "secret/x", and "/secret/x" compare the
// same way and a path can't dodge a rule via a prefix variant.
func normGatePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	p = strings.TrimPrefix(p, "/")
	return strings.TrimSuffix(p, "/")
}

// gateDeniedPath reports whether path is denied by any of the store's gate
// patterns, returning the matching pattern. Matching is doublestar glob semantics
// (** crosses path segments, * does not) — the same semantics the gate's
// Read()/Edit() deny globs are consumed under — so secret/**, **/*.key, .env*,
// and **/.ssh/** all match correctly (a path-prefix matcher would not). The
// pattern set itself comes from store.GatePatterns (single source).
func gateDeniedPath(s store.Store, path string) (pattern string, denied bool) {
	clean := normGatePath(path)
	for _, pat := range store.GatePatterns(s) {
		ok, err := doublestar.Match(normGatePath(pat), clean)
		if err == nil && ok {
			return pat, true
		}
	}
	return "", false
}
