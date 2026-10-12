package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/internal/server/routingtest"
	_ "github.com/inferplane/inferplane/providers/anthropic"
	_ "github.com/inferplane/inferplane/providers/openaicompat"
	_ "github.com/inferplane/inferplane/providers/openairesponses"
)

func responseProviderFixture(t *testing.T, kind, endpoint string) *routingtest.Fixture {
	t.Helper()
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{"p": {Type: kind, BaseURL: endpoint}},
		Models: map[string]config.ModelConfig{"premium": {
			ContextWindow: 100000, Capabilities: []string{"tools", "structured_output"},
			Targets: []config.Target{{Provider: "p", Model: "up"}},
		}},
		Pricing: config.PricingConfig{OnMissing: "block", Overrides: map[string]map[string]config.RateConfig{
			"p": {"up": {InputPerMTok: 1, OutputPerMTok: 2}},
		}},
	}
	state, _, err := live.BuildState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	holder := &live.Holder{}
	holder.Swap(state)
	return &routingtest.Fixture{Router: router.New(holder), Holder: holder, Config: cfg}
}

func TestResponsesAnthropicAdapterUsesMessagesEnvelope(t *testing.T) {
	captured := make(chan map[string]json.RawMessage, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Anthropic-Version") != "2023-06-01" {
			t.Error("Anthropic API version header missing")
		}
		var body map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&body)
		captured <- body
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"m","type":"message","role":"assistant","model":"up","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`)
	}))
	defer up.Close()
	f := responseProviderFixture(t, "anthropic", up.URL)
	rec := policyDo(policyMux(f, nil, nil, nil, nil), "/v1/responses", `{"model":"premium","input":"hello","max_output_tokens":32}`, true)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := <-captured
	if len(body["messages"]) == 0 || len(body["input"]) != 0 || len(body["max_output_tokens"]) != 0 || string(body["max_tokens"]) != "32" {
		t.Fatalf("wrong Anthropic wire: %v", body)
	}
}

func TestResponsesDoesNotDropTerminalUsageAfterToolConversionFailure(t *testing.T) {
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"id":"chat","model":"up","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"f","arguments":"{\"path\":"}}]}}]}`+"\n\n"+
			"data: "+`{"id":"chat","model":"up","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`+"\n\n"+
			"data: [DONE]\n\n")
	}))
	defer up.Close()
	f := responseProviderFixture(t, "openai_compatible", up.URL)
	store := budget.NewMemory()
	gov := governance.NewGovernor(map[string]governance.TeamPolicy{"team": {BudgetMicrosPerMonth: 10000, BudgetExceeded: "block"}}, limiter.NewMemory(), store, nil)
	body := `{"model":"premium","input":"hello","stream":true,"tools":[{"type":"function","name":"f","strict":false,"parameters":{"type":"object","properties":{"path":{"type":"string"}}}}]}`
	rec := policyDo(policyMux(f, nil, gov, nil, nil), "/v1/responses", body, true)
	window := budget.CalendarMonthIn(time.UTC)
	if got := store.Spent(budget.Key(budget.ScopeTeam, "team", window), window); got != 14 {
		t.Fatalf("terminal usage lost after truncated arguments: spent=%d, body=%s", got, rec.Body.String())
	}
	if calls.Load() != 1 || strings.Contains(rec.Body.String(), `"status":"completed"`) && strings.Contains(rec.Body.String(), "event: response.completed") {
		t.Fatal("truncated tool output retried or reported as successful")
	}
}

