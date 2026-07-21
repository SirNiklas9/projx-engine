package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	store "github.com/SirNiklas9/projx-store"
)

const reconciliationCadence = 15 * time.Minute

type reconciliationIssue struct {
	RecordID string `json:"record_id"`
	Kind     string `json:"kind"`
	Scope    string `json:"scope"`
	Reason   string `json:"reason"`
	Related  string `json:"related,omitempty"`
}

type reconciliationCheckpoint struct {
	ScannedAt int64                 `json:"scanned_at"`
	Issues    []reconciliationIssue `json:"issues"`
}

func reconciliationPath(root string) string {
	return filepath.Join(root, ".projx", "reconciliation.json")
}

// reconciliationKnowledgeRecord deliberately excludes generated/operational records.
// Code-map and declared-structure rows, settings, gates, integrations, routes and recipes
// are validated by their own deterministic subsystems; treating their legacy provenance as
// a knowledge defect would drown the human-authored review queue in noise.
func reconciliationKnowledgeRecord(r store.Record) bool {
	key := strings.ToLower(strings.TrimSpace(r.Key))
	if r.Origin == "map" || strings.HasPrefix(key, "setting/") || strings.HasPrefix(key, "integration/") || strings.HasPrefix(key, "integrations/") {
		return false
	}
	switch r.Kind {
	case store.KConvention, store.KADR, store.KDoc:
		return true
	default:
		return false
	}
}

func scanReconciliation(st store.Store, now int64) []reconciliationIssue {
	recs := st.List(store.Filter{IncludeNonAuthoritative: true})
	byID := make(map[string]store.Record, len(recs))
	activeKeys := map[string][]string{}
	for _, r := range recs {
		byID[r.ID] = r
		if !reconciliationKnowledgeRecord(r) {
			continue
		}
		if r.Authoritative() {
			key := fmt.Sprintf("%d/%d/%s", r.Scope, r.Kind, strings.ToLower(strings.TrimSpace(r.Key)))
			activeKeys[key] = append(activeKeys[key], r.ID)
		}
	}
	seen := map[string]bool{}
	var out []reconciliationIssue
	add := func(r store.Record, reason, related string) {
		key := r.ID + "\x00" + reason + "\x00" + related
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, reconciliationIssue{r.ID, r.Kind.String(), r.Scope.String(), reason, related})
	}
	for _, r := range recs {
		if !reconciliationKnowledgeRecord(r) {
			continue
		}
		switch r.LifecycleStatus() {
		case store.StatusCandidate:
			add(r, "candidate-awaiting-reconciliation", r.Supersedes)
		case store.StatusActive:
			if r.ReviewDueAt(now) {
				add(r, "review-due", "")
			}
		case store.StatusSuperseded:
			if strings.TrimSpace(r.ReplacedBy) == "" {
				add(r, "superseded-without-replacement", "")
			} else if replacement, ok := byID[r.ReplacedBy]; !ok || replacement.LifecycleStatus() != store.StatusActive {
				add(r, "replacement-missing-or-inactive", r.ReplacedBy)
			}
		}
		if r.Supersedes != "" {
			old, ok := byID[r.Supersedes]
			if !ok {
				add(r, "supersedes-missing-record", r.Supersedes)
			} else if r.Authoritative() && (old.LifecycleStatus() != store.StatusSuperseded || old.ReplacedBy != r.ID) {
				add(r, "supersession-chain-incomplete", r.Supersedes)
			}
		}
		// Empty Status is the legacy-compatible active default. Those pre-lifecycle rows
		// commonly predate provenance too, so surfacing every one as urgent would flood
		// normal sessions. Missing provenance is actionable only after a record has been
		// explicitly enrolled in the lifecycle as active.
		if r.Status == store.StatusActive && strings.TrimSpace(r.Provenance) == "" {
			add(r, "authoritative-provenance-missing", "")
		}
		if r.LifecycleStatus() == store.StatusActive && strings.EqualFold(r.ClaimClass, "volatile") {
			if r.VerifiedAt == 0 || r.ReviewAfter == 0 || strings.TrimSpace(r.Evidence) == "" || strings.TrimSpace(r.Verifier) == "" {
				add(r, "volatile-verification-metadata-incomplete", "")
			}
		}
	}
	for _, ids := range activeKeys {
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		for _, id := range ids {
			add(byID[id], "competing-active-records", strings.Join(ids, ","))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RecordID != out[j].RecordID {
			return out[i].RecordID < out[j].RecordID
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}

func loadReconciliation(root string) (reconciliationCheckpoint, error) {
	var cp reconciliationCheckpoint
	b, err := os.ReadFile(reconciliationPath(root))
	if err != nil {
		return cp, err
	}
	err = json.Unmarshal(b, &cp)
	return cp, err
}

func refreshReconciliation(root string, force bool) (reconciliationCheckpoint, error) {
	if !force {
		if cp, err := loadReconciliation(root); err == nil && time.Since(time.UnixMilli(cp.ScannedAt)) < reconciliationCadence {
			return cp, nil
		}
	}
	st, err := openStoreSafe(root)
	if err != nil {
		return reconciliationCheckpoint{}, err
	}
	defer st.Close()
	cp := reconciliationCheckpoint{ScannedAt: time.Now().UnixMilli(), Issues: scanReconciliation(st, time.Now().UnixMilli())}
	b, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return cp, err
	}
	b = append(b, '\n')
	path := reconciliationPath(root)
	if existing, readErr := os.ReadFile(path); readErr == nil && string(existing) == string(b) {
		return cp, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return cp, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return cp, err
	}
	// os.Rename cannot replace an existing file on Windows. Remove only after the
	// complete replacement has been durably staged next to it, then rename locally.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		_ = os.Remove(tmp)
		return cp, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return cp, err
	}
	return cp, nil
}

