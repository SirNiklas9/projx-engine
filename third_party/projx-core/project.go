package core

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Project is a parsed tree of files — the unit graph, context, and verify all read
// (a single File is rarely enough; a call graph spans files). File paths are stored
// relative to Root and slash-normalized, so stable IDs are portable across machines.
type Project struct {
	Root  string
	Files []*File
}

// skipDir names directories ParseDir never descends into — VCS internals, dependency
// caches, and build/tooling artifacts. "runtime" + "dist" matter for ProjX's own
// distributable: it carries a bundled Go toolchain under runtime/ (whose stdlib source
// must never be parsed as project code) and build output under dist/.
var skipDir = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, ".idea": true, ".vscode": true,
	"runtime": true, "dist": true,
}

// ParseDir parses every file under root that has a registered language pack. Files
// without a pack are ignored; files that fail to read or parse are skipped and
// their paths returned, so a bad file never sinks the whole project.
func ParseDir(root string) (proj *Project, skipped []string, err error) {
	p := &Project{Root: root}
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			if path != root && (skipDir[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if _, ok := registry[strings.ToLower(filepath.Ext(path))]; !ok {
			return nil // no language pack for this extension
		}
		src, re := os.ReadFile(path)
		if re != nil {
			skipped = append(skipped, path)
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		f, pe := Parse(rel, src)
		if pe != nil {
			skipped = append(skipped, path)
			return nil
		}
		p.Files = append(p.Files, f)
		return nil
	})
	if walkErr != nil {
		return nil, skipped, walkErr
	}
	sort.Slice(p.Files, func(i, j int) bool { return p.Files[i].Path < p.Files[j].Path })
	return p, skipped, nil
}

// Symbols flattens every file's symbols into one slice.
func (p *Project) Symbols() []Symbol {
	var out []Symbol
	for _, f := range p.Files {
		out = append(out, f.Symbols...)
	}
	return out
}

// SymbolByID finds a symbol anywhere in the project by its stable ID.
func (p *Project) SymbolByID(id string) (Symbol, bool) {
	for _, f := range p.Files {
		if s, ok := f.SymbolByID(id); ok {
			return s, true
		}
	}
	return Symbol{}, false
}

// SymbolFile returns the File that owns the symbol with the given stable ID.
func (p *Project) SymbolFile(id string) (*File, bool) {
	for _, f := range p.Files {
		if _, ok := f.SymbolByID(id); ok {
			return f, true
		}
	}
	return nil, false
}

// CallEdges resolves the project-wide call graph: a callee name binds to a symbol
// in the SAME file first, else to any symbol of that name in the project. Still
// Tier-1 (name-based) — ambiguous names across files collapse to one; true
// cross-package resolution is a later layer. Deduped and sorted.
func (p *Project) CallEdges() []Edge {
	global := make(map[string]string)
	for _, f := range p.Files {
		for _, s := range f.Symbols {
			global[s.Name] = s.ID
		}
	}

	seen := map[Edge]bool{}
	var edges []Edge
	for _, f := range p.Files {
		local := make(map[string]string, len(f.Symbols))
		for _, s := range f.Symbols {
			local[s.Name] = s.ID
		}
		for _, s := range f.Symbols {
			for _, callee := range s.Calls {
				id, ok := local[callee]
				if !ok {
					id, ok = global[callee]
				}
				if !ok || id == s.ID {
					continue
				}
				e := Edge{From: s.ID, To: id}
				if !seen[e] {
					seen[e] = true
					edges = append(edges, e)
				}
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