func TestFailedNativeUsageIsSettledBeforeRetryAdmission(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(fmt.Sprint("strict=", strict), func(t *testing.T) {
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"r","object":"response","status":"failed","model":"up","output":[],"error":{"message":"failure"},"usage":{"input_tokens":10,"output_tokens":2}}`)
			}))
			defer first.Close()
			var retried atomic.Int64
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				retried.Add(1)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"r2","object":"response","status":"completed","model":"up","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
			}))
			defer second.Close()
			f := responseProviderFixture(t, "openai_responses", first.URL)
			f.Config.Providers["second"] = config.ProviderConfig{Type: "openai_responses", BaseURL: second.URL}
			f.Config.Pricing.Overrides["second"] = map[string]config.RateConfig{"up": {InputPerMTok: 1, OutputPerMTok: 2}}
			m := f.Config.Models["premium"]
			m.Targets = append(m.Targets, config.Target{Provider: "second", Model: "up"})
			f.Config.Models["premium"] = m
			state, _, err := live.BuildState(f.Config)
			if err != nil {
				t.Fatal(err)
			}
			f.Holder.Swap(state)
			store := budget.NewMemory()
			limit := int64(12)
			if strict {
				limit = 1000
			}
			gov := governance.NewGovernor(map[string]governance.TeamPolicy{"team": {BudgetMicrosPerMonth: limit, BudgetExceeded: "block"}}, limiter.NewMemory(), store, nil)
			window := budget.CalendarMonthIn(time.UTC)
			if strict {
				f.Router.SetBudgetConstraintGate(func(keystore.Principal) map[string]string {
					if store.Spent(budget.Key(budget.ScopeTeam, "team", window), window) >= 10 {
						return map[string]string{"premium": "economy"}
					}
					return nil
				})
			}
			rec := policyDo(policyMux(f, nil, gov, nil, nil), "/v1/responses", `{"model":"premium","input":"hello"}`, true)
			spent := store.Spent(budget.Key(budget.ScopeTeam, "team", window), window)
			if spent != 14 || retried.Load() != 0 || rec.Code != 402 {
				t.Fatalf("failed usage or retry admission lost: spent=%d retries=%d status=%d", spent, retried.Load(), rec.Code)
			}
		})
	}
}

// A native target forwards with the gateway's shared upstream credential, so a
// stored-state reference would let any caller who learns another user's
// response, conversation or item ID continue that user's provider-held state.
func TestResponsesRefusesStoredStateReferencesBeforeDispatch(t *testing.T) {
	for _, extra := range []string{
		`"previous_response_id":"resp_other_user"`,
		`"conversation":"conv_other_user"`,
		`"conversation":{"id":"conv_other_user"}`,
		`"input":[{"type":"item_reference","id":"msg_other_user"}]`,
	} {
		t.Run(extra, func(t *testing.T) {
			var calls atomic.Int64
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"r","object":"response","status":"completed","model":"up","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
			}))
			defer up.Close()
			f := responseProviderFixture(t, "openai_responses", up.URL)
			body := `{"model":"premium",` + extra
			if !strings.Contains(extra, `"input"`) {
				body += `,"input":"hello"`
			}
			rec := policyDo(policyMux(f, nil, nil, nil, nil), "/v1/responses", body+`}`, true)
			if rec.Code != 400 || calls.Load() != 0 {
				t.Fatalf("stored reference dispatched: status=%d calls=%d body=%s", rec.Code, calls.Load(), rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "other_user") {
				t.Fatal("error echoed the submitted reference")
			}
		})
	}
}

func TestResponsesNativeStillForwardsStatelessRequests(t *testing.T) {
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"r","object":"response","status":"completed","model":"up","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer up.Close()
	f := responseProviderFixture(t, "openai_responses", up.URL)
	for _, body := range []string{
		`{"model":"premium","input":"hello"}`,
		`{"model":"premium","input":"hello","store":true}`,
		`{"model":"premium","input":"hello","previous_response_id":null,"conversation":null}`,
	} {
		if rec := policyDo(policyMux(f, nil, nil, nil, nil), "/v1/responses", body, true); rec.Code != 200 {
			t.Fatalf("stateless native request refused: body=%s status=%d resp=%s", body, rec.Code, rec.Body.String())
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("calls=%d", calls.Load())
	}
}
