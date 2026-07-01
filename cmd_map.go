package main

// cmd_map.go — the CODE MAP (step 6: graph-map / anchors).
//
// `map sync` parses the project with projx-core and materializes a navigable index
// of its declarations into the STORE as declared-structure records — one per symbol,
// carrying the signature, doc-comment, and a `path:line` anchor in the record Body
// (JSON). Because they are ordinary store records, the Step-5 machinery carries them
// for free: a task that mentions a symbol pulls only THAT symbol's record (task-slice),
// and an unchanged symbol is not re-injected (delta). The agent reads the signature +
// anchor and JUMPS to the code instead of grepping.
//
// We ADOPT projx-core's parser (the single source of symbol truth) — no reimplementation.
// The records are Origin="map" so a re-sync can prune stale entries without touching
// human/agent-authored knowledge.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	core "github.com/SirNiklas9/projx-core"
	store "github.com/SirNiklas9/projx-store"
)

// mapRecordOrigin marks store records owned by `map sync` so re-syncs can prune
// stale ones without disturbing human/agent-authored knowledge.
const mapRecordOrigin = "map"

// mapAnchorBody is the JSON shape stored in a code-map record's Body: enough for the
// agent to understand the symbol and jump to it without opening the whole file.
//
// Field ORDER matters: the record renders into the injected context as an INDEX line
// whose one-line summary is the head of this body (truncated). Anchor + signature come
// FIRST so the "jump to" target and the shape survive truncation; the doc tail (least
// critical, fetched in full via `store get`) is what gets cut if anything does.
type mapAnchorBody struct {
	Anchor    string `json:"anchor"` // "path:line"
	Signature string `json:"signature"`
	Kind      string `json:"kind"`
	Doc       string `json:"doc,omitempty"`
	// Terms are distinctive body words (calls + string literals) so a concept buried in
	// a differently-named function is still matched — deterministic Level-1 auto-seed.
	Terms string `json:"terms,omitempty"`
}

// runMapCmd dispatches `map <sync|list>`.
func runMapCmd(absRoot string, args []string) {
	sub := "sync"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "sync":
		runMapSync(absRoot, args[1:]) // extra args = additional repo source dirs (multi-repo workspace)
	case "list":
		runMapList(absRoot)
	default:
		die("map: unknown subcommand %q (want: sync, list)", sub)
	}
}

// runMapSync is the CLI face: it runs syncMap and prints a summary. Extra srcs index
// ADDITIONAL repos into this root's store (a multi-repo workspace map), with each
// symbol's anchor/key prefixed by its repo so cross-repo jumps stay unambiguous.
func runMapSync(absRoot string, srcs []string) {
	written, pruned, skipped, err := syncMap(absRoot, srcs)
	if err != nil {
		die("map sync: %v", err)
	}
	fmt.Printf("map sync: %d symbol(s) indexed, %d pruned", written, pruned)
	if len(skipped) > 0 {
		fmt.Printf(", %d file(s) skipped", len(skipped))
	}
	fmt.Println()
}

// syncMap re-parses each source repo and upserts one declared-structure record per
// symbol into absRoot's store, pruning map records whose symbol no longer exists.
// srcs == nil → the single project at absRoot (no repo prefix). Multiple srcs → a
// workspace map spanning them, each symbol prefixed by its repo (basename of the src).
// Idempotent and print-free (shared by the CLI and the SessionStart hook).
func syncMap(absRoot string, srcs []string) (written, pruned int, skipped []string, err error) {
	// Workspace resolution: explicit srcs DECLARE the workspace (persisted); with no
	// srcs, re-use the declared workspace — so the SessionStart refresh indexes the
	// member repos, not the empty root (which would prune the whole map).
	prefixed := false
	switch {
	case len(srcs) > 0:
		saveWorkspaceSrcs(absRoot, srcs)
		prefixed = true
	default:
		if ws := loadWorkspaceSrcs(absRoot); len(ws) > 0 {
			srcs = ws
			prefixed = true
		} else {
			srcs = []string{absRoot}
		}
	}

	st := openStore(absRoot)
	defer st.Close()

	want := make(map[string]store.Record)
	for _, src := range srcs {
		absSrc, aerr := filepath.Abs(src)
		if aerr != nil {
			continue
		}
		repo := ""
		if prefixed {
			repo = filepath.Base(absSrc)
		}
		digests, sk, derr := core.DigestDir(absSrc)
		if derr != nil {
			return written, pruned, append(skipped, sk...), fmt.Errorf("parse %s: %w", src, derr)
		}
		skipped = append(skipped, sk...)
		for _, d := range digests {
			r := mapRecordFor(d, repo)
			want[r.ID] = r
		}
	}

	for _, ex := range st.List(store.OfKind(store.KDeclaredStructure)) {
		if ex.Origin == mapRecordOrigin {
			if _, keep := want[ex.ID]; !keep {
				if delErr := st.Delete(ex.ID); delErr == nil {
					pruned++
				}
			}
		}
	}
	for _, r := range want {
		if putErr := st.Put(r); putErr != nil {
			return written, pruned, skipped, fmt.Errorf("put %s: %w", r.ID, putErr)
		}
		written++
	}
	return written, pruned, skipped, nil
}

// runMapList prints the current code-map records (id + key + anchor).
func runMapList(absRoot string) {
	st := openStore(absRoot)
	defer st.Close()
	n := 0
	for _, r := range st.List(store.OfKind(store.KDeclaredStructure)) {
		if r.Origin != mapRecordOrigin {
			continue
		}
		var b mapAnchorBody
		_ = json.Unmarshal([]byte(r.Body), &b)
		fmt.Printf("%s\t%s\t%s\n", r.Key, b.Signature, b.Anchor)
		n++
	}
	if n == 0 {
		fmt.Println("map list: no code-map records (run `map sync`)")
	}
}

// mapRecordFor renders one symbol digest into a stable declared-structure record.
// The ID is derived from the symbol's stable ID (re-sync upserts in place); the Key
// is a lowercase `code/<path>/<name>` path so the task-slicer matches it by token. A
// non-empty repo prefixes the ID/key/anchor so cross-repo entries stay distinct and
// jumpable (e.g. anchor "Evolution/handlers/auth.go:12").
func mapRecordFor(d core.SymbolDigest, repo string) store.Record {
	name := d.Name
	if d.Recv != "" {
		name = d.Recv + "." + name
	}
	anchor := d.Anchor
	id := "map:" + d.ID
	if repo != "" {
		anchor = repo + "/" + anchor
		id = "map:" + repo + "/" + d.ID
	}
	path := anchor
	if i := strings.LastIndexByte(path, ':'); i >= 0 {
		path = path[:i] // strip ":line"
	}
	base := strings.TrimSuffix(path, filepath.Ext(path))
	key := "code/" + strings.ToLower(base) + "/" + strings.ToLower(name)

	body, _ := json.Marshal(mapAnchorBody{
		Kind:      d.Kind.String(),
		Signature: d.Signature,
		Doc:       d.Doc,
		Anchor:    anchor,
		Terms:     d.Terms,
	})
	return store.Record{
		ID:     id,
		Kind:   store.KDeclaredStructure,
		Scope:  store.ScopeProject,
		Key:    key,
		Body:   string(body),
		Origin: mapRecordOrigin,
	}
}
