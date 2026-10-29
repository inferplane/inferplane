package analytics

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
)

type sqlExec interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func sqliteExec(e sqlExec) ExecFunc {
	return func(q string, args ...any) error { _, err := e.Exec(q, args...); return err }
}

func (ix *Index) run(q string, args []any, each func(RowScanner) error) error {
	rows, err := ix.db.Query(q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := each(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func ingestAudit(e sqlExec, r audit.Record, line []byte) error {
	row, ok := ProjectAuditEvent(r, line)
	if !ok {
		return nil
	}
	return InsertAuditEvent(SQLiteDialect, sqliteExec(e), row)
}

// ingestAuditSavepoint projects one console row inside a savepoint of the
// replay transaction. The console projection is best-effort per record: a
// failed insert rolls back to the savepoint, is logged by record ID and
// driver code only, and never aborts the usage replay. Only a failing
// savepoint statement (an unusable transaction) is returned.
func ingestAuditSavepoint(tx *sql.Tx, r audit.Record, line []byte) error {
	if _, err := tx.Exec(`SAVEPOINT audit_projection`); err != nil {
		return fmt.Errorf("begin audit projection savepoint: %w", err)
	}
	if err := ingestAudit(tx, r, line); err != nil {
		if _, rbErr := tx.Exec(`ROLLBACK TO SAVEPOINT audit_projection`); rbErr != nil {
			return fmt.Errorf("roll back audit projection savepoint: %w", rbErr)
		}
		LogAuditSkip(r.ID, sqliteCode(err))
	}
	if _, err := tx.Exec(`RELEASE SAVEPOINT audit_projection`); err != nil {
		return fmt.Errorf("release audit projection savepoint: %w", err)
	}
	return nil
}

// sqliteCode names a driver error by its numeric code, never its message.
func sqliteCode(err error) string {
	var c interface{ Code() int }
	if errors.As(err, &c) {
		return fmt.Sprintf("sqlite code %d", c.Code())
	}
	return "error"
}

// IngestAudit projects one terminal record into the console table.
func (ix *Index) IngestAudit(r audit.Record, line []byte) error {
	if err := ingestAudit(ix.db, r, line); err != nil {
		return err
	}
	ix.markIngest(time.Now().UTC())
	return nil
}

func (ix *Index) AuditEvents(f AuditFilter) ([]AuditEvent, string, error) {
	return RunAuditEvents(SQLiteDialect, ix.run, f)
}
func (ix *Index) AuditEvent(id string) (AuditEventDetail, bool, error) {
	return RunAuditEvent(SQLiteDialect, ix.run, id)
}
func (ix *Index) AuditSessions(f AuditFilter) ([]AuditSession, string, error) {
	return RunAuditSessions(SQLiteDialect, ix.run, f)
}
func (ix *Index) AuditSessionEvents(ref string, f AuditFilter) ([]AuditEvent, string, error) {
	return RunAuditSessionEvents(SQLiteDialect, ix.run, ref, f)
}
func (ix *Index) AuditSummary(f AuditFilter) (AuditSummary, error) {
	return RunAuditSummary(SQLiteDialect, ix.run, f)
}

var _ AuditEventQuerier = (*Index)(nil)
