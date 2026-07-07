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

// projectStore is the engine's view of the declared-knowledge store: ONE logical
// Store (store.Workspace) over TWO physical files — the per-repo PROJECT store
// (<root>/.projx/store.db, committable, shared with the Workbench) and the
// per-user YOURS store (global + workspace records that travel with you). Records
// route to the owning file by Scope.Owner(); callers see a single Store.
type projectStore struct {
	*store.Workspace
	project *store.SQLite
	yours   *store.SQLite
	space   *store.SQLite // optional workspace-level store (nil when not in a workspace)
}

// Close releases the underlying files.
func (p *projectStore) Close() error {
	e1 := p.project.Close()
	if err := p.yours.Close(); err != nil && e1 == nil {
		e1 = err
	}
	if p.space != nil {
		if err := p.space.Close(); err != nil && e1 == nil {
			e1 = err
		}
	}
	return e1
}

// yoursDir is the per-user directory for the YOURS store (global + workspace
// records). PROJX_YOURS_DIR overrides it (tests / custom home); otherwise it is
// <UserConfigDir>/projx (the same per-user root secrets already use). Empty means
// no per-user dir is available — the caller falls back to the repo's .projx.
func yoursDir() string {
	if d := strings.TrimSpace(os.Getenv("PROJX_YOURS_DIR")); d != "" {
		return d
	}
	if cfg, err := os.UserConfigDir(); err == nil {
		return filepath.Join(cfg, "projx")
	}
	return ""
}

// openStore opens the project store as a two-file Workspace: project records in
// <root>/.projx/store.db (stays with the repo, shared with the Workbench), and
// global+workspace records in the per-user yours store. The engine OWNS the
// store; every face reads this same file.
func openStore(absRoot string) *projectStore {
	projDir := filepath.Join(absRoot, ".projx")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		die("mkdir .projx: %v", err)
	}
	project, err := store.Open(filepath.Join(projDir, "store.db"))
	if err != nil {
		die("open project store: %v", err)
	}
	fallbackYours := filepath.Join(projDir, "yours.db") // project-local fallback
	yoursPath := fallbackYours
	if yd := yoursDir(); yd != "" {
		if err := os.MkdirAll(yd, 0o755); err == nil {
			yoursPath = filepath.Join(yd, "store.db")
		}
	}
	yours, err := store.Open(yoursPath)
	if err != nil {
		// The per-user YOURS store can be unreachable from inside a confined
		// agent run: Landlock (Linux) / AppContainer (Windows) scope the agent
		// to the project root, which excludes <UserConfigDir>/projx, so opening
		// it fails with SQLITE_CANTOPEN. Fall back to a project-local yours store
		// (inside the cage) so project-scope commits still persist. The agent's
		// knowledge is project knowledge anyway; global/workspace records are the
		// human's and are not the caged agent's to write.
		if yoursPath != fallbackYours {
			yours, err = store.Open(fallbackYours)
		}
		if err != nil {
			die("open yours store: %v", err)
		}
	}
	// Optional WORKSPACE level: a ".projx-workspace" folder on an ancestor of the repo
	// (a multi-repo workspace like "MonkeyLabs" with its own rules). When present,
	// workspace-scoped records compose from THERE instead of the per-user yours store.
	// Optional — a bare repo has none, and everything still composes (project + global).
	var space *store.SQLite
	var spaceIface store.Store // MUST stay a true-nil interface when absent (a nil *SQLite boxed in an interface is != nil)
	if wp := workspaceStorePath(absRoot); wp != "" {
		if s, err := store.Open(wp); err == nil {
			space, spaceIface = s, s
		}
	}
	return &projectStore{
		Workspace: store.NewComposite(yours, spaceIface, project),
		project:   project, yours: yours, space: space,
	}
}

