//go:build !wasm

package store

import (
	"database/sql"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite" (no CGo).
)

// nativeConn is the local backend: modernc.org/sqlite over database/sql. This is the
// library path — ProjX running as a plain Go binary on your machine. It is excluded
// from wasm builds (modernc does not target wasm); there the Pulp backend takes over.
type nativeConn struct {
	db *sql.DB
}

// openConn opens (or creates) a modernc SQLite database at path (":memory:" for an
// ephemeral one). One connection keeps an in-memory DB coherent and serializes
// writes; this store is small and not a hot path.
func openConn(path string) (sqlConn, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return &nativeConn{db: db}, nil
}

func (c *nativeConn) exec(query string, args ...any) error {
	_, err := c.db.Exec(query, args...)
	return err
}

func (c *nativeConn) query(query string, args ...any) ([][]any, error) {
	rows, err := c.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		out = append(out, vals)
	}
	return out, rows.Err()
}

func (c *nativeConn) close() error { return c.db.Close() }
