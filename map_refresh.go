package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const mapRefreshLockName = "map-refresh.lock"

var startDetachedMapRefresh = func(root, lockPath string) error {
	self := backgroundBinaryPath()
	cmd := exec.Command(self, "--root", root, "__map-sync-background", lockPath)
	cmd.SysProcAttr = detachHookWorkerSysProcAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func scheduleMapRefresh(root string) bool {
	return scheduleMapRefreshDecision(root) == "started"
}

// scheduleMapRefreshDecision records why SessionStart did or did not create the
// one explicitly detached maintenance worker. The value is telemetry, not policy.
func scheduleMapRefreshDecision(root string) string {
	lockPath := mapRefreshLockPath(root)
	if lockPath == "" {
		return "not-applicable"
	}
	if info, err := os.Stat(lockPath); err == nil {
		if time.Since(info.ModTime()) < 10*time.Minute {
			return "existing-lock"
		}
		_ = os.Remove(lockPath)
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return "mkdir-failed"
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "lock-race"
	}
	_ = f.Close()
	if err := startDetachedMapRefresh(root, lockPath); err != nil {
		_ = os.Remove(lockPath)
		return "start-failed"
	}
	return "started"
}

func runBackgroundMapSync(root string, args []string) {
	lockPath := mapRefreshLockPath(root)
	if len(args) != 1 || filepath.Clean(args[0]) != filepath.Clean(lockPath) || lockPath == "" {
		return
	}
	defer os.Remove(lockPath)
	_, _, _, _ = syncMap(root, nil)
}

func mapRefreshInProgress(root string) bool {
	lockPath := mapRefreshLockPath(root)
	if lockPath == "" {
		return false
	}
	info, err := os.Stat(lockPath)
	return err == nil && time.Since(info.ModTime()) < 10*time.Minute
}

func mapRefreshLockPath(root string) string {
	if root == "" {
		return ""
	}
	if project := nearestProjxDir(root); project != "" {
		// A stale project store in the user home must never turn the entire
		// profile into an automatic code-map source.
		if isUserHomeRoot(project) {
			return ""
		}
		return filepath.Join(project, ".projx", mapRefreshLockName)
	}
	if workspacePath := workspaceStorePath(root); workspacePath != "" {
		return filepath.Join(filepath.Dir(workspacePath), mapRefreshLockName)
	}
	return ""
}
