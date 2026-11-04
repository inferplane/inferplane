package analytics

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
)

func str(s string) *string { return &s }

func rec(id, event string, status int, mutate func(*audit.Record)) audit.Record {
	r := audit.Record{SchemaVersion: 1, Event: event, ID: id, TS: "2026-10-29T01:00:00Z",
		Principal: audit.PrincipalRef{KeyID: "ik_a", Team: "platform"}, Request: audit.RequestRef{Ingress: "anthropic", ModelRequested: "claude-sonnet-5"},
		Outcome: &audit.OutcomeRef{Status: status}}
	if mutate != nil {
		mutate(&r)
	}
	return r
}

func TestKindOf(t *testing.T) {
	cases := map[string]audit.Record{
		KindOK:           rec("1", "request_completed", 200, nil),
		KindPolicyDenied: rec("2", "request_started", 429, func(r *audit.Record) { r.Outcome.Error = str("key_rate_limited") }),
		KindPrivacyBlocked: rec("3", "request_started", 403, func(r *audit.Record) {
			r.Request.Routing = &audit.RoutingRef{Reason: "sensitive_blocked", Privacy: "block"}
		}),
		KindPrivacyMasked:   rec("4", "request_completed", 200, func(r *audit.Record) { r.Request.Routing = &audit.RoutingRef{Masked: true} }),
		KindPrivacyInternal: rec("5", "request_completed", 200, func(r *audit.Record) { r.Request.Routing = &audit.RoutingRef{Privacy: "internal_only"} }),
		KindUpstreamError:   rec("6", "request_completed", 504, nil),
		KindAutoModeBlocked: rec("7", "request_completed", 200, func(r *audit.Record) { r.AutoMode = &audit.AutoModeRef{Verdict: "block"} }),
	}
	for want, r := range cases {
		if got := KindOf(r); got != want {
			t.Fatalf("KindOf(%s) = %s, want %s", r.ID, got, want)
		}
	}
	if got := KindOf(rec("8", "request_started", 403, func(r *audit.Record) { r.Outcome.Error = str("model_forbidden") })); got != KindPolicyDenied {
		t.Fatalf("responses model_forbidden = %s", got)
	}
	if got := KindOf(rec("9", "request_started", 400, func(r *audit.Record) { r.Outcome.Error = str("context_window_exceeded") })); got != KindUpstreamError {
		t.Fatalf("context error = %s", got)
	}
	failed := audit.AttemptRef{Provider: "a", Status: "upstream_error", HTTP: 503, StartMs: 1, DurMs: 5}
	if got := KindOf(rec("10", "request_completed", 503, func(r *audit.Record) {
		r.Outcome.Error = str("overloaded")
		r.Attempts = []audit.AttemptRef{failed}
	})); got != KindUpstreamError {
		t.Fatalf("responses completed 503 = %s", got)
	}
	if got := KindOf(rec("11", "request_completed", 429, func(r *audit.Record) {
		r.Outcome.Error = str("key_rate_limited")
		r.Attempts = []audit.AttemptRef{failed}
	})); got != KindUpstreamError {
		t.Fatalf("completed 429 colliding with deny code = %s", got)
	}
	if got := KindOf(rec("12", "request_started", 409, func(r *audit.Record) {
		r.Outcome.Error = str("budget_target_changed")
		r.Attempts = []audit.AttemptRef{failed}
	})); got != KindPolicyDenied {
		t.Fatalf("denied after earlier attempt = %s", got)
	}
	okAttempt := []audit.AttemptRef{{Provider: "a", Status: audit.AttemptOK, HTTP: 200}}
	if got := KindOf(rec("14", "request_completed", 403, func(r *audit.Record) {
		r.Outcome.Error = str("model_forbidden")
		r.Attempts = okAttempt
	})); got != KindPolicyDenied {
		t.Fatalf("completed deny, last attempt ok = %s", got)
	}
	if got := KindOf(rec("15", "request_completed", 403, func(r *audit.Record) {
		r.Request.Routing = &audit.RoutingRef{Privacy: "block", Reason: "sensitive_blocked"}
		r.Attempts = okAttempt
	})); got != KindPrivacyBlocked {
		t.Fatalf("completed privacy block, last attempt ok = %s", got)
	}
	if got := KindOf(rec("13", "request_completed", 429, func(r *audit.Record) { r.Outcome.Error = str("key_rate_limited") })); got != KindPolicyDenied {
		t.Fatalf("legacy completed deny without attempts = %s", got)
	}
	// A committed stream that ended with an upstream error event, or without
	// message_stop, is written 200, not partial, last attempt stream_failed.
	streamFailed := audit.AttemptRef{Provider: "anthropic", Model: "m", Status: audit.AttemptStreamFailed, HTTP: 200}
	if got := KindOf(rec("16", "request_completed", 200, func(r *audit.Record) { r.Attempts = []audit.AttemptRef{streamFailed} })); got != KindUpstreamError {
		t.Fatalf("200 + last attempt stream_failed = %s", got)
	}
	if got := KindOf(rec("17", "request_completed", 200, func(r *audit.Record) {
		r.Request.Routing = &audit.RoutingRef{Masked: true}
		r.Attempts = []audit.AttemptRef{streamFailed}
	})); got != KindUpstreamError {
		t.Fatalf("masked stream that failed = %s (failure wins like partial)", got)
	}
	if got := KindOf(rec("18", "request_completed", 200, func(r *audit.Record) {
		r.Attempts = []audit.AttemptRef{streamFailed, okAttempt[0]}
	})); got != KindOK {
		t.Fatalf("earlier stream_failed, last ok = %s", got)
	}
	if got := KindOf(rec("19", "request_completed", 200, func(r *audit.Record) {
		r.Attempts = []audit.AttemptRef{{Provider: "a", Status: audit.AttemptCanceled, HTTP: 200}}
	})); got != KindOK {
		t.Fatalf("client-canceled stream = %s (only stream_failed is an upstream error)", got)
	}
}

