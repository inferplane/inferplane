package local

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/inferplane/inferplane/internal/governance"
)

func TestJournalUpdatesOnlyChangedEntries(t *testing.T) {
	s, _ := openTest(t)
	funded(t, s, 100, 100, definition("hard", true))
	for range 8 {
		reserveTest(t, s, 1)
	}
	var raw []byte
	if err := s.db.QueryRow(`SELECT state FROM local_escrow`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var meta journal
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Version != 2 || len(meta.Permits) != 0 {
		t.Fatalf("whole journal remains on hot row: version=%d permits=%d", meta.Version, len(meta.Permits))
	}
	if _, err := s.db.Exec(`CREATE TABLE observed_changes(kind TEXT);
		CREATE TRIGGER observe_entry_updates AFTER UPDATE ON local_escrow_entries
		BEGIN INSERT INTO observed_changes VALUES(new.kind); END`); err != nil {
		t.Fatal(err)
	}
	p := reserveTest(t, s, 1)
	finishTest(t, s, p, amount(1), true)
	var updates int
	if err := s.db.QueryRow(`SELECT count(*) FROM observed_changes WHERE kind='permits'`).Scan(&updates); err != nil {
		t.Fatal(err)
	}
	if updates != 1 {
		t.Fatalf("unchanged pending permits rewritten: %d updates", updates)
	}
}

func TestCachedJournalRollbackAndExternalCorruption(t *testing.T) {
	s, _ := openTest(t)
	funded(t, s, 10, 10, definition("hard", true))
	rollback := errors.New("rollback")
	if err := s.transaction(context.Background(), func(j *journal) error {
		for _, g := range j.Grants {
			g.Consumed = g.Wire.Amount
		}
		j.Ready = false
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	p := reserveTest(t, s, 1)
	if _, err := s.db.Exec(`UPDATE local_escrow_entries SET value='null' WHERE kind='permits' AND id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Reserve(context.Background(), testSubject, 1); got != nil || !errors.Is(err, governance.ErrAuthorityInvalid) {
		t.Fatalf("cached state hid corrupt durable entry: %v %v", got, err)
	}
}

func TestDeletedBudgetEntityCannotDisableHardCap(t *testing.T) {
	s, _ := openTest(t)
	funded(t, s, 10, 10, definition("hard", true))
	if _, err := s.db.Exec(`DELETE FROM local_escrow_entries WHERE kind='budgets'`); err != nil {
		t.Fatal(err)
	}
	if p, err := s.Reserve(context.Background(), testSubject, 1); p != nil || !errors.Is(err, governance.ErrAuthorityInvalid) {
		t.Fatalf("missing entity disabled budget: %v %v", p, err)
	}
}

func TestVersionOneMigrationBurnsAndFencesOldAuthority(t *testing.T) {
	old, path := openTest(t)
	funded(t, old, 10, 10, definition("hard", true))
	reserveTest(t, old, 3)
	j, _ := readJournal(t, old)
	j.Version, j.EntryCounts = 1, nil
	raw, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`DELETE FROM local_escrow_entries; DROP TRIGGER local_entries_insert;
		DROP TRIGGER local_entries_update; DROP TRIGGER local_entries_delete;
		DROP TABLE local_escrow_entries; ALTER TABLE local_escrow DROP COLUMN revision`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`UPDATE local_escrow SET state=?`, raw); err != nil {
		t.Fatal(err)
	}
	next, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	migrated, _ := readJournal(t, next)
	if migrated.Version != 2 || migrated.Ready {
		t.Fatal("migration recovered spend authority")
	}
	for _, g := range migrated.Grants {
		if !g.Closed || g.Consumed != g.Wire.Amount || g.Reserved != 0 {
			t.Fatal("migration refunded old authority")
		}
	}
	if p, err := old.Reserve(context.Background(), testSubject, 1); p != nil || !errors.Is(err, governance.ErrAuthorityUnavailable) {
		t.Fatalf("old owner survived migration: %v %v", p, err)
	}
}
