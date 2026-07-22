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
	rt, copied, err := provisionManagedRuntime(home)
	if err != nil {
		t.Fatal(err)
	}
	if !copied {
		t.Fatal("first provision did not copy")
	}
	first := rt.CLI
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
	if runtime.GOOS == "windows" && (rt.Headless == rt.CLI || !strings.HasSuffix(rt.Headless, "-headless.exe")) {
		t.Fatalf("headless adapter path = %q", rt.Headless)
	}
	secondRT, copied, err := provisionManagedRuntime(home)
	second := secondRT.CLI
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
	oldHeadless := configuredHeadlessBinary
	configuredBinary = filepath.Join("managed", "projx-engine.exe")
	configuredHeadlessBinary = filepath.Join("managed", "projx-engine-headless.exe")
	t.Cleanup(func() { configuredBinary = old; configuredHeadlessBinary = oldHeadless })
	t.Setenv("PROJX_ENGINE_BIN", filepath.Join("legacy", "projx-engine.exe"))
	if got := mcpBinaryPath(); got != "managed/projx-engine-headless.exe" {
		t.Fatalf("mcpBinaryPath = %q", got)
	}
}

func TestBackgroundAdaptersUseHeadlessManagedBinary(t *testing.T) {
	oldCLI, oldHeadless := configuredBinary, configuredHeadlessBinary
	configuredBinary = filepath.Join("managed", "projx-engine.exe")
	configuredHeadlessBinary = filepath.Join("managed", "projx-engine-headless.exe")
	t.Cleanup(func() { configuredBinary, configuredHeadlessBinary = oldCLI, oldHeadless })
	want := "managed/projx-engine-headless.exe"
	for name, command := range map[string]string{
		"claude hook": projxHookCommand(),
		"codex hook":  codexHookCommand(),
		"dashboard":   codexDashboardCommand(),
	} {
		if !strings.Contains(command, want) || strings.Contains(command, `managed/projx-engine.exe"`) {
			t.Errorf("%s command uses wrong runtime: %s", name, command)
		}
	}
}

func TestActivateManagedBinaryConfiguresExactPath(t *testing.T) {
	old := configuredBinary
	oldHeadless := configuredHeadlessBinary
	configuredBinary = ""
	t.Cleanup(func() { configuredBinary = old; configuredHeadlessBinary = oldHeadless })
	t.Setenv("HOME", t.TempDir())
	path, _, err := activateManagedBinary()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.ToSlash(path) == mcpBinaryPath() && runtime.GOOS == "windows" {
		t.Fatalf("MCP path %q incorrectly uses interactive CLI %q", mcpBinaryPath(), path)
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
