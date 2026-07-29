package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScheduleMapRefreshIsNonblockingAndDeduplicated(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "yours"))
	st := openStore(root)
	st.Close()
	oldStart := startDetachedMapRefresh
	t.Cleanup(func() { startDetachedMapRefresh = oldStart })
	starts := 0
	startDetachedMapRefresh = func(_, _ string) error {
		starts++
		return nil
	}

	if !scheduleMapRefresh(root) {
		t.Fatal("first refresh was not scheduled")
	}
	if scheduleMapRefresh(root) {
		t.Fatal("duplicate refresh was scheduled")
	}
	if starts != 1 || !mapRefreshInProgress(root) {
		t.Fatalf("starts=%d refreshing=%t", starts, mapRefreshInProgress(root))
	}
}

func TestRunBackgroundMapSyncRejectsUnownedLockPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "yours"))
	st := openStore(root)
	st.Close()
	outside := filepath.Join(t.TempDir(), "other.lock")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	runBackgroundMapSync(root, []string{outside})
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("unowned lock path was removed")
	}
}

func TestScheduleMapRefreshSkipsUserHomeProjectStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "yours"))
	st := openStore(home)
	st.Close()

	oldStart := startDetachedMapRefresh
	t.Cleanup(func() { startDetachedMapRefresh = oldStart })
	started := false
	startDetachedMapRefresh = func(_, _ string) error {
		started = true
		return nil
	}

	if lockPath := mapRefreshLockPath(home); lockPath != "" {
		t.Fatalf("home map refresh lock = %q, want empty", lockPath)
	}
	if scheduleMapRefresh(home) {
		t.Fatal("user home was scheduled as a code-map source")
	}
	if started {
		t.Fatal("user-home map refresh worker was started")
	}
}

func TestScheduleMapRefreshDecisionExplainsDeduplication(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "yours"))
	st := openStore(root)
	st.Close()
	oldStart := startDetachedMapRefresh
	t.Cleanup(func() { startDetachedMapRefresh = oldStart })
	startDetachedMapRefresh = func(_, _ string) error { return nil }
	if got := scheduleMapRefreshDecision(root); got != "started" {
		t.Fatalf("first decision = %q, want started", got)
	}
	if got := scheduleMapRefreshDecision(root); got != "existing-lock" {
		t.Fatalf("second decision = %q, want existing-lock", got)
	}
}
