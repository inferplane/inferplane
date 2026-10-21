package pgstore

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/analytics"
	"github.com/inferplane/inferplane/internal/audit"
)

// Values are hand-derived from A–H, independently of EvidenceOf. Omitting
// costless completions or misclassifying evidence breaks the contract.
func pgObservableFixture(ts string) []audit.Record {
	makeRecord := func(id string, status int, cost int64) audit.Record {
		r := completedRecord(id, ts, "team", "model", 10, 5, cost)
		r.Usage.CacheReadInputTokens, r.Usage.CacheCreationInputTokens = 2, 3
		r.Usage.Evidence = &audit.UsageEvidence{InputKnown: true, OutputKnown: true, InputTotalKnown: true, CacheWriteTTLKnown: true}
		r.Outcome = &audit.OutcomeRef{Status: status}
		return r
	}
	a := makeRecord("A", 200, 1000)
	b := makeRecord("B", 500, 0)
	b.Cost = nil
	c := makeRecord("C", 200, 2000)
	c.Outcome.Partial = true
	d := makeRecord("D", 200, 0)
	d.Cost.PricingMissing = true
	e := makeRecord("E", 200, 50)
	e.Usage.Evidence = nil
	f := makeRecord("F", 200, 0)
	f.Usage.InputTokens, f.Usage.OutputTokens = 0, 0
	f.Usage.CacheReadInputTokens, f.Usage.CacheCreationInputTokens = 0, 0
	g := makeRecord("G", 200, 0)
	g.Outcome, g.Cost = nil, nil
	h := makeRecord("H", 400, 300)
	return []audit.Record{a, b, c, d, e, f, g, h}
}

func pgTestLease(t *testing.T, s *Store) int64 {
	t.Helper()
	epoch, ok, err := tryAcquireLease(context.Background(), s.db, "evidence", 15*time.Second)
	if err != nil || !ok {
		t.Fatalf("acquire lease: ok=%v err=%v", ok, err)
	}
	return epoch
}

