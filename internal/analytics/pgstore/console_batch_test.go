package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inferplane/inferplane/internal/audit"
)

// consoleBatch is n completed records with distinct ULIDs.
func consoleBatch(n int) []audit.Record {
	out := make([]audit.Record, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, auditRec(fmt.Sprintf("01JC00000000000000000007%02d", i), "request_completed", 200, nil))
	}
	return out
}

// A tick of 70 console rows holds at most one subtransaction. The checkpoint
// trigger pauses the open transaction so a second session can read the
// ingesting backend's subtransaction cache (pg_stat_get_backend_subxact, PG 16+).
func TestPgstoreConsoleBatchHoldsOneSubtransaction(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	cleanup := func() {
		_, _ = s.db.Exec(context.Background(), `DROP TRIGGER IF EXISTS audit_probe_pause ON checkpoints; DROP FUNCTION IF EXISTS audit_probe_pause()`)
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := s.db.Exec(ctx, `CREATE FUNCTION audit_probe_pause() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(2); RETURN NEW; END $$;
CREATE TRIGGER audit_probe_pause AFTER INSERT ON checkpoints FOR EACH ROW EXECUTE FUNCTION audit_probe_pause()`); err != nil {
		t.Fatal(err)
	}
	epoch := acquire(t, s)
	done := make(chan error, 1)
	go func() { done <- s.ingestBatch(ctx, "holder", epoch, consoleBatch(70), map[string]int64{"seg": 1}) }()
	probe := rawPool(t)
	var count int
	var overflowed bool
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := probe.QueryRow(ctx, `SELECT s.subxact_count, s.subxact_overflowed
			FROM pg_stat_get_backend_idset() AS b(id), LATERAL pg_stat_get_backend_subxact(b.id) AS s
			WHERE pg_stat_get_backend_pid(b.id) = (SELECT pid FROM pg_stat_activity
				WHERE datname = current_database() AND wait_event = 'PgSleep' AND query LIKE 'INSERT INTO checkpoints%' LIMIT 1)`).Scan(&count, &overflowed)
		if err == nil {
			break
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("ingest never paused in the checkpoint trigger")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if count > 1 || overflowed {
		t.Fatalf("one tick held %d subtransactions (overflowed=%v), want at most one", count, overflowed)
	}
	if n := auditCount(t, s, "audit_events"); n != 70 {
		t.Fatalf("console rows = %d, want 70", n)
	}
}

// A console failure that is not a data error aborts the tick: nothing
// commits, the checkpoint stays, the error names only the SQLSTATE, and the
// next tick ingests the same records.
func TestPgstoreConsoleTransientErrorAbortsTick(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	cleanup := func() {
		_, _ = s.db.Exec(context.Background(), `DROP TRIGGER IF EXISTS audit_probe_deadlock ON audit_events; DROP FUNCTION IF EXISTS audit_probe_deadlock()`)
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := s.db.Exec(ctx, `CREATE FUNCTION audit_probe_deadlock() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'probe %', NEW.model_requested USING ERRCODE = '40P01'; END $$;
CREATE TRIGGER audit_probe_deadlock BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION audit_probe_deadlock()`); err != nil {
		t.Fatal(err)
	}
	epoch := acquire(t, s)
	batch := []audit.Record{
		auditRec("01JC0000000000000000000081", "request_completed", 200, func(r *audit.Record) { r.Request.ModelRequested = "request-text-must-not-log" }),
		auditRec("01JC0000000000000000000082", "request_started", 429, func(r *audit.Record) { r.Outcome.Error = auditStr("key_rate_limited") }),
	}
	err := s.ingestBatch(ctx, "holder", epoch, batch, map[string]int64{"seg": 30})
	if err == nil || !strings.Contains(err.Error(), "SQLSTATE 40P01") || strings.Contains(err.Error(), "request-text-must-not-log") {
		t.Fatalf("transient console error = %v", err)
	}
	if n := auditCount(t, s, "events"); n != 0 {
		t.Fatalf("aborted tick committed %d usage rows", n)
	}
	if n := auditCount(t, s, "checkpoints"); n != 0 {
		t.Fatalf("aborted tick advanced %d checkpoints", n)
	}
	cleanup()
	if err := s.ingestBatch(ctx, "holder", epoch, batch, map[string]int64{"seg": 30}); err != nil {
		t.Fatalf("retry tick: %v", err)
	}
	if n := auditCount(t, s, "audit_events"); n != 2 || checkpointOf(t, s, "seg") != 30 {
		t.Fatalf("retry rows = %d checkpoint = %d", n, checkpointOf(t, s, "seg"))
	}
}
