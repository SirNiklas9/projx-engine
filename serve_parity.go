package main

// serve_parity.go — brings `projx-engine serve` to parity with the engine cell so the
// Workbench (and any client) drives ONE control plane over the SAME shared projx-store
// definitions: routing, gate, dispatcher-mode, context slicing, and the agent spec.
// Every handler FLOATS: it resolves the target repo from the request (?root=, else the
// server default) and uses the composed (project+workspace+global) store cached for it —
// so one running engine serves any repo on demand.

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

// GET /api/route?task=[&root=] -> the decider's tier choice (pin/floor/@-override/keyword).
func (s *controlServer) handleRoute(w http.ResponseWriter, r *http.Request) {
	task := r.URL.Query().Get("task")
	st := s.storeFor(s.reqRoot(r))
	d := store.RouteDecide(st, task, nil) // deterministic; serve stays offline (no triage)
	writeJSONResp(w, map[string]any{"task": task, "class": d.Class, "cmd": d.Cmd, "source": d.Source, "reason": d.Reason})
}

// GET /api/gate[?root=] -> the off-limits paths as agent file-tool deny rules.
func (s *controlServer) handleGate(w http.ResponseWriter, r *http.Request) {
	writeJSONResp(w, map[string]any{"deny": store.DenyRules(s.storeFor(s.reqRoot(r)))})
}

// GET /api/gate/check?path=[&root=] -> {denied, pattern}: the path-vs-gate decision.
func (s *controlServer) handleGateCheck(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	pat, denied := store.GateDenied(s.storeFor(s.reqRoot(r)), path)
	writeJSONResp(w, map[string]any{"path": path, "denied": denied, "pattern": pat})
}

// GET /api/gate/dispatcher[?root=] -> {on}: whether trunk-dispatch discipline is enabled.
func (s *controlServer) handleGateDispatcher(w http.ResponseWriter, r *http.Request) {
	writeJSONResp(w, map[string]any{"on": store.DispatcherModeOn(s.storeFor(s.reqRoot(r)))})
}

// GET /api/context/floor?session=[&root=] -> the lean SessionStart floor (+ checkpoint).
func (s *controlServer) handleContextFloor(w http.ResponseWriter, r *http.Request) {
	writeJSONResp(w, map[string]any{"floor": buildSessionContext(s.reqRoot(r), serveSession(r), "", false)})
}

// GET /api/context/slice?task=[&root=] -> the stateless task-sliced preview.
func (s *controlServer) handleContextSlice(w http.ResponseWriter, r *http.Request) {
	task := r.URL.Query().Get("task")
	writeJSONResp(w, map[string]any{"task": task, "context": store.AgentContextForTask(s.storeFor(s.reqRoot(r)), task)})
}

// GET /api/context/delta?session=&task=[&root=] -> the per-message delta (law + new/changed).
func (s *controlServer) handleContextDelta(w http.ResponseWriter, r *http.Request) {
	writeJSONResp(w, map[string]any{"context": buildSessionContext(s.reqRoot(r), serveSession(r), r.URL.Query().Get("task"), false)})
}

// GET /api/agent/spec?task=[&root=] -> the full launch contract (class/cmd + deny + preamble).
func (s *controlServer) handleAgentSpec(w http.ResponseWriter, r *http.Request) {
	task := r.URL.Query().Get("task")
	st := s.storeFor(s.reqRoot(r))
	d := store.RouteDecide(st, task, nil)
	writeJSONResp(w, map[string]any{
		"task": task, "class": d.Class, "cmd": d.Cmd, "source": d.Source,
		"deny": store.DenyRules(st), "preamble": store.AgentContextForTask(st, task),
	})
}
