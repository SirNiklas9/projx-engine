package core

import "sort"

// Edge is one caller→callee link in the call graph, by stable ID.
type Edge struct {
	From string // caller symbol ID
	To   string // callee symbol ID
}

// CallEdges resolves this file's intra-file call graph: a call to a name that
// matches another top-level symbol in the same file becomes an edge. Tier-1 —
// name-based, within one file (a call to `foo` resolves to this file's `foo`).
// Cross-file / cross-package resolution is a later layer; calls that don't match
// a local symbol (stdlib, imports) are simply dropped here. Edges are deduped and
// sorted for determinism.
func (f *File) CallEdges() []Edge {
	byName := make(map[string]string, len(f.Symbols)) // name → ID (ambiguous names: last wins)
	for _, s := range f.Symbols {
		byName[s.Name] = s.ID
	}

	seen := map[Edge]bool{}
	var edges []Edge
	for _, s := range f.Symbols {
		for _, callee := range s.Calls {
			id, ok := byName[callee]
			if !ok || id == s.ID { // unresolved (external) or self-call → skip
				continue
			}
			e := Edge{From: s.ID, To: id}
			if !seen[e] {
				seen[e] = true
				edges = append(edges, e)
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})
	return edges
}
