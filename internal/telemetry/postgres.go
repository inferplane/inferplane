package telemetry

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// usageSchemaLockKey is a Postgres advisory-lock key distinct from
// analytics' (847001) and bodystore's (847002) — fixed so concurrent
// inferplaned replica boots serialize migration instead of racing
// CREATE TABLE.
const usageSchemaLockKey int64 = 847003

// pgReadTimeout bounds every read so the memory fallback fires well inside
// the HTTP write deadline (P2 gate round 3).
const pgReadTimeout = 5 * time.Second

// Portable SQL types only — standard pg_dump/RDS snapshots ARE the backup
// story (spec D2); inferplaned implements no bespoke backup. The PK starts
// with dataplane, so time-range dashboard queries need the window_start
// secondary index.
const usagePGSchema = `
CREATE TABLE IF NOT EXISTS usage_windows (
  dataplane      TEXT NOT NULL,
  window_start   TIMESTAMPTZ NOT NULL,
  window_end     TIMESTAMPTZ NOT NULL,
  team           TEXT NOT NULL,
  "user"         TEXT NOT NULL DEFAULT '',
  model          TEXT NOT NULL,
  spent_micro_usd        BIGINT NOT NULL,
  input_tokens           BIGINT NOT NULL,
  output_tokens          BIGINT NOT NULL,
  cache_read_tokens      BIGINT NOT NULL,
  cache_write_5m_tokens  BIGINT NOT NULL,
  cache_write_1h_tokens  BIGINT NOT NULL,
  PRIMARY KEY (dataplane, window_start, team, "user", model)
);
CREATE INDEX IF NOT EXISTS usage_windows_start ON usage_windows(window_start);`

// PostgresAggregator is the opt-in durable store behind the memory
// aggregator (never an either/or alternative — see DurableAggregator).
// Construction NEVER dials: a Postgres outage must not block inferplaned
// boot; the pool connects lazily and the first successful connection runs
// the schema migration.
type PostgresAggregator struct {
	db *pgxpool.Pool

	mu       sync.Mutex
	migrated bool
}

// NewPostgresAggregator parses the DSN and builds a lazy pool. The DSN is
// never echoed in an error — a pgx parse failure embeds the connection
// string, which may carry a password (same rule as bodystore.NewPostgres).
func NewPostgresAggregator(dsn string) (*PostgresAggregator, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("telemetry: invalid usage-store dsn (check INFERPLANED_USAGE_DSN resolves to a valid postgres:// connection string)")
	}
	db, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, errors.New("telemetry: could not build the usage-store pool (dsn withheld)")
	}
	return &PostgresAggregator{db: db}, nil
}

// Close releases the pool.
func (p *PostgresAggregator) Close() { p.db.Close() }

// ensureSchema runs the migration once per process, on first use, under a
// session advisory lock held on ONE acquired connection (the bodystore /
// analytics precedent). Errors are returned redacted-safe: pgx connect
// errors never embed the DSN at this layer (only ParseConfig does, handled
// in the constructor).
func (p *PostgresAggregator) ensureSchema(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.migrated {
		return nil
	}
	conn, err := p.db.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("telemetry: acquire usage schema migration connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, usageSchemaLockKey); err != nil {
		return fmt.Errorf("telemetry: lock usage schema migration: %w", err)
	}
	defer conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, usageSchemaLockKey)
	if _, err := conn.Exec(ctx, usagePGSchema); err != nil {
		return fmt.Errorf("telemetry: create usage schema: %w", err)
	}
	p.migrated = true
	return nil
}

