package main

import (
	"os"
	"path/filepath"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

// gateDeniedPath delegates to the shared store matcher (one definition for every face).
func gateDeniedPath(s store.Store, path string) (pattern string, denied bool) {
	return store.GateDenied(s, path)
}

// bashShellSeps splits a shell command into candidate tokens: whitespace plus the
// metacharacters that separate words/redirections/assignments. Coarse on purpose —
// we only need the path-shaped operands, and over-tokenizing is harmless (a token
// that isn't off-limits simply doesn't match).
func bashSplit(cmd string) []string {
	return strings.FieldsFunc(cmd, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', ';', '|', '&', '<', '>', '(', ')', '=', '"', '\'', '`', ',':
			return true
		}
		return false
	})
}

// bashHitsGate scans a Bash command line for any operand that names an off-limits
// path, closing the hole where `cat .env` (a Bash call, no file_path) bypassed the
// secret gate entirely. Each token is tested in several normalized forms — as given,
// store-relative, and by basename — so `.env`, `./secret/x`, `path/to/.env`, and
// `~/.ssh/id_rsa` are all caught against `.env*`, `secret/**`, `**/*.key`, `**/.ssh/**`.
// Returns the offending token, the matched pattern, and whether it was denied.
func bashHitsGate(s store.Store, storeRoot, absRoot, cmd string) (token, pattern string, denied bool) {
	for _, tok := range bashSplit(cmd) {
		tok = strings.TrimSpace(tok)
		if tok == "" || strings.HasPrefix(tok, "-") {
			continue // flags aren't paths
		}
		for _, cand := range []string{
			gateRelPath(storeRoot, absRoot, tok),
			tok,
			filepath.Base(tok),
		} {
			if pat, hit := store.GateDenied(s, cand); hit {
				return tok, pat, true
			}
		}
	}
	return "", "", false
}

// targetStoreRoot resolves WHICH project's store governs an operation on path —
// TARGET-based, not cwd-based (adr/scope-resolution-is-target-based). It walks UP from
// the file being touched to the nearest ancestor directory that owns a ".projx"
// directory and returns that directory. So enforcement follows WHAT is being edited:
// an edit to a file inside project X fires X's project rules even when Claude runs from
// a different repo's cwd. Falls back to the cwd-resolved root (absRoot) when path is
// empty (a session-level event) or when no .projx ancestor exists (a loose file under
// the workspace, or outside any project). The GLOBAL floor still applies regardless —
// openStore always composes the per-user store on top of whichever project this returns.
func targetStoreRoot(absRoot, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return absRoot
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(absRoot, path)
	}
	// Walk up from the file's own directory looking for a .projx dir (its owning project).
	dir := filepath.Dir(abs)
	for i := 0; i < 64; i++ {
		if fi, err := os.Stat(filepath.Join(dir, ".projx")); err == nil && fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return absRoot
}

// gateRelPath expresses the tool's file_path the way the owning project's gate patterns
// are authored — relative to the store root that governs it (storeRoot). Gate globs like
// "secret/**" or ".env*" are project-relative, so a target inside a child repo must be
// matched against that child's root, not the session cwd. Falls back to the raw path when
// it can't be made relative (e.g. a different drive / outside the store root).
func gateRelPath(storeRoot, absRoot, path string) string {
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(absRoot, path)
	}
	if rel, err := filepath.Rel(storeRoot, abs); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return path
}
