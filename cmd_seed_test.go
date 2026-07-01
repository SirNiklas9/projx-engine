package main

import (
	"os"
	"path/filepath"
	"testing"

	store "github.com/SirNiklas9/projx-store"
)

// TestSeedApplyAndPrune proves the bake cycle: apply upserts records, re-applying an
// edited file prunes the ones removed, and empty entries are skipped.
func TestSeedApplyAndPrune(t *testing.T) {
	root := t.TempDir()
	seed := filepath.Join(root, "projx.seed.toml")
	write := func(s string) {
		if err := os.WriteFile(seed, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(`
[[convention]]
key = "routing"
body = "cheapest-first"

[[convention]]
key = "deploy"
body = "main = prod"

[[gate]]
pattern = "secret/**"

[[doc]]
key = "billing/webhook"
anchor = "svc/router.go:88"
body = "webhook lives in Setup"

[[convention]]
key = ""
`)
	applySeedFile(root, seed)

	st := openStore(root)
	conv := map[string]store.Record{}
	for _, r := range st.List(store.OfKind(store.KConvention)) {
		conv[r.Key] = r
	}
	st.Close()
	if _, ok := conv["routing"]; !ok {
		t.Error("routing convention not baked")
	}
	if _, ok := conv["deploy"]; !ok {
		t.Error("deploy convention not baked")
	}
	if _, ok := conv[""]; ok {
		t.Error("empty convention entry should be skipped")
	}

	// The doc's anchor is prepended so it survives the one-line summary.
	st = openStore(root)
	var docBody string
	for _, r := range st.List(store.OfKind(store.KDoc)) {
		if r.Key == "billing/webhook" {
			docBody = r.Body
		}
	}
	st.Close()
	if len(docBody) < 4 || docBody[:3] != "svc" {
		t.Errorf("doc body should start with the anchor, got %q", docBody)
	}

	// Edit: drop the "deploy" convention → re-apply prunes it.
	write(`
[[convention]]
key = "routing"
body = "cheapest-first"
`)
	applySeedFile(root, seed)
	st = openStore(root)
	defer st.Close()
	for _, r := range st.List(store.OfKind(store.KConvention)) {
		if r.Key == "deploy" {
			t.Error("deploy convention should have been pruned on re-bake")
		}
	}
}
