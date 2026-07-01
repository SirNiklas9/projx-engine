package main

// dispatch_model.go — the CHEAP-MODEL splitter behind dispatch's decompose step.
//
// store.Decompose is the deterministic floor (splits on "then"/";"/newlines/lists). When
// it can't cleanly separate a plainly multi-task message ("rename the config var and
// clean up the auth module while designing a cache"), a tiny cheap-model call splits it —
// through the SAME vendor-neutral completer as triage (the active integration), so there
// is no vendor coupling here either. Returns nil (no split) when no provider is reachable.
// Splitting is a far smaller problem than the work — a small model does it fine.

import (
	"encoding/json"
	"strings"
)

const decomposeSystemPrompt = `You split a user's coding request into the discrete tasks it contains, preserving order. Do NOT solve or elaborate — only separate. If it is a single task, return that one task. Reply with ONLY a compact JSON array of short task strings, no prose. Example: ["rename the config var","refactor the auth module","design a caching layer"].`

// modelDecompose asks the cheap model to split a message into tasks. Returns nil when no
// provider is reachable or on any error, so the deterministic split stands.
func modelDecompose(absRoot, message string) []string {
	c, ok := resolveCompleter(absRoot)
	if !ok {
		return nil
	}
	reply, ok := c.complete(decomposeSystemPrompt+"\n\nSplit this request:\n"+message, cheapModel())
	if !ok {
		return nil
	}
	return parseTaskList(reply)
}

// parseTaskList extracts a JSON array of task strings from a model reply, tolerating
// surrounding prose/code fences. Returns nil if it can't find a clean list.
func parseTaskList(content string) []string {
	i, j := strings.Index(content, "["), strings.LastIndex(content, "]")
	if i < 0 || j <= i {
		return nil
	}
	var raw []string
	if json.Unmarshal([]byte(content[i:j+1]), &raw) != nil {
		return nil
	}
	var out []string
	for _, t := range raw {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}
