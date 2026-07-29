package core

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseDirCrossFile(t *testing.T) {
	dir := t.TempDir()
	// Two Go files where b.go calls a function defined in a.go — a cross-FILE edge
	// that single-file CallEdges can't see. Plus a Python file and an unsupported
	// file, to prove pack-filtering and multi-language projects.
	writeFile(t, dir, "a.go", "package p\n\nfunc Helper() int { return 1 }\n")
	writeFile(t, dir, "b.go", "package p\n\nfunc Use() int { return Helper() }\n")
	writeFile(t, dir, "thing.py", "def thing():\n    return helper2()\n\ndef helper2():\n    return 2\n")
	writeFile(t, dir, "readme.md", "# not code\n")          // unsupported ext → ignored
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, ".git/config", "package p\nfunc Sneaky() {}\n") // must be skipped

	proj, skipped, err := ParseDir(dir)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	if len(skipped) != 0 {
		t.Errorf("unexpected skipped: %v", skipped)
	}
	// 2 go + 1 py = 3 files; .md ignored, .git skipped.
	if len(proj.Files) != 3 {
		paths := []string{}
		for _, f := range proj.Files {
			paths = append(paths, f.Path)
		}
		t.Fatalf("Files = %v, want 3 (a.go, b.go, thing.py)", paths)
	}
	if _, ok := proj.SymbolByID(".git/config::Sneaky"); ok {
		t.Error(".git was not skipped — Sneaky leaked in")
	}

	// Cross-file edge: b.go::Use → a.go::Helper (Helper is defined in a different file).
	wantCross := Edge{From: "b.go::Use", To: "a.go::Helper"}
	// Intra-file edge in Python: thing.py::thing → thing.py::helper2.
	wantPy := Edge{From: "thing.py::thing", To: "thing.py::helper2"}

	edges := proj.CallEdges()
	has := func(e Edge) bool {
		for _, g := range edges {
			if g == e {
				return true
			}
		}
		return false
	}
	if !has(wantCross) {
		t.Errorf("missing cross-file edge %+v in %v", wantCross, edges)
	}
	if !has(wantPy) {
		t.Errorf("missing python edge %+v in %v", wantPy, edges)
	}
}

func TestParseDirSymbols(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package p\nfunc A() {}\n")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "sub/b.go", "package q\nfunc B() {}\n")

	proj, _, err := ParseDir(dir)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	// Stable IDs use the slash-normalized relative path, so nested files are fine.
	if _, ok := proj.SymbolByID("a.go::A"); !ok {
		t.Error("missing a.go::A")
	}
	if _, ok := proj.SymbolByID("sub/b.go::B"); !ok {
		t.Errorf("missing sub/b.go::B (symbols: %d files)", len(proj.Files))
	}
}
