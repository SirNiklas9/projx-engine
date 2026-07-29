//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// preferProviderRuntime selects Codex Desktop's complete managed CLI pair when
// an older standalone codex.exe appears earlier on PATH. The desktop pair keeps
// its Windows sandbox helper beside the CLI, and the newest complete pair wins
// so updates are picked up without a user-maintained absolute path.
func preferProviderRuntime(provider, fallback string) string {
	provider = strings.TrimSuffix(filepath.Base(strings.TrimSpace(provider)), ".exe")
	if !strings.EqualFold(provider, "codex") {
		return fallback
	}
	base := filepath.Join(os.Getenv("LOCALAPPDATA"), "OpenAI", "Codex", "bin")
	entries, err := os.ReadDir(base)
	if err != nil {
		return fallback
	}
	var best string
	var bestTime time.Time
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(base, entry.Name())
		candidate := filepath.Join(dir, "codex.exe")
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "codex-windows-sandbox-setup.exe")); err != nil {
			continue
		}
		info, err := os.Stat(candidate)
		if err == nil && (best == "" || info.ModTime().After(bestTime)) {
			best, bestTime = candidate, info.ModTime()
		}
	}
	if best != "" {
		return best
	}
	return fallback
}
