package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func ra(project, id, role string) runningAgent {
	return runningAgent{
		root:     filepath.Join("C:\\dev", project),
		project:  project,
		m:        &dispatchManifest{ID: id, State: "running", Started: time.Now()},
		cur:      &dispatchStepStat{Role: role, State: "running"},
		curIndex: 1,
		total:    1,
	}
}

func TestPickFatAgent(t *testing.T) {
	agents := []runningAgent{
		ra("Evolution", "d-evo", "sonnet worker"),
		ra("Sessions", "d-sess", "opus worker"),
		ra("projx-engine", "d-eng", "haiku worker"),
	}
	active := filepath.Join("C:\\dev", "Sessions")

	// No focus → the in-scope agent (Sessions) is fat.
	if got := pickFatAgent(agents, active, ""); got != 1 {
		t.Fatalf("scope fat: want 1 (Sessions), got %d", got)
	}
	// Focus by project name wins over scope.
	if got := pickFatAgent(agents, active, "projx-engine"); got != 2 {
		t.Fatalf("focus-by-project: want 2, got %d", got)
	}
	// Focus by dispatch id.
	if got := pickFatAgent(agents, active, "d-evo"); got != 0 {
		t.Fatalf("focus-by-id: want 0, got %d", got)
	}
	// Focus by role.
	if got := pickFatAgent(agents, active, "opus worker"); got != 1 {
		t.Fatalf("focus-by-role: want 1, got %d", got)
	}
	// Focus matching nothing running → fall back to scope.
	if got := pickFatAgent(agents, active, "ghost"); got != 1 {
		t.Fatalf("focus-no-match: want scope 1, got %d", got)
	}
	// No focus, scope matches nothing, multiple agents → none fat.
	if got := pickFatAgent(agents, filepath.Join("C:\\dev", "Elsewhere"), ""); got != -1 {
		t.Fatalf("no-scope-multi: want -1, got %d", got)
	}
	// Single agent, no scope match → the sole agent is fat.
	if got := pickFatAgent(agents[:1], filepath.Join("C:\\dev", "Elsewhere"), ""); got != 0 {
		t.Fatalf("single-agent fallback: want 0, got %d", got)
	}
}

func TestCurrentStep(t *testing.T) {
	m := &dispatchManifest{Steps: []dispatchStepStat{
		{State: "done"}, {State: "running"}, {State: "pending"},
	}}
	if s, idx := currentStep(m); idx != 2 || s.State != "running" {
		t.Fatalf("want running step at idx 2, got idx %d state %q", idx, s.State)
	}
	// No running step → most recent done.
	m2 := &dispatchManifest{Steps: []dispatchStepStat{{State: "done"}, {State: "done"}}}
	if _, idx := currentStep(m2); idx != 2 {
		t.Fatalf("want last done at idx 2, got %d", idx)
	}
	// Empty.
	if s, idx := currentStep(&dispatchManifest{}); s != nil || idx != 0 {
		t.Fatalf("empty manifest: want nil,0 got %v,%d", s, idx)
	}
}

func TestCompactDur(t *testing.T) {
	cases := map[time.Duration]string{
		5 * time.Second:  "5s",
		90 * time.Second: "1m",
		3 * time.Minute:  "3m",
		62 * time.Minute: "1h2m",
		-1 * time.Second: "0s",
	}
	for d, want := range cases {
		if got := compactDur(d); got != want {
			t.Errorf("compactDur(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestBranchOf(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/feat/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := branchOf(dir); got != "feat/x" {
		t.Fatalf("branchOf ref: want feat/x, got %q", got)
	}
	// Detached HEAD → short sha.
	_ = os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("0123456789abcdef\n"), 0o644)
	if got := branchOf(dir); got != "0123456" {
		t.Fatalf("branchOf detached: want 0123456, got %q", got)
	}
	// Not a repo → empty.
	if got := branchOf(t.TempDir()); got != "" {
		t.Fatalf("branchOf non-repo: want empty, got %q", got)
	}
}

func TestFocusRoundTrip(t *testing.T) {
	t.Setenv("PROJX_YOURS_DIR", t.TempDir())
	if got := readFocus(); got != "" {
		t.Fatalf("fresh focus: want empty, got %q", got)
	}
	if err := writeFocus("projx-engine"); err != nil {
		t.Fatal(err)
	}
	if got := readFocus(); got != "projx-engine" {
		t.Fatalf("after write: want projx-engine, got %q", got)
	}
	if err := writeFocus(""); err != nil {
		t.Fatal(err)
	}
	if got := readFocus(); got != "" {
		t.Fatalf("after clear: want empty, got %q", got)
	}
}

func TestRegisterAndPruneDispatchRoots(t *testing.T) {
	yours := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", yours)

	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	registerDispatchRoot(proj)
	registerDispatchRoot(proj) // idempotent

	roots := dispatchRoots()
	if len(roots) != 1 || !pathEq(roots[0], proj) {
		t.Fatalf("register: want [%s], got %v", proj, roots)
	}

	// A root that is not a ProjX dir is pruned on read.
	registerDispatchRoot(t.TempDir()) // no .projx under it
	if roots := dispatchRoots(); len(roots) != 1 || !pathEq(roots[0], proj) {
		t.Fatalf("prune: want only [%s], got %v", proj, roots)
	}
}
