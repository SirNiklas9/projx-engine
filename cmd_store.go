package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

// openStore opens (or creates) the project store at <root>/.projx/store.db.
func openStore(absRoot string) *store.SQLite {
	dir := filepath.Join(absRoot, ".projx")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		die("mkdir .projx: %v", err)
	}
	st, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		die("open store: %v", err)
	}
	return st
}

func runStoreCmd(absRoot string, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: store <get|list|query|commit|rm|log|undo|revert|cherry-pick|checkout>")
		os.Exit(1)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "get":
		storeGet(absRoot, rest)
	case "list":
		storeList(absRoot, rest)
	case "query":
		storeQuery(absRoot, rest)
	case "commit":
		storeCommit(absRoot, rest)
	case "rm":
		storeRm(absRoot, rest)
	case "log":
		storeLog(absRoot, rest)
	case "undo":
		storeUndo(absRoot, rest)
	case "revert":
		storeRevert(absRoot, rest)
	case "cherry-pick":
		storeCherryPick(absRoot, rest)
	case "checkout":
		storeCheckout(absRoot, rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown store subcommand %q (get|list|query|commit|rm|log|undo|revert|cherry-pick|checkout)\n", sub)
		os.Exit(1)
	}
}

func storeGet(absRoot string, args []string) {
	if len(args) == 0 {
		die("usage: store get <id>")
	}
	id := args[0]
	st := openStore(absRoot)
	defer st.Close()
	r, ok := st.Get(id)
	if !ok {
		fmt.Fprintf(os.Stderr, "not found: %s\n", id)
		os.Exit(1)
	}
	printRecord(r)
}

func storeList(absRoot string, args []string) {
	fs := flag.NewFlagSet("store list", flag.ExitOnError)
	kindFlag := fs.String("kind", "", "filter by kind name")
	scopeFlag := fs.String("scope", "", "filter by scope: global|workspace|project")
	_ = fs.Parse(args)

	f := store.Filter{}
	if *kindFlag != "" {
		k, err := parseKindForList(*kindFlag)
		if err != nil {
			die("%v", err)
		}
		f.Kind = &k
	}
	if *scopeFlag != "" {
		s, err := parseScopeName(*scopeFlag)
		if err != nil {
			die("%v", err)
		}
		f.Scope = &s
	}

	st := openStore(absRoot)
	defer st.Close()
	for _, r := range st.List(f) {
		fmt.Printf("%s\t[%s/%s]\t%s\t%s\n", r.ID, r.Kind.String(), r.Scope.String(), r.Key, oneLine(r.Body))
	}
}

// storeQuery implements `store query` — selective read with optional filters.
// All flags are optional; no flags → all records (same as store list).
// --kind and --scope use the same enum parsing as store list.
// --key and --text are case-insensitive substring matches on Key and Body respectively.
func storeQuery(absRoot string, args []string) {
	fs := flag.NewFlagSet("store query", flag.ExitOnError)
	kindFlag := fs.String("kind", "", "filter by kind name")
	scopeFlag := fs.String("scope", "", "filter by scope: global|workspace|project")
	keyFlag := fs.String("key", "", "case-insensitive substring match on record key")
	textFlag := fs.String("text", "", "case-insensitive substring match on record body")
	_ = fs.Parse(args)

	f := store.Filter{}
	if *kindFlag != "" {
		k, err := parseKindForList(*kindFlag)
		if err != nil {
			die("%v", err)
		}
		f.Kind = &k
	}
	if *scopeFlag != "" {
		s, err := parseScopeName(*scopeFlag)
		if err != nil {
			die("%v", err)
		}
		f.Scope = &s
	}

	keyLower := strings.ToLower(*keyFlag)
	textLower := strings.ToLower(*textFlag)

	st := openStore(absRoot)
	defer st.Close()
	for _, r := range st.List(f) {
		if keyLower != "" && !strings.Contains(strings.ToLower(r.Key), keyLower) {
			continue
		}
		if textLower != "" && !strings.Contains(strings.ToLower(r.Body), textLower) {
			continue
		}
		fmt.Printf("%s\t[%s/%s]\t%s\t%s\n", r.ID, r.Kind.String(), r.Scope.String(), r.Key, oneLine(r.Body))
	}
}

