//go:build linux

package core

import (
	"os"
	"strings"
)

// ApplyLandlockFromEnv reads a Landlock policy from environment variables and
// applies it to the current process. It is intended to be called from a
// re-exec'd child (e.g. the egress gateway child via egress.PreExecHook) right
// before syscall.Exec, so that FS confinement is in place when the final target
// process image is loaded.
//
// Environment variables:
//
//	PROJX_CONFINE_ROOT  — the project root path (always RW)
//	PROJX_CONFINE_RO    — os.PathListSeparator-joined list of read-only paths
//	PROJX_CONFINE_RW    — os.PathListSeparator-joined list of extra RW paths
//
// Returns nil (no-op) if PROJX_CONFINE_ROOT is empty.
func ApplyLandlockFromEnv() error {
	root := os.Getenv("PROJX_CONFINE_ROOT")
	if root == "" {
		return nil
	}

	var ro []string
	if v := os.Getenv("PROJX_CONFINE_RO"); v != "" {
		ro = strings.Split(v, string(os.PathListSeparator))
	}

	var rw []string
	if v := os.Getenv("PROJX_CONFINE_RW"); v != "" {
		rw = strings.Split(v, string(os.PathListSeparator))
	}

	p := Policy{
		Root:      root,
		ReadOnly:  ro,
		ReadWrite: rw,
	}
	c := landlockConfiner{}
	return c.Apply(p)
}
