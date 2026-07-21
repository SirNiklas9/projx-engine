package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	store "github.com/SirNiklas9/projx-store"
)

func TestScanReconciliationFindsLifecycleAndFreshnessIssuesWithoutBodies(t *testing.T) {
	m := store.NewMem()
	now := time.Now().UnixMilli()
	recs := []store.Record{
		{ID: "doc/stale", Kind: store.KDoc, Scope: store.ScopeProject, Key: "auth.go", Body: "SECRET STALE BODY", Status: store.StatusActive, Provenance: store.ProvenanceHuman, ReviewAfter: now - 1, Evidence: "internal/auth.go"},
		{ID: "doc/candidate", Kind: store.KDoc, Scope: store.ScopeProject, Key: "new", Status: store.StatusCandidate, Provenance: store.ProvenanceAgent},
		{ID: "adr/old", Kind: store.KADR, Scope: store.ScopeProject, Key: "old", Status: store.StatusSuperseded, ReplacedBy: "adr/missing"},
		{ID: "doc/volatile", Kind: store.KDoc, Scope: store.ScopeProject, Key: "price", Status: store.StatusActive, Provenance: store.ProvenanceHuman, ClaimClass: "volatile"},
	}
	for _, r := range recs {
		if err := m.Put(r); err != nil {
			t.Fatal(err)
		}
	}
	issues := scanReconciliation(m, now)
	joined := ""
	for _, i := range issues {
		joined += i.RecordID + ":" + i.Reason + "\n"
	}
	for _, want := range []string{"doc/stale:review-due", "doc/candidate:candidate-awaiting-reconciliation", "adr/old:replacement-missing-or-inactive", "doc/volatile:volatile-verification-metadata-incomplete"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "SECRET STALE BODY") {
		t.Fatal("reconciliation queue leaked a stale body")
	}
}

func TestSessionStartInjectsBodyFreeReconciliationPrompt(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "yours"))
	st := openStore(root)
	err := st.Put(store.Record{ID: "doc/expired", Kind: store.KDoc, Scope: store.ScopeProject, Key: "expired", Body: "DO NOT ASSERT THIS STALE BODY", Status: store.StatusActive, Provenance: store.ProvenanceHuman, ReviewAfter: time.Now().Add(-time.Hour).UnixMilli()})
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	out, _, code := handleHook(root, []byte(`{"session_id":"reconcile","hook_event_name":"SessionStart"}`))
	if code != 0 || !strings.Contains(out, "doc/expired") || !strings.Contains(out, "review-due") {
		t.Fatalf("missing reconciliation prompt (code=%d): %s", code, out)
	}
	if strings.Contains(out, "DO NOT ASSERT THIS STALE BODY") {
		t.Fatal("session prompt asserted stale body")
	}
}

func TestReconciliationGateOnlyBlocksRelevantTargets(t *testing.T) {
	m := store.NewMem()
	r := store.Record{ID: "doc/auth", Kind: store.KDoc, Scope: store.ScopeProject, Key: "auth", Status: store.StatusActive, Evidence: "internal/auth.go"}
	if err := m.Put(r); err != nil {
		t.Fatal(err)
	}
	issues := []reconciliationIssue{{RecordID: r.ID, Reason: "review-due"}}
	if _, blocked := reconciliationBlocksTargets(m, issues, []string{"README.md"}); blocked {
		t.Fatal("unrelated target blocked")
	}
	msg, blocked := reconciliationBlocksTargets(m, issues, []string{"internal/auth.go"})
	if !blocked || !strings.Contains(msg, r.ID) {
		t.Fatalf("relevant stale knowledge was not gated: %q", msg)
	}
	if strings.Contains(msg, r.Body) && r.Body != "" {
		t.Fatal("gate leaked stale body")
	}
}

func TestScanReconciliationIgnoresGeneratedAndOperationalRecordNoise(t *testing.T) {
	m := store.NewMem()
	for i := 0; i < 500; i++ {
		r := store.Record{ID: fmt.Sprintf("map/%04d", i), Kind: store.KDeclaredStructure, Scope: store.ScopeProject, Key: fmt.Sprintf("symbol/%04d", i), Status: store.StatusActive}
		if err := m.Put(r); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []store.Record{
		{ID: "gate/off-limits", Kind: store.KGateRule, Scope: store.ScopeProject, Key: "off-limits", Status: store.StatusActive},
		{ID: "convention/dispatcher", Kind: store.KConvention, Scope: store.ScopeProject, Key: "setting/dispatcher-mode", Status: store.StatusActive},
		{ID: "doc/integration", Kind: store.KDoc, Scope: store.ScopeProject, Key: "integration/mcp", Status: store.StatusActive},
		{ID: "doc/generated", Kind: store.KDoc, Scope: store.ScopeProject, Key: "generated", Origin: "map", Status: store.StatusActive},
		{ID: "recipe/default", Kind: store.KRecipe, Scope: store.ScopeGlobal, Key: "default", Status: store.StatusActive},
		{ID: "route/default", Kind: store.KRoute, Scope: store.ScopeGlobal, Key: "default", Status: store.StatusActive},
		{ID: "history/import", Kind: store.KHistory, Scope: store.ScopeProject, Key: "import", Status: store.StatusCandidate},
	} {
		if err := m.Put(r); err != nil {
			t.Fatal(err)
		}
	}
	if issues := scanReconciliation(m, time.Now().UnixMilli()); len(issues) != 0 {
		t.Fatalf("generated/operational records created reconciliation noise: %+v", issues)
	}
}

func TestScanReconciliationDoesNotUrgentlyFlagLegacyBlankLifecycleProvenance(t *testing.T) {
	m := store.NewMem()
	legacy := []store.Record{
		{ID: "convention/legacy", Kind: store.KConvention, Scope: store.ScopeProject, Key: "legacy convention"},
		{ID: "adr/legacy", Kind: store.KADR, Scope: store.ScopeProject, Key: "legacy adr"},
		{ID: "doc/legacy", Kind: store.KDoc, Scope: store.ScopeProject, Key: "legacy doc"},
	}
	for _, r := range legacy {
		if err := m.Put(r); err != nil {
			t.Fatal(err)
		}
	}
	if issues := scanReconciliation(m, time.Now().UnixMilli()); len(issues) != 0 {
		t.Fatalf("legacy blank-status records should not enter the urgent queue: %+v", issues)
	}
	explicit := store.Record{ID: "doc/explicit", Kind: store.KDoc, Scope: store.ScopeProject, Key: "explicit", Status: store.StatusActive}
	if err := m.Put(explicit); err != nil {
		t.Fatal(err)
	}
	issues := scanReconciliation(m, time.Now().UnixMilli())
	if len(issues) != 1 || issues[0].RecordID != explicit.ID || issues[0].Reason != "authoritative-provenance-missing" {
		t.Fatalf("explicit active record without provenance should be actionable: %+v", issues)
	}
}

func TestRefreshReconciliationReplacesExistingCheckpoint(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "yours"))
	first, err := refreshReconciliation(root, true)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	second, err := refreshReconciliation(root, true)
	if err != nil {
		t.Fatalf("replace existing checkpoint: %v", err)
	}
	if second.ScannedAt <= first.ScannedAt {
		t.Fatalf("checkpoint was not refreshed: %d <= %d", second.ScannedAt, first.ScannedAt)
	}
	if _, err := loadReconciliation(root); err != nil {
		t.Fatalf("replacement checkpoint unreadable: %v", err)
	}
}
