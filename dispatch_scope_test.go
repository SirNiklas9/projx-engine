package main

import (
	"strings"
	"testing"
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
