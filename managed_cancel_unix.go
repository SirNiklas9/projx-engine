//go:build !windows

package main

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

// Unix supervisors start their own session/process group. Graceful cancellation
// signals that complete group; a bounded escalation guarantees no child survives.
func terminateManagedTree(pid int) error {
	if pid < 1 {
		return fmt.Errorf("invalid managed supervisor PID %d", pid)
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
