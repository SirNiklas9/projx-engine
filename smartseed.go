package main

// smartseed.go — the "smart init" seed: on `init`, scan the project and auto-seed a
// rich starting store DETERMINISTICALLY (parse config files, NO LLM calls), the way
// Claude Code's /init generates a comprehensive CLAUDE.md — but committed as store
// knowledge, not a loose .md.
//
// It seeds, over and above the universal floor (profiles.go / store.SeedFloor):
//   - recipes — the project's build/test/run commands, parsed from Makefile targets,
//     package.json scripts, and the Go toolchain (go build/test);
//   - gate rules — the off-limits floor (.env*, keys, secrets, ssh) if a store predates
//     the floor gates and is missing them;
//   - conventions — obvious "how this repo is worked" rules read off config files
//     (package manager, CI system, containerization, formatter/linter/typecheck);
//   - architecture — a high-level overview doc + one declared-structure record per
//     top-level source module + a doc of key entrypoints (main/init), BEYOND the raw
//     per-symbol map that `map sync` already produces.
//
// IDEMPOTENT: every write is skipped when a record with that ID already exists (and
// gate rules are matched by pattern too), so re-running `init` never duplicates or
// clobbers a hand-edited record.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

// smartSeedOrigin tags every record the smart seed writes, distinct from the floor
// ("seed:floor"), the per-stack profiles ("seed:<stack>"), and the code map ("map").
const smartSeedOrigin = "seed:smart"

// smartSkipDirs are directories the architecture scan never treats as source modules.
var smartSkipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "dist": true, "build": true,
	"target": true, "testdata": true, "tmp": true, "out": true,
	".git": true, ".idea": true, ".vscode": true, ".projx": true, ".claude": true,
}

// smartOffLimits is the off-limits floor the smart seed guarantees. It mirrors the
// store floor gates so a store that predates them still ends up protected.
var smartOffLimits = []profileRec{
	{"dotenv files", ".env*"},
	{"private keys", "**/*.key"},
	{"secrets dir", "secret/**"},
	{"ssh material", "**/.ssh/**"},
}

// smartSeed scans absRoot and seeds recipes, off-limits gates, and an architecture
// overview into st. Returns the number of records written and one summary line per
// category for the init log. Best-effort: unreadable files are skipped, never fatal.
func smartSeed(st store.Store, absRoot string) (int, []string) {
	// existing gate PATTERNS (bodies) so we never seed a duplicate off-limits rule
	// under a different key/ID than the floor already used.
	haveGate := map[string]bool{}
	for _, g := range st.List(store.OfKind(store.KGateRule)) {
		haveGate[strings.TrimSpace(g.Body)] = true
	}

	n := 0
	var notes []string

	// put writes rec only if its ID is not already present — the idempotency guard.
	put := func(kind store.Kind, key, body string) bool {
		id := kind.String() + "/" + seedSlug(key)
		if _, ok := st.Get(id); ok {
			return false
		}
		rec := store.Record{
			ID: id, Kind: kind, Scope: store.ScopeProject,
			Key: key, Body: body, Origin: smartSeedOrigin,
		}
		if st.Put(rec) == nil {
			n++
			return true
		}
		return false
	}

	// 1. Recipes — build/test/run commands.
	added := 0
	for _, r := range detectRecipes(absRoot) {
		if put(store.KRecipe, r.Key, r.Body) {
			added++
		}
	}
	if added > 0 {
		notes = append(notes, fmt.Sprintf("%d build/test/run recipe(s)", added))
	}

	// 2. Off-limits gates — ensure the floor set is present even on a pre-floor store.
	added = 0
	for _, g := range smartOffLimits {
		if haveGate[g.Body] {
			continue
		}
		if put(store.KGateRule, g.Key, g.Body) {
			haveGate[g.Body] = true
			added++
		}
	}
	if added > 0 {
		notes = append(notes, fmt.Sprintf("%d off-limits gate(s)", added))
	}

	// 3. Conventions — obvious, high-signal project conventions read off config files
	// (package manager, CI, containerization, formatter/linter, typecheck). These are
	// the "how this repo is worked" rules an agent should follow, committed as real
	// KConvention records (idempotent via put's ID guard) — beyond the per-stack floor.
	added = 0
	for _, c := range detectConventions(absRoot) {
		if put(store.KConvention, c.Key, c.Body) {
			added++
		}
	}
	if added > 0 {
		notes = append(notes, fmt.Sprintf("%d convention(s)", added))
	}

	// 4. Architecture — overview doc + per-module declared-structure + entrypoints doc.
	arch := 0
	overview, modules, entrypoints := scanArchitecture(absRoot)
	if overview != "" && put(store.KDoc, "architecture/overview", overview) {
		arch++
	}
	for _, m := range modules {
		if put(store.KDeclaredStructure, "module:"+m.name, m.body) {
			arch++
		}
	}
	if entrypoints != "" && put(store.KDoc, "architecture/entrypoints", entrypoints) {
		arch++
	}
	if arch > 0 {
		notes = append(notes, fmt.Sprintf("%d architecture record(s)", arch))
	}

	return n, notes
}

