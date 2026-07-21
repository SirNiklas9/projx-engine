//go:build !windows

package main

import "syscall"

// quietSysProcAttr is a Windows presentation concern. Unix headless children
// inherit the caller's normal process attributes.
func quietSysProcAttr() *syscall.SysProcAttr { return nil }
