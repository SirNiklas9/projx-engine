package main

// impact.go — blast-radius: "who calls X, transitively, and what breaks if I change
// it." Built from the code-map's per-symbol Calls list (raw callee names the parser
// found — see projx-core SymbolDigest.Calls). This is a NAME-MATCHED, APPROXIMATE
// call graph: a callee name is matched against the bare (unqualified) name of every
// indexed symbol, so it can't resolve dynamic dispatch or distinguish two same-named
// methods on different types. A precise, type-aware call graph is out of scope here —
// that's a job for a dedicated resolver (e.g. an external index). This stays
// self-contained: no new dependency, works in the cage, floats like everything else.

import (
	"encoding/json"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

// ImpactHit is one symbol in a target's blast radius.
type ImpactHit struct {
	Name   string `json:"name"`   // bare symbol name (Recv.Name for methods)
	Anchor string `json:"anchor"` // "path:line" (repo-prefixed in a multi-repo workspace)
	Key    string `json:"key"`    // the store key, for a follow-up `store get`
	Depth  int    `json:"depth"`  // 1 = direct caller, 2 = caller-of-caller, ...
}

const (
	impactDefaultDepth = 3
	impactMaxResults   = 200 // safety cap on very hot symbols (e.g. a logger)
)

// mapSymbol is one parsed code-map record — the BFS node.
type mapSymbol struct {
	name  string
	key   string
	anch  string
	calls []string
}

// computeImpact returns the blast radius of target (a bare or dotted symbol name):
// every indexed symbol whose Calls list reaches it, direct first, then transitively,
// up to maxDepth hops (<=0 uses impactDefaultDepth). truncated reports whether the
// impactMaxResults cap cut the walk short.
func computeImpact(st store.Store, target string, maxDepth int) (hits []ImpactHit, truncated bool) {
	if maxDepth <= 0 {
		maxDepth = impactDefaultDepth
	}
	syms := loadMapSymbols(st)

	// byCallee: bare callee name -> indices of symbols that call it. Built once, O(n).
	byCallee := map[string][]int{}
	for i, s := range syms {
		for _, c := range s.calls {
			bare := bareCalleeName(c)
			if bare == "" {
				continue
			}
			byCallee[bare] = append(byCallee[bare], i)
		}
	}

	visited := map[string]bool{} // by symbol name, so a symbol is reported once at its shallowest depth
	frontier := []string{bareCalleeName(target)}
	for depth := 1; depth <= maxDepth && len(frontier) > 0 && len(hits) < impactMaxResults; depth++ {
		var next []string
		for _, name := range frontier {
			for _, idx := range byCallee[name] {
				caller := syms[idx]
				if visited[caller.name] || caller.name == "" {
					continue
				}
				visited[caller.name] = true
				hits = append(hits, ImpactHit{Name: caller.name, Anchor: caller.anch, Key: caller.key, Depth: depth})
				next = append(next, caller.name)
				if len(hits) >= impactMaxResults {
					truncated = true
					break
				}
			}
			if truncated {
				break
			}
		}
		frontier = next
	}
	return hits, truncated
}

// loadMapSymbols parses every code-map record in the store into the BFS node shape.
// Non-map / malformed records are skipped (best-effort — a bad record never crashes it).
func loadMapSymbols(st store.Store) []mapSymbol {
	var out []mapSymbol
	for _, r := range st.List(store.OfKind(store.KDeclaredStructure)) {
		if !strings.HasPrefix(r.ID, "map:") {
			continue
		}
		var b mapAnchorBody
		if json.Unmarshal([]byte(r.Body), &b) != nil {
			continue
		}
		out = append(out, mapSymbol{name: b.Name, key: r.Key, anch: b.Anchor, calls: b.Calls})
	}
	return out
}

// bareCalleeName strips a call reference down to its unqualified name for matching:
// drops a trailing "()" if present, then takes the segment after the last '.' — so
// "pkg.Fn", "recv.Method()", and "Fn" all normalize to "Fn"/"Method". This is the
// APPROXIMATION: two different types' same-named methods collide here on purpose
// (a precise resolver would need type information this index doesn't have).
func bareCalleeName(call string) string {
	call = strings.TrimSpace(call)
	call = strings.TrimSuffix(call, "()")
	if i := strings.LastIndexByte(call, '.'); i >= 0 {
		call = call[i+1:]
	}
	return strings.TrimSpace(call)
}
