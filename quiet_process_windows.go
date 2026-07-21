//go:build windows

package main

import "syscall"

// quietSysProcAttr prevents a headless child from creating a visible console
// window when projx-engine itself is running without an attached console.
func quietSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: createNoWindow}
}
