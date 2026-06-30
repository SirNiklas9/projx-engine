package main

// triage.go — the LIVE cheap-model triage behind the decider's TriageFunc seam.
//
// The decider (store.RouteDecide) routes unambiguous tasks for free by rule and only
// asks a model for the ambiguous middle. This file implements that model call: a tiny,
// vendor-neutral OpenAI-compatible /chat/completions request to a CHEAP model (haiku by
// default) that returns just a tier + a confidence. The insight (see SMART-CONTEXT-PLAN
// "The DECIDER"): you don't need opus to know a task NEEDS opus — triage is a far smaller
// problem than the work.
//
// Default = USE THE HARNESS: triage drives the agent CLI the user already runs
// (claude -p … --model haiku), so it needs no separate credential — "whatever the
// harness uses." An explicit OpenAI-compatible endpoint (e.g. OpenRouter) is used ONLY
// when PROJX_TRIAGE_API_KEY is set (a deliberate opt-in; a general OPENROUTER_API_KEY is
// NOT scavenged). With neither, newTriageFunc returns nil and the decider stays purely
// deterministic. Any error → ("", false) → the decider ignores triage and
// falls to the deterministic default. Confidence drives escalate-on-uncertainty in
// RouteDecide (unsure → up a tier, never down).

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	store "github.com/SirNiklas9/projx-store"
)

// triageConfig is the resolved endpoint for the cheap triage model.
type triageConfig struct {
	BaseURL string // OpenAI-compatible base, e.g. https://openrouter.ai/api/v1
	Model   string // a CHEAP model slug
	APIKey  string
}

const (
	defaultTriageBaseURL = "https://openrouter.ai/api/v1"
	defaultTriageModel   = "anthropic/claude-haiku-4.5"
)

// loadTriageConfig activates the explicit HTTP endpoint ONLY when PROJX_TRIAGE_API_KEY
// is set — a triage-specific opt-in. It deliberately does NOT scavenge a general
// OPENROUTER_API_KEY from env/secrets (that key is kept for other features); without an
// explicit triage key the default path is the harness agent CLI (see newTriageFunc).
func loadTriageConfig() (cfg triageConfig, ok bool) {
	key := strings.TrimSpace(os.Getenv("PROJX_TRIAGE_API_KEY"))
	if key == "" {
		return triageConfig{}, false
	}
	return triageConfig{
		BaseURL: firstNonEmpty(os.Getenv("PROJX_TRIAGE_BASE_URL"), defaultTriageBaseURL),
		Model:   firstNonEmpty(os.Getenv("PROJX_TRIAGE_MODEL"), defaultTriageModel),
		APIKey:  key,
	}, true
}

// newTriageFunc returns a live store.TriageFunc, choosing the cheapest available path:
//  1. an explicit OpenAI-compatible API endpoint, IF a key is configured (env/secrets);
//  2. otherwise the HARNESS's own agent CLI (`claude -p … --model haiku`) — no separate
//     key, it uses whatever auth Claude Code already has. This is the default: "use what
//     the harness uses."
//  3. nil when neither is available, so the decider stays purely deterministic.
func newTriageFunc() store.TriageFunc {
	if cfg, ok := loadTriageConfig(); ok {
		c := triageClient{cfg: cfg, http: &http.Client{Timeout: 8 * time.Second}}
		return c.Triage
	}
	if bin := resolveTriageBin(); bin != "" {
		return cliTriageClient{bin: bin, model: triageModel()}.Triage
	}
	return nil
}

// triageModel is the cheap model used for triage (alias or full id). Override with
// PROJX_TRIAGE_MODEL; defaults to the haiku alias the harness CLI understands.
func triageModel() string { return firstNonEmpty(os.Getenv("PROJX_TRIAGE_MODEL"), "haiku") }

// resolveTriageBin finds the agent CLI to drive triage: the binary from
// PROJX_AGENT_CMD / PROJX_AGENT, else `claude` on PATH. "" if none is found.
func resolveTriageBin() string {
	if cmd := strings.TrimSpace(os.Getenv("PROJX_AGENT_CMD")); cmd != "" {
		if f := strings.Fields(cmd); len(f) > 0 {
			return f[0]
		}
	}
	name := firstNonEmpty(os.Getenv("PROJX_AGENT"), "claude")
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// cliTriageClient triages by invoking the harness agent CLI in one-shot print mode.
// It runs in a neutral working directory so the project's own ProjX hooks / CLAUDE.md
// do NOT load for a throwaway classification (cheap + no recursion).
type cliTriageClient struct {
	bin   string
	model string
}

// Triage runs `<bin> -p "<prompt>" --model <cheap>` and parses the tier from stdout.
// Any error → ("", false) so the decider safely ignores it.
func (c cliTriageClient) Triage(task string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	prompt := triageSystemPrompt + "\n\nClassify this task:\n" + task
	cmd := exec.CommandContext(ctx, c.bin, "-p", prompt, "--model", c.model)
	cmd.Dir = neutralTriageDir() // avoid loading the project's .claude hooks/context
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return parseTriageReply(string(out))
}

// neutralTriageDir is a scratch cwd with no .claude, so the triage agent invocation
// doesn't trigger the project's lifecycle hooks. Falls back to the OS temp dir.
func neutralTriageDir() string {
	d := filepath.Join(os.TempDir(), "projx-triage")
	if os.MkdirAll(d, 0o755) == nil {
		return d
	}
	return os.TempDir()
}

type triageClient struct {
	cfg  triageConfig
	http *http.Client
}

const triageSystemPrompt = `You are a routing triage for a coding assistant. Classify the user's task into exactly one TIER by how much reasoning it needs:
- "cheap-fast": rename/format/list/lookup/typo/trivial one-liners.
- "default": standard coding — implement a feature, write tests, a normal edit.
- "deep-reasoning": architecture, multi-file refactor, debugging a hard bug, design, redesign.
Reply with ONLY compact JSON, no prose: {"tier":"<one of the three>","confident":<true|false>}. Set confident=false if you are genuinely unsure.`

// Triage asks the cheap model for a tier. Returns (class, confident); ("", false) on any
// error or unparseable reply so the decider safely ignores it.
func (c triageClient) Triage(task string) (string, bool) {
	reqBody, _ := json.Marshal(map[string]any{
		"model":       c.cfg.Model,
		"max_tokens":  40,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": triageSystemPrompt},
			{"role": "user", "content": task},
		},
	})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(c.cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return "", false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	resp, err := c.http.Do(req)
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
	return parseTriageReply(out.Choices[0].Message.Content)
}

// parseTriageReply delegates to the shared store parser (one definition for every face).
func parseTriageReply(content string) (string, bool) { return store.ParseTierReply(content) }

// firstNonEmpty returns the first non-empty, trimmed string.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
