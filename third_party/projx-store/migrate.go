package store

import (
	"embed"
	"fmt"

	sow "github.com/BananaLabs-OSS/sow"
)

// migrate.go — schema migrations, now run by the `sow` engine over the SAME sqlConn seam
// the store already uses (native modernc / the Pulp host's storage.sqlite capability). sow
// is driver-agnostic, so this works identically as a plain binary and inside the wasm cell.
// Forward-only: each step adds one column with a constant DEFAULT so existing rows back-fill.

//go:embed migrations/manifest.json migrations/*.sql
var storeMigrationFS embed.FS

// loadStoreMigrations reads the shipped plan in its explicitly authored order. Keeping the
// plan and SQL embedded makes migrations portable to both native and wasm/Pulp backends.
func loadStoreMigrations() ([]sow.Migration, error) {
	return sow.LoadManifestFS(storeMigrationFS, "migrations/manifest.json")
}

// sowConn adapts the internal sqlConn seam to sow.Conn. It is deliberately NOT a TxConn:
// the store has always migrated best-effort (step-by-step, no cross-call transaction),
// and the Pulp host seam exposes only exec/query — so sow drives it the same way on both
// backends.
type sowConn struct{ c sqlConn }

func (s sowConn) Exec(query string, args ...any) error             { return s.c.exec(query, args...) }
func (s sowConn) Query(query string, args ...any) ([][]any, error) { return s.c.query(query, args...) }

// migrate brings the connection up to the latest schema, applying any pending steps via
// sow. Idempotent: a fully-migrated DB is a no-op.
func migrate(c sqlConn) error {
	storeMigrations, err := loadStoreMigrations()
	if err != nil {
		return fmt.Errorf("store: load migrations: %w", err)
	}
	d, err := sow.New(sowConn{c: c})
	if err != nil {
		return fmt.Errorf("store: migrate init: %w", err)
	}
	if err := adoptLegacyVersion(c, d, storeMigrations); err != nil {
		return err
	}
	if _, err := d.Up(storeMigrations, sow.Options{}); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	return nil
}

// adoptLegacyVersion performs the ONE-TIME transition from the pre-sow `schema_version`
// integer counter to sow's `sow_migrations` ledger: a DB migrated by the old mechanism
// (schema_version = N) has its first N steps marked applied so sow doesn't re-run them
// (which would fail — "table records already exists"). It runs only when sow's ledger is
// still empty; afterward sow_migrations is the source of truth and this is a no-op.
func adoptLegacyVersion(c sqlConn, d *sow.DB, storeMigrations []sow.Migration) error {
	applied, err := d.AppliedVersions()
	if err != nil {
		return fmt.Errorf("store: read migration ledger: %w", err)
	}
	if len(applied) > 0 {
		return nil // already on sow's ledger (or a fresh sow DB mid-apply)
	}
	rows, err := c.query(`SELECT version FROM schema_version LIMIT 1`)
	if err != nil || len(rows) == 0 || len(rows[0]) == 0 {
		return nil // no legacy counter → fresh DB, nothing to adopt
	}
	n := asInt(rows[0][0])
	for i := 0; i < n && i < len(storeMigrations); i++ {
		if err := d.MarkApplied(storeMigrations[i].Version, storeMigrations[i].Name, 0); err != nil {
			return fmt.Errorf("store: adopt legacy migration %d: %w", i+1, err)
		}
	}
	return nil
}
