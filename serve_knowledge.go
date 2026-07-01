package main

// serve_knowledge.go — cross-machine knowledge sync over the composed store. The engine
// owns the global level (the machine-wide "yours" store, mounted for every root), so it is
// the AUTHORITY for cross-machine Global sync: export full records for a scope, and merge a
// posted set (last-write-wins via store.Merge) into the owning level. The Workbench relays
// its cross-machine transport HERE so send AND receive operate on the engine's global store
// — never a divergent local copy. Full store.Record JSON (with UpdatedAt) so LWW holds.

import (
	"encoding/json"
	"net/http"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

func knowledgeScopeFilter(q string) (store.Filter, string) {
	switch strings.ToLower(strings.TrimSpace(q)) {
	case "workspace":
		return store.InScope(store.ScopeWorkspace), "workspace"
	case "project":
		return store.InScope(store.ScopeProject), "project"
	case "all":
		return store.Filter{}, "all"
	default:
		return store.InScope(store.ScopeGlobal), "global"
	}
}

// GET /api/knowledge/export?scope=global[&root=] -> {scope,count,records:[full store.Record]}.
func (s *controlServer) handleKnowledgeExport(w http.ResponseWriter, r *http.Request) {
	st := s.storeFor(s.reqRoot(r))
	f, name := knowledgeScopeFilter(r.URL.Query().Get("scope"))
	recs := st.List(f)
	if recs == nil {
		recs = []store.Record{}
	}
	writeJSONResp(w, map[string]any{"scope": name, "count": len(recs), "records": recs})
}

// POST /api/knowledge/merge {records:[full store.Record]}[?root=] -> reconcile report.
// Consolidates the posted records into the composed store (LWW; ties keep base). Put routes
// each record to its owning level, so global records land in the machine-wide yours store.
func (s *controlServer) handleKnowledgeMerge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Records []store.Record `json:"records"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	root := s.reqRoot(r)
	st := s.storeFor(root)
	res := store.Merge(st.List(store.Filter{}), req.Records)
	persisted := 0
	for _, rec := range res.Merged {
		if st.Put(rec) == nil {
			persisted++
		}
	}
	syncProjectClaudeMD(root, st)
	writeJSONResp(w, map[string]any{
		"ok": true, "incoming": len(req.Records), "persisted": persisted,
		"added": res.Added, "unchanged": res.Unchanged, "autoResolved": res.AutoWon, "needsReview": res.NeedReview,
	})
}
