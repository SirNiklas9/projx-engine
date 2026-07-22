package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseStatusServeOptions(t *testing.T) {
	opts := parseStatusServeOptions([]string{"--serve", "--addr", "127.0.0.1:8123", "--session", "s1", "--no-open"})
	if opts.Addr != "127.0.0.1:8123" || opts.Session != "s1" || !opts.NoOpen {
		t.Fatalf("options = %#v", opts)
	}
}

func TestStatusDashboardLinkIsMarkdown(t *testing.T) {
	if got := statusDashboardLink("http://127.0.0.1:47632"); got != "[Open ProjX dashboard](http://127.0.0.1:47632/)" {
		t.Fatalf("link = %q", got)
	}
	if !statusLinkRequested([]string{"--ensure-server", "--link"}) || !statusLinkRequested([]string{"--ensure-server", "--link-if-relevant"}) || statusLinkRequested([]string{"--ensure-server"}) {
		t.Fatal("--link detection is not adapter-specific")
	}
}

func TestRequestedStatusDashboardLinkForProjXContext(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := "[Open ProjX dashboard](http://127.0.0.1:47632/)"
	if got := requestedStatusDashboardLink(root, []string{"--ensure-server", "--link-if-relevant"}, "http://127.0.0.1:47632"); got != want {
		t.Fatalf("relevant link = %q, want %q", got, want)
	}
}

func TestRequestedCodexSystemMessageForProjXContext(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	args := []string{"--ensure-server", "--codex-system-message-if-relevant"}
	output := requestedStatusDashboardOutput(root, args, "http://127.0.0.1:47632")
	var payload map[string]string
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("systemMessage output is not JSON: %q: %v", output, err)
	}
	if got := payload["systemMessage"]; got != "ProjX live status: http://127.0.0.1:47632/" {
		t.Fatalf("systemMessage = %q", got)
	}
}

func TestStatusDashboardIsRelevantWithGlobalStore(t *testing.T) {
	globalRoot := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", globalRoot)
	globalStore, err := openYoursStore()
	if err != nil {
		t.Fatal(err)
	}
	globalStore.Close()
	if got := globalStatusRoot(); got != globalRoot {
		t.Fatalf("global status root = %q, want %q", got, globalRoot)
	}
}

func TestBackgroundStatusServerServesSnapshot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "yours"))
	seedSessionStore(t, root)
	server, ln, err := startStatusServerBackground(root, "", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	resp, err := http.Get("http://" + ln.Addr().String() + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestStatusDashboardHandlerServesLiveSnapshot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "yours"))
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := openStoreSafe(root)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	h := statusDashboardHandler(root, "serve-session")
	api := httptest.NewRecorder()
	h.ServeHTTP(api, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if api.Code != http.StatusOK || api.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("API response = %d, cache %q", api.Code, api.Header().Get("Cache-Control"))
	}
	var snapshot StatusSnapshot
	if err := json.Unmarshal(api.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.ActiveRoot != root || !snapshot.Project {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	otherStore, err := openStoreSafe(other)
	if err != nil {
		t.Fatal(err)
	}
	otherStore.Close()
	body, _ := json.Marshal(map[string]string{"root": other, "session": "other-session"})
	activate := httptest.NewRecorder()
	h.ServeHTTP(activate, httptest.NewRequest(http.MethodPost, "/api/activate", bytes.NewReader(body)))
	if activate.Code != http.StatusNoContent {
		t.Fatalf("activate status = %d", activate.Code)
	}
	retargeted := httptest.NewRecorder()
	h.ServeHTTP(retargeted, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if err := json.Unmarshal(retargeted.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.ActiveRoot != other {
		t.Fatalf("retargeted root = %q, want %q", snapshot.ActiveRoot, other)
	}

	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "fetch('/api/status'") {
		t.Fatalf("dashboard page missing live status client: %d", page.Code)
	}
}

func TestStatusDashboardHandlerRejectsUnknownPath(t *testing.T) {
	rr := httptest.NewRecorder()
	statusDashboardHandler(t.TempDir(), "").ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestLatestStatusSessionFollowsNewestBreadcrumb(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	updateCrumb(root, "older", func(c *statusCrumb) { c.R = root })
	older := crumbPath(root, "older")
	oldTime := time.Now().Add(-time.Minute)
	if err := os.Chtimes(older, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	updateCrumb(root, "newest", func(c *statusCrumb) { c.R = root })
	if got := latestStatusSession(root); got != "newest" {
		t.Fatalf("latest session = %q", got)
	}
}