// detectRecipes parses the project's build/test/run commands from its config files.
func detectRecipes(absRoot string) []profileRec {
	var out []profileRec
	seenKey := map[string]bool{}
	add := func(key, body string) {
		if key == "" || body == "" || seenKey[key] {
			return
		}
		seenKey[key] = true
		out = append(out, profileRec{Key: key, Body: body})
	}

	// Makefile targets -> `make <target>`.
	for _, name := range []string{"Makefile", "makefile", "GNUmakefile"} {
		p := filepath.Join(absRoot, name)
		if data, err := os.ReadFile(p); err == nil {
			for _, t := range makeTargets(string(data)) {
				add("make-"+t, "make "+t)
			}
			break
		}
	}

	// package.json scripts -> `<runner> run <name>`.
	if data, err := os.ReadFile(filepath.Join(absRoot, "package.json")); err == nil {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(data, &pkg) == nil {
			runner := nodeRunner(absRoot)
			names := make([]string, 0, len(pkg.Scripts))
			for name := range pkg.Scripts {
				names = append(names, name)
			}
			sort.Strings(names) // deterministic order
			for _, name := range names {
				add(runner+"-"+name, runner+" run "+name)
			}
		}
	}

	// Go toolchain -> canonical build/test (the module has no declared scripts).
	if _, err := os.Stat(filepath.Join(absRoot, "go.mod")); err == nil {
		add("go-build", "go build ./...")
		add("go-test", "go test ./...")
	}

	return out
}

// detectConventions reads OBVIOUS, unambiguous conventions off the repo's config
// files — the package manager in force, the CI system, containerization, and the
// formatter/linter/typecheck toolchain — so a freshly-init'd store already declares
// "how this repo is worked" instead of a bare floor. Deterministic and shallow (only
// stats/reads marker files at the root); returns KConvention profileRecs, de-duped by
// key. Idempotency is handled by smartSeed's put (an existing ID is skipped).
func detectConventions(absRoot string) []profileRec {
	var out []profileRec
	seenKey := map[string]bool{}
	add := func(key, body string) {
		if key == "" || body == "" || seenKey[key] {
			return
		}
		seenKey[key] = true
		out = append(out, profileRec{Key: key, Body: body})
	}
	has := func(rel string) bool { return fileExists(filepath.Join(absRoot, rel)) }

	// Package manager (Node): the lockfile that is present is the one to use.
	if has("package.json") {
		switch {
		case has("pnpm-lock.yaml"):
			add("package manager", "Node: use pnpm (pnpm-lock.yaml is present) — not npm or yarn.")
		case has("yarn.lock"):
			add("package manager", "Node: use yarn (yarn.lock is present).")
		case has("bun.lockb"):
			add("package manager", "Node: use bun (bun.lockb is present).")
		case has("package-lock.json"):
			add("package manager", "Node: use npm (package-lock.json is present).")
		}
	}

	// CI system — keep the pipeline green.
	switch {
	case dirHasFiles(filepath.Join(absRoot, ".github", "workflows")):
		add("ci pipeline", "CI runs on GitHub Actions (.github/workflows). A change that breaks CI is not done — keep the pipeline green.")
	case has(".gitlab-ci.yml"):
		add("ci pipeline", "CI runs on GitLab CI (.gitlab-ci.yml). Keep the pipeline green before merging.")
	case has(".circleci/config.yml"):
		add("ci pipeline", "CI runs on CircleCI (.circleci/config.yml). Keep the pipeline green before merging.")
	}

	// Containerization.
	if has("Dockerfile") || has("docker-compose.yml") || has("docker-compose.yaml") || has("compose.yaml") {
		add("containerized build", "Containerized: this project builds/runs via Docker (Dockerfile/compose present).")
	}

	// Editor / formatting config.
	if has(".editorconfig") {
		add("editor formatting", "Formatting is governed by .editorconfig — respect its indentation and end-of-line rules.")
	}

	// JS/TS formatter + linter + typecheck.
	if has(".prettierrc") || has(".prettierrc.json") || has(".prettierrc.js") || has(".prettierrc.yaml") || has(".prettierrc.yml") || has("prettier.config.js") {
		add("js formatting", "JS/TS formatting via Prettier — run it before committing.")
	}
	if has(".eslintrc") || has(".eslintrc.json") || has(".eslintrc.js") || has(".eslintrc.cjs") || has(".eslintrc.yaml") || has("eslint.config.js") || has("eslint.config.mjs") {
		add("js linting", "Linting via ESLint — keep lint clean before declaring done.")
	}
	if has("tsconfig.json") {
		add("typescript typecheck", "TypeScript project (tsconfig.json) — typecheck (`tsc --noEmit` or the build) before declaring done.")
	}

	// Python lint/format.
	if has("ruff.toml") || has(".ruff.toml") || pyprojectHasTool(absRoot, "ruff") {
		add("python lint", "Python linting/formatting via Ruff — run `ruff check` (and `ruff format`) before declaring done.")
	}
	if has(".flake8") {
		add("python lint", "Python linting via flake8 (.flake8) — keep it clean before declaring done.")
	}
	if pyprojectHasTool(absRoot, "black") {
		add("python formatting", "Python formatting via Black — run it before committing.")
	}

	// Rust format/lint.
	if has("rustfmt.toml") || has(".rustfmt.toml") {
		add("rust formatting", "Rust formatting via rustfmt (rustfmt.toml) — run `cargo fmt` before declaring done.")
	}
	if has("clippy.toml") || has(".clippy.toml") {
		add("rust lint", "Rust linting via Clippy — run `cargo clippy` and keep it warning-free.")
	}

	return out
}

