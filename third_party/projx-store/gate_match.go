package store

// gate_match.go — path-vs-gate matching, shared by every face (native CLI + WASM cell)
// so a path is judged off-limits identically everywhere. The gate PATTERNS come from
// GatePatterns (the store); this adds the MATCHER (one definition).

import (
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// NormGatePath normalizes a path/pattern for gate matching: backslashes→/, strip a
// leading "./" or "/", drop a trailing "/". So "./secret/x", "secret/x", and "/secret/x"
// compare the same way and a path can't dodge a rule via a prefix variant.
func NormGatePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	p = strings.TrimPrefix(p, "/")
	return strings.TrimSuffix(p, "/")
}

// GateDenied reports whether path is denied by any of the store's gate patterns,
// returning the matching pattern. Matching is doublestar glob semantics (** crosses
// path segments, * does not) — the same semantics the gate's deny globs use — so
// secret/**, **/*.key, .env*, and **/.ssh/** all match correctly (a prefix matcher
// would not). One definition for native gate checks and the cell's gate endpoint.
func GateDenied(s Store, path string) (pattern string, denied bool) {
	clean := NormGatePath(path)
	for _, pat := range GatePatterns(s) {
		if ok, err := doublestar.Match(NormGatePath(pat), clean); err == nil && ok {
			return pat, true
		}
	}
	return "", false
}