func TestAuditObservable(t *testing.T) {
	if !AuditObservable(rec("1", "request_completed", 200, nil)) || !AuditObservable(rec("2", "request_started", 403, nil)) {
		t.Fatal("terminal records must be observable")
	}
	admitted := rec("3", "request_started", 0, func(r *audit.Record) { r.Outcome = nil })
	if AuditObservable(admitted) || AuditObservable(audit.Record{Event: "admin_key_created"}) {
		t.Fatal("admitted/admin records must be skipped")
	}
}

func TestWaterfallLegacyRecord(t *testing.T) {
	w := WaterfallOf(rec("1", "request_completed", 200, nil))
	if w.TotalMs != 0 || len(w.Rows) != 0 {
		t.Fatalf("legacy waterfall = %+v", w)
	}
}

func rowKinds(w Waterfall) string {
	kinds := []string{}
	for _, row := range w.Rows {
		kinds = append(kinds, row.Kind)
	}
	return strings.Join(kinds, ",")
}

func TestWaterfallAttempts(t *testing.T) {
	r := rec("1", "request_completed", 200, func(r *audit.Record) {
		r.Latency = &audit.LatencyRef{TotalMs: 1000, TTFTMs: 300}
		r.Attempts = []audit.AttemptRef{{Provider: "a", Status: "upstream_error", HTTP: 503, StartMs: 10, DurMs: 200}, {Provider: "b", Status: "ok", StartMs: 220, DurMs: 760}}
	})
	w := WaterfallOf(r)
	if rowKinds(w) != "gateway,attempt,attempt,settle" || w.TTFTMs != 300 {
		t.Fatalf("waterfall = %+v", w)
	}
	if s := w.Rows[3]; s.StartMs != 980 || s.DurMs != 20 {
		t.Fatalf("settle row = %+v", s)
	}
	denied := WaterfallOf(rec("2", "request_started", 429, func(r *audit.Record) { r.Latency = &audit.LatencyRef{TotalMs: 4} }))
	if len(denied.Rows) != 1 || denied.Rows[0].Kind != "denied" || denied.Rows[0].DurMs != 4 {
		t.Fatalf("denied waterfall = %+v", denied)
	}
}

func TestWaterfallDeniedAfterAttempt(t *testing.T) {
	r := rec("1", "request_started", 409, func(r *audit.Record) {
		r.Outcome.Error = str("budget_target_changed")
		r.Latency = &audit.LatencyRef{TotalMs: 300}
		r.Attempts = []audit.AttemptRef{{Provider: "a", Status: "upstream_error", HTTP: 503, StartMs: 10, DurMs: 200}}
	})
	w := WaterfallOf(r)
	if rowKinds(w) != "gateway,attempt,denied" {
		t.Fatalf("waterfall = %+v", w)
	}
	if d := w.Rows[2]; d.StartMs != 210 || d.DurMs != 90 || d.Status != "budget_target_changed" || d.HTTP != 409 {
		t.Fatalf("denied row = %+v", d)
	}
}

