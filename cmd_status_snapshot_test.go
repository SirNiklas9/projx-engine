package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeStatuslineGoldenGlobalFloor(t *testing.T) {
	want := slDim + "◇ projx " + slReset + slDim + "global floor" + slReset
	if got := renderClaudeStatusline(StatusSnapshot{}); got != want {
		t.Fatalf("statusline changed\nwant %q\n got %q", want, got)
	}
}

func TestClaudeStatuslineGoldenProjectAndCrumbs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(root, "yours"))
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := openStoreSafe(root)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	base := slAccent + slBold + "◆ projx" + slReset + " " + slBold + filepath.Base(root) + slReset + " " + slDim + "0 rec" + slReset
	if got := buildStatusline(root, ""); got != base {
		t.Fatalf("project statusline changed\nwant %q\n got %q", base, got)
	}
	updateCrumb(root, "ctx", func(c *statusCrumb) { c.A = "ctx"; c.N = 1180; c.R = root })
	wantCtx := base + " " + slDim + "· ctx 1.1k↓" + slReset
	if got := buildStatusline(root, "ctx"); got != wantCtx {
		t.Fatalf("ctx statusline changed\nwant %q\n got %q", wantCtx, got)
	}
	updateCrumb(root, "gate", func(c *statusCrumb) { c.A = "gate"; c.R = root })
	wantGate := base + " " + slRed + "· blocked✋" + slReset
	if got := buildStatusline(root, "gate"); got != wantGate {
		t.Fatalf("gate statusline changed\nwant %q\n got %q", wantGate, got)
	}
}

func TestStatusSnapshotShowsFloatingScope(t *testing.T) {
	home, active := filepath.Join(t.TempDir(), "home"), filepath.Join(t.TempDir(), "active")
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "yours"))
	for _, root := range []string{home, active} {
		if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
			t.Fatal(err)
		}
		st, err := openStoreSafe(root)
		if err != nil {
			t.Fatal(err)
		}
		st.Close()
	}
	updateCrumb(home, "float", func(c *statusCrumb) { c.R = active; c.A = "ctx"; c.N = 42 })
	s := buildStatusSnapshot(home, "float")
	if s.ActiveRoot != active || s.ProjectName != filepath.Base(active) {
		t.Fatalf("scope did not float: %#v", s)
	}
	if s.LastAction != "ctx" || s.ContextBytes != 42 {
		t.Fatalf("crumb missing: %#v", s)
	}
}

func TestMCPConfiguredRecognizesCodexConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := "[mcp_servers.projx]\ncommand = \"projx-engine\"\nargs = [\"mcp\"]\n"
	if err := os.WriteFile(filepath.Join(root, ".codex", "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if !mcpConfigured(root) {
		t.Fatal("Codex project MCP registration was not detected")
	}
}

func TestMCPStatusSnapshotReturnsStructuredContent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := openStoreSafe(root)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	params, _ := json.Marshal(map[string]any{"name": "status_snapshot", "arguments": map[string]any{"root": root}})
	resp := mcpToolCall(mcpReq{ID: json.RawMessage("1"), Params: params}, root)
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T", resp.Result)
	}
	snapshot, ok := result["structuredContent"].(StatusSnapshot)
	if !ok || snapshot.ActiveRoot != root || !snapshot.Project {
		t.Fatalf("structured snapshot = %#v", result["structuredContent"])
	}
}

func TestBinaryIdentityStale(t *testing.T) {
	tests := []struct {
		name           string
		binary, source string
		dirty, want    bool
	}{
		{name: "same revision", binary: "abc123", source: "abc123"},
		{name: "different revision", binary: "abc123", source: "def456", want: true},
		{name: "dirty source", binary: "abc123", source: "abc123", dirty: true, want: true},
		{name: "not engine source", binary: "abc123", source: "", dirty: true},
		{name: "unstamped binary", binary: "", source: "abc123", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := binaryIdentityStale(tt.binary, tt.source, tt.dirty); got != tt.want {
				t.Fatalf("binaryIdentityStale(%q, %q, %t) = %t, want %t", tt.binary, tt.source, tt.dirty, got, tt.want)
			}
		})
	}
}

func TestConfiguredBinaryParity(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "projx-engine.exe")
	if !commandsUseBinary([]string{`"` + filepath.ToSlash(binary) + `" hook`}, binary) {
		t.Fatal("absolute hook command should match its running binary")
	}
	if commandsUseBinary([]string{`"C:/old/projx-engine.exe" hook`}, binary) {
		t.Fatal("stale hook command should not match the running binary")
	}
	if commandsUseBinary(nil, binary) {
		t.Fatal("missing configuration cannot be current")
	}
}

func TestConfiguredProjxMCPCommands(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(`{"mcpServers":{"projx":{"command":"C:/bin/a.exe","args":["mcp"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".codex", "config.toml"), []byte("[mcp_servers.projx]\ncommand = \"C:/bin/b.exe\"\nargs = [\"mcp\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := configuredProjxMCPCommands(root)
	if len(got) != 2 || got[0] != "C:/bin/a.exe" || got[1] != "C:/bin/b.exe" {
		t.Fatalf("configured commands = %q", got)
	}
}