// Upsert is one transaction: DELETE the (dataplane, window_start) key then
// INSERT the batch — batch-replace, identical semantics to the memory
// aggregator (a corrected retry with fewer entries leaves no stale rows).
func (p *PostgresAggregator) Upsert(ctx context.Context, b *UsageBatch) error {
	if err := b.Validate(); err != nil {
		return err
	}
	if err := p.ensureSchema(ctx); err != nil {
		return err
	}
	tx, err := p.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("telemetry: usage upsert begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`DELETE FROM usage_windows WHERE dataplane = $1 AND window_start = $2`,
		b.Dataplane, b.WindowStart); err != nil {
		return fmt.Errorf("telemetry: usage upsert delete: %w", err)
	}
	for i := range b.Entries {
		e := &b.Entries[i]
		if _, err := tx.Exec(ctx, `
			INSERT INTO usage_windows (dataplane, window_start, window_end, team, "user", model,
				spent_micro_usd, input_tokens, output_tokens,
				cache_read_tokens, cache_write_5m_tokens, cache_write_1h_tokens)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			b.Dataplane, b.WindowStart, b.WindowEnd, e.Team, e.User, e.Model,
			e.SpentMicroUSD, e.InputTokens, e.OutputTokens,
			e.CacheReadTokens, e.CacheWrite5mTokens, e.CacheWrite1hTokens); err != nil {
			return fmt.Errorf("telemetry: usage upsert insert: %w", err)
		}
	}
	return tx.Commit(ctx)
}

func (p *PostgresAggregator) Query(ctx context.Context, f QueryFilter) (QueryResult, error) {
	if !ValidGroupBy(f.GroupBy) {
		return QueryResult{}, fmt.Errorf("telemetry: invalid group_by %q", f.GroupBy)
	}
	ctx, cancel := context.WithTimeout(ctx, pgReadTimeout)
	defer cancel()
	if err := p.ensureSchema(ctx); err != nil {
		return QueryResult{}, err
	}
	// GroupBy is validated against the closed set above — never raw input.
	col := map[string]string{"team": "team", "user": `"user"`, "model": "model"}[f.GroupBy]
	rows, err := p.db.Query(ctx, fmt.Sprintf(`
		SELECT %s, COALESCE(SUM(spent_micro_usd),0), COALESCE(SUM(input_tokens),0),
			COALESCE(SUM(output_tokens),0), COALESCE(SUM(cache_read_tokens),0),
			COALESCE(SUM(cache_write_5m_tokens),0), COALESCE(SUM(cache_write_1h_tokens),0)
		FROM usage_windows
		WHERE window_start >= $1 AND window_start < $2
			AND ($3 = '' OR team = $3) AND ($4 = '' OR "user" = $4) AND ($5 = '' OR model = $5)
		GROUP BY %s ORDER BY %s`, col, col, col),
		f.Since, f.Until, f.Team, f.User, f.Model)
	if err != nil {
		return QueryResult{}, fmt.Errorf("telemetry: usage query: %w", err)
	}
	defer rows.Close()
	var res QueryResult
	for rows.Next() {
		var r QueryRow
		if err := rows.Scan(&r.Key, &r.SpentMicroUSD, &r.InputTokens, &r.OutputTokens,
			&r.CacheReadTokens, &r.CacheWrite5mTokens, &r.CacheWrite1hTokens); err != nil {
			return QueryResult{}, fmt.Errorf("telemetry: usage query scan: %w", err)
		}
		res.Rows = append(res.Rows, r)
		res.TotalMicroUSD += r.SpentMicroUSD
	}
	return res, rows.Err()
}

// Rows iterates the cursor directly — no memory lock, no materialization.
func (p *PostgresAggregator) Rows(ctx context.Context, since, until time.Time, fn func(StoredRow) error) error {
	if err := p.ensureSchema(ctx); err != nil {
		return err
	}
	rows, err := p.db.Query(ctx, `
		SELECT dataplane, window_start, window_end, team, "user", model,
			spent_micro_usd, input_tokens, output_tokens,
			cache_read_tokens, cache_write_5m_tokens, cache_write_1h_tokens
		FROM usage_windows
		WHERE window_start >= $1 AND window_start < $2
		ORDER BY window_start, dataplane`, since, until)
	if err != nil {
		return fmt.Errorf("telemetry: usage rows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r StoredRow
		if err := rows.Scan(&r.Dataplane, &r.WindowStart, &r.WindowEnd, &r.Team, &r.User, &r.Model,
			&r.SpentMicroUSD, &r.InputTokens, &r.OutputTokens,
			&r.CacheReadTokens, &r.CacheWrite5mTokens, &r.CacheWrite1hTokens); err != nil {
			return fmt.Errorf("telemetry: usage rows scan: %w", err)
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

var _ Aggregator = (*PostgresAggregator)(nil)

// DurableAggregator composes the always-on memory aggregator with the
// opt-in Postgres layer (spec D2 failure posture, r3 plan):
//
//   - Upsert is synchronous write-through: PG commit FIRST, then memory,
//     then the caller acks. A PG failure returns the error — the POST
//     handler 503s and the DATA PLANE's FIFO is the single retry store.
//     There is no control-plane-side write queue to lose on an ECS task
//     replacement.
//   - Query/Rows serve from PG (full history) when healthy; on PG error,
//     Query falls back to memory (bounded retention) with Degraded=true —
//     a billing view silently missing history is worse than an error.
//     Rows decides PG-vs-memory once, UP FRONT — never mid-cursor (a
//     fallback after emitting PG rows would duplicate committed output),
//     and a PG failure there aborts the export instead of degrading.
type DurableAggregator struct {
	mem Aggregator
	pg  Aggregator
}

// NewDurableAggregator wires memory + Postgres write-through.
func NewDurableAggregator(mem, pg Aggregator) *DurableAggregator {
	return &DurableAggregator{mem: mem, pg: pg}
}

func (d *DurableAggregator) Upsert(ctx context.Context, b *UsageBatch) error {
	if err := d.pg.Upsert(ctx, b); err != nil {
		return err // not durable → not acked; mayu's FIFO retries
	}
	// Memory is best-effort behind a committed PG write: it holds the same
	// validated batch, so the only realistic failure is validation — already
	// passed in the PG path.
	_ = d.mem.Upsert(ctx, b)
	return nil
}

func (d *DurableAggregator) Query(ctx context.Context, f QueryFilter) (QueryResult, error) {
	res, err := d.pg.Query(ctx, f)
	if err == nil {
		return res, nil
	}
	if !ValidGroupBy(f.GroupBy) {
		return QueryResult{}, err // client error — don't mask it as degraded data
	}
	mres, merr := d.mem.Query(ctx, f)
	if merr != nil {
		return QueryResult{}, err
	}
	mres.Degraded = true
	return mres, nil
}

func (d *DurableAggregator) Rows(ctx context.Context, since, until time.Time, fn func(StoredRow) error) error {
	return d.pg.Rows(ctx, since, until, fn)
}

var _ Aggregator = (*DurableAggregator)(nil)
