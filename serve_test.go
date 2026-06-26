package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	grants "github.com/BananaLabs-OSS/Pulp-grants"
)

// TestPermHubApproveFlow proves the live-permission control plane end to end: a
// broker miss blocks, the pending request is visible over HTTP, a client approves
// it, the broker is granted, the grant persists, and a client revokes it — all
// through the same surface any face (Neovim/Workbench/phone) would use.
func TestPermHubApproveFlow(t *testing.T) {
	gs := grants.NewMemStore()
	hub := newPermHub(gs, 2*time.Second)
	srv := &controlServer{root: t.TempDir(), hub: hub, store: gs}
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	// A broker (simulated cage) asks for a decision — blocks until a client acts.
	done := make(chan int, 1)
	go func() {
		d := hub.Decide(grants.Request{Kind: grants.KindFS, Subject: "secret/x", Want: 1})
		done <- d.Access
	}()

	// The pending request becomes visible over the control plane.
	var pend []PermRequest
	for i := 0; i < 50 && len(pend) == 0; i++ {
		resp, err := http.Get(ts.URL + "/api/perms/pending")
		if err != nil {
			t.Fatal(err)
		}
		json.NewDecoder(resp.Body).Decode(&pend)
		resp.Body.Close()
		if len(pend) == 0 {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if len(pend) != 1 || pend[0].Subject != "secret/x" {
		t.Fatalf("expected 1 pending for secret/x, got %+v", pend)
	}

	// Approve it (permanent) via the control plane.
	body, _ := json.Marshal(map[string]any{"id": pend[0].ID, "access": 1, "scope": "permanent"})
	resp, err := http.Post(ts.URL+"/api/perms/decide", "application/json", bytes.NewReader(body))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("decide failed: %v status=%d", err, statusOf(resp))
	}
	resp.Body.Close()

	select {
	case acc := <-done:
		if acc != 1 {
			t.Fatalf("broker should have been granted Read, got %d", acc)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("broker.Decide did not return after approval")
	}

	// The grant persisted; list + revoke it via the control plane.
	var gl []grants.Grant
	resp, _ = http.Get(ts.URL + "/api/perms/grants")
	json.NewDecoder(resp.Body).Decode(&gl)
	resp.Body.Close()
	if len(gl) != 1 {
		t.Fatalf("expected 1 persisted grant, got %d", len(gl))
	}

	rb, _ := json.Marshal(map[string]string{"kind": "fs", "subject": "secret/x"})
	resp, _ = http.Post(ts.URL+"/api/perms/revoke", "application/json", bytes.NewReader(rb))
	resp.Body.Close()
	if _, ok := gs.Lookup(grants.KindFS, "secret/x", 1); ok {
		t.Fatal("grant should be revoked after /api/perms/revoke")
	}
}

func TestPermHubFailClosed(t *testing.T) {
	hub := newPermHub(grants.NewMemStore(), 40*time.Millisecond)
	d := hub.Decide(grants.Request{Kind: grants.KindNet, Subject: "evil.test", Want: 1})
	if d.Access != 0 {
		t.Fatalf("a timed-out request must fail closed, got %d", d.Access)
	}
}

func statusOf(r *http.Response) int {
	if r == nil {
		return 0
	}
	return r.StatusCode
}
