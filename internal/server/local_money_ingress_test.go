package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/server/routingtest"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
)

const localConverseBody = `{"messages":[{"role":"user","content":[{"text":"hello"}]}],"inferenceConfig":{"maxTokens":32}}`

// Only provider I/O is replaced. Native frames carry canonical observations at
// the same seam as the real Converse provider, through the real shared handler.
type localConverseProvider struct{ *routingtest.Spy }

func (localConverseProvider) SupportsBedrockConverse(string) bool { return true }

func (p localConverseProvider) Complete(ctx context.Context, req *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	resp, err := p.Spy.Complete(ctx, req)
	if resp != nil {
		usage, _ := json.Marshal(localConverseWireUsage(p.Usage))
		resp.RawBody = append([]byte(`{"output":{"message":{"role":"assistant","content":[{"text":"hello"}]}},"stopReason":"end_turn","usage":`), usage...)
		resp.RawBody = append(resp.RawBody, '}')
	}
	return resp, err
}

func (p localConverseProvider) Stream(ctx context.Context, req *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	if _, err := p.Spy.Complete(ctx, req); err != nil {
		return nil, err
	}
	frame := func(eventType, payload string) []byte {
		var raw bytes.Buffer
		err := eventstream.NewEncoder().Encode(&raw, eventstream.Message{
			Headers: eventstream.Headers{
				{Name: ":message-type", Value: eventstream.StringValue("event")},
				{Name: ":event-type", Value: eventstream.StringValue(eventType)},
			},
			Payload: []byte(payload),
		})
		if err != nil {
			panic(err)
		}
		return raw.Bytes()
	}
	return func(yield func(*providers.StreamEvent, error) bool) {
		metadata, _ := json.Marshal(map[string]any{"usage": localConverseWireUsage(p.Usage)})
		if !yield(&providers.StreamEvent{Raw: frame("metadata", string(metadata)),
			Chunk: &schema.ChatChunk{Type: "usage", Usage: p.Usage}}, nil) {
			return
		}
		if p.Partial {
			yield(nil, errors.New("interrupted"))
			return
		}
		yield(&providers.StreamEvent{Raw: frame("messageStop", `{"stopReason":"end_turn"}`),
			Chunk: &schema.ChatChunk{Type: "message_stop"}}, nil)
	}, nil
}

func localConverseWireUsage(usage *schema.Usage) map[string]int64 {
	if usage == nil {
		return nil
	}
	wire := make(map[string]int64)
	if usage.InputTokens != nil {
		wire["inputTokens"] = *usage.InputTokens
	}
	if usage.OutputTokens != nil {
		wire["outputTokens"] = *usage.OutputTokens
	}
	return wire
}

func localConverseFixture(t *testing.T) *routingtest.Fixture {
	t.Helper()
	f := routingtest.New(t, func(cfg *config.Config) {
		cfg.Pricing.Overrides["public"]["up"] = config.RateConfig{InputPerMTok: 1, OutputPerMTok: 2}
	})
	st := f.Holder.Load()
	provs := st.Providers()
	provs["public"] = localConverseProvider{f.Public}
	f.Holder.Swap(live.NewState(provs, f.Config.Models, st.Pricing(), map[string]string{
		"public": "public", "private": "private", "retry": "retry",
	}))
	return f
}

func localConverseMoneyMux(f *routingtest.Fixture, g *governance.Governor, keyDay int64) http.Handler {
	store := stubStore{key: "dev-key", p: keystore.Principal{
		KeyID: "opaque-key", Team: "team", AllowedModels: []string{"*"},
		KeyOptions: keystore.KeyOptions{Owner: "user", BudgetUSDMicrosPerDay: keyDay},
	}}
	return DataMux(f.Router, f.Holder, store, nil, g, nil, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0)
}

func TestNativeConverseLocalMoneyRetainsUnknownAndDebitsCompleteOnce(t *testing.T) {
	for _, scenario := range liabilityInspectionScenarios([]policyIngress{
		{"bedrock-converse", "/model/premium/converse", false},
		{"bedrock-converse", "/model/premium/converse-stream", true},
	}) {
		for _, scope := range []string{"team-month", "key-day"} {
			for _, kind := range []string{"missing-usage", "input-only", "complete"} {
				t.Run(scenario.ingress.name()+"/"+scenario.mode+"/"+scope+"/"+kind, func(t *testing.T) {
					f := localConverseFixture(t)
					inspection := qualifyLiabilityInspection(f, scenario.mode)
					if kind != "missing-usage" {
						in, out := int64(1), int64(1)
						f.Public.Usage = &schema.Usage{InputTokens: &in}
						if kind == "complete" {
							f.Public.Usage.OutputTokens = &out
						} else {
							f.Public.Partial = true
						}
					}
					teamLimit, keyDay := int64(1), int64(0)
					if scope == "key-day" {
						teamLimit, keyDay = 100, 1
					}
					g := governance.NewGovernor(map[string]governance.TeamPolicy{
						"team": {BudgetMicrosPerMonth: teamLimit, BudgetExceeded: "block"},
					}, limiter.NewMemory(), budget.NewMemory(), nil)
					h := localConverseMoneyMux(f, g, keyDay)
					path := scenario.ingress.path
					first := policyDo(h, path, localConverseBody, true)
					if first.Code != 200 || len(f.Public.Calls) != 1 || f.Public.Calls[0].Protocol != "bedrock-converse" {
						t.Fatalf("native dispatch missing: status=%d calls=%+v body=%s", first.Code, f.Public.Calls, first.Body.String())
					}
					requireLiabilityInspection(t, inspection, "bedrock-converse", scenario.mode)
					status := g.UsageOf(governance.Subject{Team: "team", KeyID: "opaque-key"},
						governance.KeyPolicy{BudgetMicrosPerDay: keyDay})
					wantSpent := map[string]int64{"missing-usage": 0, "input-only": 1, "complete": 3}[kind]
					for _, money := range []*governance.BudgetUsage{status.TeamBudget, status.KeyBudgetDay} {
						if money == nil {
							continue
						}
						if money.SpentUSDMicros != wantSpent || (money.ReservedUSDMicros == 0) != (kind == "complete") {
							t.Fatalf("native money settlement is lost or duplicated: %+v want spend=%d", money, wantSpent)
						}
					}
					next := policyDo(h, path, localConverseBody, true)
					if next.Code != 402 || len(f.Public.Calls) != 1 {
						t.Fatalf("native retry bypassed independent cap: status=%d calls=%d body=%s", next.Code, len(f.Public.Calls), next.Body.String())
					}
				})
			}
		}
	}
}

