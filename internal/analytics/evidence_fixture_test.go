package analytics

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
)

// The acceptance fixture's literals are independent of classification helpers.
func observableFixture(ts string) []audit.Record {
	makeRecord := func(id string, status int, cost int64) audit.Record {
		r := evidencedRecord()
		r.ID, r.TS = id, ts
		r.Principal.Team, r.Request.ModelResolved = "team", "model"
		r.Outcome.Status, r.Cost.AmountUSDMicros = status, cost
		r.Usage.InputTokens, r.Usage.OutputTokens = 10, 5
		r.Usage.CacheReadInputTokens, r.Usage.CacheCreationInputTokens = 2, 3
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

func TestObservableProjectionContract(t *testing.T) {
	ix, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	fixture := observableFixture(time.Now().UTC().Format(time.RFC3339Nano))
	var lines strings.Builder
	for _, r := range fixture {
		if err := ix.Ingest(r); err != nil {
			t.Fatal(err)
		}
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		lines.Write(line)
		lines.WriteByte('\n')
	}
	for _, event := range []string{"request_started", "admin_denied"} {
		if err := ix.Ingest(audit.Record{Event: event, ID: event, Cost: &audit.CostRef{AmountUSDMicros: 9999}}); err != nil {
			t.Fatal(err)
		}
	}
	want := OperationTotals{
		Completed: 8, Succeeded: 4, Failed: 2, Partial: 1, Unknown: 1,
		EstimatedRequests: 3, UncertainRequests: 1, UnpricedRequests: 1,
		UnavailableRequests: 2, LegacyUnknownRequests: 1,
		EstimatedCostMicros: 1300, UncertainRecordedCostMicros: 2050,
	}
	for replay := 0; replay < 2; replay++ {
		summary, err := ix.Summary(SummaryQuery{})
		if err != nil {
			t.Fatal(err)
		}
		if summary.Totals.Operations != want {
			t.Fatalf("operations = %+v, want %+v", summary.Totals.Operations, want)
		}
		if summary.Totals.Requests != 6 || summary.Totals.CostMicros != 3350 ||
			summary.Totals.InputTokens != 50 || summary.Totals.OutputTokens != 25 ||
			summary.Totals.CacheReadTokens != 10 || summary.Totals.CacheCreationTokens != 15 {
			t.Fatalf("legacy totals = %+v, want requests=6 cost=3350 tokens=50/25/10/15", summary.Totals)
		}
		if len(summary.ByTeam) != 1 || summary.ByTeam[0].Operations != want || summary.ByTeam[0].Requests != 6 || summary.ByTeam[0].CostMicros != 3350 {
			t.Fatalf("team aggregation = %+v", summary.ByTeam)
		}
		if len(summary.ByModel) != 1 || summary.ByModel[0].Operations != want || summary.ByModel[0].Requests != 6 || summary.ByModel[0].CostMicros != 3350 {
			t.Fatalf("model aggregation = %+v", summary.ByModel)
		}
		days, err := ix.TimeSeries(TimeSeriesQuery{Days: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(days) != 1 || days[0].Operations != want || days[0].Requests != 6 || days[0].CostMicros != 3350 || days[0].InputTokens != 50 || days[0].OutputTokens != 25 {
			t.Fatalf("day aggregation = %+v", days)
		}
		recent, err := ix.Recent(20, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(recent) != 8 {
			t.Fatalf("Recent length=%d, want 8", len(recent))
		}
		wantOutcome := []OutcomeState{"failed", "unknown", "succeeded", "succeeded", "succeeded", "partial", "failed", "succeeded"}
		wantCost := []CostState{"estimated", "unavailable", "estimated", "legacy_unknown", "unpriced", "uncertain", "unavailable", "estimated"}
		for i, event := range recent {
			if event.OutcomeState != wantOutcome[i] || event.CostState != wantCost[i] ||
				event.ProjectionVersion != 1 || event.HasCost != (event.ID != "B" && event.ID != "G") || event.Partial != (event.ID == "C") {
				t.Errorf("Recent[%d] = %+v", i, event)
			}
		}
		page, err := ix.Recent(3, "F")
		if err != nil {
			t.Fatal(err)
		}
		if len(page) != 3 || page[0].ID != "E" || page[1].ID != "D" || page[2].ID != "C" {
			t.Fatalf("keyset page = %+v", page)
		}
		body, err := json.Marshal(summary)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(body, &wire); err != nil {
			t.Fatal(err)
		}
		operations := wire["totals"].(map[string]any)["operations"].(map[string]any)
		wantJSON := map[string]any{
			"completed": float64(8), "succeeded": float64(4), "failed": float64(2), "partial": float64(1), "unknown": float64(1),
			"estimated_requests": float64(3), "uncertain_requests": float64(1), "unpriced_requests": float64(1),
			"unavailable_requests": float64(2), "legacy_unknown_requests": float64(1),
			"estimated_cost_micros": float64(1300), "uncertain_recorded_cost_micros": float64(2050), "legacy_projection_records": float64(0),
		}
		if !reflect.DeepEqual(operations, wantJSON) {
			t.Fatalf("operations JSON = %+v, want %+v", operations, wantJSON)
		}
		n, err := ix.Replay(strings.NewReader(lines.String()))
		if err != nil || n != 8 {
			t.Fatalf("Replay = %d, %v, want 8,nil", n, err)
		}
	}
}

func TestCostlessGroupsRetainObservableTotals(t *testing.T) {
	ix, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	r := evidencedRecord()
	r.ID, r.TS, r.Principal.Team, r.Request.ModelResolved = "costless", time.Now().UTC().Format(time.RFC3339Nano), "costless-team", "costless-model"
	r.Cost = nil
	r.Usage.InputTokens, r.Usage.OutputTokens = 1000, 2000
	if err := ix.Ingest(r); err != nil {
		t.Fatal(err)
	}
	summary, err := ix.Summary(SummaryQuery{SinceDay: DayOf(r.TS), UntilDay: DayOf(r.TS)})
	if err != nil {
		t.Fatal(err)
	}
	want := OperationTotals{Completed: 1, Succeeded: 1, UnavailableRequests: 1}
	if summary.Totals.Operations != want || summary.Totals.Requests != 0 || summary.Totals.InputTokens != 0 || summary.Totals.OutputTokens != 0 || summary.Totals.CostMicros != 0 {
		t.Fatalf("costless totals = %+v", summary.Totals)
	}
	if len(summary.ByTeam) != 1 || summary.ByTeam[0].Operations != want || summary.ByTeam[0].Requests != 0 || summary.ByTeam[0].CostMicros != 0 {
		t.Fatalf("costless team = %+v", summary.ByTeam)
	}
	if len(summary.ByModel) != 1 || summary.ByModel[0].Operations != want || summary.ByModel[0].Requests != 0 || summary.ByModel[0].CostMicros != 0 {
		t.Fatalf("costless model = %+v", summary.ByModel)
	}
	days, err := ix.TimeSeries(TimeSeriesQuery{Days: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || days[0].Operations != want || days[0].Requests != 0 || days[0].CostMicros != 0 || days[0].InputTokens != 0 || days[0].OutputTokens != 0 {
		t.Fatalf("costless day = %+v", days)
	}
}
