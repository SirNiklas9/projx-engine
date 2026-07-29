package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

	store "github.com/SirNiklas9/projx-store"
)

func TestMCPStoreCommitStagesCandidate(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "yours"))
	params, _ := json.Marshal(map[string]any{
		"name": "store_commit",
		"arguments": map[string]any{
			"root": root, "kind": "doc", "key": "runtime/finding", "body": "observed",
			"claim_class": "volatile", "evidence": "go test ./...",
		},
	})
	resp := mcpToolCall(mcpReq{ID: json.RawMessage("1"), Params: params}, root)
	if resp.Error != nil {
		t.Fatalf("store_commit failed: %+v", resp.Error)
	}
	st := openStore(root)
	rec, ok := st.Get("doc/runtime-finding")
	st.Close()
	if !ok || rec.LifecycleStatus() != store.StatusCandidate || rec.Provenance != store.ProvenanceAgent {
		t.Fatalf("MCP discovery was not staged safely: %+v, found=%v", rec, ok)
	}
	if rec.ClaimClass != "volatile" || rec.Evidence != "go test ./..." {
		t.Fatalf("MCP lifecycle metadata missing: %+v", rec)
	}
}

func TestMCPStoreCommitRejectsTransientOrUnevidencedKnowledge(t *testing.T) {
	for _, tc := range []struct {
		name       string
		claimClass string
		evidence   string
	}{
		{name: "transient class", claimClass: "turn-observation", evidence: "go test ./..."},
		{name: "missing evidence", claimClass: "stable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "yours"))
			params, _ := json.Marshal(map[string]any{
				"name": "store_commit",
				"arguments": map[string]any{
					"root": root, "kind": "doc", "key": "turn/progress", "body": "implemented one slice",
					"claim_class": tc.claimClass, "evidence": tc.evidence,
				},
			})
			resp := mcpToolCall(mcpReq{ID: json.RawMessage("1"), Params: params}, root)
			result, ok := resp.Result.(map[string]any)
			if !ok || result["isError"] != true {
				t.Fatalf("store_commit result = %#v, want hygiene rejection", resp.Result)
			}
			st := openStore(root)
			_, exists := st.Get("doc/turn-progress")
			st.Close()
			if exists {
				t.Fatal("rejected transient knowledge was persisted")
			}
		})
	}
}
