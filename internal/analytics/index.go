// Package analytics maintains a DERIVED, rebuildable read-model of the audit log
// for the console's Usage analytics (design spec §4). The audit chain is the
// source of truth; this index is disposable — losing it never affects the data
// plane, and Replay() reconstructs it from the audit JSONL. Mode A (single
// local SQLite) and Mode B (shared HA Postgres). Ingestion is idempotent by
// record ULID after an original record upgrades a legacy projection.
package analytics

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/inferplane/inferplane/internal/audit"

	_ "modernc.org/sqlite" // pure-Go driver (CGO_ENABLED=0), registered as "sqlite"
)

// Fresh and existing databases use the same additive columns and conservative
// defaults. A legacy row has cost, but no reconstructed outcome/usage evidence.
const schema = `
CREATE TABLE IF NOT EXISTS events (
  id             TEXT PRIMARY KEY,
  day            TEXT NOT NULL DEFAULT '',
  team           TEXT NOT NULL DEFAULT '',
  model          TEXT NOT NULL DEFAULT '',
  provider       TEXT NOT NULL DEFAULT '',
  status         INTEGER NOT NULL DEFAULT 0,
  input_tokens   INTEGER NOT NULL DEFAULT 0,
  output_tokens  INTEGER NOT NULL DEFAULT 0,
  cache_read     INTEGER NOT NULL DEFAULT 0,
  cache_creation INTEGER NOT NULL DEFAULT 0,
  cost_micros    INTEGER NOT NULL DEFAULT 0,
  ts             TEXT NOT NULL DEFAULT '',
  body_ref       TEXT NOT NULL DEFAULT '',
  projection_version INTEGER NOT NULL DEFAULT 0,
  has_cost       INTEGER NOT NULL DEFAULT 1,
  outcome_state  TEXT NOT NULL DEFAULT 'unknown',
  cost_state     TEXT NOT NULL DEFAULT 'legacy_unknown',
  partial        INTEGER NOT NULL DEFAULT 0,
  error_code     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS events_day ON events(day);
CREATE INDEX IF NOT EXISTS events_team ON events(team);`

// eventsMigrationColumns are added without inferring completeness from old
// status codes or replacing the original audit records.
var eventsMigrationColumns = []string{
	`ALTER TABLE events ADD COLUMN ts TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE events ADD COLUMN body_ref TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE events ADD COLUMN projection_version INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE events ADD COLUMN has_cost INTEGER NOT NULL DEFAULT 1`,
	`ALTER TABLE events ADD COLUMN outcome_state TEXT NOT NULL DEFAULT 'unknown'`,
	`ALTER TABLE events ADD COLUMN cost_state TEXT NOT NULL DEFAULT 'legacy_unknown'`,
	`ALTER TABLE events ADD COLUMN partial INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE events ADD COLUMN error_code TEXT NOT NULL DEFAULT ''`,
}

