package pgstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/analytics"
	"github.com/inferplane/inferplane/internal/audit"
)

// Recreate only derived analytics tables in the disposable test database with
// the pre-projection schema. Legacy status and price cannot attest evidence.
func legacyProjectionStore(t *testing.T) *Store {
	t.Helper()
	s := &Store{db: rawPool(t)}
	_, err := s.db.Exec(context.Background(), `DROP TABLE IF EXISTS events, rollup_day, analytics_schema_migrations;
 CREATE TABLE events (
 id TEXT PRIMARY KEY, day TEXT NOT NULL DEFAULT '', team TEXT NOT NULL DEFAULT '',
 model TEXT NOT NULL DEFAULT '', provider TEXT NOT NULL DEFAULT '', status INTEGER NOT NULL DEFAULT 0,
 input_tokens BIGINT NOT NULL DEFAULT 0, output_tokens BIGINT NOT NULL DEFAULT 0,
 cache_read BIGINT NOT NULL DEFAULT 0, cache_creation BIGINT NOT NULL DEFAULT 0,
 cost_micros BIGINT NOT NULL DEFAULT 0, ts TEXT NOT NULL DEFAULT '', body_ref TEXT NOT NULL DEFAULT '');
 CREATE TABLE rollup_day (
 day TEXT NOT NULL, team TEXT NOT NULL, model TEXT NOT NULL,
 input_tokens BIGINT NOT NULL DEFAULT 0, output_tokens BIGINT NOT NULL DEFAULT 0,
 cache_read BIGINT NOT NULL DEFAULT 0, cache_creation BIGINT NOT NULL DEFAULT 0,
 cost_micros BIGINT NOT NULL DEFAULT 0, request_count BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(day,team,model));
 INSERT INTO events(id,day,team,model,status,input_tokens,output_tokens,cache_read,cache_creation,cost_micros,ts,body_ref)
 VALUES ('legacy','2026-07-07','alpha','m1',200,10,5,2,3,500,'2026-07-07T10:00:00Z','legacy-body');
 INSERT INTO rollup_day(day,team,model,input_tokens,output_tokens,cache_read,cache_creation,cost_micros,request_count)
 VALUES ('2026-07-07','alpha','m1',10,5,2,3,500,1);`)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPGProjectionMigrationPreservesLegacyTotals(t *testing.T) {
	s := legacyProjectionStore(t)
	ctx := context.Background()
	want := analytics.OperationTotals{Completed: 1, Unknown: 1, LegacyUnknownRequests: 1, UncertainRecordedCostMicros: 500, LegacyProjectionRecords: 1}
	for migration := 0; migration < 2; migration++ {
		if err := s.ensureSchema(ctx); err != nil {
			t.Fatal(err)
		}
		sum, err := s.Summary(analytics.SummaryQuery{})
		if err != nil {
			t.Fatal(err)
		}
		if sum.Totals.Operations != want || sum.Totals.Requests != 1 || sum.Totals.CostMicros != 500 || sum.Totals.InputTokens != 10 || sum.Totals.OutputTokens != 5 || sum.Totals.CacheReadTokens != 2 || sum.Totals.CacheCreationTokens != 3 {
			t.Fatalf("migration %d totals=%+v", migration, sum.Totals)
		}
		if len(sum.ByTeam) != 1 || sum.ByTeam[0].Operations != want || len(sum.ByModel) != 1 || sum.ByModel[0].Operations != want {
			t.Fatalf("migration %d groups=%+v", migration, sum)
		}
		recent, err := s.Recent(2, "")
		if err != nil || len(recent) != 1 {
			t.Fatalf("Recent=%+v err=%v", recent, err)
		}
		e := recent[0]
		if !e.HasCost || e.ProjectionVersion != 0 || e.OutcomeState != "unknown" || e.CostState != "legacy_unknown" || e.Partial || e.ErrorCode != "" || e.BodyRef != "legacy-body" || e.Status != 200 {
			t.Fatalf("legacy event=%+v", e)
		}
	}
	// Replaying actual source evidence upgrades the same row without duplication.
	if _, err := s.db.Exec(ctx, `DELETE FROM lease`); err != nil {
		t.Fatal(err)
	}
	epoch := pgTestLease(t, s)
	r := pgObservableFixture("2026-07-07T10:00:00Z")[0]
	r.ID, r.Principal.Team, r.Request.ModelResolved = "legacy", "alpha", "m1"
	if err := s.ingestBatch(ctx, "evidence", epoch, []audit.Record{r}, nil); err != nil {
		t.Fatal(err)
	}
	sum, err := s.Summary(analytics.SummaryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	want = analytics.OperationTotals{Completed: 1, Succeeded: 1, EstimatedRequests: 1, EstimatedCostMicros: 1000}
	if sum.Totals.Operations != want || sum.Totals.Requests != 1 || sum.Totals.CostMicros != 1000 {
		t.Fatalf("replayed totals=%+v", sum.Totals)
	}
	// A repeated migration must preserve evidence already projected at version 1.
	if err := s.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	sum, err = s.Summary(analytics.SummaryQuery{})
	if err != nil || sum.Totals.Operations != want {
		t.Fatalf("repeated migration totals=%+v err=%v", sum.Totals, err)
	}
}

// Opening current schemas must not rewrite a derived row, even when replicas
// start together. xmin exposes a replacement that identical totals would hide.
func TestPGCurrentSchemaOpenDoesNotRewriteRollups(t *testing.T) {
	s := legacyProjectionStore(t)
	ctx := context.Background()
	if err := s.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := s.db.QueryRow(ctx, `SELECT xmin::text FROM rollup_day`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	dsn := testDSN(t)
	for round := 0; round < 2; round++ {
		results := make(chan error, 3)
		for replica := 0; replica < 3; replica++ {
			go func() {
				opened, err := New(ctx, dsn)
				if opened != nil {
					opened.Close()
				}
				results <- err
			}()
		}
		for replica := 0; replica < 3; replica++ {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		var after string
		if err := s.db.QueryRow(ctx, `SELECT xmin::text FROM rollup_day`).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after != before {
			t.Fatalf("current-schema open rewrote rollup: xmin %s -> %s", before, after)
		}
	}
}

func TestPGProjectionMigrationRollsBackColumnsAndBackfill(t *testing.T) {
	s := legacyProjectionStore(t)
	ctx := context.Background()
	t.Cleanup(func() {
		if _, err := s.db.Exec(context.Background(), `ALTER TABLE rollup_day DROP CONSTRAINT IF EXISTS reject_backfill`); err != nil {
			t.Errorf("cleanup backfill rejection constraint: %v", err)
		}
	})
	// Force a backfill failure after ALTER TABLE succeeds. Neither the new
	// columns nor a cleared/partially regenerated rollup may become visible.
	if _, err := s.db.Exec(ctx, `ALTER TABLE rollup_day ADD CONSTRAINT reject_backfill CHECK(request_count < 2); INSERT INTO events(id,day,team,model,cost_micros) VALUES ('second','2026-07-07','alpha','m1',300)`); err != nil {
		t.Fatal(err)
	}
	err := s.ensureSchema(ctx)
	if err == nil {
		t.Fatal("migration must fail when regenerated rollup violates constraint")
	}
	var markerAbsent bool
	if err := s.db.QueryRow(ctx, `SELECT to_regclass('analytics_schema_migrations') IS NULL`).Scan(&markerAbsent); err != nil {
		t.Fatal(err)
	}
	if !markerAbsent {
		t.Fatal("failed first migration must roll back its durable marker table")
	}
	var columns int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND ((table_name='events' AND column_name IN ('projection_version','has_cost','outcome_state','cost_state','partial','error_code')) OR (table_name='rollup_day' AND column_name='completed'))`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 0 {
		t.Fatalf("migration left %d additive columns after rollback", columns)
	}
	var cost, count int64
	if err := s.db.QueryRow(ctx, `SELECT cost_micros,request_count FROM rollup_day`).Scan(&cost, &count); err != nil {
		t.Fatal(err)
	}
	if cost != 500 || count != 1 {
		t.Fatalf("rolled-back rollup cost=%d count=%d", cost, count)
	}
	var timeout string
	if err := s.db.QueryRow(ctx, `SHOW statement_timeout`).Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != "10s" {
		t.Fatalf("query timeout after failed migration=%q, want 10s", timeout)
	}
	if _, err := s.db.Exec(ctx, `ALTER TABLE rollup_day DROP CONSTRAINT reject_backfill`); err != nil {
		t.Fatal(err)
	}
	if err := s.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	sum, err := s.Summary(analytics.SummaryQuery{})
	if err != nil || sum.Totals.Requests != 2 || sum.Totals.CostMicros != 800 || sum.Totals.Operations.Completed != 2 {
		t.Fatalf("retry migration=%+v err=%v", sum.Totals, err)
	}
	var markers int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM analytics_schema_migrations WHERE version=1`).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if markers != 1 {
		t.Fatalf("successful retry markers=%d, want 1", markers)
	}
}

func TestPGCorrectionRecomputesBothKeys(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	epoch := pgTestLease(t, s)
	r := pgObservableFixture("2026-07-07T10:00:00Z")[0]
	r.Principal.Team, r.Request.ModelResolved = "old-team", "old-model"
	if err := s.ingestBatch(ctx, "evidence", epoch, []audit.Record{r}, map[string]int64{"segment": 100}); err != nil {
		t.Fatal(err)
	}
	r.TS, r.Principal.Team, r.Request.ModelResolved = "2026-07-08T10:00:00Z", "new-team", "new-model"
	r.Cost.AmountUSDMicros = 2000
	r.Outcome.Status = 500
	if err := s.ingestBatch(ctx, "evidence", epoch, []audit.Record{r}, map[string]int64{"segment": 200}); err != nil {
		t.Fatal(err)
	}
	sum, err := s.Summary(analytics.SummaryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	want := analytics.OperationTotals{Completed: 1, Failed: 1, EstimatedRequests: 1, EstimatedCostMicros: 2000}
	if sum.Totals.Requests != 1 || sum.Totals.CostMicros != 2000 || sum.Totals.Operations != want {
		t.Fatalf("correction totals=%+v, want exactly one corrected event", sum.Totals)
	}
	if len(sum.ByTeam) != 1 || sum.ByTeam[0].Team != "new-team" || sum.ByTeam[0].Operations != want || len(sum.ByModel) != 1 || sum.ByModel[0].Model != "new-model" || sum.ByModel[0].Operations != want {
		t.Fatalf("correction groups=%+v", sum)
	}
	old, err := s.Summary(analytics.SummaryQuery{SinceDay: "2026-07-07", UntilDay: "2026-07-07"})
	if err != nil || old.Totals.Requests != 0 || old.Totals.Operations.Completed != 0 || len(old.ByTeam) != 0 || len(old.ByModel) != 0 {
		t.Fatalf("old group=%+v err=%v", old, err)
	}
	var rows int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM rollup_day`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rollup rows=%d err=%v", rows, err)
	}
	// Correcting an existing cost record to missing cost clears financial totals.
	r.Cost = nil
	if err := s.ingestBatch(ctx, "evidence", epoch, []audit.Record{r}, nil); err != nil {
		t.Fatal(err)
	}
	sum, err = s.Summary(analytics.SummaryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	want = analytics.OperationTotals{Completed: 1, Failed: 1, UnavailableRequests: 1}
	if sum.Totals.Requests != 0 || sum.Totals.CostMicros != 0 || sum.Totals.Operations != want || sum.Totals.InputTokens != 0 {
		t.Fatalf("costless correction=%+v", sum.Totals)
	}
	// Stale corrections must leave the current projection, keys and checkpoint intact.
	if err := s.ingestBatch(ctx, "evidence", epoch-1, []audit.Record{pgObservableFixture(r.TS)[0]}, map[string]int64{"segment": 999}); !errors.Is(err, errFenced) {
		t.Fatalf("stale correction=%v", err)
	}
	after, err := s.Summary(analytics.SummaryQuery{})
	if err != nil || after.Totals != sum.Totals {
		t.Fatalf("stale correction changed totals=%+v err=%v", after.Totals, err)
	}
	var offset int64
	if err := s.db.QueryRow(ctx, `SELECT byte_offset FROM checkpoints WHERE segment='segment'`).Scan(&offset); err != nil || offset != 200 {
		t.Fatalf("stale checkpoint=%d err=%v", offset, err)
	}
}

func TestPGRebuildRecoversCostlessHistory(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	ag := newTestAggregator(t, s, dir)
	records := pgObservableFixture(time.Now().UTC().Format(time.RFC3339Nano))
	content := ""
	for _, r := range []audit.Record{records[0], records[1]} {
		b, err := r.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		content += string(b) + "\n"
	}
	writeFile(t, dir, "history.jsonl", content)
	epoch, ok, err := tryAcquireLease(ctx, s.db, ag.instanceID, 15*time.Second)
	if err != nil || !ok {
		t.Fatalf("lease=%v err=%v", ok, err)
	}
	// Simulate the old writer: checkpoint past both records, only cost stored.
	if err := s.ingestBatch(ctx, ag.instanceID, epoch, []audit.Record{records[0]}, map[string]int64{"history.jsonl": int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if err := s.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ag.tick(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := s.Summary(analytics.SummaryQuery{})
	if err != nil || before.Totals.Operations.Completed != 1 {
		t.Fatalf("migration should not replay skipped source=%+v err=%v", before.Totals, err)
	}
	if err := s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.ingestBatch(ctx, ag.instanceID, epoch, records, map[string]int64{"history.jsonl": int64(len(content))}); !errors.Is(err, errFenced) {
		t.Fatalf("stale ingest after rebuild=%v", err)
	}
	empty, err := s.Summary(analytics.SummaryQuery{})
	if err != nil || empty.Totals.Operations.Completed != 0 {
		t.Fatalf("stale projection=%+v err=%v", empty.Totals, err)
	}
	if err := ag.tick(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := s.Summary(analytics.SummaryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	want := analytics.OperationTotals{Completed: 2, Succeeded: 1, Failed: 1, EstimatedRequests: 1, UnavailableRequests: 1, EstimatedCostMicros: 1000}
	if after.Totals.Operations != want || after.Totals.Requests != 1 || after.Totals.CostMicros != 1000 {
		t.Fatalf("rebuilt totals=%+v", after.Totals)
	}
	if err := ag.tick(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := s.Summary(analytics.SummaryQuery{})
	if err != nil || again.Totals != after.Totals {
		t.Fatalf("repeat tick=%+v err=%v", again.Totals, err)
	}
}
