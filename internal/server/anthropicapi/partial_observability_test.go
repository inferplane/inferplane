package anthropicapi

import (
	"context"
	"errors"
	"iter"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/metrics"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/internal/tracing"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// usageThenErrProvider yields ONE usage-bearing message_start frame (the real
// Anthropic shape: input + cache counts nested under message.usage) and then
// breaks mid-stream. midStreamErrProvider above yields Raw bytes with no Chunk,
// so no usage is ever observed — which is exactly why it could not catch a
// partial stream that was settled and billed but never counted into the token
// metrics.
type usageThenErrProvider struct{}

func (usageThenErrProvider) Name() string               { return "midstream" }
func (usageThenErrProvider) Models() []schema.ModelInfo { return nil }
func (usageThenErrProvider) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	return nil, errors.New("unused")
}

func partialUsage() *schema.Usage {
	in, out, cacheRead, w5, w1h := int64(10), int64(3), int64(40), int64(20), int64(4)
	return &schema.Usage{
		InputTokens:          &in,
		OutputTokens:         &out,
		CacheReadInputTokens: &cacheRead,
		CacheCreation: &schema.CacheCreation{
			Ephemeral5mInputTokens: &w5,
			Ephemeral1hInputTokens: &w1h,
		},
	}
}

func (usageThenErrProvider) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	return func(yield func(*providers.StreamEvent, error) bool) {
		ev := &providers.StreamEvent{
			Raw: []byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n"),
			Chunk: &schema.ChatChunk{Type: "message_start", Message: &schema.ChatResponse{
				Usage: partialUsage(),
			}},
		}
		if !yield(ev, nil) {
			return
		}
		yield(nil, errors.New("upstream broke"))
	}, nil
}

func partialHandler(m *metrics.Metrics) *MessagesHandler {
	provs := map[string]providers.Provider{"p": usageThenErrProvider{}}
	models := map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "up"}}}}
	gov := governance.NewGovernor(nil, limiter.NewMemory(), budget.NewMemory(), nil)
	return NewMessagesHandlerMetrics(router.New(holderFor(provs, models)), nil, gov, m)
}

func partialStreamReq() *httptest.ResponseRecorder {
	return httptest.NewRecorder()
}

// TestMessagesPartialStreamCountsTokensIntoMetrics: an interrupted stream debits
// quota, spends budget, and records into the usage collector, so the token
// counters must move by the same amounts. Before this fix the partial branch
// called settle() but not observeTokens(), leaving
// gen_ai_client_token_usage_total permanently below the billed spend.
func TestMessagesPartialStreamCountsTokensIntoMetrics(t *testing.T) {
	m := metrics.New()
	h := partialHandler(m)

	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"m","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	rec := partialStreamReq()
	h.ServeHTTP(rec, allowAll(req))
	if rec.Code != 200 {
		t.Fatalf("status %d (already committed before the break)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "event: error") {
		t.Fatalf("expected the interrupted stream to surface an error event: %s", rec.Body.String())
	}

	got := tokenUsageByType(t, m)
	for _, c := range []struct {
		typ  string
		want float64
	}{
		{"input", 10},
		{"output", 3},
		{"cache_read", 40},
		{"cache_write_5m", 20},
		{"cache_write_1h", 4},
	} {
		if got[c.typ] != c.want {
			t.Errorf("token_usage[%s] = %v, want %v (full series: %v)", c.typ, got[c.typ], c.want, got)
		}
	}
}

// TestMessagesPartialStreamMarksSpanPartialAndErrored: the wire status stays 200
// (it was committed before the break), but a truncated response must be
// distinguishable in traces — otherwise an interrupted stream looks identical to
// a clean one, which the audit record already avoids via Outcome.Partial.
func TestMessagesPartialStreamMarksSpanPartialAndErrored(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tracing.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr)))
	t.Cleanup(tracing.Disable)

	h := partialHandler(metrics.New())
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"m","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	h.ServeHTTP(partialStreamReq(), allowAll(req))

	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("want 1 span, got %d", len(ended))
	}
	span := ended[0]
	if got := span.Status().Code.String(); got != "Error" {
		t.Errorf("partial span status = %s, want Error", got)
	}
	var partial bool
	ints := map[string]int64{}
	for _, a := range span.Attributes() {
		if string(a.Key) == "inferplane.response.partial" {
			partial = a.Value.AsBool()
		}
		if a.Value.Type().String() == "INT64" {
			ints[string(a.Key)] = a.Value.AsInt64()
		}
	}
	if !partial {
		t.Error("span missing the partial marker")
	}
	for k, want := range map[string]int64{
		"inferplane.usage.cache_read_input_tokens":     40,
		"inferplane.usage.cache_write_5m_input_tokens": 20,
		"inferplane.usage.cache_write_1h_input_tokens": 4,
	} {
		if ints[k] != want {
			t.Errorf("%s = %d, want %d (all: %v)", k, ints[k], want, ints)
		}
	}
	if _, ok := ints["inferplane.cost.amount_usd_micros"]; !ok {
		t.Errorf("settled cost missing from the span (all: %v)", ints)
	}
}

// TestUsageRefKeepsCacheWriteTiers: the audit record must be able to reproduce
// the billed 1.25x/2x split. Anthropic sends the tiers under cache_creation and
// omits the flat total, so mapping only the flat field wrote a zero.
func TestUsageRefKeepsCacheWriteTiers(t *testing.T) {
	u := usageRef(partialUsage())
	if u.CacheCreation5mInputTokens != 20 || u.CacheCreation1hInputTokens != 4 {
		t.Errorf("tiers lost: %+v", u)
	}
	if u.CacheCreationInputTokens != 24 {
		t.Errorf("flat total = %d, want 24 (the sum billed) — existing consumers read this field", u.CacheCreationInputTokens)
	}
}
