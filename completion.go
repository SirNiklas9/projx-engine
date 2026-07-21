package main

// completion.go — the ENGINE-side executor for the vendor-neutral provider seam.
//
// store.CompletionSpec declares HOW to reach a provider (data); this runs it. Two
// transports, no vendor-specific logic:
//   - "cli":         render the declared argv template ({prompt}/{model} as whole args)
//                    and exec it in one-shot mode — the "use the harness you already
//                    have" default (Claude Code ships as the default template, as data).
//   - "http-openai": POST to any OpenAI-compatible /chat/completions endpoint — fully
//                    vendor-neutral (OpenRouter, a local server, anything that speaks it).
//
// Both the triage decider and the dispatch decompose-splitter call one completer, so
// there is a single provider integration point. Write a new integration record and every
// model call in ProjX follows it.

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

// completer executes one-shot completions against a resolved integration spec.
type completer struct {
	spec store.CompletionSpec
}

// resolveCompleter picks the completion provider, declared-first:
//  1. the store's active integration record (the declared, agnostic way);
//  2. an implicit OpenAI-compatible endpoint if PROJX_TRIAGE_API_KEY is set and nothing
//     was declared (back-compat — the secret still lives in env, the shape is neutral);
//  3. the default CLI integration (Claude Code, as data) IF its binary resolves.
//
// Returns ok=false when no provider is reachable, so the decider stays deterministic.
func resolveCompleter(absRoot string) (completer, bool) {
	st := openStore(absRoot)
	defer st.Close()
	return newCompleter(st)
}

// newCompleter resolves the provider from an already-open store (used where the caller
// owns the store, e.g. the context selector). A nil store falls through to env/default.
func newCompleter(st store.Store) (completer, bool) {
	spec, declared := store.ResolveCompletion(st)

	if declared {
		if spec.Transport == store.TransportCLI && !cliSpecRunnable(spec) {
			return completer{}, false
		}
		return completer{spec: spec}, true
	}
	// Nothing declared — honour an env key as an implicit neutral endpoint first.
	if key := strings.TrimSpace(os.Getenv("PROJX_TRIAGE_API_KEY")); key != "" {
		return completer{spec: store.CompletionSpec{
			Transport: store.TransportHTTPOpenAI,
			BaseURL:   firstNonEmpty(os.Getenv("PROJX_TRIAGE_BASE_URL"), defaultTriageBaseURL),
			APIKeyEnv: "PROJX_TRIAGE_API_KEY",
			Model:     firstNonEmpty(os.Getenv("PROJX_TRIAGE_MODEL"), defaultTriageModel),
		}}, true
	}
	// Fall back to the default CLI integration only if its binary is actually present.
	if cliSpecRunnable(spec) {
		return completer{spec: spec}, true
	}
	return completer{}, false
}

// cliSpecRunnable reports whether a cli spec's first token resolves to an executable
// (respecting PROJX_AGENT_CMD/PROJX_AGENT the same way resolveTriageBin does).
func cliSpecRunnable(spec store.CompletionSpec) bool {
	if spec.Transport != store.TransportCLI {
		return false
	}
	args := store.RenderCLIArgs(spec.Template, "x", "x")
	if len(args) == 0 {
		return false
	}
	// Let PROJX_AGENT_CMD/PROJX_AGENT override the template's binary (still the harness).
	if bin := resolveTriageBin(); bin != "" {
		return true
	}
	_, err := exec.LookPath(args[0])
	return err == nil
}

// complete runs one completion and returns the raw model text. model overrides the
// spec's default when non-empty. Any error → ("", false) so callers degrade gracefully.
func (c completer) complete(prompt, model string) (string, bool) {
	if model == "" {
		model = c.spec.Model
	}
	switch c.spec.Transport {
	case store.TransportCLI:
		return c.completeCLI(prompt, model)
	case store.TransportHTTPOpenAI:
		return c.completeHTTP(prompt, model)
	default:
		return "", false
	}
}

func (c completer) completeCLI(prompt, model string) (string, bool) {
	args := store.RenderCLIArgs(c.spec.Template, prompt, model)
	if len(args) == 0 {
		return "", false
	}
	// PROJX_AGENT_CMD/PROJX_AGENT may retarget the binary (e.g. an absolute path).
	if bin := resolveTriageBin(); bin != "" {
		args[0] = bin
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = neutralTriageDir() // don't load the project's own hooks for a throwaway call
	cmd.SysProcAttr = quietSysProcAttr()
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

func (c completer) completeHTTP(prompt, model string) (string, bool) {
	key := strings.TrimSpace(os.Getenv(c.spec.APIKeyEnv))
	if key == "" || c.spec.BaseURL == "" {
		return "", false
	}
	reqBody, _ := json.Marshal(map[string]any{
		"model":       model,
		"max_tokens":  400,
		"temperature": 0,
		"messages":    []map[string]string{{"role": "user", "content": prompt}},
	})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(c.spec.BaseURL, "/")+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return "", false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
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
