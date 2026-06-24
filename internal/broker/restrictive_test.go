package broker

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ── constructor ───────────────────────────────────────────────────────────────

func TestNewRestrictiveBrokerEmptyRootError(t *testing.T) {
	_, err := NewRestrictiveBroker(nil, "", nil)
	if err == nil {
		t.Error("expected error for empty projectRoot, got nil")
	}
	_, err = NewRestrictiveBroker(nil, "   ", nil)
	if err == nil {
		t.Error("expected error for whitespace-only projectRoot, got nil")
	}
}

func TestNewRestrictiveBrokerNormalisesRoot(t *testing.T) {
	dir := t.TempDir()
	b, err := NewRestrictiveBroker(nil, dir, nil)
	if err != nil {
		t.Fatalf("NewRestrictiveBroker: %v", err)
	}
	if !filepath.IsAbs(b.ProjectRoot) {
		t.Errorf("ProjectRoot %q is not absolute", b.ProjectRoot)
	}
}

func TestNewRestrictiveBrokerNormalisesBins(t *testing.T) {
	b, err := NewRestrictiveBroker([]string{"Git", "GO.EXE", "make.cmd"}, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// All should be stored lowercase and extension-stripped.
	for _, want := range []string{"git", "go", "make"} {
		if !b.AllowBins[want] {
			t.Errorf("AllowBins missing normalised entry %q; map = %v", want, b.AllowBins)
		}
	}
}

// ── exec: basename normalisation ──────────────────────────────────────────────

func TestExecAllowlistedBareNameAllowed(t *testing.T) {
	b := mustBroker(t, []string{"git", "go"}, nil)
	for _, bin := range []string{"git", "go"} {
		d := b.Check(Action{Kind: "exec", Target: bin})
		if !d.Allow {
			t.Errorf("exec %q: want Allow, got deny: %s", bin, d.Reason)
		}
	}
}

func TestExecUppercaseNameAllowed(t *testing.T) {
	b := mustBroker(t, []string{"git"}, nil)
	d := b.Check(Action{Kind: "exec", Target: "GIT"})
	if !d.Allow {
		t.Errorf("exec GIT: want Allow, got deny: %s", d.Reason)
	}
}

func TestExecExeExtensionStripped(t *testing.T) {
	b := mustBroker(t, []string{"git"}, nil)
	for _, name := range []string{"git.exe", "GIT.EXE", "git.cmd", "git.bat", "git.com", "git.ps1"} {
		d := b.Check(Action{Kind: "exec", Target: name})
		if !d.Allow {
			t.Errorf("exec %q: want Allow (extension should be stripped), got deny: %s", name, d.Reason)
		}
	}
}

func TestExecNotAllowlistedDenied(t *testing.T) {
	b := mustBroker(t, []string{"git"}, nil)
	for _, bin := range []string{"powershell", "bash", "sh", "ssh", "cmd"} {
		d := b.Check(Action{Kind: "exec", Target: bin})
		if d.Allow {
			t.Errorf("exec %q: want deny, got Allow", bin)
		}
	}
}

func TestExecPowershellExeDenied(t *testing.T) {
	b := mustBroker(t, []string{"git"}, nil)
	// Absolute Windows-style path — must not bypass the allowlist.
	d := b.Check(Action{Kind: "exec", Target: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`})
	if d.Allow {
		t.Errorf("absolute powershell.exe path: want deny, got Allow")
	}
}

func TestExecAbsolutePathUsesBasenameOnly(t *testing.T) {
	b := mustBroker(t, []string{"git"}, nil)
	// /usr/bin/ssh — not allowlisted, must be denied.
	d := b.Check(Action{Kind: "exec", Target: "/usr/bin/ssh"})
	if d.Allow {
		t.Errorf("/usr/bin/ssh: want deny (ssh not allowlisted), got Allow")
	}
	// /usr/bin/git — allowlisted, must be allowed.
	d = b.Check(Action{Kind: "exec", Target: "/usr/bin/git"})
	if !d.Allow {
		t.Errorf("/usr/bin/git: want Allow (git is allowlisted), got deny: %s", d.Reason)
	}
}

// Test that both forward-slash and backslash separators are handled by execBasename.
func TestExecForeignSeparatorBasename(t *testing.T) {
	b := mustBroker(t, []string{"git"}, nil)
	// Windows-style path on any OS — manual split must extract "git.exe".
	d := b.Check(Action{Kind: "exec", Target: `C:\Program Files\Git\bin\git.exe`})
	if !d.Allow {
		t.Errorf(`Windows path C:\Program Files\Git\bin\git.exe: want Allow, got deny: %s`, d.Reason)
	}
}

func TestExecEmptyTargetDenied(t *testing.T) {
	b := mustBroker(t, []string{"git"}, nil)
	d := b.Check(Action{Kind: "exec", Target: ""})
	if d.Allow {
		t.Error("empty exec target: want deny, got Allow")
	}
	d = b.Check(Action{Kind: "exec", Target: "   "})
	if d.Allow {
		t.Error("whitespace exec target: want deny, got Allow")
	}
}

// ── read/write: path containment ──────────────────────────────────────────────

func TestReadInsideRootAllowed(t *testing.T) {
	root := t.TempDir()
	b := mustBrokerWithRoot(t, nil, root, nil)
	inside := filepath.Join(root, "subdir", "file.txt")
	d := b.Check(Action{Kind: "read", Target: inside})
	if !d.Allow {
		t.Errorf("read inside root: want Allow, got deny: %s", d.Reason)
	}
}

func TestWriteInsideRootAllowed(t *testing.T) {
	root := t.TempDir()
	b := mustBrokerWithRoot(t, nil, root, nil)
	inside := filepath.Join(root, "out.txt")
	d := b.Check(Action{Kind: "write", Target: inside})
	if !d.Allow {
		t.Errorf("write inside root: want Allow, got deny: %s", d.Reason)
	}
}

func TestReadRootItselfAllowed(t *testing.T) {
	root := t.TempDir()
	b := mustBrokerWithRoot(t, nil, root, nil)
	d := b.Check(Action{Kind: "read", Target: root})
	if !d.Allow {
		t.Errorf("read of root itself: want Allow, got deny: %s", d.Reason)
	}
}

func TestReadRelativeDotDotDenied(t *testing.T) {
	root := t.TempDir()
	b := mustBrokerWithRoot(t, nil, root, nil)
	d := b.Check(Action{Kind: "read", Target: "../outside/secret.txt"})
	if d.Allow {
		t.Errorf("relative .. traversal: want deny, got Allow")
	}
}

func TestReadDeepDotDotDenied(t *testing.T) {
	root := t.TempDir()
	b := mustBrokerWithRoot(t, nil, root, nil)
	// Many levels up — filepath.Clean will reduce this.
	target := filepath.Join("..", "..", "..", "Windows", "System32", "x.dll")
	d := b.Check(Action{Kind: "read", Target: target})
	if d.Allow {
		t.Errorf("deep .. traversal %q: want deny, got Allow", target)
	}
}

func TestReadAbsoluteOutsideRootDenied(t *testing.T) {
	root := t.TempDir()
	b := mustBrokerWithRoot(t, nil, root, nil)
	// An absolute path that does not start with root.
	outside := filepath.Join(filepath.VolumeName(root)+string(filepath.Separator), "tmp", "secret.txt")
	// Make sure we actually have a path outside.
	if isDescendantOf(filepath.Clean(outside), root) {
		t.Skip("cannot construct outside path distinct from root on this OS")
	}
	d := b.Check(Action{Kind: "read", Target: outside})
	if d.Allow {
		t.Errorf("absolute outside read %q: want deny, got Allow", outside)
	}
}

// Guard the prefix-sibling bug: /proj must NOT allow /proj-evil.
func TestReadPrefixSiblingDenied(t *testing.T) {
	// We need two temp dirs that share a parent and whose names are sibling-prefixes.
	parent := t.TempDir()
	root := filepath.Join(parent, "proj")
	sibling := filepath.Join(parent, "proj-evil")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}

	b, err := NewRestrictiveBroker(nil, root, nil)
	if err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(sibling, "secret.txt")
	d := b.Check(Action{Kind: "read", Target: target})
	if d.Allow {
		t.Errorf("prefix-sibling path %q: want deny (must not match /proj-evil under /proj), got Allow", target)
	}
}

func TestReadUNCPathDenied(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("UNC path test only meaningful on Windows")
	}
	root := t.TempDir()
	b := mustBrokerWithRoot(t, nil, root, nil)
	d := b.Check(Action{Kind: "read", Target: `\\server\share\file.txt`})
	if d.Allow {
		t.Errorf("UNC path: want deny, got Allow")
	}
}

func TestReadEmptyTargetDenied(t *testing.T) {
	root := t.TempDir()
	b := mustBrokerWithRoot(t, nil, root, nil)
	d := b.Check(Action{Kind: "read", Target: ""})
	if d.Allow {
		t.Error("empty read target: want deny, got Allow")
	}
}

// ── net: host allowlist ───────────────────────────────────────────────────────

func TestNetAllowlistedHostAllowed(t *testing.T) {
	b := mustBroker(t, nil, []string{"api.anthropic.com"})
	d := b.Check(Action{Kind: "net", Target: "https://api.anthropic.com/v1/messages"})
	if !d.Allow {
		t.Errorf("allowlisted host: want Allow, got deny: %s", d.Reason)
	}
}

func TestNetNonAllowlistedHostDenied(t *testing.T) {
	b := mustBroker(t, nil, []string{"api.anthropic.com"})
	for _, u := range []string{
		"https://evil.example.com",
		"https://api.anthropic.com.evil.com",
		"https://anthropic.com",
	} {
		d := b.Check(Action{Kind: "net", Target: u})
		if d.Allow {
			t.Errorf("net %q: want deny, got Allow", u)
		}
	}
}

func TestNetHostCaseInsensitive(t *testing.T) {
	b := mustBroker(t, nil, []string{"api.anthropic.com"})
	d := b.Check(Action{Kind: "net", Target: "https://API.ANTHROPIC.COM/v1/messages"})
	if !d.Allow {
		t.Errorf("uppercase host: want Allow, got deny: %s", d.Reason)
	}
}

func TestNetHostWithPortAllowed(t *testing.T) {
	b := mustBroker(t, nil, []string{"api.anthropic.com"})
	d := b.Check(Action{Kind: "net", Target: "https://api.anthropic.com:443/path"})
	if !d.Allow {
		t.Errorf("host with port: want Allow, got deny: %s", d.Reason)
	}
}

func TestNetBareHostnameDenied(t *testing.T) {
	// A bare hostname without scheme is not a well-formed URL with a Host field.
	b := mustBroker(t, nil, []string{"api.anthropic.com"})
	d := b.Check(Action{Kind: "net", Target: "api.anthropic.com"})
	// url.Parse("api.anthropic.com") treats the whole thing as a path, host="".
	if d.Allow {
		t.Errorf("bare hostname (no scheme): want deny (no extractable host), got Allow")
	}
}

func TestNetEmptyTargetDenied(t *testing.T) {
	b := mustBroker(t, nil, []string{"api.anthropic.com"})
	d := b.Check(Action{Kind: "net", Target: ""})
	if d.Allow {
		t.Error("empty net target: want deny, got Allow")
	}
}

// ── unknown kind ──────────────────────────────────────────────────────────────

func TestUnknownKindDenied(t *testing.T) {
	b := mustBroker(t, nil, nil)
	for _, kind := range []string{"", "delete", "spawn", "EXEC", "READ"} {
		d := b.Check(Action{Kind: kind, Target: "whatever"})
		if d.Allow {
			t.Errorf("unknown kind %q: want deny, got Allow", kind)
		}
	}
}

// ── symlink: root escape denied ───────────────────────────────────────────────

func TestReadSymlinkEscapingRootDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Symlink creation on Windows requires elevated privileges or Developer Mode.
		// Skip gracefully rather than failing.
		t.Skip("symlink creation may require elevated privileges on Windows")
	}

	outside := t.TempDir()
	root := t.TempDir()
	// Create a file outside the root.
	secretPath := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create a symlink inside the root pointing to the file outside.
	linkPath := filepath.Join(root, "link.txt")
	if err := os.Symlink(secretPath, linkPath); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	b := mustBrokerWithRoot(t, nil, root, nil)
	d := b.Check(Action{Kind: "read", Target: linkPath})
	if d.Allow {
		t.Errorf("symlink escaping root: want deny, got Allow (symlink %q → %q)", linkPath, secretPath)
	}
}

// ── normaliseExecName unit tests ──────────────────────────────────────────────

func TestNormaliseExecName(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"git", "git"},
		{"Git", "git"},
		{"GIT", "git"},
		{"git.exe", "git"},
		{"GIT.EXE", "git"},
		{"powershell.exe", "powershell"},
		{"powershell.cmd", "powershell"},
		{"powershell.bat", "powershell"},
		{"powershell.com", "powershell"},
		{"powershell.ps1", "powershell"},
		// Non-executable extension should be kept.
		{"script.sh", "script.sh"},
		{"build.py", "build.py"},
	}
	for _, tc := range cases {
		got := normaliseExecName(tc.input)
		if got != tc.want {
			t.Errorf("normaliseExecName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ── execBasename unit tests ───────────────────────────────────────────────────

func TestExecBasename(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"git", "git"},
		{"/usr/bin/git", "git"},
		{`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, "powershell.exe"},
		{`C:\Program Files\Git\bin\git.exe`, "git.exe"},
		// Mixed separators.
		{`/mixed\path/file.exe`, "file.exe"},
	}
	for _, tc := range cases {
		got := execBasename(tc.input)
		if got != tc.want {
			t.Errorf("execBasename(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ── isDescendantOf unit tests ─────────────────────────────────────────────────

func TestIsDescendantOf(t *testing.T) {
	sep := string(filepath.Separator)
	root := filepath.Join(sep, "proj")

	cases := []struct {
		target string
		want   bool
	}{
		{root, true},                                          // root itself
		{filepath.Join(root, "sub"), true},                   // child
		{filepath.Join(root, "sub", "deep"), true},           // grandchild
		{filepath.Join(sep, "proj-evil"), false},             // prefix-sibling
		{filepath.Join(sep, "proj-evil", "file"), false},     // file in prefix-sibling
		{filepath.Join(sep, "other"), false},                 // unrelated
		{filepath.Join(sep, "pr"), false},                    // shorter prefix
	}
	for _, tc := range cases {
		got := isDescendantOf(tc.target, root)
		if got != tc.want {
			t.Errorf("isDescendantOf(%q, %q) = %v, want %v", tc.target, root, got, tc.want)
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// mustBroker creates a RestrictiveBroker using t.TempDir() as the project root.
func mustBroker(t *testing.T, bins []string, hosts []string) *RestrictiveBroker {
	t.Helper()
	b, err := NewRestrictiveBroker(bins, t.TempDir(), hosts)
	if err != nil {
		t.Fatalf("NewRestrictiveBroker: %v", err)
	}
	return b
}

// mustBrokerWithRoot creates a RestrictiveBroker using the provided root.
func mustBrokerWithRoot(t *testing.T, bins []string, root string, hosts []string) *RestrictiveBroker {
	t.Helper()
	b, err := NewRestrictiveBroker(bins, root, hosts)
	if err != nil {
		t.Fatalf("NewRestrictiveBroker: %v", err)
	}
	return b
}

// quietBrokerDeny is a helper that checks that Check returns Allow==false
// and that the Reason is non-empty (never a silent deny).
func quietBrokerDeny(t *testing.T, b *RestrictiveBroker, a Action, label string) {
	t.Helper()
	d := b.Check(a)
	if d.Allow {
		t.Errorf("%s: want deny, got Allow", label)
	}
	if strings.TrimSpace(d.Reason) == "" {
		t.Errorf("%s: deny has empty Reason", label)
	}
}

// TestAllDeniesHaveReasons runs through a set of canonical deny scenarios and verifies
// that every Decision{Allow:false} has a non-empty Reason string.
func TestAllDeniesHaveReasons(t *testing.T) {
	root := t.TempDir()
	b := mustBrokerWithRoot(t, []string{"git", "go"}, root, []string{"api.anthropic.com"})

	actions := []struct {
		label  string
		action Action
	}{
		{"exec:powershell", Action{Kind: "exec", Target: "powershell"}},
		{"exec:ssh", Action{Kind: "exec", Target: "ssh"}},
		{"exec:bash", Action{Kind: "exec", Target: "bash"}},
		{"exec:empty", Action{Kind: "exec", Target: ""}},
		{"read:dotdot", Action{Kind: "read", Target: "../secret"}},
		{"read:empty", Action{Kind: "read", Target: ""}},
		{"net:evil", Action{Kind: "net", Target: "https://evil.example.com"}},
		{"net:empty", Action{Kind: "net", Target: ""}},
		{"kind:unknown", Action{Kind: "delete", Target: "anything"}},
		{"kind:empty", Action{Kind: "", Target: "anything"}},
	}
	for _, tc := range actions {
		quietBrokerDeny(t, b, tc.action, tc.label)
	}
}
