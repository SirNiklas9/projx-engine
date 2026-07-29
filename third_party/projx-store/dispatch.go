package store

// dispatch.go — DECOMPOSE a multi-task message into discrete tasks, so each can be
// routed to its own tier by the decider. The user states WHAT ("rename this, then
// refactor that, then design the other"); the rules (RouteDecide) decide the tier for
// each. This is the deterministic splitter — the offline floor; a cheap-model splitter
// layers on top for messages the connectors don't cleanly separate.

import "strings"

// decomposeConnectors are the strong task separators — deliberately conservative
// (no bare comma/"and", which over-split) so the split is high-precision.
// Comma-prefixed variants come first so ", and then " is consumed whole rather than
// leaving a trailing comma behind when " and then " matches first.
var decomposeConnectors = []string{
	"\n",
	";",
	", and then ",
	", then ",
	" and then ",
	" then ",
	", next, ",
	" next, ",
	" after that ",
}

// Decompose splits a multi-task message into discrete task fragments on natural
// connectors and numbered/bulleted list markers. Returns a single-element slice (the
// trimmed message) when there is no clear split — so a normal one-task message is
// untouched. Deterministic and order-preserving.
func Decompose(message string) []string {
	// Split ONLY on EXPLICIT task delimiters — a leading bullet ("- ", "* "), an ordered
	// marker ("1.", "2)"), a "TASK:"/"STEP:" prefix, or a "---"/"===" separator line. Prose
	// punctuation (colons, periods) NEVER splits: a single-intent spec stays ONE task. This
	// replaces the old connector-splitting that shredded one cohesive change into fragments.
	lines := strings.Split(message, "\n")
	var tasks []string
	var cur strings.Builder
	flush := func() {
		if t := cleanTaskFragment(cur.String()); t != "" {
			tasks = append(tasks, t)
		}
		cur.Reset()
	}
	delimited := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "---" || t == "===" || strings.HasPrefix(t, "---") || strings.HasPrefix(t, "===") {
			delimited = true
			flush()
			continue
		}
		if marker := taskDelimiterPrefix(t); marker >= 0 {
			delimited = true
			flush()
			cur.WriteString(t[marker:])
			cur.WriteByte('\n')
			continue
		}
		cur.WriteString(ln)
		cur.WriteByte('\n')
	}
	flush()

	// No explicit delimiters → single-intent → exactly ONE task (never split prose).
	if !delimited {
		if t := strings.TrimSpace(message); t != "" {
			return []string{t}
		}
		return nil
	}
	if len(tasks) == 0 {
		if t := strings.TrimSpace(message); t != "" {
			return []string{t}
		}
	}
	return tasks
}

// taskDelimiterPrefix returns the byte offset AFTER an explicit task-delimiter marker at
// the start of a trimmed line ("- ", "* ", "TASK:", "STEP:", "1.", "2)"), or -1 if none.
func taskDelimiterPrefix(t string) int {
	switch {
	case strings.HasPrefix(t, "- "), strings.HasPrefix(t, "* "):
		return 2
	}
	for _, p := range []string{"task:", "step:"} {
		if len(t) >= len(p) && strings.ToLower(t[:len(p)]) == p {
			return len(p)
		}
	}
	// ordered: one or more digits then '.' or ')' then a space
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	if i > 0 && i < len(t) && (t[i] == '.' || t[i] == ')') {
		j := i + 1
		if j < len(t) && t[j] == ' ' {
			return j + 1
		}
	}
	return -1
}

// cleanTaskFragment trims a fragment and strips a leading list marker (1. / 2) / - / *)
// and a leading "we need to" / "we should" filler so the fragment reads as a task.
func cleanTaskFragment(s string) string {
	s = strings.TrimSpace(s)
	// strip a leading list marker
	s = strings.TrimLeft(s, "-*• \t")
	if i := strings.IndexAny(s, ".)"); i >= 0 && i <= 3 && isAllDigits(s[:i]) {
		s = strings.TrimSpace(s[i+1:])
	}
	// strip common filler openers
	for _, filler := range []string{"we need to ", "we should ", "i need to ", "we also need to ", "also "} {
		if lower := strings.ToLower(s); strings.HasPrefix(lower, filler) {
			s = strings.TrimSpace(s[len(filler):])
			break
		}
	}
	return strings.TrimSpace(s)
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
