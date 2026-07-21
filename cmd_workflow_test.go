package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWorkflowManifestTestFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workflow.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadWorkflowManifestRejectsUnknownFields(t *testing.T) {
	path := writeWorkflowManifestTestFile(t, `{
  "name": "release",
  "steps": [{"id":"test", "task":"run tests", "gaet":"behavioral"}]
}`)

	_, err := loadWorkflowManifest(path)
	if err == nil || !strings.Contains(err.Error(), "unknown field \"gaet\"") {
		t.Fatalf("got %v, want unknown-field error", err)
	}
}

func TestLoadWorkflowManifestRejectsTrailingJSON(t *testing.T) {
	path := writeWorkflowManifestTestFile(t,
		`{"steps":[{"id":"test","task":"run tests"}]} {"steps":[]}`)

	_, err := loadWorkflowManifest(path)
	if err == nil || !strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("got %v, want trailing-JSON error", err)
	}
}

func TestLoadWorkflowManifestAcceptsDeclaredShape(t *testing.T) {
	path := writeWorkflowManifestTestFile(t, `{
  "name": "release",
  "steps": [
    {"id":"test", "task":"run tests", "gate":"behavioral"},
    {"id":"package", "task":"package release", "deps":["test"], "tier":"default", "role":"release"}
  ]
}`)

	m, err := loadWorkflowManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Steps) != 2 || m.Steps[1].Deps[0] != "test" {
		t.Fatalf("unexpected manifest: %+v", m)
	}
}