func TestNativeConverseEvaluationObserverDoesNotDoubleDebit(t *testing.T) {
	f := localConverseFixture(t)
	in, out := int64(1), int64(1)
	f.Public.Usage = &schema.Usage{InputTokens: &in, OutputTokens: &out}
	g := governance.NewGovernor(map[string]governance.TeamPolicy{
		"team": {BudgetMicrosPerMonth: 100, BudgetExceeded: "block"},
	}, limiter.NewMemory(), budget.NewMemory(), nil)
	h := WithEvaluations(http.NotFoundHandler(), localConverseMoneyMux(f, g, 100),
		[]string{"admin-fixture"}, nil, adminauth.MappingConfig{}, nil)
	req := httptest.NewRequest("POST", "/admin/evaluations", strings.NewReader(
		`{"protocol":"bedrock-converse","model":"premium","body":`+localConverseBody+`}`))
	req.Header.Set("Authorization", "Bearer admin-fixture")
	req.Header.Set("X-Inferplane-Virtual-Key", "dev-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var result evaluationResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || result.HTTPStatus != 200 || result.Attempts != 1 ||
		!result.AccountingKnown || !result.Settled || result.Cost == nil || result.Cost.AmountUSDMicros != 3 {
		t.Fatalf("observer lost complete native settlement: %d %+v", rec.Code, result)
	}
	status := g.UsageOf(governance.Subject{Team: "team", KeyID: "opaque-key"}, governance.KeyPolicy{BudgetMicrosPerDay: 100})
	for _, money := range []*governance.BudgetUsage{status.TeamBudget, status.KeyBudgetDay} {
		if money.SpentUSDMicros != 3 || money.ReservedUSDMicros != 0 {
			t.Fatalf("observer duplicated native monetary settlement: %+v", money)
		}
	}
}

// Exercise the real authentication, PreCheck, provider-attempt and settlement
// wiring of every ingress. Losing a final usage observation must not permit
// repeated billable requests against the same tiny blocking cap.
func TestEveryStreamingIngressRetainsUnknownLocalMoney(t *testing.T) {
	for _, scenario := range liabilityInspectionScenarios(authorityIngresses()) {
		ingress := scenario.ingress
		if !ingress.stream {
			continue
		}
		for _, kind := range []string{"missing-usage", "interrupted-input-only", "complete"} {
			t.Run(ingress.name()+"/"+scenario.mode+"/"+kind, func(t *testing.T) {
				f := routingtest.New(t, func(cfg *config.Config) {
					cfg.Pricing.Overrides["public"]["up"] = config.RateConfig{InputPerMTok: 1, OutputPerMTok: 2}
				})
				inspection := qualifyLiabilityInspection(f, scenario.mode)
				if kind != "missing-usage" {
					in, out := int64(1), int64(1)
					f.Public.Usage = &schema.Usage{InputTokens: &in}
					if kind == "complete" {
						f.Public.Usage.OutputTokens = &out
					} else {
						f.Public.Partial = true
					}
				}
				g := governance.NewGovernor(map[string]governance.TeamPolicy{
					"team": {BudgetMicrosPerMonth: 1, BudgetExceeded: "block"},
				}, limiter.NewMemory(), budget.NewMemory(), nil)
				h := policyMux(f, nil, g, nil, nil)
				first := policyDo(h, ingress.path, authorityBody(ingress), true)
				if first.Code != http.StatusOK || len(f.Public.Calls) != 1 {
					t.Fatalf("first below-cap attempt must dispatch: status=%d calls=%d body=%s", first.Code, len(f.Public.Calls), first.Body.String())
				}
				requireLiabilityInspection(t, inspection, ingress.protocol, scenario.mode)
				status := g.UsageOf(governance.Subject{Team: "team"}, governance.KeyPolicy{}).TeamBudget
				wantSpent := map[string]int64{"missing-usage": 0, "interrupted-input-only": 1, "complete": 3}[kind]
				if status.SpentUSDMicros != wantSpent {
					t.Fatalf("known observed cost=%d want=%d", status.SpentUSDMicros, wantSpent)
				}
				if kind != "complete" && status.ReservedUSDMicros == 0 {
					t.Fatal("incomplete observation released local liability")
				}
				if kind == "complete" && status.ReservedUSDMicros != 0 {
					t.Fatal("proved complete observation retained unused liability")
				}
				next := policyDo(h, ingress.path, authorityBody(ingress), true)
				if next.Code != http.StatusPaymentRequired || len(f.Public.Calls) != 1 {
					t.Fatalf("repeated attempt bypassed money: status=%d calls=%d body=%s", next.Code, len(f.Public.Calls), next.Body.String())
				}
			})
		}
	}
}
