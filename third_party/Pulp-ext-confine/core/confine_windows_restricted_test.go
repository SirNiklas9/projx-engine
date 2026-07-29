//go:build windows

package core

// Unit tests for the env-gated restricted-token confiner SELECTION and its
// Level() string. These are non-interactive: they prove the routing logic and
// the confiner identity without launching a process or driving a TUI. They do
// NOT assert that an interactive TUI renders (that cannot be proven by a
// non-interactive harness).

import (
	"testing"
)

func TestPlatformConfiner_DefaultIsAppContainer(t *testing.T) {
	// With the interactive env var unset, platformConfiner must return the
	// unchanged AppContainer cage.
	t.Setenv("PROJX_CONFINE_WIN_INTERACTIVE", "")
	c := platformConfiner()
	if got := c.Level(); got != "os-fs:appcontainer" {
		t.Fatalf("default confiner Level = %q, want os-fs:appcontainer", got)
	}
	if _, ok := c.(appcontainerConfiner); !ok {
		t.Fatalf("default confiner type = %T, want appcontainerConfiner", c)
	}
}

func TestPlatformConfiner_InteractiveSelectsRestricted(t *testing.T) {
	// With PROJX_CONFINE_WIN_INTERACTIVE=1, platformConfiner must return the
	// prototype restricted-token confiner.
	t.Setenv("PROJX_CONFINE_WIN_INTERACTIVE", "1")
	c := platformConfiner()
	if got := c.Level(); got != "os-fs:restricted-token" {
		t.Fatalf("interactive confiner Level = %q, want os-fs:restricted-token", got)
	}
	if _, ok := c.(restrictedTokenConfiner); !ok {
		t.Fatalf("interactive confiner type = %T, want restrictedTokenConfiner", c)
	}
	if !c.Available() {
		t.Fatal("restrictedTokenConfiner.Available() = false, want true")
	}
	// Apply is a no-op and must not error.
	if err := c.Apply(Policy{Root: t.TempDir()}); err != nil {
		t.Fatalf("restrictedTokenConfiner.Apply: %v", err)
	}
}

func TestRestrictedConfiner_EmptyArgvFailsClosed(t *testing.T) {
	// LaunchConfined must fail closed on empty argv (no process spawned).
	c := restrictedTokenConfiner{}
	_, err := c.LaunchConfined(Policy{Root: t.TempDir()}, nil, nil, "")
	if err == nil {
		t.Fatal("LaunchConfined(empty argv) = nil error, want fail-closed error")
	}
}
