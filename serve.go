package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	grants "github.com/BananaLabs-OSS/Pulp-grants"
	store "github.com/SirNiklas9/projx-store"
)

// serve.go — `projx-engine serve`: the ONE control plane every face pulls from.
// Neovim, the Workbench, a phone relay, and the CLI are all thin clients of the
// same HTTP/JSON + SSE surface. Endpoints use the /api/* convention so they slot
// directly into the Workbench. The headline capability over the CLI is the LIVE
// permission channel: pending grant requests streamed to whatever UI is
// listening, and approve/revoke flowing back — so the cage's approver can be any
// client.

// PermRequest is a pending live-permission request awaiting a human decision.
type PermRequest struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"` // "fs" | "net"
	Subject string `json:"subject"`
	Want    int    `json:"want"`
}

// PermEvent is streamed to SSE subscribers.
type PermEvent struct {
	Type     string       `json:"type"` // "pending" | "resolved"
	Req      *PermRequest `json:"req,omitempty"`
	ID       string       `json:"id,omitempty"`
	Decision string       `json:"decision,omitempty"` // "granted" | "denied"
}

// PermHub bridges the cage's grant broker to HTTP clients. It implements
// grants.Approver: on a miss it enqueues a pending request, streams it to
// subscribers, and blocks until a client decides — or a timeout fails closed.
// Approved ttl/permanent grants are persisted to the shared grants store, so a
// caged agent in another process (same .projx/grants.db) picks them up.
type PermHub struct {
	mu      sync.Mutex
	pending map[string]pendingReq
	subs    map[chan PermEvent]struct{}
	store   grants.GrantStore
	timeout time.Duration
	seq     int
}

type pendingReq struct {
	req PermRequest
	ch  chan grants.Decision
}

func newPermHub(st grants.GrantStore, timeout time.Duration) *PermHub {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &PermHub{
		pending: map[string]pendingReq{},
		subs:    map[chan PermEvent]struct{}{},
		store:   st,
		timeout: timeout,
	}
}

// Decide implements grants.Approver — called by a broker on a miss.
func (h *PermHub) Decide(req grants.Request) grants.Decision {
	h.mu.Lock()
	h.seq++
	id := fmt.Sprintf("p%d", h.seq)
	pr := PermRequest{ID: id, Kind: string(req.Kind), Subject: req.Subject, Want: req.Want}
	ch := make(chan grants.Decision, 1)
	h.pending[id] = pendingReq{req: pr, ch: ch}
	h.broadcast(PermEvent{Type: "pending", Req: &pr})
	h.mu.Unlock()

	select {
	case d := <-ch:
		return d
	case <-time.After(h.timeout):
		h.resolve(id, grants.Decision{Access: 0}, "denied") // fail closed
		return grants.Decision{Access: 0}
	}
}

// Resolve decides a pending request (called by the approve/deny endpoint). If
// granted with a ttl/permanent scope it is persisted to the grants store.
func (h *PermHub) Resolve(id string, d grants.Decision) bool {
	label := "denied"
	if d.Access > 0 {
		label = "granted"
	}
	ok := h.resolve(id, d, label)
	return ok
}

func (h *PermHub) resolve(id string, d grants.Decision, label string) bool {
	h.mu.Lock()
	p, ok := h.pending[id]
	if ok {
		delete(h.pending, id)
	}
	if ok {
		h.broadcast(PermEvent{Type: "resolved", ID: id, Decision: label})
	}
	h.mu.Unlock()
	if !ok {
		return false
	}
	if d.Access > 0 && (d.Scope == grants.ScopeTTL || d.Scope == grants.ScopePermanent) && h.store != nil {
		g := grants.Grant{
			ID: id, CellID: "agent", Kind: grants.Kind(p.req.Kind),
			Subject: p.req.Subject, Access: d.Access, Scope: d.Scope, GrantedBy: "serve",
		}
		_ = h.store.Put(g)
	}
	p.ch <- d
	return true
}

// Pending lists the currently-open requests.
func (h *PermHub) Pending() []PermRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]PermRequest, 0, len(h.pending))
	for _, p := range h.pending {
		out = append(out, p.req)
	}
	return out
}

func (h *PermHub) Subscribe() chan PermEvent {
	ch := make(chan PermEvent, 16)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *PermHub) Unsubscribe(ch chan PermEvent) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

// broadcast must be called with h.mu held.
func (h *PermHub) broadcast(ev PermEvent) {
	for ch := range h.subs {
		select {
		case ch <- ev:
		default: // drop for a slow subscriber rather than block the broker
		}
	}
}

