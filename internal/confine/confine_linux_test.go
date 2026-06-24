//go:build linux

package confine_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SirNiklas9/projx-engine/internal/confine"
)

// TestDetectLinux verifies the Linux confiner reports a landlock level.
func TestDetectLinux(t *testing.T) {
	c := confine.Detect()
	if !strings.Contains(c.Level(), "landlock") {
		t.Errorf("expected a landlock level, got %q", c.Level())
	}
}

// TestAvailableLinux checks that Available() does not apply any restriction
// to the process -- it is a pure syscall probe.
func TestAvailableLinux(t *testing.T) {
	c := confine.Detect()
	t.Logf("Landlock available: %v", c.Available())
	// Verify the probe did not restrict us.
	f, err := os.Open("/dev/null")
	if err != nil {
		t.Errorf("Available() must not restrict process; /dev/null open: %v", err)
		return
	}
	f.Close()
}

// TestFilterExistingViaDefaultPolicy checks DefaultPolicy filters non-existent paths.
func TestFilterExistingViaDefaultPolicy(t *testing.T) {
	root := t.TempDir()
	p := confine.DefaultPolicy(root, "/nonexistent-jaildir-xyz", "/nonexistent-agentdir-xyz")
	found := false
	for _, r := range p.ReadWrite {
		if r == root {
			found = true
		}
		if strings.Contains(r, "nonexistent") {
			t.Errorf("nonexistent path %q leaked into ReadWrite", r)
		}
	}
	if !found {
		t.Errorf("expected root %q in ReadWrite, got %v", root, p.ReadWrite)
	}
	for _, r := range p.ReadOnly {
		if strings.Contains(r, "nonexistent") {
			t.Errorf("nonexistent path %q leaked into ReadOnly", r)
		}
	}
}

// TestConfinementDenialProofViaLauncher is the REAL denial proof. It exercises
// the production path: `projx-engine __confined-launch <root> <agent> [args]`
// applies the Landlock domain then syscall.Exec's the agent. Landlock is
// inherited across execve into a fresh single-threaded process — the only
// reliable way to confine a Go program (Landlock domains are PER-THREAD, so
// applying in-process to a multithreaded test binary and reading on another
// thread is not a valid test and can falsely report "not enforcing").
//
// A tiny helper tries to read (a) a file INSIDE the root — must succeed — and
// (b) a file in $HOME OUTSIDE the root — must be kernel-denied.
func TestConfinementDenialProofViaLauncher(t *testing.T) {
	if !confine.Detect().Available() {
		t.Skip("Landlock not available on this kernel")
	}

	root := t.TempDir()

	// Build the projx-engine binary (the launcher) from the module root.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	moduleRoot := filepath.Join(wd, "..", "..")
	enginePath := filepath.Join(t.TempDir(), "projx-engine")
	build := exec.Command("go", "build", "-o", enginePath, ".")
	build.Dir = moduleRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("could not build engine (skipping): %v\n%s", err, out)
	}

	// Build the helper INTO the root (granted, so it is readable/executable under
	// the policy). Static (CGO disabled) to minimise filesystem deps.
	helperSrc := filepath.Join(t.TempDir(), "helper.go")
	helperSource := `package main
import ("fmt";"os")
func main(){
	r := func(label, path string){
		b, err := os.ReadFile(path)
		if err != nil { fmt.Printf("%s:DENIED:%v\n", label, err) } else { fmt.Printf("%s:OK:%s\n", label, string(b)) }
	}
	r("INROOT", os.Args[1])
	r("OUTSIDE", os.Args[2])
}
`
	if err := os.WriteFile(helperSrc, []byte(helperSource), 0o644); err != nil {
		t.Fatalf("write helper src: %v", err)
	}
	helperPath := filepath.Join(root, "helper")
	hb := exec.Command("go", "build", "-o", helperPath, helperSrc)
	hb.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := hb.CombinedOutput(); err != nil {
		t.Skipf("could not build helper (skipping): %v\n%s", err, out)
	}

	// File inside the root — must be readable.
	inroot := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(inroot, []byte("INSIDEOK"), 0o644); err != nil {
		t.Fatalf("write inroot: %v", err)
	}

	// File in $HOME, OUTSIDE the root and outside every granted path — must be denied.
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir to place the outside-secret in")
	}
	outside := filepath.Join(home, ".projx-confine-test-secret")
	if err := os.WriteFile(outside, []byte("TOPSECRET"), 0o600); err != nil {
		t.Skipf("cannot write outside-secret in home (skipping): %v", err)
	}
	defer os.Remove(outside)

	// Run the production launcher: apply Landlock(DefaultPolicy(root)) then exec
	// the helper, which attempts both reads.
	cmd := exec.Command(enginePath, "__confined-launch", root, helperPath, inroot, outside)
	out, runErr := cmd.CombinedOutput()
	t.Logf("launcher output:\n%s", out)
	if runErr != nil {
		t.Fatalf("confined launch failed: %v\noutput:\n%s", runErr, out)
	}
	s := string(out)

	if !strings.Contains(s, "INROOT:OK:INSIDEOK") {
		t.Errorf("in-root read should have succeeded; output:\n%s", s)
	}
	if !strings.Contains(s, "OUTSIDE:DENIED") {
		t.Errorf("DENIAL PROOF FAILED: home-dir file outside root was NOT denied; output:\n%s", s)
	}
	if strings.Contains(s, "TOPSECRET") {
		t.Errorf("LEAK: the secret value escaped confinement; output:\n%s", s)
	}
}
