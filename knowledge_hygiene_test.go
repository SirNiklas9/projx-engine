package main

import (
	"strings"
	"testing"

	store "github.com/SirNiklas9/projx-store"
)

func TestValidateAgentKnowledgeCandidate(t *testing.T) {
	valid := store.Record{
		Key: "checkout/completion", Body: "Checkout completes through the real path.",
		ClaimClass: "completion-criteria", Evidence: "integration test checkout_e2e",
	}
	if err := validateAgentKnowledgeCandidate(valid); err != nil {
		t.Fatalf("durable candidate rejected: %v", err)
	}
	for _, claimClass := range []string{"stable", "volatile", "decision", "convention", "ownership-boundary", "completion-criteria"} {
		t.Run("allows "+claimClass, func(t *testing.T) {
			rec := valid
			rec.ClaimClass = claimClass
			if err := validateAgentKnowledgeCandidate(rec); err != nil {
				t.Fatalf("allowed class %q rejected: %v", claimClass, err)
			}
		})
	}

	for _, tc := range []struct {
		name   string
		change func(*store.Record)
		want   string
	}{
		{name: "missing key", change: func(r *store.Record) { r.Key = " \t" }, want: "non-empty key"},
		{name: "missing body", change: func(r *store.Record) { r.Body = " \n" }, want: "non-empty body"},
		{name: "transient class", change: func(r *store.Record) { r.ClaimClass = "progress" }, want: "claim_class"},
		{name: "turn observation", change: func(r *store.Record) { r.ClaimClass = "turn-observation" }, want: "claim_class"},
		{name: "checkpoint", change: func(r *store.Record) { r.ClaimClass = "checkpoint" }, want: "claim_class"},
		{name: "status", change: func(r *store.Record) { r.ClaimClass = "status" }, want: "claim_class"},
		{name: "missing evidence", change: func(r *store.Record) { r.Evidence = " \t" }, want: "requires evidence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := valid
			tc.change(&rec)
			err := validateAgentKnowledgeCandidate(rec)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}
