package broker

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// trySymlink creates a symlink, skipping the test if the OS forbids it (Windows
// without Developer Mode / elevation). Mirrors the skip pattern in restrictive_test.go.
func trySymlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation may require elevated privileges on Windows")
		}
		t.Fatalf("symlink: %v", err)
	}
}

// TestWriteThroughSymlinkedAncestorDenied is the regression test for the hole found in
// review: a symlinked ANCESTOR directory inside the root, escaping to outside, must not
// let a write to a NOT-YET-EXISTENT leaf land outside the root. A naive
// EvalSymlinks(whole-path) errors on the missing leaf and would wrongly allow it.
func TestWriteThroughSymlinkedAncestorDenied(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir() // a sibling temp dir, outside root

	link := filepath.Join(root, "escape") // root/escape -> outside
	trySymlink(t, outside, link)

	b, err := NewRestrictiveBroker([]string{"git"}, root, nil)
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}

	// Leaf does not exist yet — the classic "create a file via a symlinked dir" escape.
	target := filepath.Join(link, "newfile.txt")
	d := b.Check(Action{Kind: "write", Target: target})
	if d.Allow {
		t.Fatalf("write through symlinked ancestor to outside root was ALLOWED (escape): %q reason=%q", target, d.Reason)
	}
}

// TestReadThroughSymlinkedAncestorDenied is the read-side counterpart.
func TestReadThroughSymlinkedAncestorDenied(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	link := filepath.Join(root, "escape")
	trySymlink(t, outside, link)

	b, err := NewRestrictiveBroker([]string{"git"}, root, nil)
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}

	target := filepath.Join(link, "secret.txt")
	d := b.Check(Action{Kind: "read", Target: target})
	if d.Allow {
		t.Fatalf("read through symlinked ancestor to outside root was ALLOWED (escape): %q reason=%q", target, d.Reason)
	}
}

// TestWriteThroughSymlinkedAncestorStayingInsideAllowed proves the fix does not
// over-deny: a symlinked ancestor that resolves to a path STILL inside the root must
// remain allowed, even with a non-existent leaf.
func TestWriteThroughSymlinkedAncestorStayingInsideAllowed(t *testing.T) {
	root := t.TempDir()

	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(root, "alias") // root/alias -> root/real (both inside root)
	trySymlink(t, realDir, link)

	b, err := NewRestrictiveBroker([]string{"git"}, root, nil)
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}

	target := filepath.Join(link, "newfile.txt")
	d := b.Check(Action{Kind: "write", Target: target})
	if !d.Allow {
		t.Fatalf("write via in-root symlink alias was wrongly DENIED: %q reason=%q", target, d.Reason)
	}
}

// TestResolveDeepestAncestorReappendsMissingLeaf verifies the core mechanism of the
// symlink fix WITHOUT needing symlink privilege, so it runs on every OS: for a path
// whose leaf components don't exist, it must resolve the deepest existing ancestor and
// re-append the missing components literally. (If this used EvalSymlinks(whole-path) it
// would error on the missing leaf and the symlinked-ancestor escape would slip through.)
func TestResolveDeepestAncestorReappendsMissingLeaf(t *testing.T) {
	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("evalsymlinks root: %v", err)
	}

	// None of a/b/c exist under root.
	target := filepath.Join(root, "a", "b", "c")
	got := resolveDeepestAncestor(target)
	want := filepath.Join(resolvedRoot, "a", "b", "c")
	if got != want {
		t.Fatalf("resolveDeepestAncestor(%q) = %q, want %q", target, got, want)
	}

	// An existing path resolves to its symlink-evaluated form.
	if got := resolveDeepestAncestor(root); got != filepath.Clean(resolvedRoot) {
		t.Fatalf("resolveDeepestAncestor(existing %q) = %q, want %q", root, got, filepath.Clean(resolvedRoot))
	}
}
