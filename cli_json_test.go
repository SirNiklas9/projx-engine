package main

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	store "github.com/SirNiklas9/projx-store"
)

func TestTakeJSONFlagAcceptsExplicitBooleanValues(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantArgs []string
		wantJSON bool
	}{
		{name: "bare flag", args: []string{"list", "--json"}, wantArgs: []string{"list"}, wantJSON: true},
		{name: "explicit true", args: []string{"--json=true", "list"}, wantArgs: []string{"list"}, wantJSON: true},
		{name: "explicit false", args: []string{"list", "--json=false"}, wantArgs: []string{"list"}, wantJSON: false},
		{name: "last value wins", args: []string{"--json", "list", "--json=false"}, wantArgs: []string{"list"}, wantJSON: false},
		{name: "invalid value is preserved", args: []string{"list", "--json=maybe"}, wantArgs: []string{"list", "--json=maybe"}, wantJSON: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotArgs, gotJSON := takeJSONFlag(tt.args)
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("args = %#v, want %#v", gotArgs, tt.wantArgs)
			}
			if gotJSON != tt.wantJSON {
				t.Errorf("json = %v, want %v", gotJSON, tt.wantJSON)
			}
		})
	}
}

func TestCoreReadCommandsEmitPureJSON(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "yours"))
	st := openStore(root)
	if err := st.Put(store.Record{
		ID: "doc/checkout", Kind: store.KDoc, Scope: store.ScopeProject,
		Key: "checkout", Body: "Checkout uses one payment path.", Status: store.StatusActive,
		Provenance: store.ProvenanceHuman, ClaimClass: "stable",
		Evidence: "checkout integration test",
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	commands := map[string]func(){
		"store get":   func() { storeGet(root, []string{"doc/checkout", "--json"}) },
		"store list":  func() { storeList(root, []string{"--json"}) },
		"store query": func() { storeQuery(root, []string{"checkout", "--json"}) },
		"route":       func() { runRouteCmd(root, []string{"review checkout", "--json"}) },
		"impact":      func() { runImpactCmd(root, []string{"Checkout", "--json"}) },
		"map list":    func() { runMapCmd(root, []string{"list", "--json"}) },
		"mode":        func() { runModeCmd(root, []string{"dispatcher", "--json"}) },
		"gate list":   func() { runGateCmd(root, []string{"list", "--json"}) },
	}
	for name, command := range commands {
		t.Run(name, func(t *testing.T) {
			out := captureStdout(t, command)
			var value any
			if err := json.Unmarshal([]byte(out), &value); err != nil {
				t.Fatalf("%s output is not pure JSON: %q: %v", name, out, err)
			}
		})
	}

	out := captureStdout(t, func() { storeGet(root, []string{"doc/checkout", "--json"}) })
	var record map[string]any
	if err := json.Unmarshal([]byte(out), &record); err != nil {
		t.Fatalf("decode store get JSON: %v", err)
	}
	if record["claim_class"] != "stable" || record["evidence"] != "checkout integration test" {
		t.Fatalf("store get JSON omitted lifecycle review metadata: %#v", record)
	}
}
