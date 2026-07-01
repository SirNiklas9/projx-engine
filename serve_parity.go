package main

// serve_parity.go — brings `projx-engine serve` to parity with the engine cell so the
// Workbench (and any client) drives ONE control plane over the SAME shared projx-store
// definitions: routing, gate, dispatcher-mode, context slicing, and the agent spec —
// not just the store + live-perms. Cell- and serve-assembled answers are identical by
// construction (both call the same store functions). This is what lets the Workbench
// retire its own store/gate/route/context copies and relay to the engine.

import (
	"net/http"

	store "github.com/SirNiklas9/projx-store"
)

func serveSession(r *http.Request) string {
	if sid := r.URL.Query().Get("session"); sid != "" {
		return sid
	}
	return "default"
}

// GET /api/route?task= -> the decider's tier choice (pin/floor/@-override/keyword).
func (s *controlServer) handleRoute(w http.ResponseWriter, r *http.Request) {
	task := r.URL.Query().Get("task")
	st := openStore(s.root)
	defer st.Close()
	d := store.RouteDecide(st, task, nil) // deterministic; serve stays offline (no triage)
	writeJSONResp(w, map[string]any{"task": task, "class": d.Class, "cmd": d.Cmd, "source": d.Source, "reason": d.Reason})
}

// GET /api/gate -> the off-limits paths as agent file-tool deny rules.
func (s *controlServer) handleGate(w http.ResponseWriter, r *http.Request) {
	st := openStore(s.root)
	defer st.Close()
	writeJSONResp(w, map[string]any{"deny": store.DenyRules(st)})
}

// GET /api/gate/check?path= -> {denied, pattern}: the path-vs-gate decision.
func (s *controlServer) handleGateCheck(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	st := openStore(s.root)
	defer st.Close()
	pat, denied := store.GateDenied(st, path)
	writeJSONResp(w, map[string]any{"path": path, "denied": denied, "pattern": pat})
}

// GET /api/gate/dispatcher -> {on}: whether trunk-dispatch discipline is enabled.
func (s *controlServer) handleGateDispatcher(w http.ResponseWriter, r *http.Request) {
	st := openStore(s.root)
	defer st.Close()
	writeJSONResp(w, map[string]any{"on": store.DispatcherModeOn(st)})
}

// GET /api/context/floor?session= -> the lean SessionStart floor (+ checkpoint).
func (s *controlServer) handleContextFloor(w http.ResponseWriter, r *http.Request) {
	writeJSONResp(w, map[string]any{"floor": buildSessionContext(s.root, serveSession(r), "", false)})
}

// GET /api/context/slice?task= -> the stateless task-sliced preview.
func (s *controlServer) handleContextSlice(w http.ResponseWriter, r *http.Request) {
	task := r.URL.Query().Get("task")
	st := openStore(s.root)
	defer st.Close()
	writeJSONResp(w, map[string]any{"task": task, "context": store.AgentContextForTask(st, task)})
}

// GET /api/context/delta?session=&task= -> the per-message delta (law + new/changed).
func (s *controlServer) handleContextDelta(w http.ResponseWriter, r *http.Request) {
	writeJSONResp(w, map[string]any{"context": buildSessionContext(s.root, serveSession(r), r.URL.Query().Get("task"), false)})
}

// GET /api/agent/spec?task= -> the full launch contract (class/cmd + deny + preamble).
func (s *controlServer) handleAgentSpec(w http.ResponseWriter, r *http.Request) {
	task := r.URL.Query().Get("task")
	st := openStore(s.root)
	defer st.Close()
	d := store.RouteDecide(st, task, nil)
	writeJSONResp(w, map[string]any{
		"task": task, "class": d.Class, "cmd": d.Cmd, "source": d.Source,
		"deny": store.DenyRules(st), "preamble": store.AgentContextForTask(st, task),
	})
}
