package analytics

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
)

// captureStderr returns what fn writes to os.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = old
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// The live sink path commits a console row with its categories; a row the
// store refuses leaves neither, is logged by record ID and driver code only,
// and lands on a later attempt.
func TestIngestAuditIsAtomicAndLogsRefusal(t *testing.T) {
	ix := openIndex(t)
	if _, err := ix.db.Exec(`CREATE TRIGGER audit_probe_fail BEFORE INSERT ON audit_event_categories BEGIN SELECT RAISE(ABORT, 'probe'); END`); err != nil {
		t.Fatal(err)
	}
	r := rec("01JC0000000000000000000091", "request_completed", 200, func(r *audit.Record) {
		r.Request.ModelRequested = "request-text-must-not-log"
		r.Request.Routing = &audit.RoutingRef{Masked: true, Categories: []string{"email"}}
	})
	var err error
	logged := captureStderr(t, func() { err = ix.IngestAudit(r, nil) })
	if err == nil {
		t.Fatal("a refused row reported success")
	}
	if _, ok, _ := ix.AuditEvent(r.ID); ok {
		t.Fatal("row committed without its categories")
	}
	if !strings.Contains(logged, `skipped record "01JC0000000000000000000091" (sqlite code`) ||
		strings.Contains(logged, "request-text-must-not-log") || strings.Contains(logged, "probe") {
		t.Fatalf("log = %q", logged)
	}
	if _, err := ix.db.Exec(`DROP TRIGGER audit_probe_fail`); err != nil {
		t.Fatal(err)
	}
	if err := ix.IngestAudit(r, nil); err != nil {
		t.Fatal(err)
	}
	if d, ok, err := ix.AuditEvent(r.ID); err != nil || !ok || len(d.Event.Categories) != 1 {
		t.Fatalf("retried row ok=%v err=%v categories=%v", ok, err, d.Event.Categories)
	}
}
