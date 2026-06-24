package jail_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/SirNiklas9/projx-engine/internal/jail"
)

// stubFile creates a small non-empty temp file that can serve as a
// stand-in for the engine binary in link/copy tests.
func stubFile(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "engine-stub-*.bin")
	if err != nil {
		t.Fatalf("create stub file: %v", err)
	}
	// Write a few bytes so the file is non-empty.
	if _, err := f.WriteString("stub"); err != nil {
		f.Close()
		t.Fatalf("write stub: %v", err)
	}
	f.Close()
	return f.Name()
}

// shimName returns the expected shim filename for a given bare name on the
// current OS (adds .exe on Windows).
func shimName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// TestBuildCreatesShimsOnlyForAllowed verifies that Build materialises shims
// for the requested bins and does NOT create shims for other names.
func TestBuildCreatesShimsOnlyForAllowed(t *testing.T) {
	stub := stubFile(t)
	jailDir := t.TempDir()

	allowBins := []string{"git", "go"}
	if err := jail.Build(jailDir, stub, allowBins); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Allowed bins must be present.
	for _, name := range allowBins {
		p := filepath.Join(jailDir, shimName(name))
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("expected shim %q to exist, got: %v", p, err)
		}
	}

	// Non-allowed bins must NOT be present.
	notAllowed := []string{"powershell", "ssh", "bash", "cmd"}
	for _, name := range notAllowed {
		p := filepath.Join(jailDir, shimName(name))
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("unexpected shim %q exists (should not have been created)", p)
		}
	}
}

// TestBuildIdempotenent ensures calling Build twice on the same dir does not error.
func TestBuildIdempotent(t *testing.T) {
	stub := stubFile(t)
	jailDir := t.TempDir()

	if err := jail.Build(jailDir, stub, []string{"git"}); err != nil {
		t.Fatalf("first Build: %v", err)
	}
	if err := jail.Build(jailDir, stub, []string{"git"}); err != nil {
		t.Fatalf("second Build: %v", err)
	}
}

// TestEnvSetsJailedPath asserts that Jail.Env overrides PATH to only the jail
// dir, injects PROJX_REAL_PATH and PROJX_BROKER_ALLOW_BINS, and does not leave
// duplicate PATH entries.
func TestEnvSetsJailedPath(t *testing.T) {
	jailDir := t.TempDir()
	realPath := "/usr/local/bin:/usr/bin"
	if runtime.GOOS == "windows" {
		realPath = `C:\Windows\System32;C:\Windows`
	}

	j := &jail.Jail{
		Dir:        jailDir,
		RealPath:   realPath,
		Root:       t.TempDir(),
		AllowBins:  []string{"git", "go"},
		AllowHosts: []string{"api.anthropic.com"},
	}

	// Parent env with pre-existing PATH and a stale PROJX_BROKER_ROOT that
	// must be stripped.
	parent := []string{
		"HOME=/home/user",
		"PATH=/stale/path",
		"PROJX_BROKER_ROOT=/old-root",
		"GOPATH=/home/user/go",
	}

	out := j.Env(parent)

	// Collect env as a map for easy lookup.
	m := make(map[string]string)
	for _, kv := range out {
		idx := strings.IndexByte(kv, '=')
		if idx < 0 {
			continue
		}
		key := kv[:idx]
		val := kv[idx+1:]
		// Detect duplicates.
		if _, dup := m[strings.ToUpper(key)]; dup {
			t.Errorf("duplicate env key %q in output", key)
		}
		m[strings.ToUpper(key)] = val
	}

	// PATH must be exactly the jail dir.
	if got := m["PATH"]; got != jailDir {
		t.Errorf("PATH = %q, want %q", got, jailDir)
	}

	// PROJX_REAL_PATH must be present and match what we set.
	if got := m["PROJX_REAL_PATH"]; got != realPath {
		t.Errorf("PROJX_REAL_PATH = %q, want %q", got, realPath)
	}

	// PROJX_BROKER_ALLOW_BINS must be comma-joined.
	if got := m["PROJX_BROKER_ALLOW_BINS"]; got != "git,go" {
		t.Errorf("PROJX_BROKER_ALLOW_BINS = %q, want %q", got, "git,go")
	}

	// PROJX_BROKER_ALLOW_HOSTS must be present.
	if got := m["PROJX_BROKER_ALLOW_HOSTS"]; got != "api.anthropic.com" {
		t.Errorf("PROJX_BROKER_ALLOW_HOSTS = %q, want %q", got, "api.anthropic.com")
	}

	// PROJX_BROKER_ROOT must be our new root, not the old one.
	if got := m["PROJX_BROKER_ROOT"]; got != j.Root {
		t.Errorf("PROJX_BROKER_ROOT = %q, want %q", got, j.Root)
	}

	// HOME must still be present (non-jail env preserved).
	if got := m["HOME"]; got != "/home/user" {
		t.Errorf("HOME = %q, want %q", got, "/home/user")
	}
}

// TestEnvNoDuplicatePathOnReApply ensures that calling Env again on the result
// of a previous Env call does not accumulate duplicate PATH entries.
func TestEnvNoDuplicatePathOnReApply(t *testing.T) {
	j := &jail.Jail{
		Dir:       t.TempDir(),
		RealPath:  "/usr/bin",
		Root:      t.TempDir(),
		AllowBins: []string{"git"},
	}

	first := j.Env(os.Environ())
	second := j.Env(first)

	// Count PATH occurrences in second.
	count := 0
	for _, kv := range second {
		if strings.HasPrefix(strings.ToUpper(kv), "PATH=") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 PATH entry after double Env(), got %d", count)
	}
}
