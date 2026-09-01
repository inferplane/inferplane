package controlplane

import (
	"database/sql"
	"fmt"
	"time"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	_ "modernc.org/sqlite" // pure-Go driver (CGO_ENABLED=0), registered as "sqlite"
)

// sqliteLedger is the SQLite LedgerStore (roadmap ② durability half).
// Single writer by design — only the control plane's heartbeat handler
// touches it, serialized under the server mutex; busy_timeout + WAL match
// the keystore/bodystore conventions anyway so an operator inspecting the
// file with the sqlite3 CLI doesn't wedge a running inferplaned.
type sqliteLedger struct {
	db *sql.DB
}

// NewSQLiteLedger opens (creating if absent) the durable lease ledger at
// path.
func NewSQLiteLedger(path string) (LedgerStore, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("ledger store: open: %w", err)
	}
	schema := `
CREATE TABLE IF NOT EXISTS lease_ledger (
  policy    TEXT    NOT NULL,
  rule      TEXT    NOT NULL,
  dataplane TEXT    NOT NULL,
  period    TEXT    NOT NULL,
  spent     INTEGER NOT NULL,
  allowance INTEGER NOT NULL,
  PRIMARY KEY (policy, rule, dataplane)
);
CREATE TABLE IF NOT EXISTS dataplanes (
  id             TEXT    PRIMARY KEY,
  last_seen_unix INTEGER NOT NULL
);`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("ledger store: schema: %w", err)
	}
	return &sqliteLedger{db: db}, nil
}

func (l *sqliteLedger) Load() ([]LedgerRow, []DataplaneRow, error) {
	rows, err := l.db.Query(`SELECT policy, rule, dataplane, period, spent, allowance FROM lease_ledger`)
	if err != nil {
		return nil, nil, fmt.Errorf("ledger store: load: %w", err)
	}
	defer rows.Close()
	var ledger []LedgerRow
	for rows.Next() {
		var r LedgerRow
		var period string
		if err := rows.Scan(&r.Policy, &r.Rule, &r.Dataplane, &period, &r.Spent, &r.Allowance); err != nil {
			return nil, nil, fmt.Errorf("ledger store: load: %w", err)
		}
		r.Period = v1alpha1.BudgetPeriod(period)
		ledger = append(ledger, r)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("ledger store: load: %w", err)
	}

	dps, err := l.db.Query(`SELECT id, last_seen_unix FROM dataplanes`)
	if err != nil {
		return nil, nil, fmt.Errorf("ledger store: load: %w", err)
	}
	defer dps.Close()
	var planes []DataplaneRow
	for dps.Next() {
		var d DataplaneRow
		var unix int64
		if err := dps.Scan(&d.ID, &unix); err != nil {
			return nil, nil, fmt.Errorf("ledger store: load: %w", err)
		}
		d.LastSeen = time.Unix(0, unix)
		planes = append(planes, d)
	}
	if err := dps.Err(); err != nil {
		return nil, nil, fmt.Errorf("ledger store: load: %w", err)
	}
	return ledger, planes, nil
}

func (l *sqliteLedger) SaveDataplane(dp DataplaneRow, rows []LedgerRow) error {
	tx, err := l.db.Begin()
	if err != nil {
		return fmt.Errorf("ledger store: save: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO dataplanes (id, last_seen_unix) VALUES (?, ?)
		 ON CONFLICT(id) DO UPDATE SET last_seen_unix = excluded.last_seen_unix`,
		dp.ID, dp.LastSeen.UnixNano()); err != nil {
		return fmt.Errorf("ledger store: save: %w", err)
	}
	for _, r := range rows {
		if _, err := tx.Exec(
			`INSERT INTO lease_ledger (policy, rule, dataplane, period, spent, allowance) VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT(policy, rule, dataplane) DO UPDATE SET
			   period = excluded.period, spent = excluded.spent, allowance = excluded.allowance`,
			r.Policy, r.Rule, r.Dataplane, string(r.Period), r.Spent, r.Allowance); err != nil {
			return fmt.Errorf("ledger store: save: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ledger store: save: %w", err)
	}
	return nil
}

func (l *sqliteLedger) DeleteDataplane(id string) error {
	tx, err := l.db.Begin()
	if err != nil {
		return fmt.Errorf("ledger store: delete: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM lease_ledger WHERE dataplane = ?`, id); err != nil {
		return fmt.Errorf("ledger store: delete: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM dataplanes WHERE id = ?`, id); err != nil {
		return fmt.Errorf("ledger store: delete: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ledger store: delete: %w", err)
	}
	return nil
}

func (l *sqliteLedger) Close() error { return l.db.Close() }
