package auditconsoleapi

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/analytics"
)

type fakeQ struct {
	last analytics.AuditFilter
	err  error
}

func (f *fakeQ) AuditEvents(x analytics.AuditFilter) ([]analytics.AuditEvent, string, error) {
	f.last = x
	return []analytics.AuditEvent{{ID: "01JC0000000000000000000001", Kind: "ok"}}, "01JC0000000000000000000001", f.err
}
func (f *fakeQ) AuditEvent(id string) (analytics.AuditEventDetail, bool, error) {
	return analytics.AuditEventDetail{Event: analytics.AuditEvent{ID: id}, Record: json.RawMessage(`{}`)}, id == "01JC0000000000000000000001", f.err
}
func (f *fakeQ) AuditSessions(x analytics.AuditFilter) ([]analytics.AuditSession, string, error) {
	f.last = x
	return []analytics.AuditSession{}, "", f.err
}
func (f *fakeQ) AuditSessionEvents(ref string, x analytics.AuditFilter) ([]analytics.AuditEvent, string, error) {
	f.last = x
	return []analytics.AuditEvent{}, "", f.err
}
func (f *fakeQ) AuditSummary(x analytics.AuditFilter) (analytics.AuditSummary, error) {
	f.last = x
	return analytics.AuditSummary{ByKind: map[string]int64{}}, f.err
}

var now = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func TestParseFilterNormalizesOffsets(t *testing.T) {
	f, code := ParseFilter(url.Values{"since": {"2026-10-29T09:00:00+09:00"}, "until": {"2026-10-29T10:00:00Z"}}, now, 200)
	if code != "" || f.SinceMs != time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("since=%d code=%q", f.SinceMs, code)
	}
	if f.UntilMs != time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("until=%d", f.UntilMs)
	}
	// until with an offset is normalized too: 22:00+09:00 == 13:00Z.
	f, code = ParseFilter(url.Values{"since": {"2026-10-29T12:00:00Z"}, "until": {"2026-10-29T22:00:00+09:00"}}, now, 200)
	if code != "" || f.UntilMs != time.Date(2026, 10, 29, 13, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("until offset = %d code=%q", f.UntilMs, code)
	}
	// An offset that makes the range empty is invalid.
	if _, code := ParseFilter(url.Values{"since": {"2026-10-29T20:00:00+09:00"}, "until": {"2026-10-29T10:00:00Z"}}, now, 200); code != "invalid_range" {
		t.Fatalf("offset-inverted range = %q", code)
	}
}

func TestParseFilterDefaultsAndErrors(t *testing.T) {
	f, code := ParseFilter(url.Values{}, now, 200)
	if code != "" || f.Limit != 100 || f.UntilMs != now.UnixMilli() || f.SinceMs != now.Add(-24*time.Hour).UnixMilli() {
		t.Fatalf("defaults = %+v %q", f, code)
	}
	for q, want := range map[string]string{
		"kind=ok,bogus":     "invalid_kind",
		"limit=201":         "invalid_limit",
		"limit=0":           "invalid_limit",
		"limit=abc":         "invalid_limit",
		"before=not-a-ulid": "invalid_cursor",
		"after=not-a-ulid":  "invalid_cursor",
		"since=yesterday":   "invalid_range",
		"until=tomorrow":    "invalid_range",
		"since=2026-10-29T12:00:00Z&until=2026-10-29T11:00:00Z": "invalid_range",
		"since=2025-01-01T00:00:00Z":                            "invalid_range",
		"session=raw-id":                                        "invalid_filter",
		"trace_id=XYZ":                                          "invalid_filter",
		"code=Not%20Closed":                                     "invalid_filter",
		"team=" + strings.Repeat("x", 257):                      "invalid_filter",
	} {
		v, _ := url.ParseQuery(q)
		if _, code := ParseFilter(v, now, 200); code != want {
			t.Fatalf("%s -> %q, want %q", q, code, want)
		}
	}
}

