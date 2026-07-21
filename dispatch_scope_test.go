package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDispatchPerWorkerScope proves the per-agent ProjX scope for dispatched workers:
// for a 2-step run, each worker's computed scope (role) is distinct and its INJECTED
// context is sliced to its OWN step — step A sees A's records + role, not B's, and
// vice versa — never the whole project. This is the scope the supervisor passes to
// each detached child (role via PROJX_WORKER_ROLE, task via --task <step>).
func TestDispatchPerWorkerScope(t *testing.T) {
	root := t.TempDir()
	seedSessionStore(t, root) // law (secret/**) + convention + doc mc-login + doc billing

	stepA := dispatchStepStat{Task: "work on the minecraft login backend", Tier: "deep-reasoning", Kind: "agent"}
	stepB := dispatchStepStat{Task: "fix the billing checkout flow", Tier: "cheap-fast", Kind: "agent"}

	scA := scopeForStep(stepA)
	scB := scopeForStep(stepB)

	// Roles are per-step (derived from the routing tier), so the two workers are
	// scoped to distinct roles, not one generic bucket.
	if scA.Role == scB.Role {
		t.Fatalf("expected distinct per-step roles, both = %q", scA.Role)
	}
	if !strings.Contains(scA.Role, "deep-reasoning") {
		t.Errorf("step A role not derived from its tier: %q", scA.Role)
	}
	if !strings.Contains(scB.Role, "cheap-fast") {
		t.Errorf("step B role not derived from its tier: %q", scB.Role)
	}

	// The INJECTED context each worker receives = task-sliced preamble + its role banner.
	// sliceOnly is the pre-banner task slice (compared to full below); ctx is what the
	// worker actually sees.
	sliceOnly := func(sc workerScope) string {
		st := openStore(root)
		p := compileStorePreambleForTask(st, sc.Task)
		st.Close()
		return p
	}
	slicedA, slicedB := sliceOnly(scA), sliceOnly(scB)
	ctxA := applyWorkerRole(slicedA, scA.Role)
	ctxB := applyWorkerRole(slicedB, scB.Role)

	// Full (whole-project) context for the size comparison.
	stFull := openStore(root)
	full := compileStorePreamble(stFull)
	stFull.Close()

	// Worker A: its step's doc + role, NOT the unrelated billing doc.
	if !strings.Contains(ctxA, "minecraft/login/backend") {
		t.Error("worker A context missing its own step's record")
	}
	if strings.Contains(ctxA, "billing/checkout") {
		t.Error("worker A context leaked the unrelated billing record (not scoped to its step)")
	}
	if !strings.Contains(ctxA, scA.Role) {
		t.Error("worker A context missing its role banner")
	}
	if !strings.Contains(ctxA, "READ BEFORE ACTING") || !strings.Contains(ctxA, "KNOWLEDGE OUT = store.commit") {
		t.Error("worker A did not carry the governed recall/learn lifecycle policy")
	}

	// Worker B: its step's doc + role, NOT the minecraft doc.
	if !strings.Contains(ctxB, "billing/checkout") {
		t.Error("worker B context missing its own step's record")
	}
	if strings.Contains(ctxB, "minecraft/login/backend") {
		t.Error("worker B context leaked worker A's record (not scoped to its step)")
	}
	if !strings.Contains(ctxB, scB.Role) {
		t.Error("worker B context missing its role banner")
	}
	if !strings.Contains(ctxB, "READ BEFORE ACTING") || !strings.Contains(ctxB, "KNOWLEDGE OUT = store.commit") {
		t.Error("worker B did not carry the governed recall/learn lifecycle policy")
	}

	// Law (the off-limits gate) is carried into every worker regardless of slice.
	if !strings.Contains(ctxA, "secret/**") || !strings.Contains(ctxB, "secret/**") {
		t.Error("workers must still carry the law (secret/**) even when scoped")
	}

	// Each worker's task slice (before the role banner) is strictly smaller than the
	// whole-project dump — it drops the other worker's records.
	if len(slicedA) >= len(full) || len(slicedB) >= len(full) {
		t.Errorf("scoped worker slice (A=%d B=%d) should be smaller than full project (%d)", len(slicedA), len(slicedB), len(full))
	}
}

