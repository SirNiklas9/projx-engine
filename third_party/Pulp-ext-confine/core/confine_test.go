package core_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/BananaLabs-OSS/Pulp-ext-confine/core"
)

// TestDetect verifies that Detect() returns a non-nil Confiner on all platforms.
func TestDetect(t *testing.T) {
	c := core.Detect()
	if c == nil {
		t.Fatal("Detect() returned nil")
	}
	t.Logf("platform confiner: Level=%q Available=%v", c.Level(), c.Available())
}

// TestDefaultPolicy verifies that DefaultPolicy returns a Policy whose Root
// matches the input and does not panic.
func TestDefaultPolicy(t *testing.T) {
	root := t.TempDir()
	p := core.DefaultPolicy(root, "", "")
	if p.Root != root {
		t.Errorf("DefaultPolicy root: got %q, want %q", p.Root, root)
	}
}

// TestFilterExisting verifies that ExistingPaths drops non-existent entries.
func TestFilterExisting(t *testing.T) {
	tmp := t.TempDir()
	got := core.ExistingPaths([]string{tmp, "/this/path/does/not/exist/ever", ""})
	if len(got) != 1 || got[0] != tmp {
		t.Errorf("ExistingPaths: got %v, want [%s]", got, tmp)
	}
}

// TestPolicyNetAllow verifies the NetAllow field is present and round-trips.
func TestPolicyNetAllow(t *testing.T) {
	p := core.Policy{
		Root:     "/tmp/test",
		NetAllow: []string{"example.com:443"},
	}
	if len(p.NetAllow) != 1 {
		t.Errorf("NetAllow: got %v", p.NetAllow)
	}
}

// TestLaunchConfinedDenialProof is the launcher-based denial proof.
//
// On Linux: compiles a standalone launcher (using go-landlock via the module
// cache) and a reader helper binary. Then verifies that reading inside the
// allowed root succeeds (exit 0) and reading outside is denied by Landlock
// (exit != 0).
//
// On Windows: verifies AppContainer plumbing (SID creation, icacls, CreateProcess)
// by running a helper inside an allowed dir.
//
// Other platforms: skipped (cooperative mode).
func TestLaunchConfinedDenialProof(t *testing.T) {
	c := core.Detect()
	if !c.Available() {
		t.Skipf("confinement not available on %s (level=%s): skipping", runtime.GOOS, c.Level())
	}

	switch runtime.GOOS {
	case "linux":
		testLinuxDenialProof(t, c)
	case "windows":
		testWindowsDenialProof(t, c)
	default:
		t.Skipf("no denial proof for %s", runtime.GOOS)
	}
}

