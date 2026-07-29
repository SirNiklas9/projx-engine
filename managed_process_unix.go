//go:build !windows

package main

import (
	"os"
	"syscall"
	"time"
)

// Each managed child leads a distinct process group. Provider CLIs inherit the
// group, allowing one emergency signal to remove the whole managed tree.
func managedChildSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// startManagedParentWatcher gives Unix the Windows Job Object's crash-cleanup
// property. If the supervisor dies or is killed, this child detects the changed
// parent and kills its own complete process group. Normal completion never
// reaches this path because the supervisor waits for the child first.
func startManagedParentWatcher() {
	parentPID := managedParentPID()
	if parentPID == 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			if os.Getppid() != parentPID {
				// Parent loss is a crash/abandon path, not a graceful cancellation:
				// SIGKILL guarantees provider grandchildren cannot linger.
				_ = syscall.Kill(-os.Getpid(), syscall.SIGKILL)
				return
			}
		}
	}()
}
