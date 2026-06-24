// Package main — red-team acceptance suite for the command/file/network broker.
//
// # Contract
//
// Every test in this file was originally skipped because the Permissive broker
// allows everything.  RestrictiveBroker (milestone 2) must:
//   - deny every bypass attempt in TestBrokerDeniesUnsafeActions, and
//   - allow every legitimately-allowlisted action in TestBrokerAllowsLegitActions.
//
// These are the formal acceptance criteria for the broker's teeth.
package main

import (
	"path/filepath"
	"testing"

	"github.com/SirNiklas9/projx-engine/internal/broker"
)

// TestBrokerDeniesUnsafeActions verifies that RestrictiveBroker blocks all
// known bypass categories.  This is the milestone-2 red-team acceptance suite.
func TestBrokerDeniesUnsafeActions(t *testing.T) {
	root := t.TempDir()
	b, err := broker.NewRestrictiveBroker(
		[]string{"git", "go"},
		root,
		[]string{"api.anthropic.com"},
	)
	if err != nil {
		t.Fatalf("NewRestrictiveBroker: %v", err)
	}

	attempts := []struct {
		name   string
		action broker.Action
	}{
		// ── original five ──────────────────────────────────────────────────────
		{"exec powershell (bare)", broker.Action{Kind: "exec", Target: "powershell"}},
		{"exec ssh (bare)", broker.Action{Kind: "exec", Target: "ssh"}},
		{"read outside secret (relative ..)", broker.Action{Kind: "read", Target: "../outside/secret.txt"}},
		{"exec bash (bare)", broker.Action{Kind: "exec", Target: "bash"}},
		{"net evil.example.com", broker.Action{Kind: "net", Target: "https://evil.example.com"}},

		// ── additional exec denials ────────────────────────────────────────────
		// Absolute Windows-style powershell path — absolute path must NOT bypass.
		{
			"exec powershell absolute Windows path",
			broker.Action{Kind: "exec", Target: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`},
		},
		// Absolute POSIX ssh path.
		{
			"exec /usr/bin/ssh absolute POSIX path",
			broker.Action{Kind: "exec", Target: "/usr/bin/ssh"},
		},

		// ── additional read/write denials ─────────────────────────────────────
		// An absolute path that is definitely outside the temp root.
		{
			"read absolute path outside root",
			broker.Action{Kind: "read", Target: filepath.Join(filepath.VolumeName(root)+string(filepath.Separator), "tmp", "evil", "secret.txt")},
		},
		// Deep relative traversal — filepath.Clean reduces this but it still escapes.
		{
			"read deep .. traversal",
			broker.Action{Kind: "read", Target: filepath.Join("..", "..", "..", "Windows", "System32", "x.dll")},
		},
	}

	for _, tt := range attempts {
		t.Run(tt.name, func(t *testing.T) {
			d := b.Check(tt.action)
			if d.Allow {
				t.Errorf("broker allowed unsafe action %+v — must be denied by RestrictiveBroker (reason: %q)", tt.action, d.Reason)
			}
			if d.Reason == "" {
				t.Errorf("deny has empty Reason for action %+v", tt.action)
			}
		})
	}
}

// TestBrokerAllowsLegitActions proves that RestrictiveBroker is not simply a
// deny-all: actions that are explicitly allowlisted must return Allow==true.
func TestBrokerAllowsLegitActions(t *testing.T) {
	root := t.TempDir()
	b, err := broker.NewRestrictiveBroker(
		[]string{"git", "go"},
		root,
		[]string{"api.anthropic.com"},
	)
	if err != nil {
		t.Fatalf("NewRestrictiveBroker: %v", err)
	}

	legit := []struct {
		name   string
		action broker.Action
	}{
		// Allowlisted exec binaries.
		{"exec git (bare)", broker.Action{Kind: "exec", Target: "git"}},
		{"exec go (bare)", broker.Action{Kind: "exec", Target: "go"}},

		// Read a path inside the project root.
		{"read inside root", broker.Action{Kind: "read", Target: filepath.Join(root, "subdir", "file.go")}},

		// Net: allowlisted hostname with real-ish path.
		{"net api.anthropic.com", broker.Action{Kind: "net", Target: "https://api.anthropic.com/v1/messages"}},
	}

	for _, tt := range legit {
		t.Run(tt.name, func(t *testing.T) {
			d := b.Check(tt.action)
			if !d.Allow {
				t.Errorf("broker denied legitimate action %+v — must be allowed by RestrictiveBroker (reason: %q)", tt.action, d.Reason)
			}
		})
	}
}
