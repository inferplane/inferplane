package policystore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// policySchemaLockKey is a Postgres advisory-lock key distinct from
// analytics' (847001), bodystore's (847002) and telemetry's (847003).
const policySchemaLockKey int64 = 847004

// pgReadTimeout bounds every read so a stalled DB cannot pin an HTTP handler.
const pgReadTimeout = 5 * time.Second

// Portable SQL only — pg_dump/RDS snapshots are the backup story.
const policyPGSchema = `
CREATE TABLE IF NOT EXISTS policies (
  name       TEXT PRIMARY KEY,
  doc_yaml   TEXT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS policy_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);`

// seededKey is the policy_meta row that marks the one-time file→DB seed done.
const seededKey = "seeded"

// PostgresStore is the Postgres-backed Store. Construction NEVER dials: a
// Postgres outage must not fail construction; the pool connects lazily and
// the first successful connection runs the schema migration.
type PostgresStore struct {
	db *pgxpool.Pool

	mu       sync.Mutex
	migrated bool
}

// NewPostgres parses the DSN and builds a lazy pool. The DSN is never echoed
// in an error — a pgx parse failure embeds the connection string, which may
// carry a password (same rule as telemetry.NewPostgresAggregator).
func NewPostgres(dsn string) (*PostgresStore, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("policystore: invalid policy-store dsn (check INFERPLANED_POLICY_DSN resolves to a valid postgres:// connection string)")
	}
	db, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, errors.New("policystore: could not build the policy-store pool (dsn withheld)")
	}
	return &PostgresStore{db: db}, nil
}

// Close releases the pool.
func (p *PostgresStore) Close() { p.db.Close() }

// ensureSchema runs the migration once per process, on first use, under a
// session advisory lock held on ONE acquired connection (the telemetry /
// bodystore / analytics precedent). Errors are returned redacted-safe: pgx
// connect errors never embed the DSN at this layer (only ParseConfig does,
// handled in the constructor).
func (p *PostgresStore) ensureSchema(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.migrated {
		return nil
	}
	conn, err := p.db.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("policystore: acquire policy schema migration connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, policySchemaLockKey); err != nil {
		return fmt.Errorf("policystore: lock policy schema migration: %w", err)
	}
	defer conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, policySchemaLockKey)
	if _, err := conn.Exec(ctx, policyPGSchema); err != nil {
		return fmt.Errorf("policystore: create policy schema: %w", err)
	}
	p.migrated = true
	return nil
}

// List returns every document ordered by name.
func (p *PostgresStore) List(ctx context.Context) ([]Doc, error) {
	ctx, cancel := context.WithTimeout(ctx, pgReadTimeout)
	defer cancel()
	if err := p.ensureSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := p.db.Query(ctx, `SELECT name, doc_yaml, updated_at FROM policies ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("policystore: list policies: %w", err)
	}
	defer rows.Close()
	var docs []Doc
	for rows.Next() {
		var d Doc
		var docYAML string
		if err := rows.Scan(&d.Name, &docYAML, &d.UpdatedAt); err != nil {
			return nil, fmt.Errorf("policystore: list policies scan: %w", err)
		}
		d.YAML = []byte(docYAML)
		docs = append(docs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("policystore: list policies rows: %w", err)
	}
	return docs, nil
}

// Put upserts one whole document.
func (p *PostgresStore) Put(ctx context.Context, name string, docYAML []byte) error {
	ctx, cancel := context.WithTimeout(ctx, pgReadTimeout)
	defer cancel()
	if err := p.ensureSchema(ctx); err != nil {
		return err
	}
	if _, err := p.db.Exec(ctx,
		`INSERT INTO policies (name, doc_yaml, updated_at) VALUES ($1, $2, now())
 ON CONFLICT (name) DO UPDATE SET doc_yaml = EXCLUDED.doc_yaml, updated_at = now()`,
		name, string(docYAML)); err != nil {
		return fmt.Errorf("policystore: put policy %q: %w", name, err)
	}
	return nil
}

// Delete removes one document, ErrNotFound when absent.
func (p *PostgresStore) Delete(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, pgReadTimeout)
	defer cancel()
	if err := p.ensureSchema(ctx); err != nil {
		return err
	}
	res, err := p.db.Exec(ctx, `DELETE FROM policies WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("policystore: delete policy %q: %w", name, err)
	}
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Seeded reports whether the durable seed marker is set.
func (p *PostgresStore) Seeded(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, pgReadTimeout)
	defer cancel()
	if err := p.ensureSchema(ctx); err != nil {
		return false, err
	}
	var v string
	err := p.db.QueryRow(ctx, `SELECT value FROM policy_meta WHERE key = $1`, seededKey).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("policystore: read seed marker: %w", err)
	}
	return v == "1", nil
}

// Seed imports docs and sets the durable marker in ONE transaction, but only
// if not already seeded. Returns true if it seeded. The MARKER, not a row
// count, gates this — an emptied store is never re-seeded. Empty docs still
// set the marker: an inferplaned booted against an empty --policies directory
// must not re-seed on its next boot. Seed runs on the caller's ctx (boot-time,
// may legitimately take longer than a read).
func (p *PostgresStore) Seed(ctx context.Context, docs []Doc) (bool, error) {
	if err := p.ensureSchema(ctx); err != nil {
		return false, err
	}
	tx, err := p.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("policystore: seed begin: %w", err)
	}
	defer tx.Rollback(ctx)

	// The marker row IS the mutex, and it is claimed FIRST: a plain SELECT
	// re-check would let two replicas booting a fresh store both pass it and
	// then collide on the policies primary key, failing one boot on a
	// duplicate-key error. ON CONFLICT DO NOTHING instead makes the second
	// replica block on this row until the first commits, then see zero rows
	// affected — a clean no-op. (Rollback releases it, so a crashed seeder
	// does not wedge the next boot.)
	tag, err := tx.Exec(ctx,
		`INSERT INTO policy_meta (key, value) VALUES ($1, '1') ON CONFLICT (key) DO NOTHING`, seededKey)
	if err != nil {
		return false, fmt.Errorf("policystore: mark seeded: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil // already seeded (or being seeded) — no-op
	}

	for _, d := range docs {
		if _, err := tx.Exec(ctx, `INSERT INTO policies (name, doc_yaml) VALUES ($1, $2)`,
			d.Name, string(d.YAML)); err != nil {
			return false, fmt.Errorf("policystore: seed policy %q: %w", d.Name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("policystore: seed commit: %w", err)
	}
	return true, nil
}

var _ Store = (*PostgresStore)(nil)
