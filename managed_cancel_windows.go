//go:build windows

package main

import (
	"fmt"
	"os"
)

// The supervisor owns a kill-on-close Job Object, so terminating it closes the
// job and removes every managed descendant as one tree.
func terminateManagedTree(pid int) error {
	if pid < 1 {
		return fmt.Errorf("invalid managed supervisor PID %d", pid)
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