// migrateEventsColumns adds any of eventsMigrationColumns not already
// present without losing existing rows. A fresh database already has them
// from the CREATE TABLE above, so this is a no-op there.
func migrateEventsColumns(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(events)`)
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for _, stmt := range eventsMigrationColumns {
		// Each ALTER statement's target column name is token index 5:
		// "ALTER(0) TABLE(1) events(2) ADD(3) COLUMN(4) <name>(5) ...".
		fields := strings.Fields(stmt)
		col := fields[5]
		if existing[col] {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate events.%s: %w", col, err)
		}
	}
	return nil
}

type Index struct {
	db *sql.DB

	mu         sync.Mutex
	lastIngest time.Time
}

func OpenSQLite(path string) (*Index, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	// Deliberately NO SetMaxOpenConns(1): unlike providerstore, this index is
	// read-concurrent (HTTP query handlers) while a single goroutine (the audit
	// Sink) writes. Capping to one connection would let a slow query block
	// Ingest() on the audit-writer goroutine and stall the fan-out. WAL mode
	// (DSN above) supports concurrent readers + one writer safely.
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateEventsColumns(db); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(AuditEventsDDL); err != nil {
		db.Close()
		return nil, err
	}
	return &Index{db: db}, nil
}

func (ix *Index) Close() error { return ix.db.Close() }

// Billable mirrors cmd/mayu/report.go: only settled completed records.
func Billable(r audit.Record) bool { return r.Event == "request_completed" && r.Cost != nil }

// ModelOf returns the resolved model when available, falling back to the
// requested model.
func ModelOf(r audit.Record) string {
	if r.Request.ModelResolved != "" {
		return r.Request.ModelResolved
	}
	return r.Request.ModelRequested
}

// DayOf extracts the UTC YYYY-MM-DD prefix from an RFC3339(Nano) timestamp.
// The audit TS is already UTC (RFC3339Nano), so a prefix is correct and cheap.
func DayOf(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ""
}

func billable(r audit.Record) bool  { return Billable(r) }
func modelOf(r audit.Record) string { return ModelOf(r) }
func dayOf(ts string) string        { return DayOf(ts) }

// execer is satisfied by both *sql.DB and *sql.Tx, so Ingest can run either on
// the live DB (one autocommit per record) or batched inside a Replay tx.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func ingest(e execer, r audit.Record) error {
	if !Observable(r) {
		return nil
	}
	var in, out, cacheRead, cacheCreate int64
	if r.Usage != nil {
		in, out = r.Usage.InputTokens, r.Usage.OutputTokens
		cacheRead, cacheCreate = r.Usage.CacheReadInputTokens, r.Usage.CacheCreationInputTokens
	}
	status := 0
	if r.Outcome != nil {
		status = r.Outcome.Status
	}
	var bodyRef string
	if r.BodyRef != nil {
		bodyRef = *r.BodyRef
	}
	var cost int64
	if r.Cost != nil {
		cost = r.Cost.AmountUSDMicros
	}
	evidence := EvidenceOf(r)
	_, err := e.Exec(
		`INSERT INTO events(id,day,team,model,provider,status,input_tokens,output_tokens,cache_read,cache_creation,cost_micros,ts,body_ref,
		 projection_version,has_cost,outcome_state,cost_state,partial,error_code)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
		 day=excluded.day,team=excluded.team,model=excluded.model,provider=excluded.provider,status=excluded.status,
		 input_tokens=excluded.input_tokens,output_tokens=excluded.output_tokens,cache_read=excluded.cache_read,
		 cache_creation=excluded.cache_creation,cost_micros=excluded.cost_micros,ts=excluded.ts,body_ref=excluded.body_ref,
		 projection_version=excluded.projection_version,has_cost=excluded.has_cost,outcome_state=excluded.outcome_state,
		 cost_state=excluded.cost_state,partial=excluded.partial,error_code=excluded.error_code
		 WHERE events.projection_version=0`,
		r.ID, DayOf(r.TS), r.Principal.Team, ModelOf(r), r.Request.Provider, status,
		in, out, cacheRead, cacheCreate, cost, r.TS, bodyRef,
		evidence.HasCost, evidence.OutcomeState, evidence.CostState, evidence.Partial, evidence.ErrorCode)
	return err
}

// Ingest projects one completed record. An original may upgrade a legacy
// version=0 row; version=1 keeps the first-write-wins contract for its ULID.
func (ix *Index) Ingest(r audit.Record) error {
	if err := ingest(ix.db, r); err != nil {
		return err
	}
	if Observable(r) {
		ix.markIngest(time.Now().UTC())
	}
	return nil
}

func (ix *Index) markIngest(t time.Time) {
	ix.mu.Lock()
	ix.lastIngest = t
	ix.mu.Unlock()
}

// Health reports Mode A freshness for /admin/analytics/health.
func (ix *Index) Health() (Health, error) {
	ix.mu.Lock()
	lastIngest := ix.lastIngest
	ix.mu.Unlock()

	h := Health{
		Mode:            "A",
		IsLeader:        true,
		LeaseEpoch:      0,
		LagSeconds:      0,
		SegmentsTracked: 0,
	}
	if !lastIngest.IsZero() {
		h.LastIngestTS = lastIngest.Format(time.RFC3339Nano)
	}
	return h, nil
}

// Replay scans a JSONL audit stream and ingests every observable, newline-
// terminated line inside ONE transaction (so a large boot replay does not pay
// an fsync per row). It reads only complete lines (ReadString), dropping an
// unterminated final remainder exactly as cmd/mayu/report.go does — so a
// crash-truncated last record is never half-ingested. Malformed lines are
// skipped. Idempotent. The returned count is observable lines SEEN (duplicates
// already present count as seen) — only for a boot log line.
func (ix *Index) Replay(r io.Reader) (int, error) {
	tx, err := ix.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() // no-op after a successful Commit

	br := bufio.NewReader(r)
	n := 0
	for {
		line, readErr := br.ReadString('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' { // complete line only
			var rec audit.Record
			if json.Unmarshal([]byte(line), &rec) == nil {
				if Observable(rec) {
					if e := ingest(tx, rec); e != nil {
						return n, e
					}
					n++
				}
				// Console projection is independent of the usage projection;
				// the insert is ON CONFLICT DO NOTHING, so replay is idempotent.
				if AuditObservable(rec) {
					if e := ingestAudit(tx, rec, []byte(strings.TrimRight(line, "\r\n"))); e != nil {
						return n, e
					}
				}
			}
		}
		if readErr != nil { // io.EOF (drops any unterminated trailing remainder)
			break
		}
	}
	if err := tx.Commit(); err != nil {
		return n, err
	}
	if n > 0 {
		ix.markIngest(time.Now().UTC())
	}
	return n, nil
}

// SummaryQuery bounds the aggregate by inclusive UTC day (YYYY-MM-DD); empty =
// unbounded at this layer. The API handler applies the default 30-day / max
// 366-day window before calling this (spec §13).
type SummaryQuery struct{ SinceDay, UntilDay string }
type TimeSeriesQuery struct{ Days int }

type Totals struct {
	Requests            int64           `json:"requests"`
	InputTokens         int64           `json:"input_tokens"`
	OutputTokens        int64           `json:"output_tokens"`
	CacheReadTokens     int64           `json:"cache_read_tokens"`
	CacheCreationTokens int64           `json:"cache_creation_tokens"`
	CostMicros          int64           `json:"cost_micros"`
	Operations          OperationTotals `json:"operations"`
}
type TeamRow struct {
	Team       string          `json:"team"`
	Requests   int64           `json:"requests"`
	CostMicros int64           `json:"cost_micros"`
	Operations OperationTotals `json:"operations"`
}
type ModelRow struct {
	Model      string          `json:"model"`
	Requests   int64           `json:"requests"`
	CostMicros int64           `json:"cost_micros"`
	Operations OperationTotals `json:"operations"`
}
type Summary struct {
	Totals  Totals     `json:"totals"`
	ByTeam  []TeamRow  `json:"by_team"`
	ByModel []ModelRow `json:"by_model"`
}
type DayPoint struct {
	Day          string          `json:"day"`
	Requests     int64           `json:"requests"`
	CostMicros   int64           `json:"cost_micros"`
	InputTokens  int64           `json:"input_tokens"`
	OutputTokens int64           `json:"output_tokens"`
	Operations   OperationTotals `json:"operations"`
}

// Event is one row from Recent — the console's Logs list (D4, ADR-018).
// BodyRef is "" when the request wasn't captured (log_bodies off, an error
// response, or a streaming response — request-only capture there).
type Event struct {
	ID                string       `json:"id"`
	TS                string       `json:"ts"`
	Team              string       `json:"team"`
	Model             string       `json:"model"`
	Provider          string       `json:"provider"`
	Status            int          `json:"status"`
	InputTokens       int64        `json:"input_tokens"`
	OutputTokens      int64        `json:"output_tokens"`
	CostMicros        int64        `json:"cost_micros"`
	BodyRef           string       `json:"body_ref,omitempty"`
	HasCost           bool         `json:"has_cost"`
	OutcomeState      OutcomeState `json:"outcome_state"`
	CostState         CostState    `json:"cost_state"`
	Partial           bool         `json:"partial"`
	ErrorCode         string       `json:"error_code,omitempty"`
	ProjectionVersion int          `json:"projection_version"`
}

// recentEventsLimit bounds a single Recent page — the console paginates via
// `before` (id keyset) rather than requesting unbounded history in one call.
const recentEventsLimit = 200

// Recent returns the most recent events, newest first (ULIDs are
// lexicographically time-ordered, so an id DESC sort is correct). limit is
// clamped to (0, recentEventsLimit]; before, when set, keyset-paginates
// strictly older than that event's ID (id-based, not offset-based, so a
// concurrent insert can't shift or duplicate a page).
func (ix *Index) Recent(limit int, before string) ([]Event, error) {
	if limit <= 0 || limit > recentEventsLimit {
		limit = recentEventsLimit
	}
	query := `SELECT id, ts, team, model, provider, status, input_tokens, output_tokens, cost_micros, body_ref,
		has_cost, outcome_state, cost_state, partial, error_code, projection_version FROM events`
	args := []any{}
	if before != "" {
		query += ` WHERE id < ?`
		args = append(args, before)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := ix.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.TS, &e.Team, &e.Model, &e.Provider, &e.Status,
			&e.InputTokens, &e.OutputTokens, &e.CostMicros, &e.BodyRef,
			&e.HasCost, &e.OutcomeState, &e.CostState, &e.Partial, &e.ErrorCode, &e.ProjectionVersion); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func dayWhere(since, until string) (string, []any) {
	clause, args := "", []any{}
	if since != "" {
		clause += " AND day >= ?"
		args = append(args, since)
	}
	if until != "" {
		clause += " AND day <= ?"
		args = append(args, until)
	}
	return clause, args
}

// Every completion contributes once to each mutually exclusive state family.
const operationTotalsSQL = `COUNT(*),
	COALESCE(SUM(CASE WHEN outcome_state='succeeded' THEN 1 ELSE 0 END),0),
	COALESCE(SUM(CASE WHEN outcome_state='failed' THEN 1 ELSE 0 END),0),
	COALESCE(SUM(CASE WHEN outcome_state='partial' THEN 1 ELSE 0 END),0),
	COALESCE(SUM(CASE WHEN outcome_state='unknown' THEN 1 ELSE 0 END),0),
	COALESCE(SUM(CASE WHEN cost_state='estimated' THEN 1 ELSE 0 END),0),
	COALESCE(SUM(CASE WHEN cost_state='uncertain' THEN 1 ELSE 0 END),0),
	COALESCE(SUM(CASE WHEN cost_state='unpriced' THEN 1 ELSE 0 END),0),
	COALESCE(SUM(CASE WHEN cost_state='unavailable' THEN 1 ELSE 0 END),0),
	COALESCE(SUM(CASE WHEN cost_state='legacy_unknown' THEN 1 ELSE 0 END),0),
	COALESCE(SUM(CASE WHEN cost_state='estimated' THEN cost_micros ELSE 0 END),0),
	COALESCE(SUM(CASE WHEN cost_state IN ('uncertain','legacy_unknown') THEN cost_micros ELSE 0 END),0),
	COALESCE(SUM(CASE WHEN projection_version=0 THEN 1 ELSE 0 END),0)`

const legacyRequestsSQL = `COALESCE(SUM(CASE WHEN has_cost=1 THEN 1 ELSE 0 END),0)`
const legacyCostSQL = `COALESCE(SUM(CASE WHEN has_cost=1 THEN cost_micros ELSE 0 END),0)`

func operationScanTargets(o *OperationTotals) []any {
	return []any{&o.Completed, &o.Succeeded, &o.Failed, &o.Partial, &o.Unknown,
		&o.EstimatedRequests, &o.UncertainRequests, &o.UnpricedRequests, &o.UnavailableRequests, &o.LegacyUnknownRequests,
		&o.EstimatedCostMicros, &o.UncertainRecordedCostMicros, &o.LegacyProjectionRecords}
}

func (ix *Index) Summary(q SummaryQuery) (Summary, error) {
	s := Summary{ByTeam: []TeamRow{}, ByModel: []ModelRow{}}
	where, args := dayWhere(q.SinceDay, q.UntilDay)

	row := ix.db.QueryRow(`SELECT `+legacyRequestsSQL+`,
		COALESCE(SUM(CASE WHEN has_cost=1 THEN input_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN has_cost=1 THEN output_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN has_cost=1 THEN cache_read ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN has_cost=1 THEN cache_creation ELSE 0 END),0),`+legacyCostSQL+`,`+operationTotalsSQL+`
		FROM events WHERE 1=1`+where, args...)
	if err := row.Scan(append([]any{&s.Totals.Requests, &s.Totals.InputTokens, &s.Totals.OutputTokens,
		&s.Totals.CacheReadTokens, &s.Totals.CacheCreationTokens, &s.Totals.CostMicros}, operationScanTargets(&s.Totals.Operations)...)...); err != nil {
		return s, err
	}

	teamRows, err := ix.db.Query(`SELECT team, `+legacyRequestsSQL+`,`+legacyCostSQL+`,`+operationTotalsSQL+` FROM events
		WHERE 1=1`+where+` GROUP BY team ORDER BY `+legacyCostSQL+` DESC`, args...)
	if err != nil {
		return s, err
	}
	for teamRows.Next() {
		var r TeamRow
		if err := teamRows.Scan(append([]any{&r.Team, &r.Requests, &r.CostMicros}, operationScanTargets(&r.Operations)...)...); err != nil {
			teamRows.Close()
			return s, err
		}
		s.ByTeam = append(s.ByTeam, r)
	}
	if err := teamRows.Err(); err != nil {
		teamRows.Close()
		return s, err
	}
	teamRows.Close()

	modelRows, err := ix.db.Query(`SELECT model, `+legacyRequestsSQL+`,`+legacyCostSQL+`,`+operationTotalsSQL+` FROM events
		WHERE 1=1`+where+` GROUP BY model ORDER BY `+legacyCostSQL+` DESC`, args...)
	if err != nil {
		return s, err
	}
	defer modelRows.Close()
	for modelRows.Next() {
		var r ModelRow
		if err := modelRows.Scan(append([]any{&r.Model, &r.Requests, &r.CostMicros}, operationScanTargets(&r.Operations)...)...); err != nil {
			return s, err
		}
		s.ByModel = append(s.ByModel, r)
	}
	return s, modelRows.Err()
}

func (ix *Index) TimeSeries(q TimeSeriesQuery) ([]DayPoint, error) {
	days := q.Days
	if days <= 0 {
		days = 30
	}
	if days > 366 {
		days = 366
	}
	// Bound the SCAN by day (uses the events_day index), not just the result
	// LIMIT — otherwise SQLite would group the entire history before limiting.
	// date('now', ...) is UTC and returns 'YYYY-MM-DD', matching the day column.
	rows, err := ix.db.Query(`SELECT day, `+legacyRequestsSQL+`,`+legacyCostSQL+`,
		COALESCE(SUM(CASE WHEN has_cost=1 THEN input_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN has_cost=1 THEN output_tokens ELSE 0 END),0),`+operationTotalsSQL+`
		FROM events WHERE day >= date('now', ?) GROUP BY day ORDER BY day DESC LIMIT ?`,
		fmt.Sprintf("-%d days", days), days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DayPoint{}
	for rows.Next() {
		var p DayPoint
		if err := rows.Scan(append([]any{&p.Day, &p.Requests, &p.CostMicros, &p.InputTokens, &p.OutputTokens},
			operationScanTargets(&p.Operations)...)...); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
