package main

// cmd_statusline.go — `projx-engine statusline` : a one-line ProjX badge for the
// Claude Code status bar. Claude Code runs the configured statusLine command on
// each render and paints its stdout at the bottom of the screen; it passes a small
// JSON payload on stdin ({session_id, cwd, workspace:{current_dir}, …}). This
// command reads that, resolves the ProjX scope, and prints a compact colored badge
// so the human can SEE, at a glance, when ProjX is engaged and in what state —
// distinct from plain harness activity.
//
// FLOATING scope: the badge does NOT just reflect the session's cwd. ProjX's scope
// follows what is being TOUCHED — so as any agent edits/reads files in a project,
// the hook records that project as the active scope and the badge leads with it,
// updating turn-to-turn. It falls back to the cwd's project only before anything
// has been touched this session.
//
// Contract: this runs every render and its stdout IS the status line, so it must be
// FAST and can NEVER hard-fail. Any error degrades to a minimal badge (or nothing),
// never a stack trace in the status bar.

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// absPathRe matches a Windows absolute path inside free text (e.g. a path named in a
// prompt: "fix C:\Users\...\Sessions\src\x.astro"). Used to probe which project a
// prompt is about BEFORE any file is touched. Windows-only on purpose: a bare POSIX
// "/foo/bar" pattern in prose produces too many false positives ("and/or", URLs).
var absPathRe = regexp.MustCompile(`[A-Za-z]:[\\/][^\s"'` + "`" + `]+`)

// activeContextRoot resolves WHICH project's context should be injected for a turn —
// ProjX's floating scope applied to context, not just the badge. Priority:
//  1. a project named by an explicit path in the prompt (probe-from-prompt: the human
//     told us where the work is before we touch anything);
//  2. the project of the last file any agent touched this session (floated scope);
//  3. the session's own cwd project (the default).
//
// Always returns a directory; callers open the store there (openStore composes the
// global floor over it, so law travels regardless of which project is active).
func activeContextRoot(absRoot, sid, prompt string) string {
	if p := firstProjectInPrompt(absRoot, prompt); p != "" {
		return p
	}
	home := targetStoreRoot(absRoot, filepath.Join(absRoot, "_"))
	if sid != "" && isProjxDir(home) {
		if c, ok := readStatusCrumb(home, sid); ok && c.R != "" && isProjxDir(c.R) {
			return c.R
		}
	}
	return absRoot
}

// firstProjectInPrompt returns the ProjX project owning the first absolute path named
// in the prompt, or "" if none resolves to a project.
func firstProjectInPrompt(absRoot, prompt string) string {
	if prompt == "" {
		return ""
	}
	m := strings.TrimSpace(absPathRe.FindString(prompt))
	if m == "" {
		return ""
	}
	if tr := targetStoreRoot(absRoot, m); isProjxDir(tr) {
		return tr
	}
	return ""
}

// ANSI helpers. Claude Code renders ANSI SGR in the status line. Kept as 256-color
// codes so the palette is stable across terminals (indigo accent, semantic hues).
const (
	slReset  = "\x1b[0m"
	slDim    = "\x1b[2m"
	slBold   = "\x1b[1m"
	slAccent = "\x1b[38;5;111m" // indigo/blue — ProjX mark
	slGreen  = "\x1b[38;5;71m"  // healthy / delegated
	slAmber  = "\x1b[38;5;179m" // dispatcher / attention
	slRed    = "\x1b[38;5;167m" // cage / hard block
)

// statuslinePayload is the subset of the Claude Code statusLine stdin we read.
type statuslinePayload struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Workspace struct {
		CurrentDir string `json:"current_dir"`
	} `json:"workspace"`
}

// runStatuslineCmd reads the payload from stdin and prints the badge to stdout.
// Best-effort throughout: it recovers from any panic and prints a minimal badge so
// a bug here can never blank or break the user's status line.
func runStatuslineCmd(absRoot string, _ []string) {
	defer func() {
		if r := recover(); r != nil {
			os.Stdout.WriteString(slDim + "◇ projx" + slReset)
		}
	}()

	data, _ := io.ReadAll(os.Stdin)
	var p statuslinePayload
	_ = json.Unmarshal(data, &p) // tolerate empty/garbage → fall back to absRoot

	cwd := strings.TrimSpace(p.Cwd)
	if cwd == "" {
		cwd = strings.TrimSpace(p.Workspace.CurrentDir)
	}
	if cwd == "" {
		cwd = absRoot
	}
	if a, err := filepath.Abs(cwd); err == nil {
		cwd = a
	}

	os.Stdout.WriteString(buildStatusline(cwd, p.SessionID))
}

