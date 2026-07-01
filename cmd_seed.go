package main

// cmd_seed.go — BAKE a declared knowledge set from an editable TOML file.
//
// `projx-engine seed apply [file]` reads a seed file (default projx.seed.toml, else
// .projx/seed.toml) and UPSERTS its records into the store, then PRUNES any prior
// seed-file records no longer present — so the FILE is the source of truth for the
// project's declared knowledge (conventions, gates, decisions, docs, model-tier routes).
// Edit the file and re-apply to re-bake. `init` applies it automatically, so a friend
// who clones the repo gets the whole rule-set with one command.
//
// `projx-engine seed export [file]` writes the store's human records BACK to a seed file
// (the auto-generated code-map is excluded), so an existing store can be captured into a
// shareable, version-controlled seed.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	store "github.com/SirNiklas9/projx-store"
)

// seedFileOrigin marks records written by `seed apply` so a re-bake can prune the ones
// dropped from the file without touching the floor seed (seed:floor) or code-map (map).
const seedFileOrigin = "seed:file"

// seedFile is the on-disk schema — arrays of typed records, human-editable.
type seedFile struct {
	Convention []seedKV    `toml:"convention"`
	Gate       []seedGate  `toml:"gate"`
	ADR        []seedKV    `toml:"adr"`
	Doc        []seedDoc   `toml:"doc"`
	Structure  []seedKV    `toml:"structure"`
	Route      []seedRoute `toml:"route"`
}

type seedKV struct {
	Key   string `toml:"key"`
	Body  string `toml:"body"`
	Scope string `toml:"scope,omitempty"` // global|workspace|project (default project)
}
type seedGate struct {
	Pattern string `toml:"pattern"`
	Key     string `toml:"key,omitempty"` // optional human label
}
type seedDoc struct {
	Key    string `toml:"key"`
	Body   string `toml:"body"`
	Anchor string `toml:"anchor,omitempty"` // "repo/file.go:line" — prepended so it survives truncation
	Scope  string `toml:"scope,omitempty"`
}
type seedRoute struct {
	Class string `toml:"class"` // cheap-fast|default|deep-reasoning
	Cmd   string `toml:"cmd"`   // launch command / model id
}

func runSeedCmd(absRoot string, args []string) {
	sub := "apply"
	rest := args
	if len(args) > 0 {
		sub, rest = args[0], args[1:]
	}
	switch sub {
	case "apply":
		applySeedFile(absRoot, seedPathArg(absRoot, rest))
	case "export":
		exportSeedFile(absRoot, seedPathArg(absRoot, rest))
	default:
		die("seed: unknown subcommand %q (want: apply, export)", sub)
	}
}

