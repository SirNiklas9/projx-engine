package core

import "fmt"

// Parse is the foundation cell: source bytes -> the abstract File model, choosing
// the normalizer by file extension. At this grain `parse` and `build-model` are a
// single step; they split into separate cells only when a language needs an
// explicit CST stage between them. Per the plan, these are plain Go functions
// first — the cell boundary is deferred until distribution actually needs it.
func Parse(path string, src []byte) (f *File, err error) {
	n, nerr := normalizerFor(path)
	if nerr != nil {
		return nil, nerr
	}
	// GUARD: a grammar not linked into this build (or a tree-sitter bug) panics
	// deep in the normalizer. Recover so ONE file degrades to a skipped parse
	// instead of taking down the whole project parse (and, before the pulp_step
	// guard, the entire cell). The caller skips files that return an error.
	defer func() {
		if r := recover(); r != nil {
			f, err = nil, fmt.Errorf("parse %s: grammar/normalizer panic recovered: %v", path, r)
		}
	}()
	return n.Normalize(path, src)
}

// SymbolByID returns the symbol with the given stable ID, if present.
func (f *File) SymbolByID(id string) (Symbol, bool) {
	for _, s := range f.Symbols {
		if s.ID == id {
			return s, true
		}
	}
	return Symbol{}, false
}