func TestWaterfallNoNaNAndNoLatency(t *testing.T) {
	r := rec("1", "request_completed", 200, func(r *audit.Record) {
		r.Attempts = []audit.AttemptRef{{Provider: "a", Status: "ok", StartMs: 1, DurMs: 2}}
	})
	if w := WaterfallOf(r); w.TotalMs != 0 || len(w.Rows) != 0 {
		t.Fatalf("no latency => no rows, got %+v", w)
	}
}

func openIndex(t *testing.T) *Index {
	t.Helper()
	ix, err := OpenSQLite(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	return ix
}

func TestReplayProjectsCompletedAndDenied(t *testing.T) {
	ix := openIndex(t)
	lines := []string{
		`{"schema_version":1,"event":"request_completed","id":"01JC0000000000000000000001","ts":"2026-10-29T01:00:00Z","instance":"i","principal":{"key_id":"ik_a","team":"platform"},"request":{"ingress":"anthropic","model_requested":"m","stream":false},"outcome":{"status":200,"fallback_used":false,"partial":false,"error":null},"trace_id":null,"prev_hash":""}`,
		`{"schema_version":1,"event":"request_started","id":"01JC0000000000000000000002","ts":"2026-10-29T01:00:01Z","instance":"i","principal":{"key_id":"ik_a","team":"platform"},"request":{"ingress":"anthropic","model_requested":"m","stream":false},"outcome":{"status":429,"fallback_used":false,"partial":false,"error":"key_rate_limited"},"trace_id":null,"prev_hash":""}`,
		`{"schema_version":1,"event":"request_started","id":"01JC0000000000000000000003","ts":"2026-10-29T01:00:02Z","instance":"i","principal":{"key_id":"ik_a","team":"platform"},"request":{"ingress":"anthropic","model_requested":"m","stream":false},"trace_id":null,"prev_hash":""}`,
		`not json`,
	}
	for range 2 { // idempotent re-ingest
		if _, err := ix.Replay(strings.NewReader(strings.Join(lines, "\n") + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	got, next, err := ix.AuditEvents(AuditFilter{Limit: 10})
	if err != nil || next != "" || len(got) != 2 {
		t.Fatalf("events = %+v next=%q err=%v", got, next, err)
	}
	if got[0].Kind != KindPolicyDenied || got[0].DenyCode != "key_rate_limited" || got[1].Kind != KindOK {
		t.Fatalf("kinds = %+v", got)
	}
	detail, ok, err := ix.AuditEvent("01JC0000000000000000000002")
	if err != nil || !ok || !strings.Contains(string(detail.Record), `"key_rate_limited"`) {
		t.Fatalf("detail = %+v ok=%v err=%v", detail, ok, err)
	}
	if _, ok, _ := ix.AuditEvent("01JC0000000000000000000009"); ok {
		t.Fatal("unknown id must not be found")
	}
	sum, err := ix.AuditSummary(AuditFilter{})
	if err != nil || sum.ByKind[KindPolicyDenied] != 1 || sum.ByKind[KindOK] != 1 || len(sum.Denials) != 1 || sum.CoverageStartMs == 0 {
		t.Fatalf("summary = %+v err=%v", sum, err)
	}
}

func TestIngestAuditFiltersAndSessions(t *testing.T) {
	ix := openIndex(t)
	mk := func(id, kind string, mutate func(*audit.Record)) {
		r := rec(id, "request_completed", 200, mutate)
		if err := ix.IngestAudit(r, nil); err != nil {
			t.Fatal(err)
		}
	}
	mk("01JC0000000000000000000011", "", func(r *audit.Record) {
		r.Client = &audit.ClientRef{RequestClass: "main", SessionRef: "s1:" + strings.Repeat("a", 32)}
		r.Request.Routing = &audit.RoutingRef{Masked: true, Categories: []string{"email", "BAD CATEGORY"}}
	})
	mk("01JC0000000000000000000012", "", func(r *audit.Record) {
		r.Client = &audit.ClientRef{RequestClass: "subagent", SessionRef: "s1:" + strings.Repeat("a", 32)}
	})
	mk("01JC0000000000000000000013", "", func(r *audit.Record) { r.Principal.KeyID = "ik_b" })
	pii, _, err := ix.AuditEvents(AuditFilter{PII: true, Limit: 10})
	if err != nil || len(pii) != 1 || strings.Join(pii[0].Categories, ",") != "email" {
		t.Fatalf("pii = %+v err=%v", pii, err)
	}
	byCat, _, _ := ix.AuditEvents(AuditFilter{Category: "email", Limit: 10})
	byKey, _, _ := ix.AuditEvents(AuditFilter{KeyID: "ik_b", Limit: 10})
	if len(byCat) != 1 || len(byKey) != 1 {
		t.Fatalf("category=%d key=%d", len(byCat), len(byKey))
	}
	page, next, _ := ix.AuditEvents(AuditFilter{Limit: 2})
	rest, _, _ := ix.AuditEvents(AuditFilter{Limit: 2, Before: next})
	if len(page) != 2 || next == "" || len(rest) != 1 {
		t.Fatalf("paging: %d next=%q rest=%d", len(page), next, len(rest))
	}
	sessions, _, err := ix.AuditSessions(AuditFilter{Limit: 10})
	if err != nil || len(sessions) != 1 || sessions[0].Requests != 2 || sessions[0].Subagents != 1 || sessions[0].PII != 1 {
		t.Fatalf("sessions = %+v err=%v", sessions, err)
	}
	inSession, _, err := ix.AuditSessionEvents(sessions[0].SessionRef, AuditFilter{Limit: 10})
	if err != nil || len(inSession) != 2 || inSession[0].ID > inSession[1].ID {
		t.Fatalf("session events = %+v err=%v", inSession, err)
	}
}

func TestAuditModelFilter(t *testing.T) {
	ix := openIndex(t)
	put := func(id, team, requested, actual string) {
		r := rec(id, "request_completed", 200, func(r *audit.Record) {
			r.Principal.Team = team
			r.Request.ModelRequested = requested
			r.Request.Routing = &audit.RoutingRef{RequestedModel: requested, ActualModel: actual}
		})
		if err := ix.IngestAudit(r, nil); err != nil {
			t.Fatal(err)
		}
	}
	put("01JC0000000000000000000021", "platform", "sonnet", "glm")
	put("01JC0000000000000000000022", "data", "opus", "sonnet")
	put("01JC0000000000000000000023", "platform", "opus", "opus")

	byReq, _, err := ix.AuditEvents(AuditFilter{Model: "sonnet", Limit: 10})
	if err != nil || len(byReq) != 2 { // 21 by requested, 22 by actual
		t.Fatalf("model sonnet = %+v err=%v", byReq, err)
	}
	byAct, _, err := ix.AuditEvents(AuditFilter{Model: "glm", Limit: 10})
	if err != nil || len(byAct) != 1 || byAct[0].ID != "01JC0000000000000000000021" {
		t.Fatalf("model glm = %+v err=%v", byAct, err)
	}
	combo, _, err := ix.AuditEvents(AuditFilter{Model: "sonnet", Team: "data", Limit: 10})
	if err != nil || len(combo) != 1 || combo[0].ID != "01JC0000000000000000000022" {
		t.Fatalf("model+team = %+v err=%v", combo, err)
	}
	sum, err := ix.AuditSummary(AuditFilter{Model: "opus"})
	if err != nil || sum.ByKind[KindOK] != 2 {
		t.Fatalf("summary model = %+v err=%v", sum, err)
	}
}

// routing.reason is a deny code only for gateway decisions; on a completed
// upstream error it is the routing outcome ("unchanged") and must not surface
// as a denial.
func TestDenyCodeOnlyFromGatewayDecisions(t *testing.T) {
	upstream := rec("01JC0000000000000000000071", "request_completed", 529, func(r *audit.Record) {
		r.Request.Routing = &audit.RoutingRef{Reason: "unchanged"}
		r.Attempts = []audit.AttemptRef{{Provider: "anthropic", Status: audit.AttemptUpstreamError, HTTP: 529}}
	})
	row, _ := ProjectAuditEvent(upstream, nil)
	if row.DenyCode != "" || row.Kind != KindUpstreamError || row.RoutingReason != "unchanged" {
		t.Fatalf("completed 529: deny_code=%q kind=%s routing_reason=%q", row.DenyCode, row.Kind, row.RoutingReason)
	}
	denied := rec("01JC0000000000000000000072", "request_started", 403, func(r *audit.Record) {
		r.Request.Routing = &audit.RoutingRef{Reason: "no_safe_route"}
	})
	row, _ = ProjectAuditEvent(denied, nil)
	if row.DenyCode != "no_safe_route" || row.Kind != KindPolicyDenied {
		t.Fatalf("gateway decision: deny_code=%q kind=%s", row.DenyCode, row.Kind)
	}
}
