//go:build windows

package confine

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestConfinedProcessCreationFlagsAreHeadless(t *testing.T) {
	want := uint32(windows.EXTENDED_STARTUPINFO_PRESENT |
		windows.CREATE_UNICODE_ENVIRONMENT |
		windows.CREATE_NO_WINDOW)
	if confinedProcessCreationFlags != want {
		t.Fatalf("creation flags = %#x, want %#x", confinedProcessCreationFlags, want)
	}
}
