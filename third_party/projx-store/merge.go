package store

import (
	"encoding/json"
	"sort"
)

// Merge / Export / Import: consolidating one store's knowledge into another — the
// "two brains -> one" problem, and the same engine that powers import-on-new-brain and
// carry-on-promotion. Pure data; no I/O, no backend. Callers apply the result with Put.

// Resolution is how a same-ID conflict was (or should be) settled.
type Resolution int

const (
	TookIncoming Resolution = iota // incoming record won (newer, or chosen)
	KeptBase                       // base record won (newer, or chosen)
	Unresolved                     // tie that needs a human pick (Newer == Newer == 0, etc.)
)

// Conflict is one ID present on BOTH sides with a DIFFERENT body. The merge auto-resolves
// by last-write-wins when the timestamps differ; a true tie (equal/zero UpdatedAt) is
// reported Unresolved for the caller to reconcile.
type Conflict struct {
	ID         string
	Base       Record
	Incoming   Record
	Resolution Resolution // TookIncoming / KeptBase = auto-resolved (LWW); Unresolved = needs you
}

// Resolved reports whether the conflict was settled automatically (LWW). Unresolved
// conflicts are the only ones a human must reconcile.
func (c Conflict) Resolved() bool { return c.Resolution != Unresolved }

// MergeResult is the outcome: the consolidated record set plus a per-ID accounting and
// the conflicts (auto-resolved AND the ones still needing a pick).
type MergeResult struct {
	Merged     []Record   // the consolidated set (ID-sorted), with auto-resolutions applied
	Conflicts  []Conflict // every same-ID-different-body case (check .Resolved())
	Added      int        // ids only in incoming
	Unchanged  int        // ids identical on both sides (deduped)
	AutoWon    int        // conflicts settled by last-write-wins
	NeedReview int        // conflicts left Unresolved (a human must pick)
}

// content reports whether two records carry the same meaning (ignoring merge metadata).
func sameContent(a, b Record) bool {
	return a.Kind == b.Kind && a.Scope == b.Scope && a.Key == b.Key && a.Body == b.Body &&
		a.Enforcement == b.Enforcement && a.Provenance == b.Provenance &&
		a.Status == b.Status && a.Supersedes == b.Supersedes && a.ReplacedBy == b.ReplacedBy &&
		a.ClaimClass == b.ClaimClass && a.VerifiedAt == b.VerifiedAt && a.ReviewAfter == b.ReviewAfter &&
		a.Verifier == b.Verifier && a.Evidence == b.Evidence && a.Model == b.Model &&
		a.Confidence == b.Confidence && a.Approval == b.Approval
}

// Merge consolidates `incoming` into `base` by ID:
//   - id only in base        -> kept
//   - id only in incoming    -> added
//   - same id, same content  -> deduped (the newer-stamped copy is kept for metadata)
//   - same id, diff content  -> CONFLICT: newer UpdatedAt wins (LWW); equal/zero -> Unresolved
//
// Unresolved conflicts keep the BASE record in Merged (no silent overwrite — you stay the
// author) until the caller picks; resolve with Apply.
func Merge(base, incoming []Record) MergeResult {
	byID := map[string]Record{}
	for _, r := range base {
		byID[r.ID] = r
	}
	res := MergeResult{}
	for _, in := range incoming {
		b, exists := byID[in.ID]
		if !exists {
			byID[in.ID] = in
			res.Added++
			continue
		}
		if sameContent(b, in) {
			// identical meaning — keep the newer-stamped copy so metadata converges
			if in.UpdatedAt > b.UpdatedAt {
				byID[in.ID] = in
			}
			res.Unchanged++
			continue
		}
		// genuine conflict: same id, different body
		c := Conflict{ID: in.ID, Base: b, Incoming: in}
		switch {
		case in.UpdatedAt > b.UpdatedAt && in.UpdatedAt != 0:
			c.Resolution = TookIncoming
			byID[in.ID] = in
			res.AutoWon++
		case b.UpdatedAt > in.UpdatedAt && b.UpdatedAt != 0:
			c.Resolution = KeptBase
			res.AutoWon++
		default: // tie (equal, or both 0) -> human must reconcile; keep base for now
			c.Resolution = Unresolved
			res.NeedReview++
		}
		res.Conflicts = append(res.Conflicts, c)
	}
	res.Merged = sortedRecords(byID)
	return res
}

// Apply settles an Unresolved conflict: pick the incoming record (true) or keep base
// (false), returning the chosen record to Put. The chosen record's UpdatedAt is bumped to
// now so the decision itself is the newest write (and future merges respect it).
func (c Conflict) Apply(takeIncoming bool) Record {
	r := c.Base
	if takeIncoming {
		r = c.Incoming
	}
	r.UpdatedAt = stamp()
	return r
}

// ExportJSON serializes records (typically one scope) to portable JSON — the durable form
// of a "your store" for git-backing, new-brain import, and promotion carry. (Distinct from
// Export, which renders the project store as read-only markdown.)
func ExportJSON(recs []Record) ([]byte, error) { return json.MarshalIndent(recs, "", "  ") }

// ImportJSON parses an ExportJSON blob.
func ImportJSON(data []byte) ([]Record, error) {
	var recs []Record
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, err
	}
	return recs, nil
}

// ListAll reads every record from a store (helper for export/merge callers).
func ListAll(s Store) []Record { return s.List(Filter{IncludeNonAuthoritative: true}) }

func sortedRecords(byID map[string]Record) []Record {
	out := make([]Record, 0, len(byID))
	for _, r := range byID {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
