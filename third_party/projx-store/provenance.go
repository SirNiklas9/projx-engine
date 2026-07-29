package store

// provenance.go — how a record's claim was established. Lets a reader tell a proven fact
// from a narrator's assertion (field-report #7: "the store trusts the narrator").

const (
	// ProvenanceHuman: a person committed or approved the record.
	ProvenanceHuman = "human-confirmed"
	// ProvenanceGate: the verify gate ran a real build/test and it passed.
	ProvenanceGate = "gate-verified"
	// ProvenanceAgent: an AI committed it without an independent check.
	ProvenanceAgent = "agent-asserted"
)

// ProvenanceFor maps a commit actor ("ui"|"agent") to its default provenance. Empty actor
// or anything else → unknown ("").
func ProvenanceFor(by string) string {
	switch by {
	case "ui":
		return ProvenanceHuman
	case "agent":
		return ProvenanceAgent
	}
	return ""
}
