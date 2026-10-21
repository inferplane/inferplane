package analyticsapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/analytics"
)

func TestLogsHandler_returnsEvents(t *testing.T) {
	rec := httptest.NewRecorder()
	LogsHandler(&fakeQ{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/logs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"01FAKE"`) {
		t.Fatalf("body missing event: %s", rec.Body.String())
	}
}

func TestLogsHandler_passesLimitAndBefore(t *testing.T) {
	var gotLimit int
	var gotBefore string
	q := &recordingLogsQ{fakeQ: &fakeQ{}, onRecent: func(limit int, before string) {
		gotLimit, gotBefore = limit, before
	}}
	rec := httptest.NewRecorder()
	LogsHandler(q).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/logs?limit=10&before=01ABC", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if gotLimit != 10 || gotBefore != "01ABC" {
		t.Fatalf("Recent called with limit=%d before=%q, want 10, 01ABC", gotLimit, gotBefore)
	}
}

func TestLogsHandler_rejectsNonGET(t *testing.T) {
	rec := httptest.NewRecorder()
	LogsHandler(&fakeQ{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/logs", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}
}

func TestLogsHandler_500OnQueryError(t *testing.T) {
	q := &recordingLogsQ{fakeQ: &fakeQ{}, err: errQueryFailed}
	rec := httptest.NewRecorder()
	LogsHandler(q).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/logs", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
}

var errQueryFailed = &queryError{}

type queryError struct{}

func (*queryError) Error() string { return "query failed" }

// recordingLogsQ wraps fakeQ to observe/override Recent's arguments and error.
type recordingLogsQ struct {
	*fakeQ
	onRecent func(limit int, before string)
	err      error
}

func (q *recordingLogsQ) Recent(limit int, before string) ([]analytics.Event, error) {
	if q.onRecent != nil {
		q.onRecent(limit, before)
	}
	if q.err != nil {
		return nil, q.err
	}
	return q.fakeQ.Recent(limit, before)
}

func TestLogsEvidenceSerialization(t *testing.T) {
	rec := httptest.NewRecorder()
	LogsHandler(evidenceIndex(t)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/logs", nil))
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Events []map[string]json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Events) != 8 {
		t.Fatalf("logs=%d want=8", len(body.Events))
	}
	want := map[string][3]string{"A": {"succeeded", "estimated", "true"}, "B": {"failed", "unavailable", "false"}, "C": {"partial", "uncertain", "true"}, "D": {"succeeded", "unpriced", "true"}, "E": {"succeeded", "legacy_unknown", "true"}, "F": {"succeeded", "estimated", "true"}, "G": {"unknown", "unavailable", "false"}, "H": {"failed", "estimated", "true"}}
	for _, e := range body.Events {
		var id, outcome, cost string
		json.Unmarshal(e["id"], &id)
		json.Unmarshal(e["outcome_state"], &outcome)
		json.Unmarshal(e["cost_state"], &cost)
		w, ok := want[id]
		if !ok {
			t.Fatalf("unexpected id=%q", id)
		}
		if outcome != w[0] || cost != w[1] || string(e["has_cost"]) != w[2] {
			t.Errorf("%s evidence=%s", id, mustJSON(e))
		}
		if string(e["projection_version"]) != "1" {
			t.Errorf("%s projection=%s", id, e["projection_version"])
		}
		if id == "C" {
			if string(e["partial"]) != "true" || string(e["status"]) != "200" || string(e["error_code"]) != `"stream_incomplete"` {
				t.Errorf("partial evidence=%s", mustJSON(e))
			}
		} else if string(e["partial"]) != "false" {
			t.Errorf("%s partial=%s", id, e["partial"])
		}
	}
}
func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