// workspaceStorePath walks UP from absRoot for a workspace marker — a ".projx-workspace"
// directory on an ancestor folder (holding store.db). Returns the workspace store path,
// or "" if the repo isn't inside a workspace. Bounded walk; stops at the filesystem root.
func workspaceStorePath(absRoot string) string {
	dir := absRoot
	for i := 0; i < 24; i++ {
		marker := filepath.Join(dir, ".projx-workspace")
		if fi, err := os.Stat(marker); err == nil && fi.IsDir() {
			return filepath.Join(marker, "store.db")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func runStoreCmd(absRoot string, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: store <get|list|query|commit|move|rm|log|undo|revert|cherry-pick|checkout|seed>")
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
	case "move":
		storeMove(absRoot, rest)
	case "seed":
		storeSeed(absRoot, rest)
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
		fmt.Fprintf(os.Stderr, "unknown store subcommand %q (get|list|query|commit|move|rm|log|undo|revert|cherry-pick|checkout)\n", sub)
		os.Exit(1)
	}
}

// physicalFor returns the underlying single-file store that OWNS a scope's level,
// matching the composite's write routing (project→project store, workspace→the
// workspace store or your store when there is none, global→your store). `store move`
// uses this to relocate a record between physical files PRECISELY — the composite
// Delete removes an id from every level at once, so a move must delete from the
// source file directly, not through the Workspace.
func (p *projectStore) physicalFor(s store.Scope) store.Store {
	switch s.Owner() {
	case "project":
		return p.project
	case "workspace":
		if p.space != nil {
			return p.space
		}
	}
	return p.yours
}

// storeMove relocates a record to a different SCOPE without recreating it: the id,
// kind, key, and body are preserved; only Scope changes and the row physically moves
// to the file owning the new level (project store ↔ your global/workspace store). This
// is the "rebase" the store lacked — promote a project convention to global, or demote
// a global one to a single project, keeping ONE record with its history intact instead
// of delete+recreate. Human-only: enforceAgentContext refuses it under agent context
// (cross-scope promotion is the human's call; agents write project scope).
func storeMove(absRoot string, args []string) {
	// The id is the first positional; the stdlib flag parser stops at the first
	// non-flag arg, so pull the id off BEFORE parsing --to/--by from the remainder.
	if len(args) == 0 {
		die("usage: store move <id> --to <global|workspace|project>")
	}
	id := args[0]
	fs := flag.NewFlagSet("store move", flag.ExitOnError)
	toFlag := fs.String("to", "", "destination scope: global|workspace|project (required)")
	byFlag := fs.String("by", "ui", "actor: ui|agent")
	_ = fs.Parse(args[1:])
	if strings.TrimSpace(*toFlag) == "" {
		die("--to <scope> is required")
	}
	to, err := parseScopeName(*toFlag)
	if err != nil {
		die("%v", err)
	}

	st := openStore(absRoot)
	defer st.Close()

	cur, ok := st.Get(id)
	if !ok {
		fmt.Fprintf(os.Stderr, "not found: %s\n", id)
		os.Exit(1)
	}
	if cur.Scope == to {
		fmt.Printf("%s is already %s — nothing to move\n", id, to)
		return
	}
	// Refuse moving TO a workspace level that doesn't exist rather than silently
	// landing the record in your global file (the composite's fallback-up behaviour).
	if to == store.ScopeWorkspace && st.space == nil {
		die("not in a workspace (no .projx-workspace ancestor) — cannot move to workspace scope")
	}

	moved := cur
	moved.Scope = to
	dst := st.physicalFor(to)
	src := st.physicalFor(cur.Scope)
	if err := dst.Put(moved); err != nil {
		die("move: put %s: %v", id, err)
	}
	// Delete the original copy from ITS file only. Skip when source and destination
	// resolve to the same physical file (e.g. workspace↔global with no workspace store):
	// the Put above already overwrote the row in place, so deleting would drop it.
	if src != dst {
		if err := src.Delete(id); err != nil {
			die("move: delete old %s: %v", id, err)
		}
	}
	recordStoreOp(absRoot, "move", *byFlag, &cur, &moved)
	fmt.Printf("moved %s: %s → %s\n", id, cur.Scope, to)
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
	case "route":
		return store.KRoute, nil
	}
	return 0, fmt.Errorf("unknown kind %q (recipe|convention|adr|doc|history|gate-rule|declared-structure|route)", name)
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
