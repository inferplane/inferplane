package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/live"
)

func TestNativeResponsesContextTiersReachBudgetSettlement(t *testing.T) {
	for _, total := range []int64{272000, 272001} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("input=%d/stream=%v", total, stream), func(t *testing.T) {
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					response := fmt.Sprintf(`{"id":"r","model":"up","status":"completed","output":[],"usage":{"input_tokens":%d,"input_tokens_details":{"cached_tokens":270000},"output_tokens":10000}}`, total)
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "event: response.completed\ndata: "+`{"type":"response.completed","response":`+response+"}\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, response)
					}
				}))
				defer up.Close()
				f := responseProviderFixture(t, "openai_responses", up.URL)
				model := f.Config.Models["premium"]
				model.ContextWindow = 1000000
				f.Config.Models["premium"] = model
				f.Config.Pricing.Overrides["p"]["up"] = config.RateConfig{
					InputPerMTok: 2, OutputPerMTok: 10, CacheReadPerMTok: .1, ShortContextLimit: 272000,
					LongContext: &config.RateConfig{InputPerMTok: 4, OutputPerMTok: 15, CacheReadPerMTok: .2},
				}
				state, _, err := live.BuildState(f.Config)
				if err != nil {
					t.Fatal(err)
				}
				f.Holder.Swap(state)
				store := budget.NewMemory()
				gov := governance.NewGovernor(map[string]governance.TeamPolicy{"team": {BudgetMicrosPerMonth: 10000000, BudgetExceeded: "block"}}, limiter.NewMemory(), store, nil)
				body, _ := json.Marshal(map[string]any{"model": "premium", "input": "hello", "stream": stream})
				rec := policyDo(policyMux(f, nil, gov, nil, nil), "/v1/responses", string(body), true)
				if rec.Code != 200 {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
				}
				want := int64(131000)
				if total > 272000 {
					want = 212004
				}
				window := budget.CalendarMonthIn(time.UTC)
				if got := store.Spent(budget.Key(budget.ScopeTeam, "team", window), window); got != want {
					t.Fatalf("settlement=%d want=%d; inclusive input/cache must trigger one whole-response tier", got, want)
				}
			})
		}
	}
}

func TestNativeResponsesRefusesUnpricedServiceTierWithoutAnAttempt(t *testing.T) {
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer up.Close()
	f := responseProviderFixture(t, "openai_responses", up.URL)
	f.Config.Pricing.Overrides["p"]["up"] = config.RateConfig{InputPerMTok: 2, OutputPerMTok: 10, ShortContextLimit: 272000, LongContext: &config.RateConfig{InputPerMTok: 4, OutputPerMTok: 15}}
	state, _, err := live.BuildState(f.Config)
	if err != nil {
		t.Fatal(err)
	}
	f.Holder.Swap(state)
	store := budget.NewMemory()
	gov := governance.NewGovernor(map[string]governance.TeamPolicy{"team": {BudgetMicrosPerMonth: 1000000, BudgetExceeded: "block"}}, limiter.NewMemory(), store, nil)
	rec := policyDo(policyMux(f, nil, gov, nil, nil), "/v1/responses", `{"model":"premium","input":"hello","service_tier":"ultrafast"}`, true)
	if rec.Code != 402 || calls.Load() != 0 {
		t.Fatalf("unpriced service reached upstream: status=%d calls=%d", rec.Code, calls.Load())
	}
	window := budget.CalendarMonthIn(time.UTC)
	if store.Spent(budget.Key(budget.ScopeTeam, "team", window), window) != 0 {
		t.Fatal("refused request billed")
	}
}
