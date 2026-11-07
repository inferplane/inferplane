package local

import (
	"context"
	"database/sql"
	"encoding/json"
	"maps"
	"reflect"
	"slices"

	"github.com/inferplane/inferplane/internal/policy"
)

// Version 2 keeps entities separately so one reservation does not rewrite all
// pending liabilities. Triggers fence decoded snapshots even when another
// connection (or a maintenance write on this connection) edits an entity.
func entrySchema(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(local_escrow)`)
	if err != nil {
		return err
	}
	hasRevision := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		hasRevision = hasRevision || name == "revision"
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !hasRevision {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE local_escrow ADD COLUMN revision INTEGER NOT NULL DEFAULT 0
			CHECK(typeof(revision)='integer' AND revision>=0)`); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS local_escrow_entries(
		kind TEXT NOT NULL, id TEXT NOT NULL, value BLOB NOT NULL, PRIMARY KEY(kind,id));
		CREATE TRIGGER IF NOT EXISTS local_entries_insert AFTER INSERT ON local_escrow_entries
		BEGIN UPDATE local_escrow SET revision=revision+1 WHERE id=1; END;
		CREATE TRIGGER IF NOT EXISTS local_entries_update AFTER UPDATE ON local_escrow_entries
		BEGIN UPDATE local_escrow SET revision=revision+1 WHERE id=1; END;
		CREATE TRIGGER IF NOT EXISTS local_entries_delete AFTER DELETE ON local_escrow_entries
		BEGIN UPDATE local_escrow SET revision=revision+1 WHERE id=1; END;`)
	return err
}

func loadJournal(ctx context.Context, tx *sql.Tx, raw []byte) (*journal, error) {
	j := emptyJournal()
	if err := decode(raw, j); err != nil {
		return nil, err
	}
	if j.Version == 1 {
		return j, nil
	} // atomic migration at initialize
	if len(j.Budgets)+len(j.Grants)+len(j.Requests)+len(j.Permits)+len(j.Meters)+len(j.Receipts) != 0 || len(j.EntryCounts) != 6 {
		return nil, invalid("incomplete journal entity manifest")
	}
	rows, err := tx.QueryContext(ctx, `SELECT kind,id,value FROM local_escrow_entries`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, id string
		var value []byte
		if err := rows.Scan(&kind, &id, &value); err != nil {
			return nil, err
		}
		switch kind {
		case "budgets":
			err = decodeEntry(j.Budgets, id, value)
		case "grants":
			err = decodeEntry(j.Grants, id, value)
		case "requests":
			err = decodeEntry(j.Requests, id, value)
		case "permits":
			err = decodeEntry(j.Permits, id, value)
		case "meters":
			err = decodeEntry(j.Meters, id, value)
		case "receipts":
			err = decodeEntry(j.Receipts, id, value)
		default:
			return nil, invalid("unknown journal entity")
		}
		if err != nil {
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for kind, count := range entryCounts(j) {
		if expected, ok := j.EntryCounts[kind]; !ok || expected != count {
			return nil, invalid("missing journal entities")
		}
	}
	if err := validateJournal(j); err != nil {
		return nil, err
	}
	return j, nil
}

func decodeEntry[V any](dst map[string]V, id string, raw []byte) error {
	var value V
	if err := json.Unmarshal(raw, &value); err != nil {
		return invalid("corrupt journal entity")
	}
	dst[id] = value
	return nil
}

func journalMetadata(j *journal) ([]byte, error) {
	meta := *j
	meta.Version = 2
	meta.EntryCounts = entryCounts(j)
	meta.Budgets = map[string]policy.AuthorityBudget{}
	meta.Grants = map[string]*grant{}
	meta.Requests = map[string]*grantRequest{}
	meta.Permits = map[string]*permit{}
	meta.Meters = map[string]*meter{}
	meta.Receipts = map[string]replayReceipt{}
	return json.Marshal(&meta)
}

func entryCounts(j *journal) map[string]int {
	return map[string]int{"budgets": len(j.Budgets), "grants": len(j.Grants),
		"requests": len(j.Requests), "permits": len(j.Permits),
		"meters": len(j.Meters), "receipts": len(j.Receipts)}
}

func persistEntries(ctx context.Context, tx *sql.Tx, before, after *journal) error {
	if before == nil {
		before = emptyJournal()
	}
	for _, err := range []error{
		updateEntries(ctx, tx, "budgets", before.Budgets, after.Budgets),
		updateEntries(ctx, tx, "grants", before.Grants, after.Grants),
		updateEntries(ctx, tx, "requests", before.Requests, after.Requests),
		updateEntries(ctx, tx, "permits", before.Permits, after.Permits),
		updateEntries(ctx, tx, "meters", before.Meters, after.Meters),
		updateEntries(ctx, tx, "receipts", before.Receipts, after.Receipts),
	} {
		if err != nil {
			return err
		}
	}
	return nil
}

func updateEntries[V any](ctx context.Context, tx *sql.Tx, kind string, before, after map[string]V) error {
	for id, value := range after {
		if old, exists := before[id]; exists && reflect.DeepEqual(old, value) {
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO local_escrow_entries(kind,id,value) VALUES(?,?,?)
			ON CONFLICT(kind,id) DO UPDATE SET value=excluded.value`, kind, id, raw); err != nil {
			return err
		}
	}
	for id := range before {
		if _, exists := after[id]; !exists {
			if _, err := tx.ExecContext(ctx, `DELETE FROM local_escrow_entries WHERE kind=? AND id=?`, kind, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// Copy before mutation: rollback and cancellation must never publish tentative
// credit or terminal outcomes through the decoded snapshot.
func cloneJournal(j *journal) *journal {
	c := *j
	c.Budgets = maps.Clone(j.Budgets)
	c.Grants = cloneEntries(j.Grants)
	c.Requests = cloneEntries(j.Requests)
	c.Permits = cloneEntries(j.Permits)
	for _, p := range c.Permits {
		p.Grants, p.Meters = slices.Clone(p.Grants), slices.Clone(p.Meters)
		if p.Actual != nil {
			actual := *p.Actual
			p.Actual = &actual
		}
	}
	c.Meters = cloneEntries(j.Meters)
	c.Receipts = maps.Clone(j.Receipts)
	c.ObsoleteRequests = slices.Clone(j.ObsoleteRequests)
	c.Completed = slices.Clone(j.Completed)
	c.ReceiptOrder = slices.Clone(j.ReceiptOrder)
	return &c
}

func cloneEntries[V any](entries map[string]*V) map[string]*V {
	out := make(map[string]*V, len(entries))
	for id, entry := range entries {
		value := *entry
		out[id] = &value
	}
	return out
}