func TestPGObservableProjectionContract(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	epoch := pgTestLease(t, s)
	fixture := pgObservableFixture(time.Now().UTC().Format(time.RFC3339Nano))
	records := append(append([]audit.Record{}, fixture...), audit.Record{Event: "request_started", ID: "started", Cost: &audit.CostRef{AmountUSDMicros: 9999}}, audit.Record{Event: "admin_denied", ID: "admin", Cost: &audit.CostRef{AmountUSDMicros: 9999}})
	want := analytics.OperationTotals{Completed: 8, Succeeded: 4, Failed: 2, Partial: 1, Unknown: 1, EstimatedRequests: 3, UncertainRequests: 1, UnpricedRequests: 1, UnavailableRequests: 2, LegacyUnknownRequests: 1, EstimatedCostMicros: 1300, UncertainRecordedCostMicros: 2050}
	for replay := 0; replay < 2; replay++ {
		if err := s.ingestBatch(ctx, "evidence", epoch, records, map[string]int64{"fixture": int64(replay + 1)}); err != nil {
			t.Fatal(err)
		}
		summary, err := s.Summary(analytics.SummaryQuery{})
		if err != nil {
			t.Fatal(err)
		}
		if summary.Totals.Operations != want {
			t.Fatalf("operations = %+v, want %+v", summary.Totals.Operations, want)
		}
		totals := summary.Totals
		if totals.Requests != 6 || totals.CostMicros != 3350 || totals.InputTokens != 50 || totals.OutputTokens != 25 || totals.CacheReadTokens != 10 || totals.CacheCreationTokens != 15 {
			t.Fatalf("legacy totals = %+v, want requests=6 cost=3350 tokens=50/25/10/15", totals)
		}
		if len(summary.ByTeam) != 1 || summary.ByTeam[0].Operations != want || summary.ByTeam[0].Requests != 6 || summary.ByTeam[0].CostMicros != 3350 {
			t.Fatalf("teams = %+v", summary.ByTeam)
		}
		if len(summary.ByModel) != 1 || summary.ByModel[0].Operations != want || summary.ByModel[0].Requests != 6 || summary.ByModel[0].CostMicros != 3350 {
			t.Fatalf("models = %+v", summary.ByModel)
		}
		day := analytics.DayOf(fixture[0].TS)
		filtered, err := s.Summary(analytics.SummaryQuery{SinceDay: day, UntilDay: day})
		if err != nil || filtered.Totals != totals {
			t.Fatalf("filtered summary=%+v err=%v", filtered, err)
		}
		days, err := s.TimeSeries(analytics.TimeSeriesQuery{Days: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(days) != 1 || days[0].Operations != want || days[0].Requests != 6 || days[0].CostMicros != 3350 || days[0].InputTokens != 50 || days[0].OutputTokens != 25 {
			t.Fatalf("days = %+v", days)
		}
		recent, err := s.Recent(20, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(recent) != 8 {
			t.Fatalf("Recent length=%d, want 8", len(recent))
		}
		wantOutcome := []analytics.OutcomeState{"failed", "unknown", "succeeded", "succeeded", "succeeded", "partial", "failed", "succeeded"}
		wantCost := []analytics.CostState{"estimated", "unavailable", "estimated", "legacy_unknown", "unpriced", "uncertain", "unavailable", "estimated"}
		for i, event := range recent {
			if event.OutcomeState != wantOutcome[i] || event.CostState != wantCost[i] || event.ProjectionVersion != 1 || event.HasCost != (event.ID != "B" && event.ID != "G") || event.Partial != (event.ID == "C") {
				t.Errorf("Recent[%d] = %+v", i, event)
			}
		}
		page, err := s.Recent(3, "F")
		if err != nil || len(page) != 3 || page[0].ID != "E" || page[1].ID != "D" || page[2].ID != "C" {
			t.Fatalf("page = %+v, err=%v", page, err)
		}
		body, err := json.Marshal(summary)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(body, &wire); err != nil {
			t.Fatal(err)
		}
		wantJSON := map[string]any{"completed": float64(8), "succeeded": float64(4), "failed": float64(2), "partial": float64(1), "unknown": float64(1), "estimated_requests": float64(3), "uncertain_requests": float64(1), "unpriced_requests": float64(1), "unavailable_requests": float64(2), "legacy_unknown_requests": float64(1), "estimated_cost_micros": float64(1300), "uncertain_recorded_cost_micros": float64(2050), "legacy_projection_records": float64(0)}
		if !reflect.DeepEqual(wire["totals"].(map[string]any)["operations"], wantJSON) {
			t.Fatalf("operations JSON = %s", body)
		}
	}
}

func TestPGCostlessGroupsAndBoundedErrors(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	epoch := pgTestLease(t, s)
	r := pgObservableFixture(time.Now().UTC().Format(time.RFC3339Nano))[1]
	r.ID, r.Principal.Team, r.Request.ModelResolved = "costless", "costless-team", "costless-model"
	rawError := "private provider error with prompt or key"
	r.Outcome.Error = &rawError
	if err := s.ingestBatch(ctx, "evidence", epoch, []audit.Record{r}, nil); err != nil {
		t.Fatal(err)
	}
	sum, err := s.Summary(analytics.SummaryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	want := analytics.OperationTotals{Completed: 1, Failed: 1, UnavailableRequests: 1}
	if sum.Totals.Operations != want || sum.Totals.Requests != 0 || sum.Totals.CostMicros != 0 || sum.Totals.InputTokens != 0 || sum.Totals.OutputTokens != 0 || sum.Totals.CacheReadTokens != 0 || sum.Totals.CacheCreationTokens != 0 {
		t.Fatalf("costless totals = %+v", sum.Totals)
	}
	if len(sum.ByTeam) != 1 || sum.ByTeam[0].Operations != want || sum.ByTeam[0].Requests != 0 || len(sum.ByModel) != 1 || sum.ByModel[0].Operations != want || sum.ByModel[0].Requests != 0 {
		t.Fatalf("costless groups = %+v", sum)
	}
	days, err := s.TimeSeries(analytics.TimeSeriesQuery{Days: 1})
	if err != nil || len(days) != 1 || days[0].Operations != want || days[0].Requests != 0 || days[0].InputTokens != 0 || days[0].OutputTokens != 0 {
		t.Fatalf("costless days = %+v, %v", days, err)
	}
	recent, err := s.Recent(1, "")
	if err != nil || len(recent) != 1 || recent[0].ErrorCode != "upstream_error" || recent[0].HasCost {
		t.Fatalf("costless event = %+v, %v", recent, err)
	}
}
