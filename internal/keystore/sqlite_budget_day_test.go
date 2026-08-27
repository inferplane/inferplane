package keystore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// TestMigration_keysTablePreDailyBudgetColumn is the TDD entry point for the
// per-day budget column: a keystore.db written before this change must open,
// keep every existing row, and report the new column as 0 for those rows —
// "0 = not limited on this dimension", never a surprise cap. It mirrors
// TestTeamsTable_migratesAllowedRegionsColumn's structure (hand-written
// pre-migration DDL, a pre-existing row, then the real API).
func TestMigration_keysTablePreDailyBudgetColumn(t *testing.T) {
	const plaintext = "ik_pre_daily_budget_fixture"
	path := filepath.Join(t.TempDir(), "pre-daily-budget.db")
	old, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	// Every column the current schema has EXCEPT budget_usd_micros_per_day.
	if _, err := old.Exec(`CREATE TABLE keys (
		key_id TEXT PRIMARY KEY, key_hash TEXT NOT NULL UNIQUE, team TEXT NOT NULL,
		allowed_models TEXT NOT NULL, created_at TEXT NOT NULL,
		revoked INTEGER NOT NULL DEFAULT 0,
		budget_usd_micros INTEGER NOT NULL DEFAULT 0,
		tpm INTEGER NOT NULL DEFAULT 0, rpm INTEGER NOT NULL DEFAULT 0,
		expires_at TEXT NOT NULL DEFAULT '', owner TEXT NOT NULL DEFAULT '',
		metadata TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	hash := hashKey(plaintext)
	if _, err := old.Exec(
		`INSERT INTO keys (key_id, key_hash, team, allowed_models, created_at, budget_usd_micros, tpm)
		 VALUES (?,?,?,?,?,?,?)`,
		"ik_"+hash[:12], hash, "pre-existing", "*", "t1", 5_000_000, 1234); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite on a keys table predating budget_usd_micros_per_day: %v", err)
	}
	defer s.Close()

	got, err := s.Resolve(context.Background(), plaintext)
	if err != nil {
		t.Fatalf("pre-existing row lost during migration: %v", err)
	}
	if got.Team != "pre-existing" || got.BudgetUSDMicros != 5_000_000 || got.TPM != 1234 {
		t.Fatalf("pre-existing columns corrupted by the migration: %+v", got)
	}
	if got.BudgetUSDMicrosPerDay != 0 {
		t.Fatalf("migrated column must default to 0 (unlimited) for an existing row, got %d", got.BudgetUSDMicrosPerDay)
	}

	// A write after the migration must actually persist the new column.
	plaintext2, _, err := s.CreateWithOptions(context.Background(), "t", []string{"*"},
		KeyOptions{BudgetUSDMicros: 9_000_000, BudgetUSDMicrosPerDay: 250_000})
	if err != nil {
		t.Fatalf("create after migration: %v", err)
	}
	got2, err := s.Resolve(context.Background(), plaintext2)
	if err != nil {
		t.Fatalf("resolve after migration: %v", err)
	}
	if got2.BudgetUSDMicros != 9_000_000 || got2.BudgetUSDMicrosPerDay != 250_000 {
		t.Fatalf("post-migration budget round-trip (values must not be swapped): %+v", got2)
	}
}

// TestKeys_budgetPerDayRoundTrip pins CreateWithOptions → Resolve for both
// budget columns at once, with DISTINCT values so a transposed scan argument
// fails instead of passing.
func TestKeys_budgetPerDayRoundTrip(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "roundtrip.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	plaintext, _, err := s.CreateWithOptions(context.Background(), "t", []string{"*"},
		KeyOptions{BudgetUSDMicros: 5_000_000, BudgetUSDMicrosPerDay: 250_000})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Resolve(context.Background(), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if got.BudgetUSDMicros != 5_000_000 {
		t.Fatalf("BudgetUSDMicros = %d, want 5000000", got.BudgetUSDMicros)
	}
	if got.BudgetUSDMicrosPerDay != 250_000 {
		t.Fatalf("BudgetUSDMicrosPerDay = %d, want 250000", got.BudgetUSDMicrosPerDay)
	}
}

// TestKeys_budgetPerDayZeroMeansUnlimited pins the zero-value convention: a
// key created with no options reads both budget dimensions back as 0.
func TestKeys_budgetPerDayZeroMeansUnlimited(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "zero.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	plaintext, _, err := s.CreateWithOptions(context.Background(), "t", []string{"*"}, KeyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Resolve(context.Background(), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if got.BudgetUSDMicros != 0 || got.BudgetUSDMicrosPerDay != 0 {
		t.Fatalf("zero-value key must read back 0/0 (unlimited), got %d/%d",
			got.BudgetUSDMicros, got.BudgetUSDMicrosPerDay)
	}
}

// TestEnsureKey_updatesBudgetPerDay catches a missed ON CONFLICT DO UPDATE SET
// clause, which is otherwise completely silent: two EnsureKey calls with the
// SAME plaintext and DIFFERENT per-day budgets — the second must win.
func TestEnsureKey_updatesBudgetPerDay(t *testing.T) {
	const plaintext = "ik_ensure_per_day_fixture"
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "ensure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.EnsureKey(context.Background(), plaintext, "t", []string{"*"},
		KeyOptions{BudgetUSDMicrosPerDay: 100_000}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureKey(context.Background(), plaintext, "t", []string{"*"},
		KeyOptions{BudgetUSDMicrosPerDay: 300_000}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Resolve(context.Background(), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if got.BudgetUSDMicrosPerDay != 300_000 {
		t.Fatalf("second EnsureKey must win (ON CONFLICT DO UPDATE SET): got %d, want 300000", got.BudgetUSDMicrosPerDay)
	}
}

// TestTeams_budgetPerDayRoundTrip pins UpsertTeam → GetTeam AND ListTeams —
// separate query sites through the same teamColumns/scanTeam pair — with
// distinct values so a transposed scan argument fails.
func TestTeams_budgetPerDayRoundTrip(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "teams-roundtrip.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertTeam(context.Background(), TeamRecord{
		Name:                  "acme",
		BudgetUSDMicros:       7_000_000,
		BudgetUSDMicrosPerDay: 450_000,
	}); err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.GetTeam(context.Background(), "acme")
	if err != nil || !ok {
		t.Fatalf("GetTeam: ok=%v err=%v", ok, err)
	}
	if got.BudgetUSDMicros != 7_000_000 || got.BudgetUSDMicrosPerDay != 450_000 {
		t.Fatalf("GetTeam budget round-trip (values must not be swapped): %+v", got)
	}

	list, err := s.ListTeams(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("ListTeams returned %d teams, want 1", len(list))
	}
	if list[0].BudgetUSDMicros != 7_000_000 || list[0].BudgetUSDMicrosPerDay != 450_000 {
		t.Fatalf("ListTeams budget round-trip (values must not be swapped): %+v", list[0])
	}
}

// TestTeams_migratesBudgetPerDayColumn proves a teams table that predates the
// per-day budget column — i.e. has every column through allowed_regions but
// not this one — gets the column added in place, without losing existing rows.
// It mirrors TestTeamsTable_migratesAllowedRegionsColumn's structure.
func TestTeams_migratesBudgetPerDayColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-daily-budget-teams.db")
	old, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	// Every column the current teams schema has EXCEPT budget_usd_micros_per_day.
	if _, err := old.Exec(`CREATE TABLE teams (
		name TEXT PRIMARY KEY, allowed_models TEXT NOT NULL DEFAULT '',
		rpm INTEGER NOT NULL DEFAULT 0, tpm INTEGER NOT NULL DEFAULT 0,
		tokens_per_day INTEGER NOT NULL DEFAULT 0, quota_on_exceeded TEXT NOT NULL DEFAULT '',
		budget_usd_micros INTEGER NOT NULL DEFAULT 0, budget_on_exceeded TEXT NOT NULL DEFAULT '',
		guardrail_id TEXT NOT NULL DEFAULT '', guardrail_version TEXT NOT NULL DEFAULT '',
		allowed_regions TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO teams (name, budget_usd_micros, created_at, updated_at) VALUES ('pre-existing', 6000000, 't1', 't1')`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite on a teams table predating budget_usd_micros_per_day: %v", err)
	}
	defer s.Close()

	// The pre-existing row survives the migration, its monthly budget intact,
	// and the new column defaults to 0 (unlimited).
	got, ok, err := s.GetTeam(context.Background(), "pre-existing")
	if err != nil || !ok {
		t.Fatalf("pre-existing row lost during migration: ok=%v err=%v", ok, err)
	}
	if got.BudgetUSDMicros != 6_000_000 {
		t.Fatalf("pre-existing budget_usd_micros lost during migration: %+v", got)
	}
	if got.BudgetUSDMicrosPerDay != 0 {
		t.Fatalf("migrated column must default to 0 (unlimited) for an existing row, got %d", got.BudgetUSDMicrosPerDay)
	}

	// New writes with the per-day budget work post-migration.
	if err := s.UpsertTeam(context.Background(), TeamRecord{Name: "new", BudgetUSDMicrosPerDay: 450_000}); err != nil {
		t.Fatalf("upsert after migration: %v", err)
	}
	got2, ok, err := s.GetTeam(context.Background(), "new")
	if err != nil || !ok || got2.BudgetUSDMicrosPerDay != 450_000 {
		t.Fatalf("post-migration per-day budget write/read: ok=%v err=%v got=%+v", ok, err, got2)
	}
}
