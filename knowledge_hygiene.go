package main

import (
	"fmt"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

var durableAgentClaimClasses = map[string]bool{
	"stable":              true,
	"volatile":            true,
	"decision":            true,
	"convention":          true,
	"ownership-boundary":  true,
	"completion-criteria": true,
}

// validateAgentKnowledgeCandidate keeps transient execution state out of the
// durable store regardless of which agent harness submits the record.
func validateAgentKnowledgeCandidate(rec store.Record) error {
	if strings.TrimSpace(rec.Key) == "" {
		return fmt.Errorf("agent knowledge requires a non-empty key")
	}
	if strings.TrimSpace(rec.Body) == "" {
		return fmt.Errorf("agent knowledge requires a non-empty body")
	}
	claimClass := strings.ToLower(strings.TrimSpace(rec.ClaimClass))
	if !durableAgentClaimClasses[claimClass] {
		return fmt.Errorf("agent knowledge claim_class must be one of: stable, volatile, decision, convention, ownership-boundary, completion-criteria")
	}
	if strings.TrimSpace(rec.Evidence) == "" {
		return fmt.Errorf("agent knowledge requires evidence from a durable source or verification")
	}
	return nil
}
