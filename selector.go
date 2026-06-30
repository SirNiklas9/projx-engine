package main

// selector.go — v2 SEMANTIC context selection behind store.SelectorFunc.
//
// v1 context slicing (significantTokens) matches a task's words against record keys —
// cheap, deterministic, offline, but LITERAL: a rambling paragraph that never says
// "billing" won't pull the billing doc even if it's about money movement. v2 hands the
// candidate record keys + the task to the SAME cheap model the decider uses and lets it
// PROPOSE the relevant subset.
//
// OPT-IN: per-message selection means a model call per prompt, so it is OFF unless
// PROJX_SMART_CONTEXT is set. When off, newSelectorFunc returns nil and the store uses
// the v1 token floor. Uses the harness agent CLI by default (no key), HTTP if a triage
// key is set — the same transport choice as triage.go. Any failure → nil → v1 fallback.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	store "github.com/SirNiklas9/projx-store"
)

const selectorInstruction = `You select which knowledge areas are relevant to a coding task. You are given a list of KEYS (slash-separated knowledge paths) and a TASK. Reply with ONLY a compact JSON array of the keys — copied EXACTLY from the list — that are relevant to the task. Use [] if none are relevant. No prose, no new keys.`

// newSelectorFunc returns a v2 semantic selector, or nil when smart context is not opted
// into or no cheap model is available (→ the store falls back to deterministic v1).
func newSelectorFunc() store.SelectorFunc {
	if strings.TrimSpace(os.Getenv("PROJX_SMART_CONTEXT")) == "" {
		return nil // opt-in: a model call per message is off by default
	}
	if !cheapModelAvailable() {
		return nil
	}
	return func(task string, keys []string) []string {
		if len(keys) == 0 {
			return nil
		}
		prompt := selectorInstruction + "\n\nKEYS:\n" + strings.Join(keys, "\n") + "\n\nTASK:\n" + task
		reply, ok := cheapComplete(prompt)
		if !ok {
			return nil // model failed → store degrades to v1
		}
		return parseSelectedKeys(reply, keys)
	}
}

// cheapModelAvailable reports whether some cheap-model path (HTTP key or agent CLI) exists.
func cheapModelAvailable() bool {
	if _, ok := loadTriageConfig(); ok {
		return true
	}
	return resolveTriageBin() != ""
}

// cheapComplete sends one prompt to the cheap model — the explicit HTTP endpoint if a
// triage key is set, otherwise the harness agent CLI — and returns the reply text.
func cheapComplete(prompt string) (string, bool) {
	if cfg, ok := loadTriageConfig(); ok {
		return httpComplete(cfg, prompt)
	}
	if bin := resolveTriageBin(); bin != "" {
		return cliComplete(bin, triageModel(), prompt)
	}
	return "", false
}

// httpComplete posts a single user message to an OpenAI-compatible endpoint.
func httpComplete(cfg triageConfig, prompt string) (string, bool) {
	body, _ := json.Marshal(map[string]any{
		"model":       cfg.Model,
		"max_tokens":  300,
		"temperature": 0,
		"messages":    []map[string]string{{"role": "user", "content": prompt}},
	})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	resp, err := (&http.Client{Timeout: 12 * time.Second}).Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || len(out.Choices) == 0 {
		return "", false
	}
	return out.Choices[0].Message.Content, true
}

// cliComplete drives the harness agent CLI (`<bin> -p <prompt> --model <cheap>`) in a
// neutral cwd so the project's own hooks don't fire for a throwaway query.
func cliComplete(bin, model, prompt string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-p", prompt, "--model", model)
	cmd.Dir = neutralTriageDir()
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// parseSelectedKeys extracts a JSON array from the reply and keeps only keys that are in
// the candidate set (the model can't invent or rename a key). Order follows the reply.
func parseSelectedKeys(reply string, candidates []string) []string {
	valid := make(map[string]bool, len(candidates))
	for _, k := range candidates {
		valid[k] = true
	}
	i := strings.IndexByte(reply, '[')
	j := strings.LastIndexByte(reply, ']')
	if i < 0 || j <= i {
		return nil
	}
	var arr []string
	if json.Unmarshal([]byte(reply[i:j+1]), &arr) != nil {
		return nil
	}
	var out []string
	for _, k := range arr {
		if k = strings.TrimSpace(k); valid[k] {
			out = append(out, k)
		}
	}
	return out
}
