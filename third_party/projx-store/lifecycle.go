package store

// Knowledge lifecycle states. The empty value is deliberately interpreted as active so
// records written before schema #6 remain authoritative without a destructive backfill.
const (
	StatusCandidate  = "candidate"
	StatusActive     = "active"
	StatusSuperseded = "superseded"
	StatusRejected   = "rejected"
)

const (
	FreshnessFresh     = "fresh"
	FreshnessReviewDue = "review-due"
)

// LifecycleStatus returns the effective status, including the compatibility default.
func (r Record) LifecycleStatus() string {
	if r.Status == "" {
		return StatusActive
	}
	return r.Status
}

// Authoritative reports whether the record may speak as current declared knowledge.
// Freshness (ReviewAfter) is intentionally evaluated by the consuming engine because it
// owns the clock and the policy for re-verification.
func (r Record) Authoritative() bool { return r.LifecycleStatus() == StatusActive }

// ReviewDueAt reports whether an active record has crossed its authored review
// deadline. A zero deadline means no review schedule, preserving legacy behavior.
func (r Record) ReviewDueAt(now int64) bool {
	return r.Authoritative() && r.ReviewAfter > 0 && now >= r.ReviewAfter
}

// FreshnessAt evaluates whether a record may be asserted as current at now.
func (r Record) FreshnessAt(now int64) string {
	if r.ReviewDueAt(now) {
		return FreshnessReviewDue
	}
	return FreshnessFresh
}

// FreshAt is the context-injection predicate: current lifecycle state and no review due.
func (r Record) FreshAt(now int64) bool { return r.Authoritative() && !r.ReviewDueAt(now) }

// RecordsDueForReview returns active records whose review deadline has passed. It is an
// audit/verification view, never an authoritative context view.
func RecordsDueForReview(st Store, f Filter, now int64) []Record {
	if st == nil {
		return nil
	}
	f.IncludeNonAuthoritative = true
	var out []Record
	for _, r := range st.List(f) {
		if r.ReviewDueAt(now) {
			out = append(out, r)
		}
	}
	return out
}
