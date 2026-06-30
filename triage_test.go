package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestParseTriageReply covers the reply parser: strict JSON, JSON with surrounding
// prose, a bare tier word (→ not confident), and garbage (→ no tier).
func TestParseTriageReply(t *testing.T) {
	cases := []struct {
		name          string
		in            string
		wantTier      string
		wantConfident bool
	}{
		{"strict json confident", `{"tier":"deep-reasoning","confident":true}`, "deep-reasoning", true},
		{"strict json unsure", `{"tier":"default","confident":false}`, "default", false},
		{"json with prose", "Sure!\n{\"tier\":\"cheap-fast\",\"confident\":true}\nhope that helps", "cheap-fast", true},
		{"confident absent → true", `{"tier":"default"}`, "default", true},
		{"invalid tier in json", `{"tier":"medium","confident":true}`, "", false},
		{"bare word fallback", "I'd say this is deep-reasoning territory", "deep-reasoning", false},
		{"garbage", "no idea honestly", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tier, conf := parseTriageReply(c.in)
			if tier != c.wantTier || conf != c.wantConfident {
				t.Errorf("parseTriageReply(%q) = %q/%v, want %q/%v", c.in, tier, conf, c.wantTier, c.wantConfident)
			}
		})
	}
}

// TestTriageClientHTTP drives the full client against a fake OpenAI-compatible server:
// it must send the model + the task, and parse the returned tier.
func TestTriageClientHTTP(t *testing.T) {
	var gotModel, gotUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing/wrong auth header: %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		gotModel = req.Model
		for _, m := range req.Messages {
			if m.Role == "user" {
				gotUser = m.Content
			}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"tier\":\"deep-reasoning\",\"confident\":true}"}}]}`)
	}))
	defer srv.Close()

	c := triageClient{
		cfg:  triageConfig{BaseURL: srv.URL, Model: "test-haiku", APIKey: "test-key"},
		http: &http.Client{Timeout: 5 * time.Second},
	}
	tier, conf := c.Triage("redesign the whole auth subsystem")
	if tier != "deep-reasoning" || !conf {
		t.Fatalf("Triage = %q/%v, want deep-reasoning/true", tier, conf)
	}
	if gotModel != "test-haiku" {
		t.Errorf("model not sent: %q", gotModel)
	}
	if !strings.Contains(gotUser, "redesign the whole auth") {
		t.Errorf("task not sent as user message: %q", gotUser)
	}
}

// TestTriageClientNon200 proves a non-200 (or any error) yields ("", false) so the
// decider safely ignores triage and stays deterministic.
func TestTriageClientNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := triageClient{cfg: triageConfig{BaseURL: srv.URL, Model: "m", APIKey: "k"}, http: &http.Client{Timeout: 5 * time.Second}}
	if tier, conf := c.Triage("x"); tier != "" || conf {
		t.Errorf("non-200 should yield empty/false, got %q/%v", tier, conf)
	}
}

// TestNewTriageFuncFallbacks proves the selection order: nil only when there is neither
// an API key NOR an agent CLI; an agent CLI on PATH yields a (CLI) triage func.
func TestNewTriageFuncFallbacks(t *testing.T) {
	t.Setenv("PROJX_TRIAGE_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("PROJX_SECRETS_DIR", t.TempDir()) // empty secrets store
	t.Setenv("PROJX_AGENT_CMD", "")
	t.Setenv("PROJX_AGENT", "")

	// No key, no agent binary on PATH → nil (deterministic).
	t.Setenv("PATH", t.TempDir())
	if fn := newTriageFunc(); fn != nil {
		t.Error("newTriageFunc should be nil with no key and no agent CLI")
	}

	// A fake agent on PATH → CLI triage func (no key needed — uses the harness).
	bindir := t.TempDir()
	fake := filepath.Join(bindir, "claude")
	if runtime.GOOS == "windows" {
		fake += ".exe"
	}
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho '{\"tier\":\"default\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bindir)
	if fn := newTriageFunc(); fn == nil {
		t.Error("newTriageFunc should return a CLI triage func when an agent CLI is on PATH")
	}
}
