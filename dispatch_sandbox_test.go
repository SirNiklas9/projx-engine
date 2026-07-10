package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SirNiklas9/projx-engine/internal/broker"
)

// TestLanguageAwareSandboxGrants proves the sandbox half of the language-aware gate
// (Task #18): a dispatched worker on a Rust repo gets cargo + crates.io granted into
// its allow-list, a Go repo still gets go, and nothing beyond the detected language's
// PROFILE-declared toolchain is granted. Asserted via the actual broker construction
// (the same NewRestrictiveBroker the jailed worker's brokered-exec uses).
func TestLanguageAwareSandboxGrants(t *testing.T) {
	// buildWorkerBroker mirrors cmd_agent.go's allow-list construction for a worker:
	// the base tools plus the detected language's granted toolchain + net-allow.
	buildWorkerBroker := func(root, task string) *broker.RestrictiveBroker {
		t.Helper()
		bins := []string{"git", "projx-engine"}
		hosts := []string{"api.anthropic.com"}
		langTools, langHosts := profileGrants(detectStacksForTask(root, task))
		bins = dedupStrings(append(bins, langTools...))
		hosts = dedupStrings(append(hosts, langHosts...))
		b, err := broker.NewRestrictiveBroker(bins, root, hosts)
		if err != nil {
			t.Fatalf("NewRestrictiveBroker: %v", err)
		}
		return b
	}
	execAllowed := func(b *broker.RestrictiveBroker, bin string) bool {
		return b.Check(broker.Action{Kind: "exec", Target: bin}).Allow
	}
	netAllowed := func(b *broker.RestrictiveBroker, host string) bool {
		return b.Check(broker.Action{Kind: "net", Target: "https://" + host + "/x"}).Allow
	}

	// ── Rust repo → cargo + crates.io granted; go NOT granted (not detected) ──
	rustRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(rustRoot, "Cargo.toml"), []byte("[package]\nname=\"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rb := buildWorkerBroker(rustRoot, "implement the parser")
	if !execAllowed(rb, "cargo") {
		t.Error("Rust repo: cargo should be granted into the worker allow-list")
	}
	if !execAllowed(rb, "rustc") {
		t.Error("Rust repo: rustc should be granted")
	}
	if !netAllowed(rb, "crates.io") {
		t.Error("Rust repo: crates.io should be net-allowed")
	}
	if execAllowed(rb, "go") {
		t.Error("Rust repo: go must NOT be granted (sandbox not widened beyond detected language)")
	}

	// ── Go repo → go + gofmt granted; cargo NOT granted ──
	goRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(goRoot, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gb := buildWorkerBroker(goRoot, "fix the handler")
	if !execAllowed(gb, "go") {
		t.Error("Go repo: go should be granted")
	}
	if !execAllowed(gb, "gofmt") {
		t.Error("Go repo: gofmt should be granted")
	}
	if execAllowed(gb, "cargo") {
		t.Error("Go repo: cargo must NOT be granted")
	}

	// ── Task-language hint alone (no repo marker) grants the toolchain ──
	bareRoot := t.TempDir()
	tb := buildWorkerBroker(bareRoot, "port this module to Rust with cargo")
	if !execAllowed(tb, "cargo") {
		t.Error("Task hinting Rust should grant cargo even without a Cargo.toml")
	}
}

// TestDetectStacksForTask pins the detection: repo markers + task keyword hints, unioned.
func TestDetectStacksForTask(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := detectStacksForTask(root, "also add a python helper script")
	has := func(s string) bool {
		for _, x := range got {
			if x == s {
				return true
			}
		}
		return false
	}
	if !has("go") {
		t.Errorf("go.mod marker not detected: %v", got)
	}
	if !has("python") {
		t.Errorf("python task hint not detected: %v", got)
	}
	if has("rust") {
		t.Errorf("rust should not be detected here: %v", got)
	}
}
