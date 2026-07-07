package main

import (
	"os"
	"path/filepath"
	"testing"

	store "github.com/SirNiklas9/projx-store"
)

// TestInstallConnectorWritesCommands proves the embedded connector lands on disk: the
// namespaced /projx:* slash commands. It must NOT write a per-project settings.json —
// the ProjX lifecycle hook is installed once, globally.
func TestInstallConnectorWritesCommands(t *testing.T) {
	root := t.TempDir()
	written, _, err := installConnector(root)
	if err != nil {
		t.Fatal(err)
	}
	if written == 0 {
		t.Fatal("installConnector wrote nothing")
	}
	mustExist := []string{
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
	// The per-project hook file must NOT be created — the global hook owns injection.
	if _, err := os.Stat(filepath.Join(root, ".claude", "settings.json")); err == nil {
		t.Error("installConnector wrote a per-project settings.json — the global hook should be the only injector")
	}
}

// TestInstallConnectorPreservesExistingSettings proves a project's own settings.json is
// left untouched (and noted), while the slash commands still install.
func TestInstallConnectorPreservesExistingSettings(t *testing.T) {
	root := t.TempDir()
	cdir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := `{"mine":true}`
	if err := os.WriteFile(filepath.Join(cdir, "settings.json"), []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	_, note, err := installConnector(root)
	if err != nil {
		t.Fatal(err)
	}
	if note == "" {
		t.Error("expected a note about the existing settings.json")
	}
	got, _ := os.ReadFile(filepath.Join(cdir, "settings.json"))
	if string(got) != sentinel {
		t.Error("existing settings.json was modified")
	}
	// A command file still installed alongside the preserved settings.
	if _, err := os.Stat(filepath.Join(cdir, "commands", "projx", "remember.md")); err != nil {
		t.Error("slash command not installed alongside preserved settings")
	}
}

// TestInitSeedsAndMaps runs the full init against a tiny Go project and asserts the store
// ends up seeded (a floor gate rule), code-mapped (a symbol record), and smart-seeded
// (a build/test recipe + an architecture overview doc).
func TestInitSeedsAndMaps(t *testing.T) {
	root := t.TempDir()
	writeGoFile(t, root, "main.go", `package main

// Greet says hello.
func Greet() string { return "hi" }

func main() { println(Greet()) }
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
	// Smart seed: the Go toolchain recipes land as recipe records.
	if _, ok := st.Get("recipe/go-test"); !ok {
		t.Error("smart seed did not add the go-test recipe")
	}
	// Smart seed: an architecture overview doc lands.
	if _, ok := st.Get("doc/architecture-overview"); !ok {
		t.Error("smart seed did not add the architecture overview doc")
	}
}

// TestSmartSeedIdempotent proves re-running the smart seed adds nothing the second time.
func TestSmartSeedIdempotent(t *testing.T) {
	root := t.TempDir()
	writeGoFile(t, root, "main.go", "package main\n\nfunc main() {}\n")
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("build:\n\tgo build ./...\n\ntest:\n\tgo test ./...\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := openStore(root)
	defer st.Close()

	first, _ := smartSeed(st, root)
	if first == 0 {
		t.Fatal("first smart seed wrote nothing")
	}
	second, _ := smartSeed(st, root)
	if second != 0 {
		t.Errorf("second smart seed wrote %d record(s); want 0 (must be idempotent)", second)
	}
	// Makefile targets became recipes.
	if _, ok := st.Get("recipe/make-build"); !ok {
		t.Error("smart seed did not add the make-build recipe")
	}
}

// TestMakeTargets checks the Makefile parser keeps real targets and drops noise.
func TestMakeTargets(t *testing.T) {
	src := "# comment\n" +
		".PHONY: build test\n" +
		"VAR := value\n" +
		"build: deps\n\tgo build ./...\n" +
		"test:\n\tgo test ./...\n" +
		"%.o: %.c\n\tcc -c $<\n" +
		"\tindented-not-a-target:\n"
	got := makeTargets(src)
	want := map[string]bool{"build": true, "test": true}
	if len(got) != len(want) {
		t.Fatalf("makeTargets = %v; want keys %v", got, want)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected target %q", g)
		}
	}
}
