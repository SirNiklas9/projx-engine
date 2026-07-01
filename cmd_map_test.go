package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	store "github.com/SirNiklas9/projx-store"
)

// writeGoFile drops a .go source file under root for the parser to pick up.
func writeGoFile(t *testing.T, root, rel, src string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestMapSyncIndexesAndSlices proves `map sync` materializes symbols as declared-
// structure records with anchors, prunes stale ones, and that the Step-5 task-slice
// then injects ONLY the relevant symbol's anchor.
func TestMapSyncIndexesAndSlices(t *testing.T) {
	root := t.TempDir()
	writeGoFile(t, root, "auth/login.go", `package auth

// LoginUser verifies a credential and returns a token.
func LoginUser(user, pass string) (string, error) {
	return "", nil
}
`)
	writeGoFile(t, root, "billing/charge.go", `package billing

// ChargeCard runs a stripe charge.
func ChargeCard(cents int) error {
	return nil
}
`)

	runMapSync(root, nil)

	// The store now holds two code-map records with anchors.
	st := openStore(root)
	mapRecs := map[string]store.Record{}
	for _, r := range st.List(store.OfKind(store.KDeclaredStructure)) {
		if r.Origin == mapRecordOrigin {
			mapRecs[r.Key] = r
		}
	}
	st.Close()
	if len(mapRecs) != 2 {
		t.Fatalf("want 2 code-map records, got %d: %v", len(mapRecs), keysOf(mapRecs))
	}
	loginRec, ok := mapRecs["code/auth/login/loginuser"]
	if !ok {
		t.Fatalf("missing login code-map record; have %v", keysOf(mapRecs))
	}
	var body mapAnchorBody
	if err := json.Unmarshal([]byte(loginRec.Body), &body); err != nil {
		t.Fatal(err)
	}
	if body.Anchor != "auth/login.go:4" {
		t.Errorf("login anchor = %q, want auth/login.go:4", body.Anchor)
	}
	if !strings.Contains(body.Signature, "func LoginUser(user, pass string)") {
		t.Errorf("login signature = %q", body.Signature)
	}
	if !strings.Contains(body.Doc, "verifies a credential") {
		t.Errorf("login doc = %q", body.Doc)
	}

	// Task-slice: a prompt about login pulls the login anchor, NOT the billing one.
	sliced := captureStdout(t, func() { runSessionContext(root, "map-sess", "fix the loginuser flow", false) })
	if !strings.Contains(sliced, "auth/login.go:4") {
		t.Error("task slice missing the login anchor")
	}
	if strings.Contains(sliced, "billing/charge.go") || strings.Contains(sliced, "ChargeCard") {
		t.Error("task slice leaked the unrelated billing symbol")
	}

	// Prune: delete the billing file, re-sync → its record is removed, login remains.
	if err := os.Remove(filepath.Join(root, "billing", "charge.go")); err != nil {
		t.Fatal(err)
	}
	runMapSync(root, nil)
	st = openStore(root)
	defer st.Close()
	got := 0
	var keys []string
	for _, r := range st.List(store.OfKind(store.KDeclaredStructure)) {
		if r.Origin == mapRecordOrigin {
			got++
			keys = append(keys, r.Key)
		}
	}
	if got != 1 || keys[0] != "code/auth/login/loginuser" {
		t.Errorf("after prune want only the login record, got %d: %v", got, keys)
	}
}

func keysOf(m map[string]store.Record) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
