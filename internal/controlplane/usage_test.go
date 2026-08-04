package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/telemetry"
)

func usageMux(t *testing.T, token string) (*http.ServeMux, *telemetry.MemoryAggregator) {
	t.Helper()
	agg := telemetry.NewMemoryAggregator(24 * time.Hour)
	mux := http.NewServeMux()
	NewUsageServer(token, agg).Mount(mux)
	return mux, agg
}

const goodBatch = `{
  "dataplane": "dp-1",
  "window_start": "2026-08-04T12:00:00Z",
  "window_end":   "2026-08-04T12:01:00Z",
  "entries": [{"team":"demo","user":"u1","model":"m1","spent_micro_usd":100,
    "input_tokens":10,"output_tokens":5,"cache_read_tokens":3,
    "cache_write_5m_tokens":2,"cache_write_1h_tokens":1}]
}`

func postUsage(mux *http.ServeMux, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/v1alpha1/usage", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestUsageIngestAndQuery(t *testing.T) {
	mux, _ := usageMux(t, "tok")
	if rec := postUsage(mux, "tok", goodBatch); rec.Code != 200 {
		t.Fatalf("ingest: %d %s", rec.Code, rec.Body.String())
	}
	// Duplicate delivery doesn't double.
	if rec := postUsage(mux, "tok", goodBatch); rec.Code != 200 {
		t.Fatalf("duplicate ingest: %d", rec.Code)
	}

	req := httptest.NewRequest("GET", "/v1alpha1/usage?team=demo&since=2026-08-04&group_by=model", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("query: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"total_micro_usd":100`) {
		t.Fatalf("duplicate double-counted or lost: %s", body)
	}
	if !strings.Contains(body, `"key":"m1"`) || !strings.Contains(body, `"cache_write_1h_tokens":1`) {
		t.Fatalf("grouped row missing fields: %s", body)
	}
}

func TestUsageAuth(t *testing.T) {
	mux, _ := usageMux(t, "tok")
	if rec := postUsage(mux, "wrong", goodBatch); rec.Code != 401 {
		t.Fatalf("bad token must 401, got %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/v1alpha1/usage", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("query without token must 401, got %d", rec.Code)
	}
}

func TestUsageIngestRejections(t *testing.T) {
	mux, _ := usageMux(t, "tok")
	if rec := postUsage(mux, "tok", `{not json`); rec.Code != 400 {
		t.Fatalf("malformed must 400, got %d", rec.Code)
	}
	// Validation failure (empty dataplane) → 400 (permanent for the pusher).
	bad := strings.Replace(goodBatch, `"dp-1"`, `""`, 1)
	if rec := postUsage(mux, "tok", bad); rec.Code != 400 {
		t.Fatalf("invalid batch must 400, got %d", rec.Code)
	}
	// Oversized → 413.
	huge := strings.Replace(goodBatch, `"u1"`, `"`+strings.Repeat("x", usageMaxBodyBytes)+`"`, 1)
	if rec := postUsage(mux, "tok", huge); rec.Code != 413 {
		t.Fatalf("oversized must 413, got %d", rec.Code)
	}
}

// A failing aggregator (durable store down, Task 8) must 503 so the data
// plane's FIFO keeps the batch — the ack means stored.
type failingAgg struct{}

func (failingAgg) Upsert(context.Context, *telemetry.UsageBatch) error {
	return errors.New("store down")
}
func (failingAgg) Query(context.Context, telemetry.QueryFilter) (telemetry.QueryResult, error) {
	return telemetry.QueryResult{}, errors.New("store down")
}
func (failingAgg) Rows(context.Context, time.Time, time.Time, func(telemetry.StoredRow) error) error {
	return errors.New("store down")
}

func TestUsageIngest503WhenStoreDown(t *testing.T) {
	mux := http.NewServeMux()
	NewUsageServer("tok", failingAgg{}).Mount(mux)
	if rec := postUsage(mux, "tok", goodBatch); rec.Code != 503 {
		t.Fatalf("failing aggregator must 503 (ack means stored), got %d", rec.Code)
	}
}

func TestUsageQueryBadParams(t *testing.T) {
	mux, _ := usageMux(t, "")
	for _, q := range []string{"group_by=keyid", "since=notatime", "until=alsonot"} {
		req := httptest.NewRequest("GET", "/v1alpha1/usage?"+q, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Fatalf("%s must 400, got %d", q, rec.Code)
		}
	}
}

// Backend failure on the query path must be 503, never a client-error 400
// (kiro task-gate MEDIUM — Task 8's durable store hits this immediately).
func TestUsageQuery503WhenStoreDown(t *testing.T) {
	mux := http.NewServeMux()
	NewUsageServer("", failingAgg{}).Mount(mux)
	req := httptest.NewRequest("GET", "/v1alpha1/usage", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 503 {
		t.Fatalf("backend query failure must 503, got %d", rec.Code)
	}
}

func seedUsage(t *testing.T, mux *http.ServeMux) {
	t.Helper()
	if rec := postUsage(mux, "tok", goodBatch); rec.Code != 200 {
		t.Fatalf("seed: %d", rec.Code)
	}
}

func getExport(mux *http.ServeMux, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/v1alpha1/usage/export?"+query, nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestExportCSV(t *testing.T) {
	mux, _ := usageMux(t, "tok")
	seedUsage(t, mux)
	rec := getExport(mux, "since=2026-08-04&until=2026-08-05&format=csv")
	if rec.Code != 200 {
		t.Fatalf("csv export: %d %s", rec.Code, rec.Body.String())
	}
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(lines) != 2 { // header + one row
		t.Fatalf("want header+1 row, got %d lines: %q", len(lines), lines)
	}
	if !strings.HasPrefix(lines[0], "dataplane,window_start") {
		t.Fatalf("bad header: %q", lines[0])
	}
	if !strings.Contains(lines[1], "demo") || !strings.Contains(lines[1], ",100,") {
		t.Fatalf("row lost fields: %q", lines[1])
	}
}

func TestExportJSONRoundTrips(t *testing.T) {
	mux, _ := usageMux(t, "tok")
	seedUsage(t, mux)
	rec := getExport(mux, "since=2026-08-04&until=2026-08-05") // default json
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("json export: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	var rows []telemetry.StoredRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("export is not valid JSON: %v: %s", err, rec.Body.String())
	}
	if len(rows) != 1 || rows[0].SpentMicroUSD != 100 || rows[0].CacheWrite1hTokens != 1 {
		t.Fatalf("round trip mangled rows: %+v", rows)
	}
}

func TestExportValidation(t *testing.T) {
	mux, _ := usageMux(t, "tok")
	for _, q := range []string{"", "since=2026-08-04", "until=2026-08-05", "since=2026-08-04&until=2026-08-05&format=xml", "since=bad&until=2026-08-05"} {
		if rec := getExport(mux, q); rec.Code != 400 {
			t.Fatalf("query %q must 400, got %d", q, rec.Code)
		}
	}
	// 401 without token.
	req := httptest.NewRequest("GET", "/v1alpha1/usage/export?since=2026-08-04&until=2026-08-05", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("export without token must 401, got %d", rec.Code)
	}
}

func TestExport503WhenStoreDown(t *testing.T) {
	mux := http.NewServeMux()
	NewUsageServer("tok", failingAgg{}).Mount(mux)
	if rec := getExport(mux, "since=2026-08-04&until=2026-08-05"); rec.Code != 503 {
		t.Fatalf("PG-down export must 503, got %d", rec.Code)
	}
}

// An export outlasting the server's WriteTimeout must NOT be truncated:
// the handler extends its own deadline per write via ResponseController.
func TestExportOutlivesServerWriteTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	agg := telemetry.NewMemoryAggregator(24 * time.Hour)
	// Seed 5 windows.
	for i := 0; i < 5; i++ {
		ws := time.Date(2026, 8, 4, 12, i, 0, 0, time.UTC)
		_ = agg.Upsert(context.Background(), &telemetry.UsageBatch{
			Dataplane: "dp-1", WindowStart: ws, WindowEnd: ws.Add(time.Minute),
			Entries: []telemetry.UsageEntry{{Team: "demo", Model: "m1", SpentMicroUSD: 1}},
		})
	}
	// slowAgg delays each row past the server's WriteTimeout in total.
	mux := http.NewServeMux()
	NewUsageServer("", slowAgg{inner: agg, delay: 120 * time.Millisecond}).Mount(mux)
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.WriteTimeout = 300 * time.Millisecond // 5 rows × 120ms = 600ms > 300ms
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1alpha1/usage/export?since=2026-08-04&until=2026-08-05&format=csv")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("export truncated by server WriteTimeout: %v (got %d bytes)", err, len(body))
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 6 { // header + 5 rows
		t.Fatalf("want 6 lines, got %d — truncated by WriteTimeout", len(lines))
	}
}

// slowAgg delays each streamed row (simulating a big export) to prove the
// per-write deadline extension outlives the server WriteTimeout.
type slowAgg struct {
	inner telemetry.Aggregator
	delay time.Duration
}

func (s slowAgg) Upsert(ctx context.Context, b *telemetry.UsageBatch) error {
	return s.inner.Upsert(ctx, b)
}
func (s slowAgg) Query(ctx context.Context, f telemetry.QueryFilter) (telemetry.QueryResult, error) {
	return s.inner.Query(ctx, f)
}
func (s slowAgg) Rows(ctx context.Context, since, until time.Time, fn func(telemetry.StoredRow) error) error {
	return s.inner.Rows(ctx, since, until, func(r telemetry.StoredRow) error {
		time.Sleep(s.delay)
		return fn(r)
	})
}
