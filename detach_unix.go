//go:build !windows

package main

import "syscall"

// detachSysProcAttr returns the SysProcAttr that fully detaches a spawned
// supervisor from the trunk: a new session (Setsid) so it is NOT killed when the
// foreground `dispatch --run` process returns and its process group is reaped.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
