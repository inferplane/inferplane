package pgstore

import (
	"context"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
)

func checkpointOf(t *testing.T, s *Store, segment string) int64 {
	t.Helper()
	var off int64
	if err := s.db.QueryRow(context.Background(), `SELECT byte_offset FROM checkpoints WHERE segment=$1`, segment).Scan(&off); err != nil {
		t.Fatalf("checkpoint %s: %v", segment, err)
	}
	return off
}

// The reviewer's probe: a denial whose client model carries a NUL byte, in the
// same batch as a valid completed record.
func TestPgstoreNULModelDenialKeepsUsage(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	epoch := acquire(t, s)
	bad := auditRec("01JC00000000000000000000ZZ", "request_started", 404, func(r *audit.Record) { r.Request.ModelRequested = "a\x00b" })
	good := auditRec("01JC00000000000000000000ZY", "request_completed", 200, nil)
	if err := s.ingestBatch(ctx, "holder", epoch, []audit.Record{bad, good}, map[string]int64{"seg": 10}); err != nil {
		t.Fatalf("ingestBatch: %v", err)
	}
	if n := auditCount(t, s, "events"); n != 1 {
		t.Fatalf("events rows = %d, want 1", n)
	}
	if off := checkpointOf(t, s, "seg"); off != 10 {
		t.Fatalf("checkpoint = %d, want 10", off)
	}
	d, ok, err := s.AuditEvent(bad.ID)
	if err != nil || !ok || d.Event.ModelRequested != "ab" {
		t.Fatalf("denial row ok=%v err=%v model=%q", ok, err, d.Event.ModelRequested)
	}
	// Postgres TEXT holds the JSON-escaped NUL (six ASCII bytes) verbatim.
	if !strings.Contains(string(d.Record), `a\u0000b`) {
		t.Fatalf("record = %s", d.Record)
	}
}

// Any console insert failure rolls back to its savepoint; usage rows and the
// checkpoint still commit.
func TestPgstoreAuditProjectionFailureKeepsUsage(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	cleanup := func() {
		_, _ = s.db.Exec(context.Background(), `DROP TRIGGER IF EXISTS audit_probe_fail ON audit_event_categories; DROP FUNCTION IF EXISTS audit_probe_fail()`)
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := s.db.Exec(ctx, `CREATE FUNCTION audit_probe_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'probe'; END $$;
CREATE TRIGGER audit_probe_fail BEFORE INSERT ON audit_event_categories FOR EACH ROW EXECUTE FUNCTION audit_probe_fail()`); err != nil {
		t.Fatal(err)
	}
	epoch := acquire(t, s)
	categorised := auditRec("01JC0000000000000000000051", "request_completed", 200, func(r *audit.Record) {
		r.Request.Routing = &audit.RoutingRef{Masked: true, Categories: []string{"email"}}
	})
	plain := auditRec("01JC0000000000000000000052", "request_completed", 200, nil)
	if err := s.ingestBatch(ctx, "holder", epoch, []audit.Record{categorised, plain}, map[string]int64{"seg": 20}); err != nil {
		t.Fatalf("ingestBatch: %v", err)
	}
	if n := auditCount(t, s, "events"); n != 2 {
		t.Fatalf("events rows = %d, want 2", n)
	}
	if off := checkpointOf(t, s, "seg"); off != 20 {
		t.Fatalf("checkpoint = %d, want 20", off)
	}
	if _, ok, _ := s.AuditEvent(categorised.ID); ok {
		t.Fatal("failed projection left a partial console row")
	}
	if _, ok, err := s.AuditEvent(plain.ID); err != nil || !ok {
		t.Fatalf("plain console row ok=%v err=%v", ok, err)
	}
}