func TestEventsHandler(t *testing.T) {
	q := &fakeQ{}
	rec := httptest.NewRecorder()
	EventsHandler(q).ServeHTTP(rec, httptest.NewRequest("GET", "/admin/audit/events?kind=policy_denied,ok&pii=true", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"next":"01JC0000000000000000000001"`) || len(q.last.Kinds) != 2 || !q.last.PII {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), q.last)
	}
	bad := httptest.NewRecorder()
	EventsHandler(q).ServeHTTP(bad, httptest.NewRequest("GET", "/admin/audit/events?limit=999", nil))
	if bad.Code != 400 || strings.TrimSpace(bad.Body.String()) != `{"error":"invalid_limit"}` {
		t.Fatalf("bad = %d %s", bad.Code, bad.Body.String())
	}
}

func TestBadInputNeverEchoed(t *testing.T) {
	q := &fakeQ{}
	const secret = "SECRET-VALUE-123"
	for _, path := range []string{
		"/admin/audit/events?kind=" + secret,
		"/admin/audit/events?limit=" + secret,
		"/admin/audit/events?before=" + secret,
		"/admin/audit/events?since=" + secret,
		"/admin/audit/events?session=" + secret,
		"/admin/audit/summary?code=" + secret,
	} {
		rec := httptest.NewRecorder()
		h := EventsHandler(q)
		if strings.Contains(path, "summary") {
			h = SummaryHandler(q)
		}
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 400 || strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("%s -> %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestStorageErrorIsGeneric500(t *testing.T) {
	q := &fakeQ{err: errors.New("sqlite: SECRET-DSN failure")}
	ref := "s1:" + strings.Repeat("a", 32)
	for name, run := range map[string]func() *httptest.ResponseRecorder{
		"events": func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			EventsHandler(q).ServeHTTP(rec, httptest.NewRequest("GET", "/admin/audit/events", nil))
			return rec
		},
		"event": func() *httptest.ResponseRecorder {
			req := httptest.NewRequest("GET", "/x", nil)
			req.SetPathValue("id", "01JC0000000000000000000001")
			rec := httptest.NewRecorder()
			EventHandler(q).ServeHTTP(rec, req)
			return rec
		},
		"sessions": func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			SessionsHandler(q).ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
			return rec
		},
		"session": func() *httptest.ResponseRecorder {
			req := httptest.NewRequest("GET", "/x", nil)
			req.SetPathValue("ref", ref)
			rec := httptest.NewRecorder()
			SessionHandler(q).ServeHTTP(rec, req)
			return rec
		},
		"summary": func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			SummaryHandler(q).ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
			return rec
		},
	} {
		rec := run()
		if rec.Code != 500 || strings.TrimSpace(rec.Body.String()) != `{"error":"query_failed"}` {
			t.Fatalf("%s = %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestEventHandlerNotFound(t *testing.T) {
	q := &fakeQ{}
	req := httptest.NewRequest("GET", "/admin/audit/events/01JC0000000000000000000009", nil)
	req.SetPathValue("id", "01JC0000000000000000000009")
	rec := httptest.NewRecorder()
	EventHandler(q).ServeHTTP(rec, req)
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "not_found") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest("GET", "/admin/audit/events/x", nil)
	req.SetPathValue("id", "x")
	rec = httptest.NewRecorder()
	EventHandler(q).ServeHTTP(rec, req)
	if rec.Code != 400 || strings.TrimSpace(rec.Body.String()) != `{"error":"invalid_id"}` {
		t.Fatalf("bad id = %d %s", rec.Code, rec.Body.String())
	}
}

func TestSessionHandlerLimit(t *testing.T) {
	q := &fakeQ{}
	ref := "s1:" + strings.Repeat("a", 32)
	req := httptest.NewRequest("GET", "/admin/audit/sessions/"+ref+"?limit=500", nil)
	req.SetPathValue("ref", ref)
	rec := httptest.NewRecorder()
	SessionHandler(q).ServeHTTP(rec, req)
	if rec.Code != 200 || q.last.Limit != 500 {
		t.Fatalf("%d limit=%d", rec.Code, q.last.Limit)
	}
	req = httptest.NewRequest("GET", "/x?limit=501", nil)
	req.SetPathValue("ref", ref)
	rec = httptest.NewRecorder()
	SessionHandler(q).ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("limit=501 -> %d", rec.Code)
	}
	// raw session ids are rejected with a closed code and never echoed.
	req = httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("ref", "raw-session-id")
	rec = httptest.NewRecorder()
	SessionHandler(q).ServeHTTP(rec, req)
	if rec.Code != 400 || strings.Contains(rec.Body.String(), "raw-session-id") || !strings.Contains(rec.Body.String(), `"invalid_id"`) {
		t.Fatalf("raw ref -> %d %s", rec.Code, rec.Body.String())
	}
}
