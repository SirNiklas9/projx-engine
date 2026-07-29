package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	store "github.com/SirNiklas9/projx-store"
)

// installProjectAgentInstructions installs the small discovery contract in
// AGENTS.md and a CLAUDE.md import shim. Both files remain user-owned.
func installProjectAgentInstructions(root string) error {
	agentsPath := filepath.Join(root, "AGENTS.md")
	if err := updateManagedFile(agentsPath, func(existing []byte) ([]byte, bool, error) {
		return store.UpsertProjXManagedBlock(existing, store.AgentInstructionsBlock())
	}); err != nil {
		return fmt.Errorf("%s: %w", agentsPath, err)
	}

	claudePath := filepath.Join(root, "CLAUDE.md")
	if err := updateManagedFile(claudePath, store.MigrateClaudeToAgentsImport); err != nil {
		return fmt.Errorf("%s: %w", claudePath, err)
	}
	return nil
}

// syncProjectAgentInstructions refreshes only ProjX-owned marker blocks. A
// malformed or ambiguous file is left untouched and reported as a warning.
func syncProjectAgentInstructions(root string) {
	if err := installProjectAgentInstructions(root); err != nil {
		fmt.Fprintf(os.Stderr, "projx: agent instructions unchanged: %v\n", err)
	}
}

func updateManagedFile(path string, transform func([]byte) ([]byte, bool, error)) error {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	out, changed, err := transform(existing)
	if err != nil || !changed {
		return err
	}
	if bytes.Equal(existing, out) {
		return nil
	}
	return atomicWriteFile(path, out)
}

func atomicWriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".projx-agent-instructions-*")
	if err != nil {
		return err
	}
	tempPath := f.Name()
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(tempPath)
	}
	if err := f.Chmod(mode); err != nil {
		cleanup()
		return err
	}
	if _, err := f.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}
