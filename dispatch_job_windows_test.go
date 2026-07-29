//go:build windows

package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	dispatchJobOwnerEnv    = "PROJX_TEST_DISPATCH_JOB_OWNER"
	dispatchJobChildEnv    = "PROJX_TEST_DISPATCH_JOB_CHILD"
	dispatchJobPIDEnv      = "PROJX_TEST_DISPATCH_JOB_PID_FILE"
	dispatchCancelOwnerEnv = "PROJX_TEST_DISPATCH_CANCEL_OWNER"
	dispatchCancelChildEnv = "PROJX_TEST_DISPATCH_CANCEL_CHILD"
	dispatchCancelPIDEnv   = "PROJX_TEST_DISPATCH_CANCEL_PID_FILE"
)

func TestDispatchJobKillsManagedDescendantsOnExit(t *testing.T) {
	if os.Getenv(dispatchJobChildEnv) == "1" {
		time.Sleep(30 * time.Second)
		return
	}
	if os.Getenv(dispatchJobOwnerEnv) == "1" {
		if err := containDispatchDescendants(); err != nil {
			panic(err)
		}
		child := exec.Command(os.Args[0], "-test.run=^TestDispatchJobKillsManagedDescendantsOnExit$")
		child.Env = append(os.Environ(), dispatchJobChildEnv+"=1")
		child.SysProcAttr = quietSysProcAttr()
		if err := child.Start(); err != nil {
			panic(err)
		}
		if err := os.WriteFile(os.Getenv(dispatchJobPIDEnv), []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
			panic(err)
		}
		return // job handle closes as this owner process exits
	}

	pidFile := t.TempDir() + `\child.pid`
	owner := exec.Command(os.Args[0], "-test.run=^TestDispatchJobKillsManagedDescendantsOnExit$")
	owner.Env = append(os.Environ(), dispatchJobOwnerEnv+"=1", dispatchJobPIDEnv+"="+pidFile)
	owner.SysProcAttr = quietSysProcAttr()
	if output, err := owner.CombinedOutput(); err != nil {
		t.Fatalf("run dispatch-job owner: %v\n%s", err, output)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for windowsProcessRunning(pid) && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if windowsProcessRunning(pid) {
		if process, findErr := os.FindProcess(pid); findErr == nil {
			_ = process.Kill()
		}
		t.Fatalf("managed dispatch child %d survived its supervisor", pid)
	}
}

func TestTerminateManagedTreeKillsSupervisorAndChild(t *testing.T) {
	if os.Getenv(dispatchCancelChildEnv) == "1" {
		time.Sleep(30 * time.Second)
		return
	}
	if os.Getenv(dispatchCancelOwnerEnv) == "1" {
		if err := containDispatchDescendants(); err != nil {
			panic(err)
		}
		child := exec.Command(os.Args[0], "-test.run=^TestTerminateManagedTreeKillsSupervisorAndChild$")
		child.Env = append(os.Environ(), dispatchCancelChildEnv+"=1")
		child.SysProcAttr = quietSysProcAttr()
		if err := child.Start(); err != nil {
			panic(err)
		}
		body := strconv.Itoa(os.Getpid()) + "," + strconv.Itoa(child.Process.Pid)
		if err := os.WriteFile(os.Getenv(dispatchCancelPIDEnv), []byte(body), 0o644); err != nil {
			panic(err)
		}
		time.Sleep(30 * time.Second)
		return
	}

	pidFile := t.TempDir() + `\cancel.pid`
	owner := exec.Command(os.Args[0], "-test.run=^TestTerminateManagedTreeKillsSupervisorAndChild$")
	owner.Env = append(os.Environ(), dispatchCancelOwnerEnv+"=1", dispatchCancelPIDEnv+"="+pidFile)
	owner.SysProcAttr = quietSysProcAttr()
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var pids []int
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil {
			for _, field := range strings.Split(strings.TrimSpace(string(data)), ",") {
				pid, parseErr := strconv.Atoi(field)
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				pids = append(pids, pid)
			}
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(pids) != 2 {
		t.Fatal("owner did not publish supervisor and child PIDs")
	}
	if err := terminateManagedTree(pids[0]); err != nil {
		t.Fatal(err)
	}
	for _, pid := range pids {
		deadline = time.Now().Add(3 * time.Second)
		for windowsProcessRunning(pid) && time.Now().Before(deadline) {
			time.Sleep(25 * time.Millisecond)
		}
		if windowsProcessRunning(pid) {
			if p, findErr := os.FindProcess(pid); findErr == nil {
				_ = p.Kill()
			}
			t.Fatalf("managed PID %d survived cancellation", pid)
		}
	}
}
