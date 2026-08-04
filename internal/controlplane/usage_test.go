package controlplane

import (
	"context"
	"errors"
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
