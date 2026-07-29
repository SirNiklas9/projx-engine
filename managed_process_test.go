package main

import "testing"

func TestManagedChildEnvReplacesParentLease(t *testing.T) {
	got := managedChildEnv([]string{"A=B", managedParentPIDEnv + "=99"}, 42)
	if len(got) != 2 || got[0] != "A=B" || got[1] != managedParentPIDEnv+"=42" {
		t.Fatalf("managed child env = %v", got)
	}
}

func TestManagedParentPIDRejectsInvalidValues(t *testing.T) {
	t.Setenv(managedParentPIDEnv, "not-a-pid")
	if got := managedParentPID(); got != 0 {
		t.Fatalf("invalid parent pid = %d", got)
	}
	t.Setenv(managedParentPIDEnv, "42")
	if got := managedParentPID(); got != 42 {
		t.Fatalf("parent pid = %d", got)
	}
}
