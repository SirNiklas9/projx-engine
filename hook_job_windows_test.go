//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const (
	hookJobOwnerEnv         = "PROJX_TEST_HOOK_JOB_OWNER"
	hookJobChildEnv         = "PROJX_TEST_HOOK_JOB_CHILD"
	hookJobDetachedOwnerEnv = "PROJX_TEST_HOOK_JOB_DETACHED_OWNER"
	hookJobDetachedChildEnv = "PROJX_TEST_HOOK_JOB_DETACHED_CHILD"
	hookParentOwnerEnv      = "PROJX_TEST_HOOK_PARENT_OWNER"
	hookParentChildEnv      = "PROJX_TEST_HOOK_PARENT_CHILD"
	hookJobPIDEnv           = "PROJX_TEST_HOOK_JOB_PID_FILE"
	stillActive             = 259
)

func TestHookJobKillsDescendantsOnExit(t *testing.T) {
	if os.Getenv(hookJobChildEnv) == "1" {
		time.Sleep(30 * time.Second)
		return
	}
	if os.Getenv(hookJobOwnerEnv) == "1" {
		runHookJobOwner()
		return
	}

	pidFile := t.TempDir() + `\child.pid`
	cmd := exec.Command(os.Args[0], "-test.run=^TestHookJobKillsDescendantsOnExit$")
	cmd.Env = append(os.Environ(), hookJobOwnerEnv+"=1", hookJobPIDEnv+"="+pidFile)
	cmd.SysProcAttr = quietSysProcAttr()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run hook-job owner: %v\n%s", err, output)
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse child pid: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for windowsProcessRunning(pid) && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if windowsProcessRunning(pid) {
		if process, findErr := os.FindProcess(pid); findErr == nil {
			_ = process.Kill()
		}
		t.Fatalf("hook child %d survived owner exit", pid)
	}
}

func runHookJobOwner() {
	if err := containHookDescendants(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(10)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHookJobKillsDescendantsOnExit$")
	cmd.Env = append(os.Environ(), hookJobChildEnv+"=1")
	cmd.SysProcAttr = quietSysProcAttr()
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(11)
	}
	if err := os.WriteFile(os.Getenv(hookJobPIDEnv), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = cmd.Process.Kill()
		os.Exit(12)
	}
	os.Exit(0)
}

func windowsProcessRunning(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	return windows.GetExitCodeProcess(handle, &exitCode) == nil && exitCode == stillActive
}

func TestHookJobAllowsDetachedMaintenanceWorker(t *testing.T) {
	if os.Getenv(hookJobDetachedChildEnv) == "1" {
		time.Sleep(30 * time.Second)
		return
	}
	if os.Getenv(hookJobDetachedOwnerEnv) == "1" {
		runDetachedHookJobOwner()
		return
	}

	pidFile := t.TempDir() + `\detached-child.pid`
	cmd := exec.Command(os.Args[0], "-test.run=^TestHookJobAllowsDetachedMaintenanceWorker$")
	cmd.Env = append(os.Environ(), hookJobDetachedOwnerEnv+"=1", hookJobPIDEnv+"="+pidFile)
	cmd.SysProcAttr = quietSysProcAttr()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run detached hook-job owner: %v\n%s", err, output)
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse detached child pid: %v", err)
	}
	if !windowsProcessRunning(pid) {
		t.Fatalf("detached hook worker %d did not survive owner exit", pid)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Kill(); err != nil {
		t.Fatalf("clean up detached hook worker %d: %v", pid, err)
	}
}

func runDetachedHookJobOwner() {
	if err := containHookDescendants(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(20)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHookJobAllowsDetachedMaintenanceWorker$")
	cmd.Env = append(os.Environ(), hookJobDetachedChildEnv+"=1")
	cmd.SysProcAttr = detachHookWorkerSysProcAttr()
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(21)
	}
	if err := os.WriteFile(os.Getenv(hookJobPIDEnv), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = cmd.Process.Kill()
		os.Exit(22)
	}
	_ = cmd.Process.Release()
	os.Exit(0)
}

func TestHookExitsWhenLauncherDies(t *testing.T) {
	if os.Getenv(hookParentChildEnv) == "1" {
		if err := containHookDescendants(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(30)
		}
		if err := os.WriteFile(os.Getenv(hookJobPIDEnv), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(31)
		}
		time.Sleep(30 * time.Second)
		return
	}
	if os.Getenv(hookParentOwnerEnv) == "1" {
		runHookLauncherOwner()
		return
	}

	pidFile := t.TempDir() + `\watched-hook.pid`
	cmd := exec.Command(os.Args[0], "-test.run=^TestHookExitsWhenLauncherDies$")
	cmd.Env = append(os.Environ(), hookParentOwnerEnv+"=1", hookJobPIDEnv+"="+pidFile)
	cmd.SysProcAttr = quietSysProcAttr()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run hook launcher owner: %v\n%s", err, output)
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse watched hook pid: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for windowsProcessRunning(pid) && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if windowsProcessRunning(pid) {
		if process, findErr := os.FindProcess(pid); findErr == nil {
			_ = process.Kill()
		}
		t.Fatalf("hook %d survived launcher exit", pid)
	}
}

func runHookLauncherOwner() {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHookExitsWhenLauncherDies$")
	cmd.Env = append(os.Environ(), hookParentChildEnv+"=1")
	cmd.SysProcAttr = quietSysProcAttr()
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(32)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(os.Getenv(hookJobPIDEnv)); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			os.Exit(33)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = cmd.Process.Release()
	os.Exit(0)
}
