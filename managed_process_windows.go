//go:build windows

package main

import "syscall"

// Windows dispatch supervisors use a kill-on-close Job Object. Their children
// stay in that job, so no per-child watcher is needed.
func managedChildSysProcAttr() *syscall.SysProcAttr { return quietSysProcAttr() }
func startManagedParentWatcher()                    {}
