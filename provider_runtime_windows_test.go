//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPreferProviderRuntimeChoosesNewestCompleteCodexPair(t *testing.T) {
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	base := filepath.Join(local, "OpenAI", "Codex", "bin")
	old := filepath.Join(base, "old")
	current := filepath.Join(base, "current")
	for _, dir := range []string{old, current} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "codex.exe"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "codex-windows-sandbox-setup.exe"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(filepath.Join(old, "codex.exe"), time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := preferProviderRuntime("codex", "C:\\old\\codex.exe"); got != filepath.Join(current, "codex.exe") {
		t.Fatalf("runtime = %q", got)
	}
	if got := preferProviderRuntime("codex.exe", "C:\\old\\codex.exe"); got != filepath.Join(current, "codex.exe") {
		t.Fatalf("executable runtime = %q", got)
	}
	if got := preferProviderRuntime("claude", "C:\\old\\claude.exe"); got != "C:\\old\\claude.exe" {
		t.Fatalf("non-Codex runtime = %q", got)
	}
}
