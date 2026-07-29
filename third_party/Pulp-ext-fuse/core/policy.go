package core

import "strings"

// Access level for a path.
type Access int

const (
	None      Access = iota
	Read      Access = iota
	ReadWrite Access = iota
)

// Rule maps a path prefix to an access level.
type Rule struct {
	Prefix string
	Access Access
}

// Policy holds an ordered set of rules; longest-prefix match wins.
type Policy struct {
	Rules []Rule
}

// Check returns the Access for relpath using longest-prefix match; default None.
func (p Policy) Check(relpath string) Access {
	best := -1
	bestLen := -1
	for i, r := range p.Rules {
		// An empty prefix is a catch-all (the project root rule): it matches every
		// path. Longer prefixes still win, so a gate like "secret" overrides it.
		if r.Prefix == "" || r.Prefix == relpath || strings.HasPrefix(relpath, r.Prefix+"/") {
			if len(r.Prefix) > bestLen {
				bestLen = len(r.Prefix)
				best = i
			}
		}
	}
	if best < 0 {
		return None
	}
	return p.Rules[best].Access
}

// Hooks are optional callbacks invoked on every operation.
type Hooks struct {
	// Audit is called for every op. nil = noop. Must be non-blocking.
	Audit func(op, relpath string, allowed bool)
	// OnMiss is called when Policy.Check returns less than needed.
	// It may return an elevated Access to grant (live "request access").
	// nil = deny (return None).
	OnMiss func(relpath string, want Access) Access
}