// ── HTTP control plane ──────────────────────────────────────────────────────

type controlServer struct {
	root  string
	hub   *PermHub
	store grants.GrantStore

	runsMu sync.Mutex
	runs   map[string]*agentRun
	runSeq int
}

func (s *controlServer) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/perms/stream", s.handlePermStream)
	mux.HandleFunc("GET /api/perms/pending", s.handlePermPending)
	mux.HandleFunc("POST /api/perms/decide", s.handlePermDecide)
	mux.HandleFunc("GET /api/perms/grants", s.handleGrantsList)
	mux.HandleFunc("POST /api/perms/revoke", s.handleGrantsRevoke)
	mux.HandleFunc("GET /api/store", s.handleStoreList)
	mux.HandleFunc("POST /api/store", s.handleStorePut)
	mux.HandleFunc("DELETE /api/store", s.handleStoreDelete)
	mux.HandleFunc("GET /api/store/history", s.handleStoreHistory)
	mux.HandleFunc("POST /api/store/undo", s.handleStoreUndo)
	mux.HandleFunc("GET /api/profile", s.handleProfile)
	mux.HandleFunc("POST /api/agent/run", s.handleAgentRun)
	mux.HandleFunc("GET /api/agent/runs", s.handleAgentRuns)
	return mux
}

func (s *controlServer) handlePermStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch := s.hub.Subscribe()
	defer s.hub.Unsubscribe(ch)
	for _, p := range s.hub.Pending() {
		p := p
		writeSSE(w, PermEvent{Type: "pending", Req: &p})
	}
	fl.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			writeSSE(w, ev)
			fl.Flush()
		}
	}
}

func (s *controlServer) handlePermPending(w http.ResponseWriter, _ *http.Request) {
	writeJSONResp(w, s.hub.Pending())
}

// handlePermDecide body: {"id":"p1","access":1,"scope":"permanent","ttl_ms":0}
func (s *controlServer) handlePermDecide(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID     string `json:"id"`
		Access int    `json:"access"`
		Scope  string `json:"scope"`
		TTLMs  int64  `json:"ttl_ms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	d := grants.Decision{Access: body.Access, Scope: grants.Scope(body.Scope), TTL: time.Duration(body.TTLMs) * time.Millisecond}
	if !s.hub.Resolve(body.ID, d) {
		http.Error(w, "no such pending request", http.StatusNotFound)
		return
	}
	writeJSONResp(w, map[string]string{"status": "ok"})
}

func (s *controlServer) handleGrantsList(w http.ResponseWriter, _ *http.Request) {
	gs, _ := s.store.List("agent")
	writeJSONResp(w, gs)
}

// handleGrantsRevoke body: {"kind":"fs","subject":"secret/x"} or {"id":"..."}
func (s *controlServer) handleGrantsRevoke(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID      string `json:"id"`
		Kind    string `json:"kind"`
		Subject string `json:"subject"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.ID != "" {
		_ = s.store.Revoke(body.ID)
		writeJSONResp(w, map[string]string{"status": "ok"})
		return
	}
	n, _ := s.store.RevokeMatching(grants.Kind(body.Kind), body.Subject)
	writeJSONResp(w, map[string]int{"revoked": n})
}

// storeRecordView is the string-typed record shape the Workbench frontend speaks
// (kind/scope as names, not ints). The engine serve is the store's backend, so it
// emits exactly this shape and the cell pure-proxies it.
type storeRecordView struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Scope string `json:"scope"`
	Key   string `json:"key"`
	Body  string `json:"body"`
}

// handleStoreList — GET /api/store[?kind=&scope=] -> {"records":[...]}.
func (s *controlServer) handleStoreList(w http.ResponseWriter, r *http.Request) {
	st := openStore(s.root)
	defer st.Close()
	f := store.Filter{}
	if k := r.URL.Query().Get("kind"); k != "" {
		if kind, err := parseKindForList(k); err == nil {
			f.Kind = &kind
		}
	}
	if sc := r.URL.Query().Get("scope"); sc != "" {
		if scope, err := parseScopeName(sc); err == nil {
			f.Scope = &scope
		}
	}
	views := []storeRecordView{}
	for _, rec := range st.List(f) {
		views = append(views, storeRecordView{rec.ID, rec.Kind.String(), rec.Scope.String(), rec.Key, rec.Body})
	}
	writeJSONResp(w, map[string]any{"records": views})
}

