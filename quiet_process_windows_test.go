//go:build windows

package main

import "testing"

func TestQuietSysProcAttrCreatesNoWindow(t *testing.T) {
	attr := quietSysProcAttr()
	if attr == nil {
		t.Fatal("quietSysProcAttr returned nil")
	}
	if attr.CreationFlags != createNoWindow {
		t.Fatalf("CreationFlags = %#x, want %#x", attr.CreationFlags, createNoWindow)
	}
}

func TestDetachSysProcAttrIsDetachedAndQuiet(t *testing.T) {
	want := uint32(createNewProcessGroup | detachedProcess | createNoWindow)
	attr := detachSysProcAttr()
	if attr == nil {
		t.Fatal("detachSysProcAttr returned nil")
	}
	if attr.CreationFlags != want {
		t.Fatalf("CreationFlags = %#x, want %#x", attr.CreationFlags, want)
	}
}
