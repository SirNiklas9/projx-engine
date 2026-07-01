package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestParseSelectedKeys covers JSON-array extraction and the candidate filter (the
// model cannot invent keys).
func TestParseSelectedKeys(t *testing.T) {
	cands := []string{"auth/login", "billing/checkout", "infra/db"}
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"clean array", `["billing/checkout","auth/login"]`, []string{"billing/checkout", "auth/login"}},
		{"array with prose", "Relevant:\n[\"infra/db\"]\nthat's it", []string{"infra/db"}},
		{"invented key dropped", `["billing/checkout","made/up"]`, []string{"billing/checkout"}},
		{"empty array", `[]`, nil},
		{"no array", `none are relevant`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseSelectedKeys(c.in, cands)
			if len(got) != len(c.want) {
				t.Fatalf("parseSelectedKeys(%q) = %v, want %v", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("index %d: got %q want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

// TestNewSelectorFuncGating proves v2 is opt-in: nil unless PROJX_SMART_CONTEXT is set,
// and even then nil without a cheap model available.
func TestNewSelectorFuncGating(t *testing.T) {
	t.Setenv("PROJX_TRIAGE_API_KEY", "")
	t.Setenv("PROJX_AGENT_CMD", "")
	t.Setenv("PROJX_AGENT", "")

	t.Setenv("PROJX_SMART_CONTEXT", "")
	t.Setenv("PATH", t.TempDir())
	if newSelectorFunc(nil) != nil {
		t.Error("selector should be nil when PROJX_SMART_CONTEXT is unset")
	}

	// Opted in but no model on PATH → still nil.
	t.Setenv("PROJX_SMART_CONTEXT", "1")
	if newSelectorFunc(nil) != nil {
		t.Error("selector should be nil with no cheap model available")
	}

	// Opted in + a fake agent CLI on PATH → non-nil.
	bindir := t.TempDir()
	fake := filepath.Join(bindir, "claude")
	if runtime.GOOS == "windows" {
		fake += ".exe"
	}
	if err := os.WriteFile(fake, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bindir)
	if newSelectorFunc(nil) == nil {
		t.Error("selector should be non-nil when opted in with an agent CLI present")
	}
}
