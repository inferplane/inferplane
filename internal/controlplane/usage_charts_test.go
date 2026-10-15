package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/telemetry"
)

func TestUsageChartsExactBoundedRollup(t *testing.T) {
	agg := telemetry.NewMemoryAggregator(400 * time.Hour)
	start := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	// Every window remains inside the ingestion sanity bound while their
	// aggregate crosses JavaScript's safe-integer boundary.
	for i := 0; i < 11; i++ {
		when, amount := start.Add(time.Duration(i)*time.Minute), int64(900719925474099)
		if i == 9 {
			amount = 900719925474102
		}
		if i == 10 {
			when, amount = start.Add(24*time.Hour), 7
		}
		b := &telemetry.UsageBatch{Dataplane: "node", WindowStart: when, WindowEnd: when.Add(time.Minute),
			Entries: []telemetry.UsageEntry{{Team: "team", User: "account", Model: "model", SpentMicroUSD: amount, InputTokens: 2, OutputTokens: 3, CacheReadTokens: 4}}}
		if err := agg.Upsert(context.Background(), b); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	NewUsageServer("read", agg).Mount(mux)
	path := "/v1alpha1/usage/charts?since=" + start.Format(time.RFC3339) + "&until=" + start.Add(48*time.Hour).Format(time.RFC3339)
	for _, token := range []string{"", "wrong", "read"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if token != "read" {
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("unauthorized charts read: %d", rec.Code)
			}
			continue
		}
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("charts read: %d %s", rec.Code, rec.Body.String())
		}
		var result struct {
			Total int64 `json:"total_micro_usd"`
			Days  []struct {
				Day   string `json:"day"`
				Spend int64  `json:"spent_micro_usd"`
			} `json:"days"`
			Models []struct {
				Key   string `json:"key"`
				Spend int64  `json:"spent_micro_usd"`
			} `json:"models"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Total != 9007199254741000 || len(result.Days) != 2 || len(result.Models) != 1 ||
			result.Days[0].Spend != 9007199254740993 || result.Models[0].Spend != result.Total {
			t.Fatalf("rollup lost exact cost or grouping: %+v", result)
		}
		for _, private := range []string{`"user"`, `"team"`, `"dataplane"`, "account"} {
			if strings.Contains(rec.Body.String(), private) {
				t.Fatalf("chart output gained unnecessary identity: %s", private)
			}
		}
	}
	for _, query := range []string{"", "?since=bad&until=bad", "?since=2026-01-02&until=2026-01-01", "?since=2020-01-01&until=2026-01-01"} {
		req := httptest.NewRequest("GET", "/v1alpha1/usage/charts"+query, nil)
		req.Header.Set("Authorization", "Bearer read")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("unbounded/invalid query %q: %d", query, rec.Code)
		}
	}
}

func TestUsageChartsStoreFailureDoesNotProduceZeroHistory(t *testing.T) {
	mux := http.NewServeMux()
	NewUsageServer("read", failingAgg{}).Mount(mux)
	req := httptest.NewRequest("GET", "/v1alpha1/usage/charts?since=2026-10-01&until=2026-10-02", nil)
	req.Header.Set("Authorization", "Bearer read")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "total_micro_usd") {
		t.Fatalf("unavailable history looked like a zero-cost chart: %d %s", rec.Code, rec.Body.String())
	}
}