// TestWorkerRoleForStep pins the role derivation from a step's routing.
func TestWorkerRoleForStep(t *testing.T) {
	cases := []struct {
		in   dispatchStepStat
		want string
	}{
		{dispatchStepStat{Kind: "agent", Tier: "deep-reasoning"}, "deep-reasoning worker"},
		{dispatchStepStat{Kind: "agent", Tier: "agent"}, "worker"},
		{dispatchStepStat{Kind: "agent", Tier: ""}, "worker"},
		{dispatchStepStat{Kind: "deterministic", Op: "verify"}, "operator:verify"},
		{dispatchStepStat{Kind: "deterministic"}, "operator"},
	}
	for _, c := range cases {
		if got := workerRoleForStep(c.in); got != c.want {
			t.Errorf("workerRoleForStep(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestStepOutcomeLabel pins the HONESTY rule: a bare "done" is reserved for a step that
// actually changed the repo (or one where git could not tell us). A step that provably
// changed nothing must never render as an unqualified "done" — that is the exact lie
// [[dispatch-verify-keyword-swallowed-edits]] told on Jul 16.
func TestStepOutcomeLabel(t *testing.T) {
	cases := []struct {
		name string
		in   dispatchStepStat
		want string
	}{
		{"agent that edited", dispatchStepStat{Kind: "agent", State: "done", Mutated: mutatedYes}, "done"},
		{"agent that changed nothing", dispatchStepStat{Kind: "agent", State: "done", Mutated: mutatedNo}, "done (NO CHANGES)"},
		{"op that changed nothing", dispatchStepStat{Kind: "deterministic", Op: "verify", State: "done", Mutated: mutatedNo}, "done (no changes)"},
		{"op that changed something", dispatchStepStat{Kind: "deterministic", Op: "store log", State: "done", Mutated: mutatedYes}, "done"},
		// No git to ask: we have no evidence, so we make no claim either way.
		{"unknown stays plain", dispatchStepStat{Kind: "agent", State: "done", Mutated: mutatedUnknown}, "done"},
		{"missing mutation stays plain", dispatchStepStat{Kind: "agent", State: "done"}, "done"},
		// Non-done states are reported as-is; "failed" is already honest.
		{"failed", dispatchStepStat{Kind: "agent", State: "failed", Mutated: mutatedNo}, "failed"},
		{"running", dispatchStepStat{Kind: "agent", State: "running"}, "running"},
		{"pending", dispatchStepStat{Kind: "agent", State: "pending"}, "pending"},
	}
	for _, c := range cases {
		if got := stepOutcomeLabel(c.in); got != c.want {
			t.Errorf("%s: stepOutcomeLabel(%+v) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestSilentAgentStep pins WHICH steps earn the loud warning: only an agent step that
// completed and provably changed nothing. A deterministic op that changes nothing is
// normal (verify is read-only by design) and must stay quiet, and "unknown" must never
// trigger a warning we cannot back up.
func TestSilentAgentStep(t *testing.T) {
	cases := []struct {
		name string
		in   dispatchStepStat
		want bool
	}{
		{"agent, no changes -> loud", dispatchStepStat{Kind: "agent", State: "done", Mutated: mutatedNo}, true},
		{"agent, changed -> quiet", dispatchStepStat{Kind: "agent", State: "done", Mutated: mutatedYes}, false},
		{"agent, unknown -> quiet", dispatchStepStat{Kind: "agent", State: "done", Mutated: mutatedUnknown}, false},
		{"read-only op, no changes -> quiet", dispatchStepStat{Kind: "deterministic", Op: "verify", State: "done", Mutated: mutatedNo}, false},
		{"agent failed -> quiet (already honest)", dispatchStepStat{Kind: "agent", State: "failed", Mutated: mutatedNo}, false},
	}
	for _, c := range cases {
		if got := silentAgentStep(c.in); got != c.want {
			t.Errorf("%s: silentAgentStep(%+v) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

// TestMutationBetween pins the fingerprint comparison, including the deliberate
// contagion of "unknown" — an unreadable side must never collapse into a confident "no".
func TestMutationBetween(t *testing.T) {
	cases := []struct {
		name     string
		before   string
		beforeOK bool
		after    string
		afterOK  bool
		want     string
	}{
		{"identical", "sha\x00", true, "sha\x00", true, mutatedNo},
		{"tree differs", "sha\x00", true, "sha\x00 M a.go", true, mutatedYes},
		{"head moved (step committed)", "sha1\x00", true, "sha2\x00", true, mutatedYes},
		{"before unreadable", "", false, "sha\x00", true, mutatedUnknown},
		{"after unreadable", "sha\x00", true, "", false, mutatedUnknown},
		{"neither readable", "", false, "", false, mutatedUnknown},
	}
	for _, c := range cases {
		if got := mutationBetween(c.before, c.beforeOK, c.after, c.afterOK); got != c.want {
			t.Errorf("%s: mutationBetween = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestDropEngineStateLines proves the supervisor does not see its OWN manifest writes as
// repo mutations. Without this, every step would fingerprint as "mutated" simply because
// dispatch rewrites .projx/runs/<id>.json on each transition — the signal would be all
// yes and therefore worthless. This repo gitignores .projx/, but one that doesn't must
// still get a truthful answer.
func TestDropEngineStateLines(t *testing.T) {
	in := strings.Join([]string{
		"?? .projx/",
		"?? .projx/runs/d0716-1.json",
		" M internal/routing/routing.go",
		"?? .projxsomething.go", // NOT engine state — a real file that merely shares a prefix
	}, "\n")
	got := dropEngineStateLines(in)
	if strings.Contains(got, ".projx/runs") || strings.Contains(got, "?? .projx/\n") {
		t.Errorf("engine bookkeeping survived the filter: %q", got)
	}
	if !strings.Contains(got, "internal/routing/routing.go") {
		t.Errorf("real source change was filtered away: %q", got)
	}
	if !strings.Contains(got, ".projxsomething.go") {
		t.Errorf("filter over-matched a real file sharing the .projx prefix: %q", got)
	}
	if dropEngineStateLines("") != "" {
		t.Error("empty status must stay empty (a clean tree is not a mutation)")
	}
}

// TestGitFingerprintGroundTruth exercises the real thing against a real repo: the
// fingerprint is what makes the mutation claim TRUE rather than self-reported.
func TestGitFingerprintGroundTruth(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()

	// Not a git repo → unknown, never a crash and never a false "no".
	if _, ok := gitFingerprint(root); ok {
		t.Fatal("a non-repo must report ok=false (unknown), not a confident fingerprint")
	}
	nrA, nrAOK := gitFingerprint(root)
	nrB, nrBOK := gitFingerprint(root)
	if got := mutationBetween(nrA, nrAOK, nrB, nrBOK); got != mutatedUnknown {
		t.Errorf("non-repo mutation = %q, want %q", got, mutatedUnknown)
	}

	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	git("init")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "test")
	git("config", "commit.gpgsign", "false")
	write("pay.go", "package pay\n")
	git("add", ".")
	git("commit", "-m", "init")

	base, baseOK := gitFingerprint(root)
	if !baseOK {
		t.Fatal("a real repo must fingerprint")
	}

	// A step that does nothing must be indistinguishable from no step at all.
	noop, noopOK := gitFingerprint(root)
	if got := mutationBetween(base, baseOK, noop, noopOK); got != mutatedNo {
		t.Errorf("no-op step reported %q, want %q — this is the false-success case", got, mutatedNo)
	}

	// An uncommitted edit is a mutation.
	write("pay.go", "package pay\n\nfunc Charge() {}\n")
	edited, editedOK := gitFingerprint(root)
	if got := mutationBetween(base, baseOK, edited, editedOK); got != mutatedYes {
		t.Errorf("uncommitted edit reported %q, want %q", got, mutatedYes)
	}

	// A step that COMMITS its work is still a mutation — this is why HEAD is in the
	// fingerprint. Porcelain status alone would come back clean here and lie.
	git("add", ".")
	git("commit", "-m", "charge")
	committed, committedOK := gitFingerprint(root)
	if got := mutationBetween(base, baseOK, committed, committedOK); got != mutatedYes {
		t.Errorf("step that committed its work reported %q, want %q (HEAD must be in the fingerprint)", got, mutatedYes)
	}

	// The engine's own run manifests must not register as the step's work.
	if err := os.MkdirAll(filepath.Join(root, ".projx", "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(".projx", "runs", "d0716-dead.json"), `{"id":"d0716-dead"}`)
	quiet, quietOK := gitFingerprint(root)
	if got := mutationBetween(committed, committedOK, quiet, quietOK); got != mutatedNo {
		t.Errorf("engine's own .projx bookkeeping reported %q, want %q", got, mutatedNo)
	}
}

// TestSurfaceFinishedDispatchesReportsSilentAgent is the regression test for the Jul 16
// incident at the surface that actually reaches the human. The old text for this exact
// manifest read "done, verify skipped — 1/1 steps passed", which was indistinguishable
// from real work. It must now say the agent step changed nothing.
func TestSurfaceFinishedDispatchesReportsSilentAgent(t *testing.T) {
	root := t.TempDir()
	m := &dispatchManifest{
		ID:      "d0716-120000-abcd",
		Message: "edit the payment retry logic",
		State:   "done",
		Started: time.Now(),
		Steps: []dispatchStepStat{
			{Task: "edit the payment retry logic", Tier: "deep-reasoning", Kind: "agent", State: "done", Mutated: mutatedNo},
		},
	}
	if err := writeDispatchManifest(root, m); err != nil {
		t.Fatal(err)
	}
	out := surfaceFinishedDispatches(root)
	if out == "" {
		t.Fatal("a finished run must surface once")
	}
	if !strings.Contains(out, "changed NOTHING") {
		t.Errorf("silent agent step not surfaced to the human:\n%s", out)
	}
	if !strings.Contains(out, "changed nothing") {
		t.Errorf("step tally not qualified — %q still reads as real work:\n%s", "1/1 steps passed", out)
	}

	// A run whose agent actually edited must stay quiet — no crying wolf on real work.
	root2 := t.TempDir()
	m2 := &dispatchManifest{
		ID:      "d0716-130000-abcd",
		Message: "edit the payment retry logic",
		State:   "done",
		Started: time.Now(),
		Steps: []dispatchStepStat{
			{Task: "edit the payment retry logic", Tier: "deep-reasoning", Kind: "agent", State: "done", Mutated: mutatedYes},
		},
	}
	if err := writeDispatchManifest(root2, m2); err != nil {
		t.Fatal(err)
	}
	if out := surfaceFinishedDispatches(root2); strings.Contains(out, "NOTHING") || strings.Contains(out, "changed nothing") {
		t.Errorf("a step that really changed code must not be flagged:\n%s", out)
	}
}
