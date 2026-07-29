//go:build windows

package main

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// hookDescendantJob stays open for the hook process lifetime. The handle is not
// inheritable, so Windows closes it when the hook exits and terminates its job.
var hookDescendantJob windows.Handle

func containHookDescendants() error {
	if hookDescendantJob != 0 {
		return nil
	}

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create hook job: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags =
		windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("configure hook job: %w", err)
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("assign hook job: %w", err)
	}
	hookDescendantJob = job
	startHookParentWatcher()
	return nil
}

func startHookParentWatcher() {
	parentID, err := hookParentProcessID()
	if err != nil || parentID == 0 {
		return
	}
	parent, err := windows.OpenProcess(windows.SYNCHRONIZE, false, parentID)
	if err != nil {
		return
	}
	go func() {
		event, waitErr := windows.WaitForSingleObject(parent, windows.INFINITE)
		_ = windows.CloseHandle(parent)
		if waitErr == nil && event == windows.WAIT_OBJECT_0 {
			os.Exit(0)
		}
	}()
}

func hookParentProcessID() (uint32, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return 0, err
	}
	current := windows.GetCurrentProcessId()
	for {
		if entry.ProcessID == current {
			return entry.ParentProcessID, nil
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if err == windows.ERROR_NO_MORE_FILES {
				break
			}
			return 0, err
		}
	}
	return 0, fmt.Errorf("current hook process was not present in the process snapshot")
}