// buildStatusline is the print-free core: given a directory and session id it returns
// the badge string. Kept pure so it can be unit-tested by feeding a temp dir.
func buildStatusline(cwd, sid string) string {
	return renderClaudeStatusline(buildStatusSnapshot(cwd, sid))
}

// renderClaudeStatusline is deliberately kept as the Claude-specific presentation
// layer. StatusSnapshot is shared with CLI/MCP consumers; this renderer retains the
// established ANSI contract byte-for-byte.
func renderClaudeStatusline(snapshot StatusSnapshot) string {
	crumb := snapshot.crumb
	haveCrumb := snapshot.haveCrumb
	active := snapshot.ActiveRoot

	if active == "" || !snapshot.Project {
		return slDim + "◇ projx " + slReset + slDim + "global floor" + slReset
	}
	if snapshot.storeErr != "" {
		return slAccent + "◆ projx " + slReset + slDim + filepath.Base(active) + " · store?" + slReset
	}

	var b strings.Builder
	b.WriteString(slAccent + slBold + "◆ projx" + slReset)
	b.WriteString(" " + slBold + snapshot.ProjectName + slReset)
	b.WriteString(" " + slDim + itoa(snapshot.RecordCount) + " rec" + slReset)
	if snapshot.Modes.Dispatcher {
		b.WriteString(" " + slAmber + "disp✋" + slReset)
	}
	if snapshot.Modes.Cage {
		b.WriteString(" " + slRed + "cage" + slReset)
	}
	if snapshot.Modes.OverrideAuthority {
		b.WriteString(" " + slGreen + "override✓" + slReset)
	}
	if haveCrumb {
		switch crumb.A {
		case "gate":
			b.WriteString(" " + slRed + "· blocked✋" + slReset)
		case "ctx":
			if crumb.N > 0 {
				b.WriteString(" " + slDim + "· ctx " + humanBytes(crumb.N) + "↓" + slReset)
			}
		}
	}
	if db := dispatchBadge(active); db != "" {
		b.WriteString(" " + db)
	}
	if lines := agentLines(active, readFocus()); lines != "" {
		b.WriteString("\n")
		b.WriteString(lines)
	}
	return b.String()
}

// runningAgent is one live background agent for the multi-line view: a RUNNING dispatch
// or detached-workflow manifest in some project, plus its currently-executing step.
type runningAgent struct {
	root     string
	project  string
	m        *dispatchManifest
	cur      *dispatchStepStat // currently-running step (nil only if the manifest has no steps)
	curIndex int               // 1-based index of cur within m.Steps (0 when none)
	total    int
}

// agentLines renders the multi-line block: gather running agents across projects, decide
// which one is fat, and render one line each. Returns "" when nothing is running.
func agentLines(active, focusSel string) string {
	agents := gatherRunningAgents(active)
	if len(agents) == 0 {
		return ""
	}
	fat := pickFatAgent(agents, active, focusSel)
	var sb strings.Builder
	for i, a := range agents {
		if i > 0 {
			sb.WriteString("\n")
		}
		if i == fat {
			sb.WriteString(renderFatAgent(a))
		} else {
			sb.WriteString(renderLeanAgent(a))
		}
	}
	return sb.String()
}

// gatherRunningAgents scans the current scope's own project plus every project in the
// global dispatch-root index, collecting one runningAgent per RUNNING manifest. Cheap by
// design — it only reads the small per-run JSON manifests (no store open, no engine work)
// — so it is safe on every statusline render. Deterministically ordered (project, then
// start time) so the lines don't jump around between renders.
func gatherRunningAgents(active string) []runningAgent {
	roots := dispatchRoots()
	roots = append(roots, active) // the scope's own run may predate the global index write
	seen := map[string]bool{}
	var agents []runningAgent
	for _, r := range roots {
		if r == "" {
			continue
		}
		k := normRoot(r)
		if seen[k] {
			continue
		}
		seen[k] = true
		for _, m := range listDispatchManifests(r) {
			if m.State != "running" {
				continue
			}
			cur, idx := currentStep(m)
			agents = append(agents, runningAgent{
				root:     r,
				project:  filepath.Base(r),
				m:        m,
				cur:      cur,
				curIndex: idx,
				total:    len(m.Steps),
			})
		}
	}
	sort.Slice(agents, func(i, j int) bool {
		if agents[i].project != agents[j].project {
			return agents[i].project < agents[j].project
		}
		return agents[i].m.Started.Before(agents[j].m.Started)
	})
	return agents
}

