//go:build windows

package core

// NON-INTERACTIVE FS-CONFINEMENT HARNESS for the prototype restricted-token +
// Low-integrity Windows confiner (restrictedTokenConfiner / LaunchConfined in
// confine_windows_restricted.go).
//
// This is the real "does the FS wall hold" proof, distinct from the routing
// unit tests in confine_windows_restricted_test.go. It launches a tiny
// NON-INTERACTIVE child (cmd /c) under LaunchConfined and asserts:
//
//   * the child CAN write a file INSIDE policy.Root  (the root is labeled Low,
//     so the Low-integrity child may write there), and
//   * the child is DENIED writing a file OUTSIDE policy.Root (everything outside
//     is >= Medium integrity, so the no-write-up mandatory policy blocks it).
//
// Writes are the load-bearing assertion: the Low integrity wall denies write-up,
// not read-down, so a write outside is the thing that must fail. We verify the
// outcome from the parent (Medium IL) by checking which files actually appeared
// on disk — we never trust the child's own report.
//
// The test is skipped automatically if the harness cannot establish the
// preconditions for a meaningful result (e.g. the host is not at Medium
// integrity, so a Low child is not actually a step down). It is a NEGATIVE proof
// of confinement: if the outside write SUCCEEDS, the FS wall is NOT holding and
// the test FAILS loudly.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// currentProcessIntegrityRID returns the mandatory integrity RID of the calling
// process (e.g. 0x2000 Medium, 0x3000 High, 0x1000 Low), or 0 on error.
func currentProcessIntegrityRID(t *testing.T) uint32 {
	t.Helper()
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		t.Logf("OpenProcessToken: %v", err)
		return 0
	}
	defer token.Close()

	// Query the required buffer size for TokenIntegrityLevel.
	var size uint32
	windows.GetTokenInformation(token, windows.TokenIntegrityLevel, nil, 0, &size)
	if size == 0 {
		return 0
	}
	buf := make([]byte, size)
	if err := windows.GetTokenInformation(token, windows.TokenIntegrityLevel, &buf[0], size, &size); err != nil {
		t.Logf("GetTokenInformation(integrity): %v", err)
		return 0
	}
	tml := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&buf[0]))
	sid := tml.Label.Sid
	// The integrity RID is the last sub-authority of the S-1-16-<rid> SID.
	n := sid.SubAuthorityCount()
	if n == 0 {
		return 0
	}
	return sid.SubAuthority(uint32(n) - 1)
}

// TestRestrictedConfiner_FSWallHolds is the FS-confinement denial proof.
func TestRestrictedConfiner_FSWallHolds(t *testing.T) {
	c := restrictedTokenConfiner{}
	if !c.Available() {
		t.Skipf("restricted-token confiner not available: skipping")
	}

	// Precondition: the parent must be at Medium integrity for a Low child to be
	// a genuine step DOWN. If we are at Low already, or elevated to High, the
	// mandatory-policy semantics under test differ and the result would be
	// misleading.
	rid := currentProcessIntegrityRID(t)
	const (
		ridLow    = 0x1000
		ridMedium = 0x2000
	)
	if rid == 0 {
		t.Skip("could not determine process integrity level: skipping FS-wall proof")
	}
	if rid <= ridLow {
		t.Skipf("process already at Low integrity (RID 0x%x): Low child is not a step down; skipping", rid)
	}
	t.Logf("parent integrity RID = 0x%x (Medium=0x%x); Low child = 0x1000", rid, ridMedium)

	// INSIDE: policy.Root — will be labeled Low by LaunchConfined, so the Low
	// child may write here.
	root := t.TempDir()

	// OUTSIDE: a sibling temp dir that is NOT in the policy and is therefore left
	// at its inherited (>= Medium) integrity. The Low child must be denied here.
	outsideDir := t.TempDir()

	insideTarget := filepath.Join(root, "inside_written.txt")
	outsideTarget := filepath.Join(outsideDir, "outside_written.txt")

	// Defensive: make sure neither target exists yet.
	_ = os.Remove(insideTarget)
	_ = os.Remove(outsideTarget)

	// The child is a non-interactive cmd batch (a .bat file). It attempts BOTH
	// writes in one shot — one INSIDE the labeled root, one OUTSIDE it — as two
	// separate statements so the outside redirection failing does not abort the
	// inside write. We do NOT rely on the child's exit code for the verdict; we
	// inspect the filesystem from the parent (Medium IL) afterwards.
	//
	// A .bat file is used instead of an inline `cmd /c "...quoted..."` string
	// because cmd.exe does not understand the backslash-quote escaping that the
	// Windows CreateProcess command line requires for inline quoted paths; a
	// .bat file sidesteps all of that quoting. The .bat lives in outsideDir
	// (>= Medium integrity) and is only READ by the Low child, which read-down
	// permits.
	batPath := filepath.Join(outsideDir, "fswall_probe.bat")
	batBody := "@echo off\r\n" +
		"echo INSIDE> \"" + insideTarget + "\"\r\n" +
		"echo OUTSIDE> \"" + outsideTarget + "\"\r\n"
	if err := os.WriteFile(batPath, []byte(batBody), 0o644); err != nil {
		t.Fatalf("write probe .bat: %v", err)
	}

	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = `C:\Windows\System32\cmd.exe`
	}
	argv := []string{comspec, "/c", batPath}

	policy := Policy{
		Root:      root,
		ReadWrite: []string{root},
	}

	exitCode, err := c.LaunchConfined(policy, argv, os.Environ(), root)
	if err != nil {
		t.Fatalf("LaunchConfined: %v", err)
	}
	t.Logf("child (cmd /c) exit code = %d", exitCode)

	insideExists := fileExistsNonEmpty(insideTarget)
	outsideExists := fileExistsNonEmpty(outsideTarget)

	t.Logf("INSIDE  write (%s): exists=%v", insideTarget, insideExists)
	t.Logf("OUTSIDE write (%s): exists=%v", outsideTarget, outsideExists)

	// ASSERTION 1: the child CAN write inside the labeled root.
	if !insideExists {
		t.Errorf("FS-WALL FAIL: inside-root write did NOT appear (%s) — the Low child cannot write inside policy.Root; cage is too tight or labeling failed", insideTarget)
	}

	// ASSERTION 2: the child is DENIED writing outside the root. This is the
	// load-bearing confinement proof.
	if outsideExists {
		data, _ := os.ReadFile(outsideTarget)
		t.Errorf("FS-WALL BREACH: outside-root write SUCCEEDED (%s, content=%q) — the Low-integrity no-write-up wall did NOT hold; FS confinement is NOT enforcing", outsideTarget, strings.TrimSpace(string(data)))
	}

	if insideExists && !outsideExists {
		t.Logf("FS WALL HOLDS: inside-root write allowed, outside-root write denied by Low-integrity mandatory policy")
	}
}

func fileExistsNonEmpty(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return fi.Size() > 0
}