// dirHasFiles reports whether dir exists and contains at least one regular file
// (used to detect a populated CI directory like .github/workflows).
func dirHasFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			return true
		}
	}
	return false
}

// pyprojectHasTool reports whether pyproject.toml declares a [tool.<name>] section —
// a cheap, dependency-free way to detect a configured Python tool (ruff/black/…).
func pyprojectHasTool(absRoot, name string) bool {
	data, err := os.ReadFile(filepath.Join(absRoot, "pyproject.toml"))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "[tool."+name)
}

// makeTargets extracts real, invocable rule names from a Makefile: skips comments,
// variable assignments, pattern rules (`%`), special targets (`.PHONY`, dot-leading),
// and recipe/indented lines.
func makeTargets(src string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(src, "\n") {
		// a rule is `target:` at column 0 (recipes are TAB-indented, so skip indented).
		if line == "" || line[0] == '\t' || line[0] == ' ' || line[0] == '#' {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		// `target := value` / `target ::= value` are assignments, not rules.
		if colon+1 < len(line) && line[colon+1] == '=' {
			continue
		}
		name := strings.TrimSpace(line[:colon])
		// reject anything with spaces or meta chars, and special dot-targets.
		if name == "" || strings.ContainsAny(name, " \t%$(){}=") || name[0] == '.' {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// nodeRunner picks the package manager from the lockfile present (defaults to npm).
func nodeRunner(absRoot string) string {
	switch {
	case fileExists(filepath.Join(absRoot, "pnpm-lock.yaml")):
		return "pnpm"
	case fileExists(filepath.Join(absRoot, "yarn.lock")):
		return "yarn"
	case fileExists(filepath.Join(absRoot, "bun.lockb")):
		return "bun"
	default:
		return "npm"
	}
}

// archModule is one top-level source directory summarised for the graph.
type archModule struct {
	name string
	body string
}

// scanArchitecture derives a high-level architecture summary from the tree: a one-line
// overview, one record per top-level source module, and a list of entrypoints (main/init
// packages, package.json main/bin). Deterministic; reads only shallow structure.
func scanArchitecture(absRoot string) (overview string, modules []archModule, entrypoints string) {
	entries, err := os.ReadDir(absRoot)
	if err != nil {
		return "", nil, ""
	}

	stacks := detectStacks(absRoot)
	var topDirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || smartSkipDirs[name] {
			continue
		}
		sub := filepath.Join(absRoot, name)
		langs, files := moduleShape(sub)
		if files == 0 {
			continue // no source files -> not a code module (docs/assets dir)
		}
		topDirs = append(topDirs, name)
		desc := "top-level directory"
		if len(langs) > 0 {
			desc = strings.Join(langs, "/") + " sources"
		}
		modules = append(modules, archModule{
			name: name,
			body: fmt.Sprintf("%s — %d source file(s)", desc, files),
		})
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].name < modules[j].name })
	sort.Strings(topDirs)

	// Entrypoints: Go main packages + package.json main/bin.
	eps := goEntrypoints(absRoot)
	eps = append(eps, nodeEntrypoints(absRoot)...)
	sort.Strings(eps)
	eps = dedupStrings(eps)

	// Overview line.
	var b strings.Builder
	if len(stacks) > 0 {
		b.WriteString("Stack: " + strings.Join(stacks, ", ") + ". ")
	}
	if len(topDirs) > 0 {
		b.WriteString("Top-level source dirs: " + strings.Join(topDirs, ", ") + ". ")
	} else {
		b.WriteString("Flat layout (sources at the repo root). ")
	}
	if len(eps) > 0 {
		b.WriteString("Entrypoints: " + strings.Join(eps, ", ") + ".")
	}
	overview = strings.TrimSpace(b.String())

	if len(eps) > 0 {
		entrypoints = "Key entrypoints (main/init): " + strings.Join(eps, ", ")
	}
	return overview, modules, entrypoints
}

// moduleShape reports the languages present in dir (one level deep) and a source-file
// count, so the scan can tell a code module from a docs/assets directory.
func moduleShape(dir string) (langs []string, files int) {
	langSet := map[string]bool{}
	// walk shallowly (this dir + one nested level) to keep it cheap on large trees.
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p == dir {
				return nil
			}
			if smartSkipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			// limit depth: skip anything more than one level below dir.
			rel, _ := filepath.Rel(dir, p)
			if strings.Count(rel, string(filepath.Separator)) >= 1 {
				return filepath.SkipDir
			}
			return nil
		}
		if lang := langOf(d.Name()); lang != "" {
			langSet[lang] = true
			files++
		}
		return nil
	})
	for l := range langSet {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	return langs, files
}

