package analyticsapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/analytics"
	"github.com/inferplane/inferplane/internal/audit"
)

type fakeQ struct {
	gotSince, gotUntil string
}

func (f *fakeQ) Summary(q analytics.SummaryQuery) (analytics.Summary, error) {
	f.gotSince, f.gotUntil = q.SinceDay, q.UntilDay
	return analytics.Summary{Totals: analytics.Totals{Requests: 3, CostMicros: 1234}}, nil
}
func (f *fakeQ) TimeSeries(analytics.TimeSeriesQuery) ([]analytics.DayPoint, error) {
	return []analytics.DayPoint{{Day: "2026-06-29", CostMicros: 1234}}, nil
}
func (f *fakeQ) Health() (analytics.Health, error) {
	return analytics.Health{Mode: "A", IsLeader: true}, nil
}
func (f *fakeQ) Recent(limit int, before string) ([]analytics.Event, error) {
	return []analytics.Event{{ID: "01FAKE", Team: "demo"}}, nil
}

// fakeRebuildableQ additionally implements analytics.Rebuilder (Mode B shape) —
// Mode A's *analytics.Index does not, and fakeQ above deliberately doesn't
// either, so RebuildHandler's 404-vs-204 branch has a fixture for both sides.
type fakeRebuildableQ struct {
	fakeQ
	rebuilt    bool
	rebuildErr error
}

func (f *fakeRebuildableQ) Rebuild(context.Context) error {
	f.rebuilt = true
	return f.rebuildErr
}

func TestSummaryHandler_GET_defaultsWindow(t *testing.T) {
	f := &fakeQ{}
	rec := httptest.NewRecorder()
	SummaryHandler(f).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/analytics/summary", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"cost_micros"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	// Empty since must be defaulted to a bounded ~30d window, not unbounded.
	if f.gotSince == "" {
		t.Error("SummaryHandler did not default an empty since → unbounded scan (spec §13)")
	}
}

