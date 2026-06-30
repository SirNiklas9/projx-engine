package main

// triage.go — the cell's MODEL-CALLING HANDS for the decider + v2 selector. The cell is
// the brain (pure logic); it reaches a cheap model through the transport.http.outbound
// capability (pulp.HTTP.Fetch), never a raw socket — the same way the workbench cell
// calls its AI endpoint. This is what lets the DEPLOYED WASM cell triage ambiguous tasks
// and select context semantically, not just deterministically.
//
// Config from the cell's env allowlist (PROJX_AI_*): PROJX_AI_KEY enables it (nil → the
// decider/selector stay deterministic), PROJX_AI_MODEL picks the cheap model,
// PROJX_AI_BASE_URL the OpenAI-compatible endpoint (default OpenRouter). Any error →
// the store safely ignores the result and falls back to deterministic routing/selection.

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	store "github.com/SirNiklas9/projx-store"
)

const (
	cellTriageSystem = `You are a routing triage for a coding assistant. Classify the user's task into exactly one TIER by how much reasoning it needs:
- "cheap-fast": rename/format/list/lookup/typo/trivial one-liners.
- "default": standard coding — implement a feature, write tests, a normal edit.
- "deep-reasoning": architecture, multi-file refactor, debugging a hard bug, design, redesign.
Reply with ONLY compact JSON: {"tier":"<one of the three>","confident":<true|false>}. confident=false if genuinely unsure.`

	cellSelectorSystem = `You select which knowledge areas are relevant to a coding task. You are given a list of KEYS (slash-separated knowledge paths) and a TASK. Reply with ONLY a compact JSON array of the keys — copied EXACTLY from the list — that are relevant to the task. Use [] if none. No prose, no new keys.`
)

// cellAIConfig resolves the cheap-model endpoint from the cell's allowlisted env.
// ok=false (→ deterministic only) when no PROJX_AI_KEY is set.
func cellAIConfig() (url, model, key string, ok bool) {
	key = strings.TrimSpace(os.Getenv("PROJX_AI_KEY"))
	if key == "" {
		return "", "", "", false
	}
	url = firstNonEmptyCell(os.Getenv("PROJX_AI_BASE_URL"), "https://openrouter.ai/api/v1")
	model = firstNonEmptyCell(os.Getenv("PROJX_AI_MODEL"), "anthropic/claude-haiku-4.5")
	return url, model, key, true
}

// cellCheapComplete sends one prompt to the cheap model via transport.http.outbound and
// returns the reply text. ok=false on any error / non-200 / no key.
func cellCheapComplete(prompt string) (string, bool) {
	url, model, key, ok := cellAIConfig()
	if !ok {
		return "", false
	}
	body, _ := json.Marshal(map[string]any{
		"model":       model,
		"max_tokens":  300,
		"temperature": 0,
		"messages":    []map[string]string{{"role": "user", "content": prompt}},
	})
	resp, err := pulp.HTTP.Fetch(pulp.HTTPFetchRequest{
		Method:  "POST",
		URL:     strings.TrimRight(url, "/") + "/chat/completions",
		Headers: map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + key},
		Body:    body,
		Timeout: 12 * time.Second,
	})
	if err != nil || resp.Status != 200 {
		return "", false
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(resp.Body, &out) != nil || len(out.Choices) == 0 {
		return "", false
	}
	return out.Choices[0].Message.Content, true
}

// cellTriageFunc is the decider's triage for the cell (nil when no AI key → deterministic).
func cellTriageFunc() store.TriageFunc {
	if _, _, _, ok := cellAIConfig(); !ok {
		return nil
	}
	return func(task string) (string, bool) {
		reply, ok := cellCheapComplete(cellTriageSystem + "\n\nClassify this task:\n" + task)
		if !ok {
			return "", false
		}
		return store.ParseTierReply(reply)
	}
}

// cellSelectorFunc is the v2 semantic context selector for the cell — OPT-IN via
// PROJX_SMART_CONTEXT and only with an AI key (else nil → deterministic v1).
func cellSelectorFunc() store.SelectorFunc {
	if strings.TrimSpace(os.Getenv("PROJX_SMART_CONTEXT")) == "" {
		return nil
	}
	if _, _, _, ok := cellAIConfig(); !ok {
		return nil
	}
	return func(task string, keys []string) []string {
		if len(keys) == 0 {
			return nil
		}
		reply, ok := cellCheapComplete(cellSelectorSystem + "\n\nKEYS:\n" + strings.Join(keys, "\n") + "\n\nTASK:\n" + task)
		if !ok {
			return nil
		}
		return store.ParseSelectedKeys(reply, keys)
	}
}

func firstNonEmptyCell(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
