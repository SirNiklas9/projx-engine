//go:build !linux && !windows

package confine_test

import (
	"testing"

	"github.com/SirNiklas9/projx-engine/internal/confine"
)

func TestDetectNonLinux(t *testing.T) {
	c := confine.Detect()
	if c.Level() != "cooperative" {
		t.Errorf("expected level \"cooperative\", got %q", c.Level())
	}
	if c.Available() {
		t.Error("expected Available() == false on non-Linux")
	}
}

func TestApplyNonLinux(t *testing.T) {
	c := confine.Detect()
	p := confine.DefaultPolicy("/tmp", "", "")
	if err := c.Apply(p); err != nil {
		t.Errorf("Apply on non-Linux should be a no-op (nil error), got: %v", err)
	}
}
