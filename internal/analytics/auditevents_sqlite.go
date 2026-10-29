package analytics

import (
	"database/sql"
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
