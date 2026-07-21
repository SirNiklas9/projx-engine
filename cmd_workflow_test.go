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

func TestWorkflowBatchesParallelDisjointAndDeps(t *testing.T) {
	m := &WorkflowManifest{Parallel: true, Steps: []WorkflowStep{
		{ID: "a", Task: "a", Writes: []string{"internal/a/**"}},
		{ID: "b", Task: "b", Writes: []string{"internal/b/**"}},
		{ID: "c", Task: "c", Deps: []string{"a"}, Writes: []string{"internal/c/**"}},
	}}
	b, err := workflowBatches(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 2 || len(b[0]) != 2 || b[0][0] != 0 || b[0][1] != 1 || b[1][0] != 2 {
		t.Fatalf("unexpected batches: %#v", b)
	}
}

func TestWorkflowBatchesSerializesOverlappingWrites(t *testing.T) {
	m := &WorkflowManifest{Parallel: true, Steps: []WorkflowStep{
		{ID: "a", Task: "a", Writes: []string{"internal/shared/**"}},
		{ID: "b", Task: "b", Writes: []string{"internal/shared/file.go"}},
	}}
	b, err := workflowBatches(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 2 || b[0][0] != 0 || b[1][0] != 1 {
		t.Fatalf("unexpected batches: %#v", b)
	}
}

func TestParallelManifestFailsClosedWithoutWrites(t *testing.T) {
	path := writeWorkflowManifestTestFile(t, `{"parallel":true,"steps":[{"id":"a","task":"a"}]}`)
	_, err := loadWorkflowManifest(path)
	if err == nil || !strings.Contains(err.Error(), "requires a non-empty writes") {
		t.Fatalf("got %v", err)
	}
}

func TestWorkflowChangedPathsAllowedRejectsUndeclared(t *testing.T) {
	steps := []WorkflowStep{{Writes: []string{"internal/a/**"}}}
	if err := workflowChangedPathsAllowed([]string{"internal/a/x.go"}, steps); err != nil {
		t.Fatal(err)
	}
	if err := workflowChangedPathsAllowed([]string{"internal/b/x.go"}, steps); err == nil {
		t.Fatal("expected undeclared mutation error")
	}
}
