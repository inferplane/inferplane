package anthropicapi

import (
	"context"
	"iter"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
)

type validationProvider struct {
	calls int
}

func (*validationProvider) Name() string               { return "validation" }
func (*validationProvider) Models() []schema.ModelInfo { return nil }
func (p *validationProvider) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	p.calls++
	return nil, &providers.UpstreamError{StatusCode: 400, Body: []byte(`{"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"prompt is too long: 1000 tokens > 100 maximum"}}`)}
}
func (p *validationProvider) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	_, err := p.Complete(context.Background(), nil)
	return nil, err
}

func TestMessagesValidationFailureNeverReplaysHistory(t *testing.T) {
	for _, stream := range []string{"false", "true"} {
		t.Run(stream, func(t *testing.T) {
			bad := &validationProvider{}
			models := map[string]config.ModelConfig{
				"large": {Targets: []config.Target{{Provider: "bad", Model: "large"}}},
				"small": {Targets: []config.Target{{Provider: "good", Model: "small"}}},
			}
			provs := map[string]providers.Provider{"bad": bad, "good": mockprovider.New("small")}
			h := NewMessagesHandler(router.New(holderForWithFallbacks(provs, models, map[string]string{"large": "small"})))
			req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(
				`{"model":"large","stream":`+stream+`,"max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, allowAll(req))
			if rec.Code != 400 || bad.calls != 1 || rec.Header().Get("x-inferplane-fallback") != "" ||
				!strings.Contains(rec.Body.String(), "prompt is too long") {
				t.Fatalf("validation failure replayed: status=%d calls=%d body=%s headers=%v", rec.Code, bad.calls, rec.Body.String(), rec.Header())
			}
		})
	}
}

func TestMessagesFallbackChecksItsOwnSmallerContextWindow(t *testing.T) {
	for _, stream := range []string{"false", "true"} {
		t.Run(stream, func(t *testing.T) {
			models := map[string]config.ModelConfig{
				"large": {ContextWindow: 10000, Targets: []config.Target{{Provider: "bad", Model: "large"}}},
				"small": {ContextWindow: 100, Targets: []config.Target{{Provider: "good", Model: "small"}}},
			}
			provs := map[string]providers.Provider{"bad": failProvider{}, "good": mockprovider.New("small")}
			h := NewMessagesHandler(router.New(holderForWithFallbacks(provs, models, map[string]string{"large": "small"})))
			req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(
				`{"model":"large","stream":`+stream+`,"max_tokens":16,"messages":[{"role":"user","content":"`+
					strings.Repeat("word ", 400)+`"}]}`))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, allowAll(req))
			if rec.Code != 400 || !strings.Contains(rec.Body.String(), "100-token context window") {
				t.Fatalf("smaller fallback received oversized input: status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
