package grants

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go driver, registered as "sqlite"
)

// SQLiteStore is the durable GrantStore backed by one `grants` table in
// storage.sqlite. It uses the default rollback journal (not WAL) so every
// commit is durable on reopen without a checkpoint step — avoiding the
// WAL-never-checkpointed durability trap seen elsewhere in the portfolio.
type SQLiteStore struct {
	db *sql.DB
}

const grantsSchema = `
CREATE TABLE IF NOT EXISTS grants (
  id         TEXT PRIMARY KEY,
  cell_id    TEXT NOT NULL,
  kind       TEXT NOT NULL,
  subject    TEXT NOT NULL,
  access     INTEGER NOT NULL,
  scope      TEXT NOT NULL,
  expires_at INTEGER NOT NULL DEFAULT 0,
  granted_by TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  revoked_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_grants_lookup ON grants(kind, cell_id, revoked_at);
`

// OpenSQLiteStore opens (creating if needed) the grants DB at path.
func OpenSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // modernc/sqlite is a single writer; serialize
	if _, err := db.Exec(grantsSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("grants schema: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

// Close releases the database handle.
func (s *SQLiteStore) Close() error { return s.db.Close() }

func (s *SQLiteStore) activeRows(kind Kind, cellFilter string) ([]Grant, error) {
	now := nowFn()
	q := `SELECT id,cell_id,kind,subject,access,scope,expires_at,granted_by,created_at,revoked_at
	      FROM grants WHERE kind=? AND revoked_at=0 AND (expires_at=0 OR expires_at>?)`
	args := []any{string(kind), now}
	if cellFilter != "" {
		q += " AND cell_id=?"
		args = append(args, cellFilter)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGrants(rows)
}

func (s *SQLiteStore) Lookup(kind Kind, subject string, want int) (int, bool) {
	gs, err := s.activeRows(kind, "")
	if err != nil {
		return 0, false
	}
	best := 0
	for _, g := range gs {
		if SubjectCovers(kind, g.Subject, subject) && g.Access > best {
			best = g.Access
		}
	}
	return best, best >= want && best > 0
}

func (s *SQLiteStore) Put(g Grant) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO grants
		 (id,cell_id,kind,subject,access,scope,expires_at,granted_by,created_at,revoked_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		g.ID, g.CellID, string(g.Kind), g.Subject, g.Access, string(g.Scope),
		g.ExpiresAt, g.GrantedBy, g.CreatedAt, g.RevokedAt)
	return err
}

func (s *SQLiteStore) Active(cellID string, kind Kind) ([]Grant, error) {
	return s.activeRows(kind, cellID)
}

func (s *SQLiteStore) List(cellID string) ([]Grant, error) {
	rows, err := s.db.Query(
		`SELECT id,cell_id,kind,subject,access,scope,expires_at,granted_by,created_at,revoked_at
		 FROM grants WHERE cell_id=?`, cellID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGrants(rows)
}

func (s *SQLiteStore) Revoke(id string) error {
	_, err := s.db.Exec(`UPDATE grants SET revoked_at=? WHERE id=? AND revoked_at=0`, nowFn(), id)
	return err
}

func (s *SQLiteStore) RevokeMatching(kind Kind, query string) (int, error) {
	gs, err := s.activeRows(kind, "")
	if err != nil {
		return 0, err
	}
	now := nowFn()
	n := 0
	for _, g := range gs {
		if SubjectCovers(kind, g.Subject, query) || SubjectCovers(kind, query, g.Subject) {
			if _, err := s.db.Exec(`UPDATE grants SET revoked_at=? WHERE id=? AND revoked_at=0`, now, g.ID); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

func scanGrants(rows *sql.Rows) ([]Grant, error) {
	var out []Grant
	for rows.Next() {
		var g Grant
		var kind, scope string
		if err := rows.Scan(&g.ID, &g.CellID, &kind, &g.Subject, &g.Access, &scope,
			&g.ExpiresAt, &g.GrantedBy, &g.CreatedAt, &g.RevokedAt); err != nil {
			return nil, err
		}
		g.Kind = Kind(kind)
		g.Scope = Scope(scope)
		out = append(out, g)
	}
	return out, rows.Err()
}