// currentStep returns the manifest's active step (the running one; else the most recent
// completed; else the first) and its 1-based index.
func currentStep(m *dispatchManifest) (*dispatchStepStat, int) {
	if m == nil || len(m.Steps) == 0 {
		return nil, 0
	}
	for i := range m.Steps {
		if m.Steps[i].State == "running" {
			return &m.Steps[i], i + 1
		}
	}
	for i := len(m.Steps) - 1; i >= 0; i-- {
		if m.Steps[i].State == "done" {
			return &m.Steps[i], i + 1
		}
	}
	return &m.Steps[0], 1
}

// pickFatAgent selects the single agent to render fat: the focus pin wins, else the agent
// whose project == the current scope, else — only when unambiguous — the sole agent. -1
// means no agent is fat (all lean).
func pickFatAgent(agents []runningAgent, active, focusSel string) int {
	if focusSel != "" {
		for i, a := range agents {
			if agentMatchesFocus(a, focusSel) {
				return i
			}
		}
	}
	if active != "" {
		for i, a := range agents {
			if pathEq(a.root, active) {
				return i
			}
		}
	}
	if len(agents) == 1 {
		return 0
	}
	return -1
}

// agentMatchesFocus reports whether a focus selector picks this agent. A selector matches
// by dispatch id, project name, or the current step's role — whichever the human typed.
func agentMatchesFocus(a runningAgent, sel string) bool {
	sel = strings.TrimSpace(sel)
	if sel == "" {
		return false
	}
	if strings.EqualFold(sel, a.m.ID) || strings.EqualFold(sel, a.project) {
		return true
	}
	if a.cur != nil && a.cur.Role != "" && strings.EqualFold(sel, a.cur.Role) {
		return true
	}
	return false
}

// renderLeanAgent is the default line: project · current op · state (~3 fields, dim).
func renderLeanAgent(a runningAgent) string {
	return slDim + "  ▸ " + a.project + " · " + curOpLabel(a) + " · " + runStateLabel(a) + slReset
}

// renderFatAgent is the ONE focused line: richer fields (op, step, role/tier, elapsed,
// branch, verify), rendered in the accent color so it reads as the foreground agent.
func renderFatAgent(a runningAgent) string {
	fields := []string{curOpLabel(a), runStateLabel(a)}
	if role := agentRole(a); role != "" {
		fields = append(fields, role)
	}
	fields = append(fields, agentElapsed(a))
	if br := branchOf(a.root); br != "" {
		fields = append(fields, "⎇ "+br)
	}
	if a.m.Verify != "" {
		fields = append(fields, "verify:"+a.m.Verify)
	}
	return slAccent + "  ★ " + slBold + a.project + slReset + slAccent + " · " + strings.Join(fields, " · ") + slReset
}

// curOpLabel is the "currently-touched file / op" cell: a deterministic step shows its op,
// an agent step shows its (truncated) task, and a not-yet-started manifest shows "starting".
func curOpLabel(a runningAgent) string {
	if a.cur == nil {
		return "starting"
	}
	if a.cur.Op != "" {
		return a.cur.Op
	}
	if t := truncateDispatchMsg(a.cur.Task, 32); t != "" {
		return t
	}
	return "working"
}

// runStateLabel shows progress: "run k/n" for a multi-step run, "running" for a single step.
func runStateLabel(a runningAgent) string {
	if a.total > 1 {
		return "run " + itoa(a.curIndex) + "/" + itoa(a.total)
	}
	return "running"
}

// agentRole is the fat-line role/tier cell (the per-worker ProjX scope label).
func agentRole(a runningAgent) string {
	if a.cur == nil {
		return ""
	}
	if a.cur.Role != "" {
		return a.cur.Role
	}
	return a.cur.Tier
}

// agentElapsed is the fat-line elapsed-time cell.
func agentElapsed(a runningAgent) string { return compactDur(time.Since(a.m.Started)) }

// compactDur renders a duration tersely for the bar: 45s, 3m, 1h2m.
func compactDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Seconds())
	if s < 60 {
		return itoa(s) + "s"
	}
	m := s / 60
	if m < 60 {
		return itoa(m) + "m"
	}
	return itoa(m/60) + "h" + itoa(m%60) + "m"
}

// branchOf reads a repo's current branch from .git/HEAD (a cheap file read, no git exec).
// Returns the branch name, a short sha for a detached HEAD, or "" when not a git repo.
func branchOf(root string) string {
	data, err := os.ReadFile(filepath.Join(root, ".git", "HEAD"))
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(data))
	if rest, ok := strings.CutPrefix(s, "ref: refs/heads/"); ok {
		return rest
	}
	if len(s) >= 7 {
		return s[:7]
	}
	return ""
}