func TestSummaryHandler_rejectsBadDateAndHugeWindow(t *testing.T) {
	rec := httptest.NewRecorder()
	SummaryHandler(&fakeQ{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/analytics/summary?since=June", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed since: got %d, want 400", rec.Code)
	}
	rec = httptest.NewRecorder()
	SummaryHandler(&fakeQ{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/analytics/summary?since=2000-01-01&until=2026-01-01", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("oversized window: got %d, want 400", rec.Code)
	}
}

func TestSummaryHandler_rejectsPOST(t *testing.T) {
	rec := httptest.NewRecorder()
	SummaryHandler(&fakeQ{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/analytics/summary", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}
}

func TestTimeSeriesHandler_GET(t *testing.T) {
	rec := httptest.NewRecorder()
	TimeSeriesHandler(&fakeQ{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/analytics/timeseries?days=7", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"day"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestSummaryHandler_rejectsReversedRange(t *testing.T) {
	rec := httptest.NewRecorder()
	SummaryHandler(&fakeQ{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/admin/analytics/summary?since=2026-06-29&until=2026-06-01", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reversed range: got %d, want 400", rec.Code)
	}
}

// --- Phase 1b Task 4: /admin/analytics/health + /admin/analytics/rebuild ---

func TestHealthHandler_GET(t *testing.T) {
	rec := httptest.NewRecorder()
	HealthHandler(&fakeQ{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/analytics/health", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"mode":"A"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHealthHandler_rejectsPOST(t *testing.T) {
	rec := httptest.NewRecorder()
	HealthHandler(&fakeQ{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/analytics/health", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}
}

func TestRebuildHandler_callsRebuildAndReturns204(t *testing.T) {
	q := &fakeRebuildableQ{}
	rec := httptest.NewRecorder()
	RebuildHandler(q).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/analytics/rebuild", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204", rec.Code)
	}
	if !q.rebuilt {
		t.Fatal("Rebuild was not called")
	}
}

func TestRebuildHandler_405WhenQuerierIsNotARebuilder(t *testing.T) {
	// fakeQ (Mode A shape) does not implement analytics.Rebuilder.
	rec := httptest.NewRecorder()
	RebuildHandler(&fakeQ{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/analytics/rebuild", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405 (Mode A has no rebuild)", rec.Code)
	}
}

func TestRebuildHandler_rejectsGET(t *testing.T) {
	rec := httptest.NewRecorder()
	RebuildHandler(&fakeRebuildableQ{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/analytics/rebuild", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}
}

func TestRebuildHandler_500OnRebuildError(t *testing.T) {
	q := &fakeRebuildableQ{rebuildErr: context.DeadlineExceeded}
	rec := httptest.NewRecorder()
	RebuildHandler(q).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/analytics/rebuild", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
}

// A–H goes through the real SQLite projection and HTTP serialization boundary.
func evidenceIndex(t *testing.T) *analytics.Index {
	t.Helper()
	ix, err := analytics.OpenSQLite(filepath.Join(t.TempDir(), "evidence.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	amounts := []int64{1000, 0, 2000, 0, 50, 0, 0, 300}
	for n, id := range []string{"A", "B", "C", "D", "E", "F", "G", "H"} {
		r := audit.Record{ID: id, TS: time.Now().UTC().Format(time.RFC3339), Event: "request_completed",
			Principal: audit.PrincipalRef{Team: "fixture-team"}, Request: audit.RequestRef{ModelResolved: "fixture-model"},
			Outcome: &audit.OutcomeRef{Status: 200}, Cost: &audit.CostRef{AmountUSDMicros: amounts[n]},
			Usage: &audit.UsageRef{Evidence: &audit.UsageEvidence{InputKnown: true, OutputKnown: true, InputTotalKnown: true, CacheWriteTTLKnown: true}}}
		switch id {
		case "B":
			r.Outcome.Status = 502
			r.Cost = nil
		case "C":
			code := "stream_incomplete"
			r.Outcome.Partial = true
			r.Outcome.Error = &code
		case "D":
			r.Cost.PricingMissing = true
		case "E":
			r.Usage.Evidence = nil
		case "G":
			r.Outcome = nil
			r.Cost = nil
		case "H":
			r.Outcome.Status = 400
		}
		if err := ix.Ingest(r); err != nil {
			t.Fatal(err)
		}
	}
	return ix
}

func TestAnalyticsEvidenceSerialization(t *testing.T) {
	ix := evidenceIndex(t)
	rec := httptest.NewRecorder()
	SummaryHandler(ix).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/analytics/summary", nil))
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Totals struct {
			Requests   int64            `json:"requests"`
			CostMicros int64            `json:"cost_micros"`
			Operations map[string]int64 `json:"operations"`
		} `json:"totals"`
		ByTeam []struct {
			Operations map[string]int64 `json:"operations"`
		} `json:"by_team"`
		ByModel []struct {
			Operations map[string]int64 `json:"operations"`
		} `json:"by_model"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"completed": 8, "succeeded": 4, "failed": 2, "partial": 1, "unknown": 1, "estimated_requests": 3, "uncertain_requests": 1, "unpriced_requests": 1, "unavailable_requests": 2, "legacy_unknown_requests": 1, "estimated_cost_micros": 1300, "uncertain_recorded_cost_micros": 2050, "legacy_projection_records": 0}
	if body.Totals.Requests != 6 || body.Totals.CostMicros != 3350 {
		t.Fatalf("legacy totals=%+v", body.Totals)
	}
	if len(body.ByTeam) != 1 || len(body.ByModel) != 1 {
		t.Fatalf("missing group rows: %s", rec.Body.String())
	}
	for _, o := range []map[string]int64{body.Totals.Operations, body.ByTeam[0].Operations, body.ByModel[0].Operations} {
		for key, value := range want {
			got, ok := o[key]
			if !ok || got != value {
				t.Errorf("operations.%s=%d present=%v want=%d", key, got, ok, value)
			}
		}
	}
	rec = httptest.NewRecorder()
	TimeSeriesHandler(ix).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/analytics/timeseries?days=30", nil))
	var days []struct {
		Operations map[string]int64 `json:"operations"`
	}
	if rec.Code != 200 {
		t.Fatalf("timeseries=%d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &days); err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 {
		t.Fatalf("days=%d", len(days))
	}
	for key, value := range want {
		got, ok := days[0].Operations[key]
		if !ok || got != value {
			t.Errorf("daily operations.%s=%d present=%v want=%d", key, got, ok, value)
		}
	}
}
