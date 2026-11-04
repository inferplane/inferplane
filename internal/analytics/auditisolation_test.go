package analytics

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/inferplane/inferplane/internal/audit"
)

// cleanText reports whether s can be stored in Postgres TEXT and matched by a
// console filter: no NUL, valid UTF-8, at most the filter cap.
func cleanText(s string) bool {
	return !strings.Contains(s, "\x00") && utf8.ValidString(s) && len(s) <= 256 // the console API filter cap
}

func TestProjectAuditEventSanitisesClientText(t *testing.T) {
	long := "x" + strings.Repeat("é", 200) // 401 bytes; the cap falls inside a rune
	r := rec("01JC0000000000000000000041", "request_completed", 200, func(r *audit.Record) {
		r.Request.ModelRequested = "a\x00b\xffc"
		r.Request.Provider = long
		r.Request.Routing = &audit.RoutingRef{PlannedBoundary: "ext\x00", ActualBoundary: long,
			Policies: []audit.RoutingPolicyRef{{Name: "p\x00", Generation: 1, Rule: "r"}}}
		r.TraceID = str("t\x00")
		r.AutoMode = &audit.AutoModeRef{Verdict: "allow", Signature: "sig\xfe"}
		r.Client = &audit.ClientRef{RequestClass: "main\x00", SessionRef: "s1:\x00"}
	})
	row, ok := ProjectAuditEvent(r, nil)
	if !ok {
		t.Fatal("not projected")
	}
	if row.ModelRequested != "ab�c" || row.ModelActual != "ab�c" {
		t.Fatalf("model_requested=%q model_actual=%q", row.ModelRequested, row.ModelActual)
	}
	if want := "x" + strings.Repeat("é", 127); row.Provider != want || row.ActualBoundary != want {
		t.Fatalf("capped provider=%q (%d bytes)", row.Provider, len(row.Provider))
	}
	for name, v := range map[string]string{"model_requested": row.ModelRequested, "model_actual": row.ModelActual,
		"provider": row.Provider, "policy_ref": row.PolicyRef, "planned_boundary": row.PlannedBoundary,
		"actual_boundary": row.ActualBoundary, "trace_id": row.TraceID, "auto_signature": row.AutoSignature,
		"request_class": row.RequestClass, "session_ref": row.SessionRef} {
		if !cleanText(v) {
			t.Errorf("%s not sanitised: %q", name, v)
		}
	}
	// record_json stays the encoder's bytes: NUL is the six ASCII bytes \u0000.
	if !strings.Contains(row.RecordJSON, `a\u0000b`) || strings.Contains(row.RecordJSON, "\x00") {
		t.Fatalf("record_json = %s", row.RecordJSON)
	}
}

func jsonLine(t *testing.T, r audit.Record) string {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func countRows(t *testing.T, ix *Index, table string) int {
	t.Helper()
	var n int
	if err := ix.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The reviewer's pair: a NUL-model denial next to a valid completed record.
func TestReplayNULModelDenialKeepsUsage(t *testing.T) {
	ix := openIndex(t)
	bad := rec("01JC00000000000000000000ZZ", "request_started", 404, func(r *audit.Record) { r.Request.ModelRequested = "a\x00b" })
	good := rec("01JC00000000000000000000ZY", "request_completed", 200, func(r *audit.Record) {
		r.Cost = &audit.CostRef{AmountUSDMicros: 7}
	})
	if _, err := ix.Replay(strings.NewReader(jsonLine(t, bad) + jsonLine(t, good))); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if n := countRows(t, ix, "events"); n != 1 {
		t.Fatalf("events rows = %d, want 1", n)
	}
	d, ok, err := ix.AuditEvent(bad.ID)
	if err != nil || !ok || d.Event.ModelRequested != "ab" {
		t.Fatalf("denial row ok=%v err=%v model=%q", ok, err, d.Event.ModelRequested)
	}
}

// A console projection failure must never roll back the usage projection.
func TestReplayIsolatesAuditProjectionFailure(t *testing.T) {
	ix := openIndex(t)
	if _, err := ix.db.Exec(`DROP TABLE audit_event_categories`); err != nil {
		t.Fatal(err)
	}
	categorised := rec("01JC0000000000000000000051", "request_completed", 200, func(r *audit.Record) {
		r.Request.Routing = &audit.RoutingRef{Masked: true, Categories: []string{"email"}}
		r.Cost = &audit.CostRef{AmountUSDMicros: 3}
	})
	plain := rec("01JC0000000000000000000052", "request_completed", 200, func(r *audit.Record) {
		r.Cost = &audit.CostRef{AmountUSDMicros: 4}
	})
	n, err := ix.Replay(strings.NewReader(jsonLine(t, categorised) + jsonLine(t, plain)))
	if err != nil || n != 2 {
		t.Fatalf("replay n=%d err=%v", n, err)
	}
	if got := countRows(t, ix, "events"); got != 2 {
		t.Fatalf("events rows = %d, want 2", got)
	}
	if _, err := ix.db.Exec(AuditEventsDDL); err != nil { // restore the table so detail reads work
		t.Fatal(err)
	}
	// The failed record's half-written console row is rolled back with its savepoint.
	if _, ok, _ := ix.AuditEvent(categorised.ID); ok {
		t.Fatal("failed projection left a partial console row")
	}
	if _, ok, err := ix.AuditEvent(plain.ID); err != nil || !ok {
		t.Fatalf("plain console row ok=%v err=%v", ok, err)
	}
}

// The live sink ingests usage and the console row independently; a console
// failure cannot block usage there either.
func TestSinkAuditProjectionFailureKeepsUsage(t *testing.T) {
	ix := openIndex(t)
	if _, err := ix.db.Exec(`DROP TABLE audit_event_categories; DROP TABLE audit_events`); err != nil {
		t.Fatal(err)
	}
	s := NewSink(ix)
	r := rec("01JC0000000000000000000061", "request_completed", 200, func(r *audit.Record) { r.Cost = &audit.CostRef{AmountUSDMicros: 1} })
	_ = s.Write([]byte(jsonLine(t, r)))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, ix, "events"); got != 1 {
		t.Fatalf("events rows = %d, want 1", got)
	}
}
