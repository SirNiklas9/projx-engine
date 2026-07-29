package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	store "github.com/SirNiklas9/projx-store"
)

func TestInstallProjectAgentInstructionsPreservesUserFiles(t *testing.T) {
	root := t.TempDir()
	agentsPath := filepath.Join(root, "AGENTS.md")
	agentsUser := append([]byte{0xef, 0xbb, 0xbf}, []byte("# Team\r\n\r\nKeep me.\r\n")...)
	if err := os.WriteFile(agentsPath, agentsUser, 0o640); err != nil {
		t.Fatal(err)
	}
	claudePath := filepath.Join(root, "CLAUDE.md")
	claudeUser := []byte("before\n\n" + store.ManagedBlock(store.NewMem()) + "\n\nafter\n")
	if err := os.WriteFile(claudePath, claudeUser, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := installProjectAgentInstructions(root); err != nil {
		t.Fatal(err)
	}
	agents, _ := os.ReadFile(agentsPath)
	if !bytes.HasPrefix(agents, agentsUser) || !bytes.Contains(agents, []byte(store.ProjXManagedBeginV1)) {
		t.Fatalf("AGENTS.md user bytes were not preserved: %q", agents)
	}
	claude, _ := os.ReadFile(claudePath)
	if !bytes.Contains(claude, []byte("before")) || !bytes.Contains(claude, []byte("after")) {
		t.Fatalf("CLAUDE.md user bytes were not preserved: %q", claude)
	}
	if bytes.Contains(claude, []byte(store.ClaudeBegin)) || !bytes.Contains(claude, []byte("@AGENTS.md")) {
		t.Fatalf("CLAUDE.md was not migrated to the import shim: %q", claude)
	}

	firstAgents := append([]byte(nil), agents...)
	firstClaude := append([]byte(nil), claude...)
	if err := installProjectAgentInstructions(root); err != nil {
		t.Fatal(err)
	}
	agents, _ = os.ReadFile(agentsPath)
	claude, _ = os.ReadFile(claudePath)
	if !bytes.Equal(firstAgents, agents) || !bytes.Equal(firstClaude, claude) {
		t.Fatal("second install was not byte-idempotent")
	}
}

func TestInstallProjectAgentInstructionsRefusesMalformedFileWithoutTouchingClaude(t *testing.T) {
	root := t.TempDir()
	broken := []byte(store.ProjXManagedBeginV1 + "\nmissing end")
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), broken, 0o644); err != nil {
		t.Fatal(err)
	}
	claude := []byte("user claude content\n")
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), claude, 0o644); err != nil {
		t.Fatal(err)
	}

	err := installProjectAgentInstructions(root)
	if !errors.Is(err, store.ErrManagedBlockMalformed) {
		t.Fatalf("error = %v", err)
	}
	gotAgents, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	gotClaude, _ := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if !bytes.Equal(gotAgents, broken) || !bytes.Equal(gotClaude, claude) {
		t.Fatal("malformed-file refusal changed user content")
	}
}