// handleStorePut — POST /api/store {id,kind:int,scope:int,key,body}. Derives a
// stable id when blank (kind/slug(key)), journals the op, and regenerates CLAUDE.md.
func (s *controlServer) handleStorePut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID    string `json:"id"`
		Kind  int    `json:"kind"`
		Scope int    `json:"scope"`
		Key   string `json:"key"`
		Body  string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	st := openStore(s.root)
	defer st.Close()
	rec := store.Record{ID: body.ID, Kind: store.Kind(body.Kind), Scope: store.Scope(body.Scope), Key: body.Key, Body: body.Body}
	if strings.TrimSpace(rec.ID) == "" {
		base := slug(rec.Key)
		if base == "" {
			base = slug(rec.Body)
		}
		if base == "" {
			base = "rec"
		}
		rec.ID = rec.Kind.String() + "/" + base
	}
	before, had := st.Get(rec.ID)
	if err := st.Put(rec); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var bp *store.Record
	if had {
		bp = &before
	}
	recordStoreOp(s.root, "put", "ui", bp, &rec)
	syncProjectClaudeMD(s.root, st)
	writeJSONResp(w, map[string]bool{"ok": true})
}

// handleStoreDelete — DELETE /api/store?id=.
func (s *controlServer) handleStoreDelete(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	st := openStore(s.root)
	defer st.Close()
	before, had := st.Get(id)
	if err := st.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if had {
		recordStoreOp(s.root, "delete", "ui", &before, nil)
	}
	syncProjectClaudeMD(s.root, st)
	writeJSONResp(w, map[string]bool{"ok": true})
}

// handleStoreHistory — GET /api/store/history -> {"revisions":[...]} newest-first.
func (s *controlServer) handleStoreHistory(w http.ResponseWriter, _ *http.Request) {
	revs := readRevisions(s.root)
	for i, j := 0, len(revs)-1; i < j; i, j = i+1, j-1 {
		revs[i], revs[j] = revs[j], revs[i]
	}
	if revs == nil {
		revs = []storeRevision{}
	}
	writeJSONResp(w, map[string]any{"revisions": revs})
}

// handleStoreUndo — POST /api/store/undo: invert the most recent op, regen CLAUDE.md.
func (s *controlServer) handleStoreUndo(w http.ResponseWriter, _ *http.Request) {
	st := openStore(s.root)
	defer st.Close()
	rev, ok := undoLastStore(s.root, st)
	if !ok {
		writeJSONResp(w, map[string]any{"ok": false, "msg": "nothing to undo"})
		return
	}
	syncProjectClaudeMD(s.root, st)
	writeJSONResp(w, map[string]any{"ok": true, "undid": rev.Seq, "id": rev.ID})
}

// syncProjectClaudeMD regenerates the managed block in <root>/CLAUDE.md from the
// store, preserving user content. The engine owns CLAUDE.md; the renderer is the
// shared one in projx-store, so engine and cell produce identical output.
func syncProjectClaudeMD(root string, st store.Store) {
	path := filepath.Join(root, "CLAUDE.md")
	existing, _ := os.ReadFile(path)
	out := store.SpliceManagedBlock(string(existing), store.ManagedBlock(st))
	_ = os.WriteFile(path, []byte(out), 0o644)
}

func (s *controlServer) handleProfile(w http.ResponseWriter, _ *http.Request) {
	writeJSONResp(w, loadCageConfig(s.root))
}

func writeSSE(w http.ResponseWriter, ev PermEvent) {
	b, _ := json.Marshal(ev)
	fmt.Fprintf(w, "data: %s\n\n", b)
}

func writeJSONResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// runServeCmd starts the control plane: `projx-engine serve [--port N]`.
func runServeCmd(absRoot string, args []string) {
	port := "7878"
	for i := 0; i < len(args); i++ {
		if args[i] == "--port" && i+1 < len(args) {
			port = args[i+1]
			i++
		}
	}
	autoSeed(absRoot) // fresh project? seed floor + detected stack — no manual `store seed`
	gstore, err := grants.OpenSQLiteStore(grantsDBPath(absRoot))
	if err != nil {
		die("serve: open grants store: %v", err)
	}
	hub := newPermHub(gstore, 60*time.Second)
	srv := &controlServer{root: absRoot, hub: hub, store: gstore}

	fmt.Printf("projx-engine: control plane on http://127.0.0.1:%s (one surface — Neovim/Workbench/phone/CLI pull from here)\n", port)
	if err := http.ListenAndServe("127.0.0.1:"+port, srv.routes()); err != nil {
		die("serve: %v", err)
	}
}

func grantsDBPath(absRoot string) string {
	return filepath.Join(absRoot, ".projx", "grants.db")
}
