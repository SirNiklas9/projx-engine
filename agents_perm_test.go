package main

import (
	store "github.com/SirNiklas9/projx-store"
	"strings"
	"testing"
)

func TestClaudeAllowedToolsArgs(t *testing.T) {
	// empty list → no flag (worker prompts for everything, old behavior)
	if got := claudeAllowedToolsArgs(nil); got != nil {
		t.Fatalf("empty list should yield nil args, got %v", got)
	}

	args := claudeAllowedToolsArgs([]string{"git", "go"})
	joined := strings.Join(args, " ")
	for _, want := range []string{"--allowedTools", "Bash(git:*)", "Bash(go:*)", "Read", "Grep", "Glob"} {
		if !strings.Contains(joined, want) {
			t.Errorf("allowed-tools args missing %q; got %q", want, joined)
		}
	}
	// the flag must lead so the following values are its variadic list
	if args[0] != "--allowedTools" {
		t.Errorf("first arg must be --allowedTools, got %q", args[0])
	}
}

func TestWorkerAllowFloorFromStore(t *testing.T) {
	// The safe-list is DATA: with a nil store the reader falls back to the seeded
	// default, which must still let a worker build/test/commit unattended.
	set := map[string]bool{}
	for _, b := range store.WorkerAllowBins(nil) {
		set[b] = true
	}
	for _, must := range []string{"git", "go", "projx-engine"} {
		if !set[must] {
			t.Errorf("default worker allow-list missing required %q", must)
		}
	}
}

func TestIsClaudeAgent(t *testing.T) {
	cases := map[string]bool{
		`C:\tools\claude.exe`:    true,
		"/usr/local/bin/claude":  true,
		"/opt/other/gpt-cli":     false,
		`C:\bin\codex.exe`:       false,
	}
	for path, want := range cases {
		if got := isClaudeAgent(path); got != want {
			t.Errorf("isClaudeAgent(%q) = %v, want %v", path, got, want)
		}
	}
}