func reconciliationPrompt(root string) string {
	cp, err := refreshReconciliation(root, false)
	if err != nil || len(cp.Issues) == 0 {
		return ""
	}
	const capIssues = 8
	issues := cp.Issues
	if len(issues) > capIssues {
		issues = issues[:capIssues]
	}
	var b strings.Builder
	b.WriteString("## ProjX reconciliation required\n")
	b.WriteString("Review the following record IDs using current evidence. Propose candidate corrections or explicit supersession/rejection; never silently promote policy. Stale record bodies are intentionally not injected.\n")
	for _, i := range issues {
		fmt.Fprintf(&b, "- `%s` (%s)\n", i.RecordID, i.Reason)
	}
	if n := len(cp.Issues) - len(issues); n > 0 {
		fmt.Fprintf(&b, "- …and %d more (`projx-engine reconcile status`)\n", n)
	}
	b.WriteString("\n")
	return b.String()
}

func reconciliationBlocksTargets(st store.Store, issues []reconciliationIssue, targets []string) (string, bool) {
	if len(targets) == 0 {
		return "", false
	}
	for _, issue := range issues {
		if issue.Reason != "review-due" && issue.Reason != "competing-active-records" && issue.Reason != "replacement-missing-or-inactive" {
			continue
		}
		r, ok := st.Get(issue.RecordID)
		if !ok {
			continue
		}
		hay := strings.ToLower(r.Key + " " + r.Evidence)
		for _, target := range targets {
			rel := strings.ToLower(filepath.ToSlash(target))
			base := strings.ToLower(filepath.Base(target))
			if base != "." && base != "" && (strings.Contains(hay, rel) || strings.Contains(hay, base)) {
				return fmt.Sprintf("ProjX reconciliation gate: record %q is %s and is relevant to %q. Re-verify it and record a candidate outcome before acting; stale content was not asserted.", issue.RecordID, issue.Reason, target), true
			}
		}
	}
	return "", false
}

func runReconcileCmd(absRoot string, args []string) {
	fs := flag.NewFlagSet("reconcile", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "print the reconciliation queue as JSON")
	_ = fs.Parse(args)
	force := true
	if len(fs.Args()) > 0 && fs.Args()[0] == "status" {
		force = false
	}
	cp, err := refreshReconciliation(absRoot, force)
	if err != nil {
		die("reconcile: %v", err)
	}
	if *jsonOut {
		b, _ := json.MarshalIndent(cp, "", "  ")
		fmt.Println(string(b))
		return
	}
	if len(cp.Issues) == 0 {
		fmt.Println("reconciliation clean")
		return
	}
	fmt.Printf("%d reconciliation issue(s):\n", len(cp.Issues))
	for _, i := range cp.Issues {
		fmt.Printf("%s\t%s\n", i.RecordID, i.Reason)
	}
}
