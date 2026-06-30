package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleHookViaCell proves the hook proxies each lifecycle event to the cell's HTTP
// endpoints and translates the responses to the stdout/stderr/exit contract.
func TestHandleHookViaCell(t *testing.T) {
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/context/floor":
			io.WriteString(w, `{"floor":"PROTOCOL secret/** law"}`)
		case "/api/context/delta":
			io.WriteString(w, `{"context":"law + minecraft/login/backend"}`)
		case "/api/gate/check":
			if r.URL.Query().Get("path") == "secret/k" {
				io.WriteString(w, `{"denied":true,"pattern":"secret/**"}`)
			} else {
				io.WriteString(w, `{"denied":false,"pattern":""}`)
			}
		case "/api/context/reset":
			io.WriteString(w, `{"reset":true}`)
		case "/api/context/suggest":
			io.WriteString(w, `{"suggest":"do @remember","block":true}`)
		}
	}))
	defer srv.Close()
	base := srv.URL

	// SessionStart → wrapped floor.
	out, _, code := handleHookViaCell(base, []byte(`{"session_id":"s","hook_event_name":"SessionStart"}`))
	if code != 0 || !strings.Contains(out, "source=\"ProjX\"") || !strings.Contains(out, "secret/**") {
		t.Errorf("SessionStart: code=%d out=%q", code, out)
	}
	// UserPromptSubmit → wrapped delta.
	out, _, _ = handleHookViaCell(base, []byte(`{"session_id":"s","hook_event_name":"UserPromptSubmit","prompt":"fix login"}`))
	if !strings.Contains(out, "minecraft/login/backend") {
		t.Errorf("UserPromptSubmit: out=%q", out)
	}
	// PreToolUse on secret → block (exit 2 + reason).
	_, errOut, code := handleHookViaCell(base, []byte(`{"hook_event_name":"PreToolUse","tool_input":{"file_path":"secret/k"}}`))
	if code != 2 || !strings.Contains(errOut, "off-limits") {
		t.Errorf("gate block: code=%d err=%q", code, errOut)
	}
	// PreToolUse on allowed → 0.
	if _, _, code := handleHookViaCell(base, []byte(`{"hook_event_name":"PreToolUse","tool_input":{"file_path":"ok.go"}}`)); code != 0 {
		t.Error("allowed path should be code 0")
	}
	// Stop → block with the suggestion.
	_, errOut, code = handleHookViaCell(base, []byte(`{"session_id":"s","hook_event_name":"Stop"}`))
	if code != 2 || !strings.Contains(errOut, "@remember") {
		t.Errorf("Stop: code=%d err=%q", code, errOut)
	}
	// PreCompact → reset called, no output.
	out, _, code = handleHookViaCell(base, []byte(`{"session_id":"s","hook_event_name":"PreCompact"}`))
	if code != 0 || out != "" {
		t.Errorf("PreCompact: code=%d out=%q", code, out)
	}

	for _, want := range []string{"GET /api/context/floor", "GET /api/context/delta", "GET /api/gate/check", "POST /api/context/reset", "POST /api/context/suggest"} {
		found := false
		for _, h := range hits {
			if h == want {
				found = true
			}
		}
		if !found {
			t.Errorf("expected cell to receive %q; hits=%v", want, hits)
		}
	}
}