func storeCommit(absRoot string, args []string) {
	fs := flag.NewFlagSet("store commit", flag.ExitOnError)
	kindFlag := fs.String("kind", "", "kind: convention|adr|doc|declared-structure|gate-rule")
	keyFlag := fs.String("key", "", "key / short title (required)")
	bodyFlag := fs.String("body", "", "body text")
	scopeFlag := fs.String("scope", "project", "scope: project|global|workspace")
	idFlag := fs.String("id", "", "record id (derived from kind/slug(key) if omitted)")
	byFlag := fs.String("by", "ui", "actor: ui|agent")
	_ = fs.Parse(args)

	// In agent context, always treat the actor as "agent" regardless of --by.
	// This ensures agentWritableKind enforcement fires even if --by was omitted
	// or passed as "ui" by the caller.
	effectiveBy := *byFlag
	if os.Getenv("PROJX_AGENT_CONTEXT") == "1" {
		effectiveBy = "agent"
	}

	if strings.TrimSpace(*kindFlag) == "" {
		die("--kind is required")
	}
	if strings.TrimSpace(*keyFlag) == "" {
		die("--key is required")
	}

	k, err := parseKindForCommit(*kindFlag, effectiveBy)
	if err != nil {
		die("%v", err)
	}

	sc, err := parseScopeName(*scopeFlag)
	if err != nil {
		die("%v", err)
	}

	recID := *idFlag
	if recID == "" {
		recID = strings.ToLower(*kindFlag) + "/" + slug(*keyFlag)
	}

	st := openStore(absRoot)
	defer st.Close()

	var bp *store.Record
	if before, had := st.Get(recID); had {
		bp = &before
	}

	rec := store.Record{ID: recID, Kind: k, Scope: sc, Key: *keyFlag, Body: *bodyFlag}
	if err := st.Put(rec); err != nil {
		die("put: %v", err)
	}
	recordStoreOp(absRoot, "put", effectiveBy, bp, &rec)
	fmt.Println("committed", recID)
}

func storeRm(absRoot string, args []string) {
	fs := flag.NewFlagSet("store rm", flag.ExitOnError)
	byFlag := fs.String("by", "ui", "actor: ui|agent")
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) == 0 {
		die("usage: store rm <id> [--by ui|agent]")
	}
	id := rest[0]

	st := openStore(absRoot)
	defer st.Close()

	before, had := st.Get(id)
	if !had {
		fmt.Fprintf(os.Stderr, "not found: %s\n", id)
		os.Exit(1)
	}

	if *byFlag == "agent" && !agentWritableKind(before.Kind) {
		die("record %q is kind %q — human-only, agent cannot remove it", id, before.Kind.String())
	}

	if err := st.Delete(id); err != nil {
		die("delete: %v", err)
	}
	recordStoreOp(absRoot, "delete", *byFlag, &before, nil)
	fmt.Println("removed", id)
}

func storeLog(absRoot string, _ []string) {
	revs := readRevisions(absRoot)
	if len(revs) == 0 {
		fmt.Println("(no history)")
		return
	}
	// print newest-first; show refSeq annotation for meta-ops
	for i := len(revs) - 1; i >= 0; i-- {
		rev := revs[i]
		ref := ""
		switch rev.Op {
		case "undo":
			if rev.UndoOf > 0 {
				ref = fmt.Sprintf(" [undoes #%d]", rev.UndoOf)
			}
		case "revert", "cherry-pick":
			if rev.RefSeq > 0 {
				ref = fmt.Sprintf(" [refs #%d]", rev.RefSeq)
			}
		}
		fmt.Printf("#%-4d %s  %-12s  %s  %s  (%s)%s\n",
			rev.Seq, rev.Time, rev.Op, rev.Kind, rev.Key, rev.By, ref)
	}
}

