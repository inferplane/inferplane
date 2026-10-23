package analytics

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplayUpgradesLegacyProjectionOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE events (
		id TEXT PRIMARY KEY, day TEXT NOT NULL DEFAULT '', team TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '', provider TEXT NOT NULL DEFAULT '', status INTEGER NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
		cache_read INTEGER NOT NULL DEFAULT 0, cache_creation INTEGER NOT NULL DEFAULT 0,
		cost_micros INTEGER NOT NULL DEFAULT 0);
		INSERT INTO events(id,day,team,model,status,cost_micros) VALUES ('old','2026-10-21','team','model',200,17),
		('missing-original','2026-10-21','team','model',200,42);`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ix, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	summary, err := ix.Summary(SummaryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	wantLegacy := OperationTotals{Completed: 2, Unknown: 2, LegacyUnknownRequests: 2, UncertainRecordedCostMicros: 59, LegacyProjectionRecords: 2}
	if summary.Totals.Operations != wantLegacy || summary.Totals.Requests != 2 || summary.Totals.CostMicros != 59 {
		t.Fatalf("migrated totals = %+v", summary.Totals)
	}
	events, err := ix.Recent(10, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.ProjectionVersion != 0 || !event.HasCost || event.OutcomeState != "unknown" || event.CostState != "legacy_unknown" || event.Partial || event.ErrorCode != "" {
			t.Errorf("migration invented evidence: %+v", event)
		}
	}
	r := evidencedRecord()
	r.ID, r.TS, r.Principal.Team, r.Request.ModelResolved = "old", "2026-10-21T10:00:00Z", "team", "model"
	r.Cost.AmountUSDMicros = 17
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if n, err := ix.Replay(strings.NewReader(string(raw) + "\n")); err != nil || n != 1 {
			t.Fatalf("Replay = %d, %v", n, err)
		}
		summary, err = ix.Summary(SummaryQuery{})
		if err != nil {
			t.Fatal(err)
		}
		want := OperationTotals{Completed: 2, Succeeded: 1, Unknown: 1, EstimatedRequests: 1, LegacyUnknownRequests: 1,
			EstimatedCostMicros: 17, UncertainRecordedCostMicros: 42, LegacyProjectionRecords: 1}
		if summary.Totals.Operations != want || summary.Totals.Requests != 2 || summary.Totals.CostMicros != 59 {
			t.Fatalf("replayed totals = %+v", summary.Totals)
		}
	}
	// A later conflicting original never replaces a version=1 first write.
	r.Cost.AmountUSDMicros, r.Principal.Team, r.Request.ModelResolved = 999, "other-team", "other-model"
	r.Outcome.Status, r.Outcome.Partial = 500, true
	raw, err = json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Replay(strings.NewReader(string(raw) + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := ix.Ingest(r); err != nil {
		t.Fatal(err)
	}
	events, err = ix.Recent(10, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		switch event.ID {
		case "old":
			if event.ProjectionVersion != 1 || event.OutcomeState != "succeeded" || event.CostState != "estimated" || event.CostMicros != 17 || event.Team != "team" || event.Model != "model" {
				t.Errorf("version=1 overwritten: %+v", event)
			}
		case "missing-original":
			if event.ProjectionVersion != 0 || event.OutcomeState != "unknown" || event.CostState != "legacy_unknown" {
				t.Errorf("missing original recovered without evidence: %+v", event)
			}
		}
	}
}

func TestReplayKeepsTruncatedTailOut(t *testing.T) {
	ix, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	// Even a syntactically complete final record requires the JSONL newline.
	complete := `{"event":"request_completed","id":"complete","outcome":{"status":500}}` + "\n"
	tail := `{"event":"request_completed","id":"tail","outcome":{"status":200}}`
	started := `{"event":"request_started","id":"started","cost":{"amount_usd_micros":9}}` + "\n"
	n, err := ix.Replay(strings.NewReader(complete + started + "malformed\n" + tail))
	if err != nil || n != 1 {
		t.Fatalf("Replay = %d, %v, want 1,nil", n, err)
	}
	events, err := ix.Recent(10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != "complete" || events[0].HasCost || events[0].OutcomeState != "failed" || events[0].CostState != "unavailable" {
		t.Fatalf("Replay rows = %+v", events)
	}
	h, err := ix.Health()
	if err != nil || h.LastIngestTS == "" {
		t.Fatalf("costless replay freshness = %+v, %v", h, err)
	}
}
