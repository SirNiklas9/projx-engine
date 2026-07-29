package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestHomeCommandShowsFriendlyProjectStatus(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "yours"))
	st := openStore(root)
	st.Close()

	out := captureStdout(t, func() { runHomeCmd(root, nil) })
	if !strings.Contains(out, "ProjX ready") || !strings.Contains(out, filepath.Base(root)) {
		t.Fatalf("home output = %q", out)
	}
	if strings.Contains(out, "Usage:") {
		t.Fatalf("home output fell back to usage: %q", out)
	}
}

func TestHomeCommandJSONIsPureStatusSnapshot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "yours"))
	st := openStore(root)
	st.Close()

	out := captureStdout(t, func() { runHomeCmd(root, []string{"--json"}) })
	var snapshot StatusSnapshot
	if err := json.Unmarshal([]byte(out), &snapshot); err != nil {
		t.Fatalf("home JSON = %q: %v", out, err)
	}
	if snapshot.ActiveRoot != root || !snapshot.Project {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestHomeCommandDoesNotSuggestProjectInitAtWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "yours"))
	captureStdout(t, func() { runWorkspaceInit(root) })

	out := captureStdout(t, func() { runHomeCmd(root, nil) })
	if !strings.Contains(out, "ProjX workspace active") {
		t.Fatalf("workspace home output = %q", out)
	}
	if strings.Contains(out, "projx --root . init") {
		t.Fatalf("workspace home suggested project init: %q", out)
	}
}
