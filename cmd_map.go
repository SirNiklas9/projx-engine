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
}

// runMapCmd dispatches `map <sync|list>`.
func runMapCmd(absRoot string, args []string) {
	sub := "sync"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "sync":
		runMapSync(absRoot)
	case "list":
		runMapList(absRoot)
	default:
		die("map: unknown subcommand %q (want: sync, list)", sub)
	}
}

// runMapSync re-parses the project and upserts one declared-structure record per
// symbol, pruning map records whose symbol no longer exists. Idempotent.
func runMapSync(absRoot string) {
	digests, skipped, err := core.DigestDir(absRoot)
	if err != nil {
		die("map sync: parse: %v", err)
	}
	st := openStore(absRoot)
	defer st.Close()

	// Build the desired record set.
	want := make(map[string]store.Record, len(digests))
	for _, d := range digests {
		r := mapRecordFor(d)
		want[r.ID] = r
	}

	// Prune stale map records (same Origin, no longer wanted).
	pruned := 0
	for _, ex := range st.List(store.OfKind(store.KDeclaredStructure)) {
		if ex.Origin == mapRecordOrigin {
			if _, keep := want[ex.ID]; !keep {
				if err := st.Delete(ex.ID); err == nil {
					pruned++
				}
			}
		}
	}

	// Upsert the desired set.
	written := 0
	for _, r := range want {
		if err := st.Put(r); err != nil {
			die("map sync: put %s: %v", r.ID, err)
		}
		written++
	}
	fmt.Printf("map sync: %d symbol(s) indexed, %d pruned", written, pruned)
	if len(skipped) > 0 {
		fmt.Printf(", %d file(s) skipped", len(skipped))
	}
	fmt.Println()
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
// is a lowercase `code/<path>/<name>` path so the task-slicer matches it by token.
func mapRecordFor(d core.SymbolDigest) store.Record {
	name := d.Name
	if d.Recv != "" {
		name = d.Recv + "." + name
	}
	path := d.Anchor
	if i := strings.LastIndexByte(path, ':'); i >= 0 {
		path = path[:i] // strip ":line"
	}
	base := strings.TrimSuffix(path, filepath.Ext(path))
	key := "code/" + strings.ToLower(base) + "/" + strings.ToLower(name)

	body, _ := json.Marshal(mapAnchorBody{
		Kind:      d.Kind.String(),
		Signature: d.Signature,
		Doc:       d.Doc,
		Anchor:    d.Anchor,
	})
	return store.Record{
		ID:     "map:" + d.ID,
		Kind:   store.KDeclaredStructure,
		Scope:  store.ScopeProject,
		Key:    key,
		Body:   string(body),
		Origin: mapRecordOrigin,
	}
}
