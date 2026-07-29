package main

import "strings"

// automaticHookContextMaxBytes bounds the context added invisibly by lifecycle
// hooks on repeated user prompts. It deliberately does not constrain the
// once-per-session safety floor, an explicit `projx context` request, or a
// dispatched worker's task-sliced context. A byte limit is deterministic and
// conservative for ordinary UTF-8 prose (roughly one thousand tokens).
const automaticHookContextMaxBytes = 4096

const automaticHookContextTruncated = "\n\n[ProjX automatic context was capped to protect token budget. Use `projx-engine context --task \"…\"` or store query for the remaining records.]\n"

func budgetAutomaticHookContext(ctx string) string {
	if len(ctx) <= automaticHookContextMaxBytes {
		return ctx
	}
	available := automaticHookContextMaxBytes - len(automaticHookContextTruncated)
	if available <= 0 {
		return automaticHookContextTruncated
	}
	// Session context begins with the safety floor and ends with the task slice.
	// Keep both rather than clipping only the tail: the latter saves tokens but
	// silently removes the one record the prompt was asking about.
	headLimit := available / 2
	headEnd := safePrefixBoundary(ctx, headLimit)
	tailStart := safeSuffixBoundary(ctx, len(ctx)-(available-headEnd))
	return ctx[:headEnd] + automaticHookContextTruncated + ctx[tailStart:]
}

func safePrefixBoundary(s string, limit int) int {
	if limit >= len(s) {
		return len(s)
	}
	for limit > 0 && (s[limit]&0xc0) == 0x80 {
		limit--
	}
	if line := strings.LastIndex(s[:limit], "\n"); line > limit/2 {
		return line + 1
	}
	return limit
}

func safeSuffixBoundary(s string, start int) int {
	if start <= 0 {
		return 0
	}
	for start < len(s) && (s[start]&0xc0) == 0x80 {
		start++
	}
	if line := strings.Index(s[start:], "\n"); line >= 0 && start+line < len(s)-1 {
		return start + line + 1
	}
	return start
}
