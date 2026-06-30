package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	store "github.com/SirNiklas9/projx-store"
)

// TestInstallConnectorWritesTree proves the embedded connector lands on disk: the
// lifecycle hooks, settings.json, and the namespaced /projx:* slash commands.
func TestInstallConnectorWritesTree(t *testing.T) {
	root := t.TempDir()
	written, _, err := installConnector(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if written == 0 {
		t.Fatal("installConnector wrote nothing")
	}
	mustExist := []string{
		".claude/settings.json",
		".claude/hooks/projx-context.sh",
		".claude/hooks/projx-gate.sh",
		".claude/hooks/projx-precompact.sh",
		".claude/hooks/projx-stop.sh",
		".claude/commands/projx/remember.md",
		".claude/commands/projx/store.md",
		".claude/commands/projx/route.md",
		".claude/commands/projx/gate.md",
	}
	for _, rel := range mustExist {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("missing after install: %s (%v)", rel, err)
		}
	}
	// settings.json must register the five hooks.
	data, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, hook := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PreCompact", "Stop"} {
		if !strings.Contains(string(data), hook) {
			t.Errorf("settings.json missing %s hook", hook)
		}
	}
}

// TestInstallConnectorPreservesSettings proves a project's own settings.json is not
// clobbered without --force, but other files still install.
func TestInstallConnectorPreservesSettings(t *testing.T) {
	root := t.TempDir()
	cdir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := `{"mine":true}`
	if err := os.WriteFile(filepath.Join(cdir, "settings.json"), []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	_, skipped, err := installConnector(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if skipped == "" {
		t.Error("expected a skip note for the existing settings.json")
	}
	got, _ := os.ReadFile(filepath.Join(cdir, "settings.json"))
	if string(got) != sentinel {
		t.Error("existing settings.json was clobbered without --force")
	}
	// A command file still installed.
	if _, err := os.Stat(filepath.Join(cdir, "commands", "projx", "remember.md")); err != nil {
		t.Error("slash command not installed alongside preserved settings")
	}

	// With --force the connector settings overwrite the sentinel.
	if _, _, err := installConnector(root, true); err != nil {
		t.Fatal(err)
	}
	got2, _ := os.ReadFile(filepath.Join(cdir, "settings.json"))
	if string(got2) == sentinel {
		t.Error("--force did not overwrite settings.json")
	}
}

// TestInitSeedsAndMaps runs the full init against a tiny Go project and asserts the
// store ends up seeded (a gate rule from the floor) and code-mapped (a symbol record).
func TestInitSeedsAndMaps(t *testing.T) {
	root := t.TempDir()
	writeGoFile(t, root, "main.go", `package main

// Greet says hello.
func Greet() string { return "hi" }
`)
	// go.mod so the stack detector picks "go".
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runInitCmd(root, nil)

	st := openStore(root)
	defer st.Close()
	if len(st.List(store.OfKind(store.KGateRule))) == 0 {
		t.Error("init did not seed any gate rules (floor)")
	}
	mapped := 0
	for _, r := range st.List(store.OfKind(store.KDeclaredStructure)) {
		if r.Origin == mapRecordOrigin {
			mapped++
		}
	}
	if mapped == 0 {
		t.Error("init did not index any code-map records")
	}
}
