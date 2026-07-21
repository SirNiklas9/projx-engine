package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	grants "github.com/BananaLabs-OSS/Pulp-grants"
)

// TestAgentRunUncaged proves the control plane launches an agent and tracks it
// to completion through /api/agent/run + /api/agent/runs. It uses a trivial
// agent ("true") for determinism; the caged path is the composition of the
// already-proven RunCagedAgent (Pulp-cage) + the already-proven PermHub.
func TestAgentRunUncaged(t *testing.T) {
	gs := grants.NewMemStore()
	srv := &controlServer{root: t.TempDir(), hub: newPermHub(gs, time.Second), store: gs}
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	noopAgent := "true"
	if runtime.GOOS == "windows" {
		noopAgent = "cmd /c exit 0"
	}
	t.Setenv("PROJX_AGENT_CMD", noopAgent) // trivial agent: exits 0, ignores args

	body, _ := json.Marshal(map[string]any{"task": "noop", "caged": false})
	resp, err := http.Post(ts.URL+"/api/agent/run", "application/json", bytes.NewReader(body))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("run request failed: %v status=%d", err, statusOf(resp))
	}
	var run agentRun
	json.NewDecoder(resp.Body).Decode(&run)
	resp.Body.Close()
	if run.ID == "" || run.State != "running" {
		t.Fatalf("expected a running run, got %+v", run)
	}

	var runs []agentRun
	for i := 0; i < 100; i++ {
		r, _ := http.Get(ts.URL + "/api/agent/runs")
		runs = nil
		json.NewDecoder(r.Body).Decode(&runs)
		r.Body.Close()
		if len(runs) == 1 && runs[0].State != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(runs) != 1 || runs[0].State != "done" || runs[0].Exit != 0 {
		t.Fatalf("expected one completed run (done, exit 0), got %+v", runs)
	}
}
