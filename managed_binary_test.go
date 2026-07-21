package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProvisionManagedBinaryIsImmutableAndIdempotent(t *testing.T) {
	home := t.TempDir()
	first, copied, err := provisionManagedBinary(home)
	if err != nil {
		t.Fatal(err)
	}
	if !copied {
		t.Fatal("first provision did not copy")
	}
	wantRoot := filepath.Join(home, ".codex", "projx", "bin")
	rel, err := filepath.Rel(wantRoot, first)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("managed path %q is outside %q", first, wantRoot)
	}
	if runtime.GOOS == "windows" && filepath.Ext(first) != ".exe" {
		t.Fatalf("Windows binary lacks .exe: %s", first)
	}
	if info, err := os.Stat(first); err != nil || info.Size() == 0 {
		t.Fatalf("managed binary invalid: %v", err)
	}
	second, copied, err := provisionManagedBinary(home)
	if err != nil {
		t.Fatal(err)
	}
	if copied || second != first {
		t.Fatalf("second provision = %q, %v; want %q, false", second, copied, first)
	}
}

func TestSelfBinaryPathUsesConfiguredManagedBinary(t *testing.T) {
	old := configuredBinary
	configuredBinary = filepath.Join("some root", "projx-engine.exe")
	t.Cleanup(func() { configuredBinary = old })
	if got := selfBinaryPath(); got != "some root/projx-engine.exe" {
		t.Fatalf("selfBinaryPath = %q", got)
	}
}

func TestMCPBinaryPathUsesManagedBinaryNotEnvironment(t *testing.T) {
	old := configuredBinary
	configuredBinary = filepath.Join("managed", "projx-engine.exe")
	t.Cleanup(func() { configuredBinary = old })
	t.Setenv("PROJX_ENGINE_BIN", filepath.Join("legacy", "projx-engine.exe"))
	if got := mcpBinaryPath(); got != "managed/projx-engine.exe" {
		t.Fatalf("mcpBinaryPath = %q", got)
	}
}

func TestActivateManagedBinaryConfiguresExactPath(t *testing.T) {
	old := configuredBinary
	configuredBinary = ""
	t.Cleanup(func() { configuredBinary = old })
	t.Setenv("HOME", t.TempDir())
	path, _, err := activateManagedBinary()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.ToSlash(path) != mcpBinaryPath() {
		t.Fatalf("MCP path %q does not match activated %q", mcpBinaryPath(), path)
	}
}

func TestMergeMCPServerRefreshesManagedBinaryPath(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"projx":{"command":"old","args":["mcp"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"command": "new", "args": []any{"mcp"}}
	if _, wrote := mergeMCPServer(root, "projx", want); !wrote {
		t.Fatal("stale MCP registration was not refreshed")
	}
	var cfg map[string]any
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	server := cfg["mcpServers"].(map[string]any)["projx"].(map[string]any)
	if server["command"] != "new" {
		t.Fatalf("command = %v", server["command"])
	}
}
