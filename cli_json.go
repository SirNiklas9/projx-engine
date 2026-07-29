package main

import (
	"encoding/json"
	"os"

	store "github.com/SirNiklas9/projx-store"
)

type cliRecord struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Scope       string `json:"scope"`
	Key         string `json:"key"`
	Body        string `json:"body"`
	Status      string `json:"status"`
	Provenance  string `json:"provenance,omitempty"`
	UpdatedAt   int64  `json:"updated_at,omitempty"`
	VerifiedAt  int64  `json:"verified_at,omitempty"`
	ReviewAfter int64  `json:"review_after,omitempty"`
	Supersedes  string `json:"supersedes,omitempty"`
	ReplacedBy  string `json:"replaced_by,omitempty"`
	ClaimClass  string `json:"claim_class,omitempty"`
	Verifier    string `json:"verifier,omitempty"`
	Evidence    string `json:"evidence,omitempty"`
	Model       string `json:"model,omitempty"`
	Confidence  int    `json:"confidence,omitempty"`
	Approval    string `json:"approval,omitempty"`
}

func cliRecordFrom(r store.Record) cliRecord {
	return cliRecord{
		ID: r.ID, Kind: r.Kind.String(), Scope: r.Scope.String(), Key: r.Key, Body: r.Body,
		Status: r.LifecycleStatus(), Provenance: r.Provenance, UpdatedAt: r.UpdatedAt,
		VerifiedAt: r.VerifiedAt, ReviewAfter: r.ReviewAfter, Supersedes: r.Supersedes,
		ReplacedBy: r.ReplacedBy, ClaimClass: r.ClaimClass, Verifier: r.Verifier,
		Evidence: r.Evidence, Model: r.Model, Confidence: r.Confidence, Approval: r.Approval,
	}
}

func cliRecordsFrom(records []store.Record) []cliRecord {
	out := make([]cliRecord, 0, len(records))
	for _, record := range records {
		out = append(out, cliRecordFrom(record))
	}
	return out
}

func writeCLIJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
}

func takeJSONFlag(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	jsonOut := false
	for _, arg := range args {
		switch arg {
		case "--json", "--json=true":
			jsonOut = true
			continue
		case "--json=false":
			jsonOut = false
			continue
		}
		out = append(out, arg)
	}
	return out, jsonOut
}

func optionalJSONFlag(flags []bool) bool {
	return len(flags) > 0 && flags[0]
}
