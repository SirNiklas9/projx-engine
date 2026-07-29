package store

// capture.go — keyword capture. When a task signals the user wants something
// remembered (the @remember command or natural language), the launch hooks append
// CaptureHint to the task context so the agent turns the aside into a clean,
// well-formed store commit instead of a loose markdown note. Detection is
// deterministic (no model) and lives here so every face shares it.

import "strings"

// capturePhrases are natural-language signals (besides the @remember command)
// that the user wants something written to the store.
var capturePhrases = []string{
	"remember this",
	"remember that",
	"add to the knowledge base",
	"add that to the knowledge base",
	"add this to the knowledge base",
	"document this",
	"note this",
	"save this to the store",
	"commit this to the store",
	"make a note of",
}

// wantsCapture reports whether the task signals capture intent: the @remember
// command or a natural-language phrase.
func wantsCapture(task string) bool {
	t := strings.ToLower(task)
	if strings.Contains(t, "@remember") {
		return true
	}
	for _, p := range capturePhrases {
		if strings.Contains(t, p) {
			return true
		}
	}
	return false
}

// CaptureHint returns instructions for turning the user's aside into a clean
// store commit when the task signals capture intent; "" otherwise. Appended to
// the task context by the launch hooks so a "remember: …" becomes a real,
// well-formed store.commit instead of a markdown scribble.
func CaptureHint(task string) string {
	if !wantsCapture(task) {
		return ""
	}
	return captureHintText
}

const captureHintText = `
---

# CAPTURE REQUESTED — write it to the store
You were asked to REMEMBER something. Commit it only if it is durable across sessions (NOT a progress update, checkpoint, or markdown file):

    projx-engine store commit --kind <kind> --key <area>/<feature>/<subsystem> --body "<the fact>" --claim-class <class> --evidence "<source or verification>"

- KIND: ` + "`doc`" + ` (durable fact), ` + "`adr`" + ` (a decision + why), or ` + "`convention`" + ` (a standing rule).
- CLASS: ` + "`stable`" + `, ` + "`volatile`" + `, ` + "`decision`" + `, ` + "`convention`" + `, ` + "`ownership-boundary`" + `, or ` + "`completion-criteria`" + `.
- EVIDENCE: the live source, test, or durable reference that supports the claim.
- KEY: a lowercase path, e.g. ` + "`minecraft/login/backend`" + ` — group related knowledge under a shared prefix.
- If it points at code, put a file anchor in the body JSON: ` + "`{\"note\":\"…\",\"anchor\":\"internal/auth/login.go:42\"}`" + `.
- If it only describes this turn's status, do not store it.
- One commit = one fact. Do NOT write it to README/CLAUDE.md — they are not read.`
