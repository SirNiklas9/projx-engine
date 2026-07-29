//go:build !linux

package core

// ApplyLandlockFromEnv is a no-op on non-Linux platforms where Landlock is
// not available. It is safe to set as egress.PreExecHook on any platform.
func ApplyLandlockFromEnv() error { return nil }