// seedPathArg resolves the seed file: an explicit arg, else projx.seed.toml, else
// .projx/seed.toml.
func seedPathArg(absRoot string, rest []string) string {
	if len(rest) > 0 && rest[0] != "" {
		return rest[0]
	}
	if p := filepath.Join(absRoot, "projx.seed.toml"); fileExists(p) {
		return p
	}
	return filepath.Join(absRoot, ".projx", "seed.toml")
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// scopeOf maps a scope word to a store.Scope (default project).
func scopeOf(s string) store.Scope {
	switch s {
	case "global":
		return store.ScopeGlobal
	case "workspace":
		return store.ScopeWorkspace
	default:
		return store.ScopeProject
	}
}

// seedFileRecords turns a parsed seed file into the desired store records.
func seedFileRecords(sf seedFile) []store.Record {
	var recs []store.Record
	add := func(kind store.Kind, key, body, scope string) {
		if key == "" && body == "" { // skip empty/malformed table entries
			return
		}
		recs = append(recs, store.Record{
			ID: kind.String() + "/" + seedSlug(key), Kind: kind, Scope: scopeOf(scope),
			Key: key, Body: body, Origin: seedFileOrigin,
		})
	}
	for _, c := range sf.Convention {
		add(store.KConvention, c.Key, c.Body, c.Scope)
	}
	for _, a := range sf.ADR {
		add(store.KADR, a.Key, a.Body, a.Scope)
	}
	for _, s := range sf.Structure {
		add(store.KDeclaredStructure, s.Key, s.Body, s.Scope)
	}
	for _, g := range sf.Gate {
		key := g.Key
		if key == "" {
			key = g.Pattern
		}
		add(store.KGateRule, key, g.Pattern, "project")
	}
	for _, d := range sf.Doc {
		body := d.Body
		if d.Anchor != "" { // anchor first so it survives the one-line index summary
			body = d.Anchor + " — " + body
		}
		add(store.KDoc, d.Key, body, d.Scope)
	}
	for _, r := range sf.Route {
		add(store.KRoute, r.Class, r.Cmd, "global")
	}
	return recs
}

// applySeedFile bakes the file into the store: upsert every record, prune prior
// seed-file records no longer present. Idempotent.
func applySeedFile(absRoot, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		die("seed apply: read %s: %v", path, err)
	}
	var sf seedFile
	if err := toml.Unmarshal(data, &sf); err != nil {
		die("seed apply: parse %s: %v", path, err)
	}
	want := seedFileRecords(sf)
	wantID := map[string]bool{}
	for _, r := range want {
		wantID[r.ID] = true
	}

	st := openStore(absRoot)
	defer st.Close()

	pruned := 0
	for _, kind := range []store.Kind{store.KConvention, store.KADR, store.KDoc, store.KGateRule, store.KRoute, store.KDeclaredStructure} {
		for _, ex := range st.List(store.OfKind(kind)) {
			if ex.Origin == seedFileOrigin && !wantID[ex.ID] {
				if st.Delete(ex.ID) == nil {
					pruned++
				}
			}
		}
	}
	for _, r := range want {
		if err := st.Put(r); err != nil {
			die("seed apply: put %s: %v", r.ID, err)
		}
	}
	fmt.Printf("seed apply: %d record(s) baked, %d pruned, from %s\n", len(want), pruned, path)
}

// exportSeedFile writes the store's HUMAN records (not the auto code-map) to a seed file.
func exportSeedFile(absRoot, path string) {
	st := openStore(absRoot)
	defer st.Close()
	var sf seedFile
	scopeStr := func(s store.Scope) string {
		switch s {
		case store.ScopeGlobal:
			return "global"
		case store.ScopeWorkspace:
			return "workspace"
		default:
			return ""
		}
	}
	take := func(kind store.Kind) []store.Record {
		recs := st.List(store.OfKind(kind))
		out := recs[:0:0]
		for _, r := range recs {
			if r.Origin == "map" || r.Key == "" || strings.HasPrefix(r.Key, "setting/") {
				continue // skip the auto code-map, empty, and internal setting/* records
			}
			out = append(out, r)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
		return out
	}
	for _, r := range take(store.KConvention) {
		sf.Convention = append(sf.Convention, seedKV{Key: r.Key, Body: r.Body, Scope: scopeStr(r.Scope)})
	}
	for _, r := range take(store.KADR) {
		sf.ADR = append(sf.ADR, seedKV{Key: r.Key, Body: r.Body, Scope: scopeStr(r.Scope)})
	}
	for _, r := range take(store.KDoc) {
		sf.Doc = append(sf.Doc, seedDoc{Key: r.Key, Body: r.Body, Scope: scopeStr(r.Scope)})
	}
	for _, r := range take(store.KGateRule) {
		sf.Gate = append(sf.Gate, seedGate{Pattern: r.Body})
	}
	for _, r := range take(store.KRoute) {
		sf.Route = append(sf.Route, seedRoute{Class: r.Key, Cmd: r.Body})
	}

	var buf bytes.Buffer
	buf.WriteString("# ProjX seed — declared knowledge for this project. Edit + `projx-engine seed apply`.\n\n")
	if err := toml.NewEncoder(&buf).Encode(sf); err != nil {
		die("seed export: encode: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		die("seed export: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		die("seed export: write %s: %v", path, err)
	}
	fmt.Printf("seed export: wrote %s\n", path)
}