// langOf maps a filename to a coarse language label, or "" for non-source files.
func langOf(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go":
		return "Go"
	case ".ts", ".tsx":
		return "TypeScript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "JavaScript"
	case ".astro":
		return "Astro"
	case ".odin":
		return "Odin"
	case ".py":
		return "Python"
	case ".rs":
		return "Rust"
	default:
		return ""
	}
}

// goEntrypoints finds Go `package main` files that declare `func main(` (the buildable
// commands), relative to absRoot, skipping vendored/hidden trees and tests.
func goEntrypoints(absRoot string) []string {
	var out []string
	_ = filepath.WalkDir(absRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p == absRoot {
				return nil
			}
			if smartSkipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(d.Name()) != ".go" || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		src := string(data)
		if strings.Contains(src, "\npackage main") || strings.HasPrefix(src, "package main") {
			if strings.Contains(src, "func main(") {
				if rel, e := filepath.Rel(absRoot, p); e == nil {
					out = append(out, filepath.ToSlash(rel))
				}
			}
		}
		return nil
	})
	return out
}

// nodeEntrypoints reads package.json main/bin as declared entrypoints.
func nodeEntrypoints(absRoot string) []string {
	data, err := os.ReadFile(filepath.Join(absRoot, "package.json"))
	if err != nil {
		return nil
	}
	var pkg struct {
		Main string          `json:"main"`
		Bin  json.RawMessage `json:"bin"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return nil
	}
	var out []string
	if pkg.Main != "" {
		out = append(out, filepath.ToSlash(pkg.Main))
	}
	// bin is either a string or an object of name->path.
	if len(pkg.Bin) > 0 {
		var s string
		if json.Unmarshal(pkg.Bin, &s) == nil && s != "" {
			out = append(out, filepath.ToSlash(s))
		} else {
			var m map[string]string
			if json.Unmarshal(pkg.Bin, &m) == nil {
				for _, v := range m {
					out = append(out, filepath.ToSlash(v))
				}
			}
		}
	}
	return out
}
