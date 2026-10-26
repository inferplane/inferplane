package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/tracing"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestResponsesRetryPreservesCostUsageAndAuditEvidence(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tracing.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr)))
	t.Cleanup(tracing.Disable)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if request.Model == "up" {
			fmt.Fprint(w, `{"id":"first","model":"remote-alias","status":"failed","output":[],"usage":{"input_tokens":100,"output_tokens":5,"input_tokens_details":{"cached_tokens":70,"cache_write_tokens":20}}}`)
		} else {
			fmt.Fprint(w, `{"id":"second","model":"remote-alias","status":"completed","output":[],"usage":{"input_tokens":0,"output_tokens":0}}`)
		}
	}))
	defer up.Close()
	f := responseProviderFixture(t, "openai_responses", up.URL)
	f.Config.Providers["backup"] = config.ProviderConfig{Type: "openai_responses", BaseURL: up.URL}
	m := f.Config.Models["premium"]
	m.Targets = append(m.Targets, config.Target{Provider: "backup", Model: "backup-up"})
	f.Config.Models["premium"] = m
	f.Config.Pricing.Version = "fixture-price"
	f.Config.Pricing.Overrides["backup"] = map[string]config.RateConfig{"backup-up": {InputPerMTok: 2, OutputPerMTok: 4}}
	state, _, err := live.BuildState(f.Config)
	if err != nil {
		t.Fatal(err)
	}
	f.Holder.Swap(state)
	store := budget.NewMemory()
	gov := governance.NewGovernor(map[string]governance.TeamPolicy{"team": {BudgetMicrosPerMonth: 10_000_000, BudgetExceeded: "block"}}, limiter.NewMemory(), store, nil)
	aud, lines := policyAudit(t)
	closeAudit := sync.OnceValue(aud.Close)
	t.Cleanup(func() { _ = closeAudit() })
	rec := policyDo(policyMux(f, aud, gov, nil, nil), "/v1/responses", `{"model":"premium","input":"hello"}`, true)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := closeAudit(); err != nil {
		t.Fatal(err)
	}
	var completed []audit.Record
	for _, line := range strings.Split(strings.TrimSpace(lines.String()), "\n") {
		var record audit.Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record.Event == "request_completed" {
			completed = append(completed, record)
		}
	}
	if len(completed) != 2 || completed[0].Usage.Evidence == nil || !completed[0].Usage.Evidence.AccountingUncertain || completed[0].Usage.Evidence.CacheWriteTTLKnown || completed[1].Usage.Evidence.AccountingUncertain {
		t.Fatalf("audit evidence lost: %+v", completed)
	}
	spans := sr.Ended()
	if len(spans) != 1 || len(spans[0].Events()) != 2 {
		t.Fatalf("expected two cost events in one request span: %+v", spans)
	}
	for i, event := range spans[0].Events() {
		attrs := map[string]attribute.Value{}
		for _, a := range event.Attributes {
			attrs[string(a.Key)] = a.Value
		}
		if attrs["gen_ai.response.model"].AsString() != []string{"up", "backup-up"}[i] || attrs["inferplane.provider.name"].AsString() != []string{"p", "backup"}[i] {
			t.Fatal("retry cost lost routed model/provider")
		}
		if attrs["inferplane.cost.amount_usd_micros"].AsInt64() != []int64{52, 0}[i] || attrs["inferplane.usage.input_tokens_total"].AsInt64() != []int64{100, 0}[i] {
			t.Fatal("retry cost/usage overwritten or cache counted twice")
		}
		if attrs["inferplane.usage.accounting_uncertain"].AsBool() != (i == 0) || attrs["inferplane.cost.pricing_version"].AsString() != "fixture-price" {
			t.Fatal("retry pricing/evidence lost")
		}
	}
	window := budget.CalendarMonthIn(time.UTC)
	// The legacy cost evidence includes an explicitly uncertain 5m cache-write
	// estimate. Only independently priced input/output/read are observed spend;
	// the failed attempt's whole liability must remain reserved for unknown TTL.
	if got := store.Spent(budget.Key(budget.ScopeTeam, "team", window), window); got != 27 {
		t.Fatalf("known categories lost or unknown TTL estimate treated as actual spend: %d", got)
	}
	if status := gov.UsageOf(governance.Subject{Team: "team"}, governance.KeyPolicy{}).TeamBudget; status.ReservedUSDMicros == 0 {
		t.Fatalf("unknown cache TTL released the failed attempt's financial liability: %+v", status)
	}
	for _, a := range spans[0].Attributes() {
		if a.Key == "inferplane.usage.cache_read_input_tokens" && a.Value.AsInt64() != 0 {
			t.Fatal("final span inherited prior cache tokens")
		}
	}
}
