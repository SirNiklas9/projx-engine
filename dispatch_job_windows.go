//go:build windows

package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// dispatchDescendantJob belongs to a detached dispatch/workflow supervisor.
// Closing its last handle kills the complete managed tree, including provider
// CLIs started by an agent child. Detached maintenance work is never launched
// from a supervisor and is therefore the sole intentional exception.
var dispatchDescendantJob windows.Handle

func containDispatchDescendants() error {
	if dispatchDescendantJob != 0 {
		return nil
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create dispatch job: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("configure dispatch job: %w", err)
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("assign dispatch job: %w", err)
	}
	dispatchDescendantJob = job
	return nil
}
