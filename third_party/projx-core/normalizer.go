package core

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// File is the result of normalizing one source file: the language-neutral symbol
// list (shallow tier) plus the file-level Root node. Everything above core — gloss,
// graph, context, verify — reads File, never a language's native tree.
type File struct {
	Path    string
	Lang    string
	Symbols []Symbol
	Root    *Node
}

// Normalizer turns one language's source into the abstract model. THIS is the
// agnostic seam: the Go normalizer (lang_go.go) is the first concrete impl;
// tree-sitter-backed normalizers slot in here for other languages behind the same
// interface. core stays language-neutral; only the normalizers know a grammar.
type Normalizer interface {
	// Lang is a human label, e.g. "go".
	Lang() string
	// Normalize parses src (the bytes of the file at path) into the abstract model.
	Normalize(path string, src []byte) (*File, error)
}

// registry maps a lowercased file extension (with dot, e.g. ".go") to its Normalizer.
var registry = map[string]Normalizer{}

// Register associates one or more extensions with a Normalizer. Concrete language
// packages call this from an init() so importing them wires the language in.
func Register(n Normalizer, exts ...string) {
	for _, e := range exts {
		registry[strings.ToLower(e)] = n
	}
}

// normalizerFor picks a Normalizer by the file's extension.
func normalizerFor(path string) (Normalizer, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if n, ok := registry[ext]; ok {
		return n, nil
	}
	return nil, fmt.Errorf("core: no normalizer for %q (ext %q) — register one or add a grammar", path, ext)
}

// Languages returns the registered language labels, sorted — handy for "what can
// the engine read?" without exposing the registry.
func Languages() []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range registry {
		if l := n.Lang(); !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}
