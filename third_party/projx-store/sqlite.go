package store

import (
	"fmt"
	"strings"
)

// SQLite is a SQLite-backed Store. It behaves identically to Mem — insert-or-replace
// Put, (Record, bool) Get, no-op Delete, ID-sorted List — but persists through a
// sqlConn. On a native build that conn is modernc.org/sqlite (a local file or
// :memory:); built as a wasm cell it is the Pulp host's storage.sqlite capability.
// The store logic below is identical for both; only the conn differs (see conn_*.go).
type SQLite struct {
	c sqlConn
}

// Open opens (or creates) a SQLite-backed Store and runs any pending migrations.
// On native, path is a file path or ":memory:". As a Pulp cell, path is ignored —
// the host owns the database file (<storage-root>/<cell>/data.db). The returned
// *SQLite must be closed with Close.
func Open(path string) (*SQLite, error) {
	c, err := openConn(path)
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", path, err)
	}
	if err := migrate(c); err != nil {
		_ = c.close()
		return nil, fmt.Errorf("store: migrate %q: %w", path, err)
	}
	return &SQLite{c: c}, nil
}

// Close releases the underlying connection.
func (s *SQLite) Close() error { return s.c.close() }

// Put inserts or replaces a record by ID.
func (s *SQLite) Put(r Record) error {
	if r.ID == "" {
		return ErrNoID
	}
	if r.UpdatedAt == 0 { // fresh write — stamp it; merge/import pass a preserved value
		r.UpdatedAt = stamp()
	}
	if err := s.c.exec(
		`INSERT INTO records (id, kind, scope, rkey, body, updated_at, origin, enforcement, provenance, status, supersedes, replaced_by, claim_class, verified_at, review_after, verifier, evidence, model, confidence, approval) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET kind = excluded.kind, scope = excluded.scope,
		 rkey = excluded.rkey, body = excluded.body, updated_at = excluded.updated_at,
		 origin = excluded.origin, enforcement = excluded.enforcement, provenance = excluded.provenance,
		 status = excluded.status, supersedes = excluded.supersedes, replaced_by = excluded.replaced_by,
		 claim_class = excluded.claim_class, verified_at = excluded.verified_at, review_after = excluded.review_after,
		 verifier = excluded.verifier, evidence = excluded.evidence, model = excluded.model,
		 confidence = excluded.confidence, approval = excluded.approval`,
		r.ID, int64(r.Kind), int64(r.Scope), r.Key, r.Body, r.UpdatedAt, r.Origin, r.Enforcement, r.Provenance,
		r.Status, r.Supersedes, r.ReplacedBy, r.ClaimClass, r.VerifiedAt, r.ReviewAfter,
		r.Verifier, r.Evidence, r.Model, r.Confidence, r.Approval,
	); err != nil {
		return fmt.Errorf("store: put %q: %w", r.ID, err)
	}
	return nil
}

// Get returns the record with the given ID, if present.
func (s *SQLite) Get(id string) (Record, bool) {
	rows, err := s.c.query(`SELECT id, kind, scope, rkey, body, updated_at, origin, enforcement, provenance, status, supersedes, replaced_by, claim_class, verified_at, review_after, verifier, evidence, model, confidence, approval FROM records WHERE id = ?`, id)
	if err != nil || len(rows) == 0 {
		return Record{}, false
	}
	return rowToRecord(rows[0]), true
}

// Delete removes a record by ID. Deleting a missing ID is a no-op (not an error).
func (s *SQLite) Delete(id string) error {
	if err := s.c.exec(`DELETE FROM records WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete %q: %w", id, err)
	}
	return nil
}

// List returns all records matching the filter, sorted by ID. The filter is applied
// as SQL WHERE clauses so results match Mem.List exactly.
func (s *SQLite) List(f Filter) []Record {
	query := `SELECT id, kind, scope, rkey, body, updated_at, origin, enforcement, provenance, status, supersedes, replaced_by, claim_class, verified_at, review_after, verifier, evidence, model, confidence, approval FROM records`
	var (
		clauses []string
		args    []any
	)
	if f.Scope != nil {
		clauses = append(clauses, "scope = ?")
		args = append(args, int64(*f.Scope))
	}
	if f.Kind != nil {
		clauses = append(clauses, "kind = ?")
		args = append(args, int64(*f.Kind))
	}
	// KeyPrefix/Text mirror Filter.match: LIKE is case-insensitive for ASCII
	// (matches Mem's ToLower); metachars in the fragment are escaped (ESCAPE '\').
	if f.KeyPrefix != "" {
		clauses = append(clauses, `rkey LIKE ? ESCAPE '\'`)
		args = append(args, likeEscape(f.KeyPrefix)+"%")
	}
	if f.Text != "" {
		clauses = append(clauses, `(rkey LIKE ? ESCAPE '\' OR body LIKE ? ESCAPE '\')`)
		esc := "%" + likeEscape(f.Text) + "%"
		args = append(args, esc, esc)
	}
	if f.Status != "" {
		clauses = append(clauses, `COALESCE(NULLIF(status, ''), 'active') = ?`)
		args = append(args, f.Status)
	} else if !f.IncludeNonAuthoritative {
		clauses = append(clauses, `COALESCE(NULLIF(status, ''), 'active') = 'active'`)
	}
	for i, c := range clauses {
		if i == 0 {
			query += " WHERE "
		} else {
			query += " AND "
		}
		query += c
	}
	query += " ORDER BY id ASC"

	rows, err := s.c.query(query, args...)
	if err != nil {
		return nil
	}
	out := make([]Record, 0, len(rows))
	for _, row := range rows {
		out = append(out, rowToRecord(row))
	}
	return out
}

// likeEscape escapes LIKE metacharacters (\ % _) so a Key/Text fragment matches
// literally; the caller adds the real '%' wildcards. Pairs with ESCAPE '\'.
func likeEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}

// compile-time assertion that SQLite satisfies Store.
var _ Store = (*SQLite)(nil)
