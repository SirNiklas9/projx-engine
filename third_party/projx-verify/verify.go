// Package verify is the forbidden-knowledge wall: it compares the architecture you
// DECLARED (boundary rules, from store) against what the code ACTUALLY does (the
// call graph, from core) and flags every divergence. No AI — deterministic; the
// diff is mechanical and you are the verdict.
package verify

import (
	"strings"

	core "github.com/SirNiklas9/projx-core"
)

// Rule is a declared boundary: a caller matching From must not call a callee
// matching To. Matching is by stable-ID prefix, so From/To may be a full symbol ID
// ("a.go::Helper"), a file ("a.go"), or a module/dir prefix ("internal/secret") —
// the rule then covers every symbol under it. Note carries the reason (e.g. an ADR
// id), surfaced when the rule is broken.
type Rule struct {
	From string `json:"from"`
	To   string `json:"to"`
	Note string `json:"note,omitempty"`
}

// Violation is one broken rule: the rule plus the offending call edge.
type Violation struct {
	Rule Rule      `json:"rule"`
	Edge core.Edge `json:"edge"`
}

// Check runs the rules against a project's ACTUAL call graph and returns every
// violation — a resolved call from a From-matching symbol to a To-matching symbol.
// Cross-file calls are included (Check reads Project.CallEdges).
func Check(rules []Rule, p *core.Project) []Violation {
	var vs []Violation
	for _, e := range p.CallEdges() {
		for _, r := range rules {
			if idMatches(e.From, r.From) && idMatches(e.To, r.To) {
				vs = append(vs, Violation{Rule: r, Edge: e})
			}
		}
	}
	return vs
}

// idMatches reports whether a stable ID is covered by a rule pattern: an exact
// match, or the ID sits under the pattern as a prefix (so a file/module pattern
// catches every symbol within it). Prefix matching requires the next character to
// be a separator ("/" for directory boundaries, ":" for the "::" file::symbol
// boundary) so that a pattern "auth" does not accidentally match "auth_test.go".
func idMatches(id, pattern string) bool {
	if pattern == "" {
		return false
	}
	if id == pattern {
		return true
	}
	if !strings.HasPrefix(id, pattern) {
		return false
	}
	next := id[len(pattern)]
	return next == '/' || next == ':'
}
