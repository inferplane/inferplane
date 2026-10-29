package pgstore

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"os"
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

// A console row Postgres rejects as data (SQLSTATE class 22) fails the batch
// savepoint; the row-by-row retry skips only that row, and usage rows and the
// checkpoint still commit.
func TestPgstoreAuditProjectionFailureKeepsUsage(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	cleanup := func() {
		_, _ = s.db.Exec(context.Background(), `DROP TRIGGER IF EXISTS audit_probe_fail ON audit_event_categories; DROP FUNCTION IF EXISTS audit_probe_fail()`)
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := s.db.Exec(ctx, `CREATE FUNCTION audit_probe_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'probe' USING ERRCODE = '22023'; END $$;
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

// stderrOf returns what fn writes to os.Stderr (the console skip log).
func stderrOf(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()
	out := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		out <- string(b)
	}()
	fn()
	os.Stderr = old
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return <-out
}

// A console row over a Postgres program limit (SQLSTATE class 54, here a
// record ID whose primary-key entry exceeds the 2704-byte btree maximum)
// cannot succeed on retry either: it is skipped like a data error, and the
// tick's other console rows, usage rows and checkpoint commit. The good row
// before it shares the failed batch savepoint and is written again row by row.
func TestPgstoreOversizedConsoleRowIsSkipped(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	epoch := acquire(t, s)
	// Incompressible text, so the index entry cannot shrink under the limit.
	var raw []byte
	for i := 0; len(raw) < 3072; i++ {
		sum := sha256.Sum256([]byte{byte(i), byte(i >> 8)})
		raw = append(raw, sum[:]...)
	}
	huge := base64.RawStdEncoding.EncodeToString(raw)
	denied := func(r *audit.Record) { r.Outcome.Error = auditStr("key_rate_limited") }
	good := auditRec("01JC0000000000000000000061", "request_started", 429, denied)
	oversized := auditRec(huge, "request_started", 429, denied)
	usage := auditRec("01JC0000000000000000000062", "request_completed", 200, nil)
	var err error
	logged := stderrOf(t, func() {
		err = s.ingestBatch(ctx, "holder", epoch, []audit.Record{good, oversized, usage}, map[string]int64{"seg": 40})
	})
	if err != nil {
		t.Fatalf("ingestBatch: %v", err)
	}
	if n := auditCount(t, s, "events"); n != 1 {
		t.Fatalf("events rows = %d, want 1", n)
	}
	if off := checkpointOf(t, s, "seg"); off != 40 {
		t.Fatalf("checkpoint = %d, want 40", off)
	}
	if n := auditCount(t, s, "audit_events"); n != 2 {
		t.Fatalf("console rows = %d, want 2", n)
	}
	if _, ok, err := s.AuditEvent(huge); err != nil || ok {
		t.Fatalf("oversized console row ok=%v err=%v", ok, err)
	}
	for _, id := range []string{good.ID, usage.ID} {
		if _, ok, err := s.AuditEvent(id); err != nil || !ok {
			t.Fatalf("console row %s ok=%v err=%v", id, ok, err)
		}
	}
	if !strings.Contains(logged, "(SQLSTATE 54000)") {
		t.Fatalf("skip log = %q", logged)
	}
}