// dispatchBadge renders a compact summary of this project's background dispatch runs
// (e.g. "⚙ 2 running · 1 done"), or "" when there is nothing to show. RUNNING runs
// always count; FINISHED runs count only until the next-prompt hook has surfaced them
// (Reported=false) so the badge self-clears instead of growing without bound. Cheap by
// design — it just stats/reads the small per-run JSON manifests, no engine work — since
// the statusline paints on every render.
func dispatchBadge(root string) string {
	if root == "" {
		return ""
	}
	runs := listDispatchManifests(root)
	if len(runs) == 0 {
		return ""
	}
	running, done, failed := 0, 0, 0
	for _, m := range runs {
		switch m.State {
		case "running":
			running++
		case "failed":
			if !m.Reported {
				failed++
			}
		default: // done (or any terminal state)
			if !m.Reported {
				done++
			}
		}
	}
	var parts []string
	if running > 0 {
		parts = append(parts, itoa(running)+" running")
	}
	if done > 0 {
		parts = append(parts, itoa(done)+" done")
	}
	if failed > 0 {
		parts = append(parts, itoa(failed)+" failed")
	}
	if len(parts) == 0 {
		return ""
	}
	color := slGreen
	if running > 0 {
		color = slAmber
	}
	if failed > 0 {
		color = slRed
	}
	return color + "⚙ " + strings.Join(parts, " · ") + slReset
}

// nearestProjxDir returns the nearest ancestor of dir (dir inclusive) that owns a
// real ProjX PROJECT store (.projx/store.db), or "" if none. Reuses targetStoreRoot
// by handing it a path INSIDE dir so it checks dir/.projx first; targetStoreRoot
// falls back to its first arg when it finds nothing, so we verify the result
// actually is a project.
func nearestProjxDir(dir string) string {
	root := targetStoreRoot(dir, filepath.Join(dir, "_"))
	if isProjxDir(root) {
		return root
	}
	return ""
}

// isProjxDir reports whether path contains a real project store
// (.projx/store.db). A workspace root may still own a .projx runtime directory for
// routing/cage metadata; that alone must NOT make it a project.
func isProjxDir(path string) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(filepath.Join(path, ".projx", "store.db"))
	return err == nil && !fi.IsDir()
}

// statusCrumb is the tiny breadcrumb the hook writes after each event so the status
// line can show ProjX's most recent action and the actively-touched project.
//
//	A = last visible action ("ctx" | "gate")
//	N = bytes of context injected (for A=="ctx")
//	R = active project root (the .projx-owning dir of the last file any agent touched)
type statusCrumb struct {
	A string `json:"a"`
	N int    `json:"n"`
	R string `json:"r"`
}

func crumbPath(home, sid string) string {
	return filepath.Join(home, ".projx", "statusline-"+sanitizeSid(sid)+".json")
}

// sanitizeSid keeps a session id safe as a filename component (session ids are UUIDs,
// but never trust input on a path). Non [A-Za-z0-9._-] runes become '_'.
func sanitizeSid(sid string) string {
	var sb strings.Builder
	for _, r := range sid {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('_')
		}
	}
	s := sb.String()
	if s == "" {
		return "default"
	}
	return s
}

// updateCrumb read-modify-writes the breadcrumb so independent facts (the active
// project vs. the last action) don't clobber each other across events. Best-effort:
// no project home or any I/O error is silently ignored — a status breadcrumb must
// never affect the hook's actual result.
func updateCrumb(home, sid string, mut func(*statusCrumb)) {
	if home == "" || sid == "" || !isProjxDir(home) {
		return
	}
	c, _ := readStatusCrumb(home, sid)
	mut(&c)
	if data, err := json.Marshal(c); err == nil {
		_ = os.WriteFile(crumbPath(home, sid), data, 0o644)
	}
}

// readStatusCrumb loads the breadcrumb, if present.
func readStatusCrumb(home, sid string) (statusCrumb, bool) {
	data, err := os.ReadFile(crumbPath(home, sid))
	if err != nil {
		return statusCrumb{}, false
	}
	var c statusCrumb
	if json.Unmarshal(data, &c) != nil {
		return statusCrumb{}, false
	}
	return c, true
}

// humanBytes renders a byte count compactly for the badge (e.g. 1180 → "1.1k").
func humanBytes(n int) string {
	if n < 1024 {
		return itoa(n) + "b"
	}
	k := n / 1024
	frac := (n % 1024) * 10 / 1024
	if frac == 0 {
		return itoa(k) + "k"
	}
	return itoa(k) + "." + itoa(frac) + "k"
}

// itoa is a tiny allocation-light int→string for the record count (avoids pulling
// strconv just for this and keeps the hot path lean).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