func storeUndo(absRoot string, _ []string) {
	st := openStore(absRoot)
	defer st.Close()
	rev, ok := undoLastStore(absRoot, st)
	if !ok {
		fmt.Println("nothing to undo")
		return
	}
	fmt.Printf("undid #%d: %s %s (%s)\n", rev.Seq, rev.Op, rev.ID, rev.Kind)
}

// parseSeqArg parses a required positional <seq> integer argument.
func parseSeqArg(cmd string, args []string) int {
	if len(args) == 0 {
		die("usage: store %s <seq>", cmd)
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n < 1 {
		die("store %s: <seq> must be a positive integer, got %q", cmd, args[0])
	}
	return n
}

func storeRevert(absRoot string, args []string) {
	targetSeq := parseSeqArg("revert", args)
	st := openStore(absRoot)
	defer st.Close()
	newRev, err := revertRevision(absRoot, st, targetSeq)
	if err != nil {
		die("revert: %v", err)
	}
	fmt.Printf("reverted rev #%d → new rev #%d\n", targetSeq, newRev.Seq)
}

func storeCherryPick(absRoot string, args []string) {
	targetSeq := parseSeqArg("cherry-pick", args)
	st := openStore(absRoot)
	defer st.Close()
	newRev, err := cherryPickRevision(absRoot, st, targetSeq)
	if err != nil {
		die("cherry-pick: %v", err)
	}
	fmt.Printf("cherry-picked rev #%d → new rev #%d\n", targetSeq, newRev.Seq)
}

func storeCheckout(absRoot string, args []string) {
	targetSeq := parseSeqArg("checkout", args)
	recs, err := checkoutState(absRoot, targetSeq)
	if err != nil {
		die("checkout: %v", err)
	}
	fmt.Printf("historical view at rev #%d (store unchanged):\n", targetSeq)
	if len(recs) == 0 {
		fmt.Println("  (no records at this point in history)")
		return
	}
	for _, r := range recs {
		fmt.Printf("  %s\t[%s/%s]\t%s\t%s\n", r.ID, r.Kind.String(), r.Scope.String(), r.Key, oneLine(r.Body))
	}
}

// parseKindForCommit parses the kind for a commit, enforcing agent restrictions.
func parseKindForCommit(name, by string) (store.Kind, error) {
	k, err := parseKindForList(name)
	if err != nil {
		return 0, err
	}
	if by == "agent" && !agentWritableKind(k) {
		return 0, fmt.Errorf("kind %q is human-only — agent cannot write it", name)
	}
	return k, nil
}

// parseKindForList parses a kind name for listing/reading (no write restrictions).
func parseKindForList(name string) (store.Kind, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "recipe":
		return store.KRecipe, nil
	case "convention":
		return store.KConvention, nil
	case "adr":
		return store.KADR, nil
	case "doc":
		return store.KDoc, nil
	case "history":
		return store.KHistory, nil
	case "gate-rule":
		return store.KGateRule, nil
	case "declared-structure", "module":
		return store.KDeclaredStructure, nil
	}
	return 0, fmt.Errorf("unknown kind %q (recipe|convention|adr|doc|history|gate-rule|declared-structure)", name)
}

// parseScopeName parses a scope string.
func parseScopeName(name string) (store.Scope, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "global":
		return store.ScopeGlobal, nil
	case "workspace":
		return store.ScopeWorkspace, nil
	case "project":
		return store.ScopeProject, nil
	}
	return 0, fmt.Errorf("unknown scope %q (global|workspace|project)", name)
}

// printRecord prints a record to stdout.
func printRecord(r store.Record) {
	fmt.Printf("id:    %s\nkind:  %s\nscope: %s\nkey:   %s\nbody:  %s\n", r.ID, r.Kind, r.Scope, r.Key, r.Body)
}

// oneLine returns the first line of s, trimmed to ~80 chars.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	return s
}

// slug turns a key into an id-safe slug.
func slug(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
