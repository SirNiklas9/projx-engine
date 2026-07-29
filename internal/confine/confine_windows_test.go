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

func TestAppContainerCapabilitiesRequireExplicitEgress(t *testing.T) {
	without, err := appContainerCapabilities([]string{"PATH=C:\\Windows"})
	if err != nil || len(without) != 0 {
		t.Fatalf("no egress capabilities = %v, %v", without, err)
	}
	with, err := appContainerCapabilities([]string{"PROJX_BROKER_ALLOW_HOSTS=api.openai.com"})
	if err != nil || len(with) != 1 || with[0].Sid == nil || with[0].Attributes != windows.SE_GROUP_ENABLED {
		t.Fatalf("internet capability = %+v, %v", with, err)
	}
}
