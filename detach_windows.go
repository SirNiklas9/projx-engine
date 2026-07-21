//go:build windows

package main

import "syscall"

// Windows process-creation flags (not exported by the syscall package by name).
const (
	createNewProcessGroup = 0x00000200 // CREATE_NEW_PROCESS_GROUP
	detachedProcess       = 0x00000008 // DETACHED_PROCESS
	createNoWindow        = 0x08000000 // CREATE_NO_WINDOW
)

// detachSysProcAttr returns the SysProcAttr that detaches a spawned supervisor
// from the trunk on Windows: a new process group with no attached console, so it
// survives the foreground `dispatch --run` returning.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess | createNoWindow}
}
