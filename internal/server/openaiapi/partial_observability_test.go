package openaiapi

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/metrics"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/internal/tracing"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// usageThenErrProvider yields ONE usage-bearing frame, then breaks mid-stream —
// the shape needed to see whether a settled partial stream is also metered.
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
			Chunk: &schema.ChatChunk{Type: "message_start", Message: &schema.ChatResponse{
				ID: "msg", Type: "message", Role: "assistant", Model: "up",
				Content: []schema.ContentBlock{},
				Usage:   partialUsage(),
			}},
		}
		if !yield(ev, nil) {
			return
		}
		yield(nil, errors.New("upstream broke"))
	}, nil
}

func partialHandler(m *metrics.Metrics) *ChatHandler {
	provs := map[string]providers.Provider{"p": usageThenErrProvider{}}
	models := map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "up"}}}}
	gov := governance.NewGovernor(nil, limiter.NewMemory(), budget.NewMemory(), nil)
	return NewChatHandlerMetrics(router.New(holderFor(provs, models)), nil, gov, m)
}

func partialStreamReq() *http.Request {
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	return req.WithContext(principal.With(req.Context(), keystore.Principal{
		KeyID: "ik", Team: "t", AllowedModels: []string{"*"},
	}))
}

// TestChatPartialStreamCountsTokensIntoMetrics — see the anthropicapi twin: the
// partial branch settled and billed the delivered tokens but never called
// observeTokens, so the counters under-reported every interrupted stream.
func TestChatPartialStreamCountsTokensIntoMetrics(t *testing.T) {
	m := metrics.New()
	h := partialHandler(m)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, partialStreamReq())
	if rec.Code != 200 {
		t.Fatalf("status %d (already committed before the break)", rec.Code)
	}

	got := partialTokenUsage(t, m)
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

func TestChatPartialStreamMarksSpanPartialAndErrored(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tracing.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr)))
	t.Cleanup(tracing.Disable)

	h := partialHandler(metrics.New())
	h.ServeHTTP(httptest.NewRecorder(), partialStreamReq())

	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("want 1 span, got %d", len(ended))
	}
	if got := ended[0].Status().Code.String(); got != "Error" {
		t.Errorf("partial span status = %s, want Error", got)
	}
	var partial bool
	for _, a := range ended[0].Attributes() {
		if string(a.Key) == "inferplane.response.partial" {
			partial = a.Value.AsBool()
		}
	}
	if !partial {
		t.Error("span missing the partial marker")
	}
}

func TestUsageRefKeepsCacheWriteTiers(t *testing.T) {
	u := usageRef(partialUsage())
	if u.CacheCreation5mInputTokens != 20 || u.CacheCreation1hInputTokens != 4 {
		t.Errorf("tiers lost: %+v", u)
	}
	if u.CacheCreationInputTokens != 24 {
		t.Errorf("flat total = %d, want 24 (the sum billed)", u.CacheCreationInputTokens)
	}
}

// partialTokenUsage sums gen_ai_client_token_usage_total per type label,
// skipping the governance pre-check's zero placeholder (empty model label).
func partialTokenUsage(t *testing.T, m *metrics.Metrics) map[string]float64 {
	t.Helper()
	fams, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, f := range fams {
		if f.GetName() != "gen_ai_client_token_usage_total" {
			continue
		}
		for _, mm := range f.Metric {
			var typ, model string
			for _, l := range mm.Label {
				switch l.GetName() {
				case "type":
					typ = l.GetValue()
				case "model":
					model = l.GetValue()
				}
			}
			if model == "" {
				continue
			}
			out[typ] += mm.GetCounter().GetValue()
		}
	}
	return out
}
