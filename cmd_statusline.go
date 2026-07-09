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
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

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
	// The crumb HOME is the session cwd's owning project (nearest ancestor with a
	// .projx, cwd inclusive). Both this command and the hook derive it the same way
	// from the same cwd, so they agree on where the breadcrumb lives.
	home := nearestProjxDir(cwd)

	var crumb statusCrumb
	haveCrumb := false
	if sid != "" && home != "" {
		crumb, haveCrumb = readStatusCrumb(home, sid)
	}

	// FLOATING scope: lead with the project being touched (crumb.R) when set and
	// valid; otherwise the cwd's own project.
	active := home
	if haveCrumb && crumb.R != "" && isProjxDir(crumb.R) {
		active = crumb.R
	}

	// Not inside any ProjX project → ProjX is present as a global floor only.
	if active == "" || !isProjxDir(active) {
		return slDim + "◇ projx " + slReset + slDim + "global floor" + slReset
	}

	st, err := openStoreSafe(active)
	if err != nil {
		// A project is here but its store won't open — still say so, don't go dark.
		return slAccent + "◆ projx " + slReset + slDim + filepath.Base(active) + " · store?" + slReset
	}
	defer st.Close()

	var b strings.Builder
	b.WriteString(slAccent + slBold + "◆ projx" + slReset)
	b.WriteString(" " + slBold + filepath.Base(active) + slReset)

	// knowledge-record count (project scope, EXCLUDING the code map) — the code map
	// can be thousands of symbol records and would drown out the signal the human
	// actually reads: how much declared knowledge this project carries.
	n := 0
	for _, r := range st.List(store.InScope(store.ScopeProject)) {
		if r.Kind != store.KDeclaredStructure {
			n++
		}
	}
	b.WriteString(" " + slDim + itoa(n) + " rec" + slReset)

	// mode flags — only shown when notable, so a quiet project stays quiet. These
	// reflect the ACTIVE (touched) project's rules, which is the floating point:
	// jump into a repo with dispatcher-mode on and the badge shows it.
	if store.DispatcherModeOn(st) {
		b.WriteString(" " + slAmber + "disp✋" + slReset)
	}
	if store.CageModeOn(st) {
		b.WriteString(" " + slRed + "cage" + slReset)
	}
	if store.OverrideAuthorityOn(st) {
		b.WriteString(" " + slGreen + "override✓" + slReset)
	}

	// in-the-moment activity: the last thing ProjX did this session, left as a
	// breadcrumb by the hook. Makes ProjX activity visible turn-to-turn, distinct
	// from plain harness work — a block flips the badge red, a context inject shows
	// how much was slid in.
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

	return b.String()
}

// nearestProjxDir returns the nearest ancestor of dir (dir inclusive) that owns a
// .projx directory, or "" if none. Reuses targetStoreRoot by handing it a path
// INSIDE dir so it checks dir/.projx first; targetStoreRoot falls back to its first
// arg when it finds nothing, so we verify the result actually is a project.
func nearestProjxDir(dir string) string {
	root := targetStoreRoot(dir, filepath.Join(dir, "_"))
	if isProjxDir(root) {
		return root
	}
	return ""
}

// isProjxDir reports whether path contains a .projx directory (i.e. is a ProjX
// project root).
func isProjxDir(path string) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(filepath.Join(path, ".projx"))
	return err == nil && fi.IsDir()
}

// statusCrumb is the tiny breadcrumb the hook writes after each event so the status
// line can show ProjX's most recent action and the actively-touched project.
//   A = last visible action ("ctx" | "gate")
//   N = bytes of context injected (for A=="ctx")
//   R = active project root (the .projx-owning dir of the last file any agent touched)
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