func testLinuxDenialProof(t *testing.T, c core.Confiner) {
	root := t.TempDir()
	insideFile := filepath.Join(root, "allowed.txt")
	if err := os.WriteFile(insideFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	readerBin := buildReaderBin(t)
	launcherBin := buildLauncherBin(t)

	policy := core.Policy{
		Root:      root,
		ReadOnly:  core.ExistingPaths([]string{"/usr", "/lib", "/lib64", "/bin", "/proc"}),
		ReadWrite: []string{root},
	}

	// Inject the launcher so LaunchConfined uses it.
	t.Setenv("PULP_CONFINE_LAUNCHER_BIN", launcherBin)
	env := os.Environ()

	// Inside the root → allowed.
	exitCode, err := c.LaunchConfined(policy, []string{readerBin, insideFile}, env, root)
	if err != nil {
		t.Fatalf("LaunchConfined (inside root): %v", err)
	}
	if exitCode != 0 {
		t.Errorf("inside root: want exit 0, got %d", exitCode)
	}
	t.Log("inside root: allowed OK")

	// Outside the root → denied by Landlock.
	exitCode, err = c.LaunchConfined(policy, []string{readerBin, outsideFile}, env, root)
	if err != nil {
		t.Fatalf("LaunchConfined (outside root): %v", err)
	}
	if exitCode == 0 {
		t.Errorf("outside root: expected denial (non-zero exit), got 0 — Landlock not enforcing?")
	}
	t.Logf("outside root: denied (exit %d) — Landlock OK", exitCode)
}

func testWindowsDenialProof(t *testing.T, c core.Confiner) {
	root := t.TempDir()
	insideFile := filepath.Join(root, "allowed.txt")
	if err := os.WriteFile(insideFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	readerBin := buildReaderBin(t)

	policy := core.Policy{
		Root:      root,
		ReadWrite: []string{root},
	}
	env := os.Environ()
	exitCode, err := c.LaunchConfined(policy, []string{readerBin, insideFile}, env, root)
	if err != nil {
		t.Fatalf("LaunchConfined (windows): %v", err)
	}
	if exitCode != 0 {
		t.Errorf("inside root: expected exit 0, got %d", exitCode)
	}
	t.Logf("Windows AppContainer plumbing OK: exit %d", exitCode)
}

// buildReaderBin compiles a tiny stdlib-only Go binary that reads argv[1] and
// exits 0 on success or 1 on failure. No external imports, so no go.mod needed.
func buildReaderBin(t *testing.T) string {
	t.Helper()
	src := `package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: reader <file>")
		os.Exit(2)
	}
	if _, err := os.ReadFile(os.Args[1]); err != nil {
		fmt.Fprintf(os.Stderr, "read failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("ok")
}
`
	return compileSingleFile(t, "reader", src)
}

// buildLauncherBin compiles the confined launcher binary.
//
// The launcher protocol: argv = [launcherBin, root, target, target-args...]
//
// On Linux it applies Landlock via go-landlock then syscall.Exec's the target.
// It imports Pulp-ext-confine/core from the local module, resolved via a
// temporary go.mod with a replace directive pointing at the module root.
func buildLauncherBin(t *testing.T) string {
	t.Helper()

	src := `package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BananaLabs-OSS/Pulp-ext-confine/core"
)

// Protocol: os.Args = [self, root, target, target-args...]
// The target binary's directory is always added to ReadOnly so Landlock allows
// exec'ing it inside the domain.
func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: launcher <root> <target> [args...]")
		os.Exit(1)
	}
	root := os.Args[1]
	targetArgv := os.Args[2:]
	targetDir := filepath.Dir(targetArgv[0])
	policy := core.Policy{
		Root: root,
		ReadOnly: core.ExistingPaths([]string{
			"/usr", "/lib", "/lib64", "/bin", "/proc",
			targetDir,
		}),
		ReadWrite: []string{root},
	}
	core.RunConfinedLaunch(policy, targetArgv)
}
`
	// Find the module root so we can set the replace directive.
	moduleRoot := findModuleRoot(t)

	dir := t.TempDir()

	// Write source.
	srcFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(srcFile, []byte(src), 0o644); err != nil {
		t.Fatalf("write launcher src: %v", err)
	}

	// Write go.mod with local replace.
	// On Linux the path must be in Unix form; filepath.ToSlash handles both.
	gomod := "module pulp-launcher\n\ngo 1.25\n\nrequire github.com/BananaLabs-OSS/Pulp-ext-confine v0.0.0\n\nreplace github.com/BananaLabs-OSS/Pulp-ext-confine => " + filepath.ToSlash(moduleRoot) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatalf("write launcher go.mod: %v", err)
	}

	binPath := filepath.Join(dir, "launcher")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}

	// Build from the temp dir so go.mod is picked up.
	out, err := runCmdInDir(dir, "go", "build", "-mod=mod", "-o", binPath, ".")
	if err != nil {
		t.Fatalf("build launcher: %v\n%s", err, out)
	}
	return binPath
}

// compileSingleFile compiles a standalone Go source file (stdlib only, no
// go.mod required) and returns the path to the resulting binary.
func compileSingleFile(t *testing.T, name, src string) string {
	t.Helper()
	dir := t.TempDir()
	srcFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(srcFile, []byte(src), 0o644); err != nil {
		t.Fatalf("write %s src: %v", name, err)
	}
	binName := name
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(dir, binName)
	out, err := runCmd("go", "build", "-o", binPath, srcFile)
	if err != nil {
		t.Fatalf("compile %s: %v\n%s", name, err, out)
	}
	return binPath
}

// findModuleRoot walks up from the current working directory to find the
// directory containing a go.mod that declares Pulp-ext-confine.
func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		data, readErr := os.ReadFile(filepath.Join(dir, "go.mod"))
		if readErr == nil && strings.Contains(string(data), "github.com/BananaLabs-OSS/Pulp-ext-confine") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find Pulp-ext-confine module root (no go.mod found)")
		}
		dir = parent
	}
}
