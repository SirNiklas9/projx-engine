package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
)

// serve_agent.go — `/api/agent/run`: the control plane LAUNCHES the agent, which
// is what closes the live loop in-process. Because serve runs the agent, the
// cage's broker uses serve's PermHub directly as its approver — a caged miss
// streams to clients over /api/perms/stream and unblocks on approval. No
// cross-process bridge. Uncaged is the cross-platform default; caged is opt-in
// and Linux-only (launchAgentCaged is platform-tagged).

type agentRun struct {
	ID    string `json:"id"`
	Task  string `json:"task"`
	Caged bool   `json:"caged"`
	State string `json:"state"` // "running" | "done" | "error"
	Exit  int    `json:"exit"`
	Err   string `json:"err,omitempty"`
}

// handleAgentRun body: {"task":"...","caged":true}. Returns the run immediately
// (running); poll /api/agent/runs for completion. Live grant requests surface on
// /api/perms/stream while it runs.
func (s *controlServer) handleAgentRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Task  string `json:"task"`
		Caged bool   `json:"caged"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Task == "" {
		http.Error(w, "bad request: need {task}", http.StatusBadRequest)
		return
	}
	writeJSONResp(w, s.startRun(body.Task, body.Caged))
}

func (s *controlServer) startRun(task string, caged bool) *agentRun {
	s.runsMu.Lock()
	s.runSeq++
	run := &agentRun{ID: fmt.Sprintf("r%d", s.runSeq), Task: task, Caged: caged, State: "running"}
	if s.runs == nil {
		s.runs = map[string]*agentRun{}
	}
	s.runs[run.ID] = run
	s.runsMu.Unlock()

	go func() {
		var exit int
		var err error
		if caged {
			exit, err = launchAgentCaged(s.root, task, s.hub, s.store)
		} else {
			exit, err = launchAgentUncaged(s.root, task)
		}
		s.runsMu.Lock()
		run.Exit = exit
		if err != nil {
			run.State, run.Err = "error", err.Error()
		} else {
			run.State = "done"
		}
		s.runsMu.Unlock()
	}()
	return run
}

func (s *controlServer) handleAgentRuns(w http.ResponseWriter, _ *http.Request) {
	s.runsMu.Lock()
	out := make([]*agentRun, 0, len(s.runs))
	for _, r := range s.runs {
		out = append(out, r)
	}
	s.runsMu.Unlock()
	writeJSONResp(w, out)
}

// launchAgentUncaged runs the agent directly (no cage) — cross-platform.
func launchAgentUncaged(absRoot, task string) (int, error) {
	name, argv, env := agentLaunch(absRoot, task)
	cmd := exec.Command(name, argv...)
	cmd.Dir = absRoot
	cmd.Env = append(os.Environ(), kvSlice(env)...)
	cmd.SysProcAttr = quietSysProcAttr()
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), nil
		}
		return 0, err
	}
	return 0, nil
}
