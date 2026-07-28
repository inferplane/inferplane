package anthropicapi

import (
	"bytes"
	"context"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/metrics"
	"github.com/inferplane/inferplane/internal/pricing"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// allowAll wraps a request with a principal whose allow-list is "*". The
// handler now requires a principal in context (401 otherwise), so the tests
// that don't exercise the allow-list itself inject a permissive one.
func allowAll(req *http.Request) *http.Request {
	return req.WithContext(principal.With(req.Context(),
		keystore.Principal{AllowedModels: []string{"*"}}))
}

func testRouter() *router.Router {
	provs := map[string]providers.Provider{"p": mockprovider.New("claude-sonnet-4-6")}
	models := map[string]config.ModelConfig{
		"claude-sonnet-4-6": {Targets: []config.Target{{Provider: "p", Model: "claude-sonnet-4-6"}}},
	}
	return router.New(holderFor(provs, models))
}

func TestMessagesNonStreaming(t *testing.T) {
	h := NewMessagesHandler(testRouter())
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowAll(req))
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"msg_mock"`) {
		t.Fatalf("body missing mock response: %s", rec.Body.String())
	}
}

func TestMessagesStreamingTee(t *testing.T) {
	h := NewMessagesHandler(testRouter())
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-6","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowAll(req))
	body := rec.Body.String()
	if !strings.Contains(body, "event: message_start") || !strings.Contains(body, "event: message_stop") {
		t.Fatalf("stream not teed verbatim: %s", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
}

func TestMessagesUnknownModel(t *testing.T) {
	h := NewMessagesHandler(testRouter())
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"no-such-model","messages":[]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowAll(req))
	if rec.Code != 404 {
		t.Fatalf("expected 404 for unknown model, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("expected anthropic error body: %s", rec.Body.String())
	}
}

// Task 1: an unknown-model 404 must list the models the key may use, so a
// caller who fat-fingered a model name (tickets row 25/41/52) can self-correct.
func TestMessages404ListsAvailableModels(t *testing.T) {
	provs := map[string]providers.Provider{"p": mockprovider.New("claude-sonnet-4-6")}
	models := map[string]config.ModelConfig{
		"claude-sonnet-4-6": {Targets: []config.Target{{Provider: "p", Model: "claude-sonnet-4-6"}}},
		"claude-opus-4-7":   {Targets: []config.Target{{Provider: "p", Model: "claude-opus-4-7"}}},
	}
	h := NewMessagesHandler(router.New(holderFor(provs, models)))
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-9","messages":[]}`))
	rec := httptest.NewRecorder()
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik_secret", Team: "t", AllowedModels: []string{"*"}})
	h.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != 404 {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"not_found_error"`) {
		t.Fatalf("error type must stay not_found_error: %s", body)
	}
	if !strings.Contains(body, "claude-sonnet-4-6") || !strings.Contains(body, "claude-opus-4-7") {
		t.Fatalf("404 must list available models: %s", body)
	}
	if strings.Contains(body, "ik_secret") {
		t.Fatalf("404 body must not leak key id: %s", body)
	}
}

// The available list is filtered by the key's allow-list — a model the key
// can't use is not advertised.
func TestMessages404FiltersByAllowList(t *testing.T) {
	provs := map[string]providers.Provider{"p": mockprovider.New("claude-sonnet-4-6")}
	models := map[string]config.ModelConfig{
		"claude-sonnet-4-6": {Targets: []config.Target{{Provider: "p", Model: "claude-sonnet-4-6"}}},
		"secret-model":      {Targets: []config.Target{{Provider: "p", Model: "secret-model"}}},
	}
	h := NewMessagesHandler(router.New(holderFor(provs, models)))
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-9","messages":[]}`))
	rec := httptest.NewRecorder()
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"claude-sonnet-4-6"}})
	h.ServeHTTP(rec, req.WithContext(ctx))
	body := rec.Body.String()
	if strings.Contains(body, "secret-model") {
		t.Fatalf("404 must not list models outside the allow-list: %s", body)
	}
	if !strings.Contains(body, "claude-sonnet-4-6") {
		t.Fatalf("404 must list allowed models: %s", body)
	}
}

// A key allowed no configured model gets an explicit message, not a dangling list.
func TestMessages404EmptyAvailable(t *testing.T) {
	h := NewMessagesHandler(testRouter())
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-9","messages":[]}`))
	rec := httptest.NewRecorder()
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"nothing-matches"}})
	h.ServeHTTP(rec, req.WithContext(ctx))
	if !strings.Contains(rec.Body.String(), "No models available for this key") {
		t.Fatalf("empty allow-list must yield explicit message: %s", rec.Body.String())
	}
}

// F5: the 403 "model not allowed" branch also lists what the key CAN use.
func TestMessages403ListsAvailableModels(t *testing.T) {
	provs := map[string]providers.Provider{"p": mockprovider.New("claude-sonnet-4-6")}
	models := map[string]config.ModelConfig{
		"claude-sonnet-4-6": {Targets: []config.Target{{Provider: "p", Model: "claude-sonnet-4-6"}}},
	}
	h := NewMessagesHandler(router.New(holderFor(provs, models)))
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-opus-4-7","messages":[]}`))
	rec := httptest.NewRecorder()
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"claude-sonnet-4-6"}})
	h.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != 403 {
		t.Fatalf("want 403, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "claude-sonnet-4-6") {
		t.Fatalf("403 must list available models: %s", rec.Body.String())
	}
}

type errStreamProvider struct{}

func (errStreamProvider) Name() string               { return "errstream" }
func (errStreamProvider) Models() []schema.ModelInfo { return nil }
func (errStreamProvider) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	return nil, errors.New("unused")
}
func (errStreamProvider) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	return nil, &providers.UpstreamError{StatusCode: 429, Body: []byte(`{"type":"error","error":{"type":"rate_limit_error"}}`), Header: http.Header{}}
}

func TestMessagesStreamingUpstreamErrorTeed(t *testing.T) {
	provs := map[string]providers.Provider{"p": errStreamProvider{}}
	models := map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}}}
	h := NewMessagesHandler(router.New(holderFor(provs, models)))
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m","stream":true,"messages":[]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowAll(req))
	if rec.Code != 429 {
		t.Fatalf("expected upstream 429 teed, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "rate_limit_error") {
		t.Fatalf("upstream error body not teed: %s", rec.Body.String())
	}
}

// errCompleteProvider mirrors errStreamProvider for the non-streaming path —
// pins the serveComplete/serveStream parity fix: a provider whose Complete
// returns an *UpstreamError (as bedrock's now does) must have its real
// status/body teed to the client instead of falling through to a generic 502.
type errCompleteProvider struct{}

func (errCompleteProvider) Name() string               { return "errcomplete" }
func (errCompleteProvider) Models() []schema.ModelInfo { return nil }
func (errCompleteProvider) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	return nil, &providers.UpstreamError{StatusCode: 429, Body: []byte(`{"type":"error","error":{"type":"rate_limit_error"}}`)}
}
func (errCompleteProvider) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	return nil, errors.New("unused")
}

func TestMessagesNonStreamingUpstreamErrorTeed(t *testing.T) {
	provs := map[string]providers.Provider{"p": errCompleteProvider{}}
	models := map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}}}
	h := NewMessagesHandler(router.New(holderFor(provs, models)))
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m","messages":[]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowAll(req))
	if rec.Code != 429 {
		t.Fatalf("expected upstream 429 teed, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "rate_limit_error") {
		t.Fatalf("upstream error body not teed: %s", rec.Body.String())
	}
}

// TestMessagesNonStreamingUpstreamErrorFallsBackWhenNotLast: an UpstreamError
// on a non-last target still falls back to the next target — the tee only
// applies on the last target (mirrors TestMessagesNonStreamingFallsBackPreTTFT
// but with a typed UpstreamError instead of a bare transport error).
func TestMessagesNonStreamingUpstreamErrorFallsBackWhenNotLast(t *testing.T) {
	provs := map[string]providers.Provider{
		"bad":  errCompleteProvider{},
		"good": mockprovider.New("claude-sonnet-4-6"),
	}
	models := map[string]config.ModelConfig{
		"claude-sonnet-4-6": {Targets: []config.Target{
			{Provider: "bad", Model: "m1"},
			{Provider: "good", Model: "claude-sonnet-4-6"},
		}},
	}
	h := NewMessagesHandler(router.New(holderFor(provs, models)))
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowAll(req))
	if rec.Code != 200 {
		t.Fatalf("fallback to healthy provider should yield 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Inferplane-Fallback"); got != "good" {
		t.Fatalf("x-inferplane-fallback header = %q, want %q", got, "good")
	}
}

type headerProvider struct{}

func (headerProvider) Name() string               { return "hdr" }
func (headerProvider) Models() []schema.ModelInfo { return nil }
func (headerProvider) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	return &providers.ProxyResponse{
		StatusCode: 200,
		Headers:    http.Header{"Request-Id": {"req_123"}, "Anthropic-Ratelimit-Requests-Remaining": {"42"}, "Content-Type": {"application/json"}},
		RawBody:    []byte(`{"id":"msg_x","type":"message","role":"assistant","model":"m","content":[],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`),
	}, nil
}
func (headerProvider) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	return nil, errors.New("unused")
}

func TestMessagesNonStreamingTeesUpstreamHeaders(t *testing.T) {
	provs := map[string]providers.Provider{"p": headerProvider{}}
	models := map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}}}
	h := NewMessagesHandler(router.New(holderFor(provs, models)))
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m","messages":[]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowAll(req))
	if rec.Header().Get("Request-Id") != "req_123" {
		t.Fatalf("request-id not teed: %q", rec.Header().Get("Request-Id"))
	}
	if rec.Header().Get("Anthropic-Ratelimit-Requests-Remaining") != "42" {
		t.Fatalf("ratelimit header not teed")
	}
}

type retryStreamProvider struct{}

func (retryStreamProvider) Name() string               { return "retry" }
func (retryStreamProvider) Models() []schema.ModelInfo { return nil }
func (retryStreamProvider) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	return nil, errors.New("unused")
}
func (retryStreamProvider) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	return nil, &providers.UpstreamError{StatusCode: 429, Body: []byte(`{"type":"error"}`), Header: http.Header{"Retry-After": {"30"}}}
}

func TestMessagesStreamingErrorTeesHeaders(t *testing.T) {
	provs := map[string]providers.Provider{"p": retryStreamProvider{}}
	models := map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}}}
	h := NewMessagesHandler(router.New(holderFor(provs, models)))
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m","stream":true,"messages":[]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowAll(req))
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "30" {
		t.Fatalf("streaming error headers not teed: code=%d retry-after=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

// midStreamErrProvider yields one good SSE event, then a mid-stream error —
// the 200 is already committed by the time the error surfaces, so it must
// appear as an SSE `error` event, not a silently truncated stream (H4 gate
// finding, mirrors bedrockapi's midStreamErrProvider).
type midStreamErrProvider struct{}

func (midStreamErrProvider) Name() string               { return "midstream" }
func (midStreamErrProvider) Models() []schema.ModelInfo { return nil }
func (midStreamErrProvider) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	return nil, errors.New("unused")
}
func (midStreamErrProvider) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	return func(yield func(*providers.StreamEvent, error) bool) {
		if !yield(&providers.StreamEvent{Raw: []byte("event: message_start\ndata: {}\n\n")}, nil) {
			return
		}
		yield(nil, errors.New("upstream broke"))
	}, nil
}

func TestMessagesStreamingMidStreamErrorEmitsErrorEvent(t *testing.T) {
	provs := map[string]providers.Provider{"p": midStreamErrProvider{}}
	models := map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}}}
	h := NewMessagesHandler(router.New(holderFor(provs, models)))
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m","stream":true,"messages":[]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowAll(req))
	if rec.Code != 200 {
		t.Fatalf("status already committed, expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: message_start") {
		t.Fatalf("first event not teed before the break: %s", body)
	}
	if !strings.Contains(body, "event: error") || !strings.Contains(body, "upstream stream interrupted") {
		t.Fatalf("mid-stream error not surfaced as an SSE error event: %s", body)
	}
}

// failProvider always errors on Complete/Stream (transport-level), to drive the
// pre-TTFT fallback to the next target in the chain.
type failProvider struct{}

func (failProvider) Name() string               { return "fail" }
func (failProvider) Models() []schema.ModelInfo { return nil }
func (failProvider) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	return nil, errors.New("upstream down")
}
func (failProvider) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	return nil, errors.New("upstream down")
}

func TestMessagesNonStreamingFallsBackPreTTFT(t *testing.T) {
	provs := map[string]providers.Provider{
		"bad":  failProvider{},
		"good": mockprovider.New("claude-sonnet-4-6"),
	}
	models := map[string]config.ModelConfig{
		"claude-sonnet-4-6": {Targets: []config.Target{
			{Provider: "bad", Model: "m1"},
			{Provider: "good", Model: "claude-sonnet-4-6"},
		}},
	}
	h := NewMessagesHandler(router.New(holderFor(provs, models)))
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowAll(req))
	if rec.Code != 200 {
		t.Fatalf("fallback to healthy provider should yield 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"msg_mock"`) {
		t.Fatalf("body missing fallback provider response: %s", rec.Body.String())
	}
	if got := rec.Header().Get("X-Inferplane-Fallback"); got != "good" {
		t.Fatalf("x-inferplane-fallback header = %q, want %q", got, "good")
	}
}

func TestMessagesStreamingFallsBackPreTTFT(t *testing.T) {
	provs := map[string]providers.Provider{
		"bad":  failProvider{},
		"good": mockprovider.New("claude-sonnet-4-6"),
	}
	models := map[string]config.ModelConfig{
		"claude-sonnet-4-6": {Targets: []config.Target{
			{Provider: "bad", Model: "m1"},
			{Provider: "good", Model: "claude-sonnet-4-6"},
		}},
	}
	h := NewMessagesHandler(router.New(holderFor(provs, models)))
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-6","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowAll(req))
	if rec.Code != 200 {
		t.Fatalf("pre-TTFT stream fallback should yield 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "event: message_start") {
		t.Fatalf("fallback stream not teed: %s", rec.Body.String())
	}
	if got := rec.Header().Get("X-Inferplane-Fallback"); got != "good" {
		t.Fatalf("x-inferplane-fallback header = %q, want %q", got, "good")
	}
}

func TestMessagesEnforcesAllowList(t *testing.T) {
	h := NewMessagesHandler(testRouter())
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`))
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"qwen-coder"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != 403 {
		t.Fatalf("allow-list violation must be 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMessagesAllowsListedModel(t *testing.T) {
	h := NewMessagesHandler(testRouter())
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`))
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"*"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != 200 {
		t.Fatalf("listed model should pass, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMessagesNoPrincipal401(t *testing.T) {
	h := NewMessagesHandler(testRouter())
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req) // no principal injected
	if rec.Code != 401 {
		t.Fatalf("missing principal must be 401, got %d", rec.Code)
	}
}

func TestMessages404UnknownModelAudited(t *testing.T) {
	var buf bytes.Buffer
	w, _ := audit.NewWriter("i", filepath.Join(t.TempDir(), "a.wal"), []audit.Sink{audit.NewWriterSink("b", &buf, true)})
	h := NewMessagesHandlerWithAudit(testRouter(), w)
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"ghost-model","messages":[]}`))
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"*"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	w.Close()
	if rec.Code != 404 {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	if !strings.Contains(buf.String(), `"status":404`) {
		t.Fatalf("404 must be audited: %s", buf.String())
	}
}

// govPricing keys the rate table by (config-provider-name, upstream-model),
// matching how the router's ResolveProvider returns the pricing provider name.
// testRouter() uses provider config name "p" and upstream "claude-sonnet-4-6".
// Cache rates follow the real published multiples of the input rate (ADR-030):
// read 0.1x, 5m write 1.25x, 1h write 2x. With a 1 µUSD/token input rate the
// mock's streaming usage (10 in, 5 out, 40 cache_read, 20 write_5m, 4 write_1h)
// settles to 10 + 5 + 4 + 25 + 8 = 52 µUSD.
func govPricing() *pricing.Table {
	return pricing.New(pricing.OnMissingAllow, map[pricing.Key]pricing.Rate{
		{Provider: "p", Model: "claude-sonnet-4-6"}: {
			InputPerMTok:        1_000_000,
			OutputPerMTok:       1_000_000,
			CacheReadPerMTok:    100_000,
			CacheWrite5mPerMTok: 1_250_000,
			CacheWrite1hPerMTok: 2_000_000,
		},
	})
}

// D5: an unrouted "claude-opus-5" with "claude-opus-4-8" configured as its
// model_fallbacks target substitutes BEFORE the allow-list check, serves
// successfully, advertises the substitution, and audits the model actually
// served — with no config edit and no client-visible 404.
func TestMessagesModelFallbackUnroutedModel(t *testing.T) {
	provs := map[string]providers.Provider{"a": mockprovider.New("claude-opus-4-8")}
	models := map[string]config.ModelConfig{
		"claude-opus-4-8": {Targets: []config.Target{{Provider: "a", Model: "claude-opus-4-8"}}},
	}
	var buf bytes.Buffer
	w, _ := audit.NewWriter("i", filepath.Join(t.TempDir(), "a.wal"), []audit.Sink{audit.NewWriterSink("b", &buf, true)})
	h := NewMessagesHandlerWithAudit(router.New(holderForWithFallbacks(provs, models, map[string]string{"claude-opus-5": "claude-opus-4-8"})), w)
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-opus-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowAll(req))
	w.Close()
	if rec.Code != 200 {
		t.Fatalf("substituted model should serve 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Inferplane-Model-Fallback"); got != "claude-opus-4-8" {
		t.Fatalf("x-inferplane-model-fallback = %q, want %q", got, "claude-opus-4-8")
	}
	if !strings.Contains(buf.String(), `"model_requested":"claude-opus-4-8"`) {
		t.Fatalf("audit must attribute the served model, not the requested one: %s", buf.String())
	}
}

// D5: a key allowed only the ORIGINALLY REQUESTED (unconfigured) model name
// must NOT be silently downgraded to the fallback model — fail closed, same
// as any other allow-list mismatch.
func TestMessagesModelFallbackFailsClosedForRestrictedKey(t *testing.T) {
	provs := map[string]providers.Provider{"a": mockprovider.New("claude-opus-4-8")}
	models := map[string]config.ModelConfig{
		"claude-opus-4-8": {Targets: []config.Target{{Provider: "a", Model: "claude-opus-4-8"}}},
	}
	h := NewMessagesHandler(router.New(holderForWithFallbacks(provs, models, map[string]string{"claude-opus-5": "claude-opus-4-8"})))
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-opus-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"claude-opus-5"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != 403 {
		t.Fatalf("a key allowed only the unconfigured name must be denied, not silently downgraded: got %d: %s", rec.Code, rec.Body.String())
	}
}

// D5: a configured model whose upstream rejects it as unknown (404) crosses
// to the model_fallbacks target — the RBAC re-check (FilterModelAllowed)
// must let it through for an unrestricted key.
func TestMessagesModelFallbackCrossesOnUpstream404(t *testing.T) {
	provs := map[string]providers.Provider{
		"bad":  statusProvider{code: 404, body: []byte(`{"type":"error","error":{"type":"not_found_error"}}`)},
		"good": mockprovider.New("claude-opus-4-8"),
	}
	models := map[string]config.ModelConfig{
		"claude-opus-5":   {Targets: []config.Target{{Provider: "bad", Model: "claude-opus-5"}}},
		"claude-opus-4-8": {Targets: []config.Target{{Provider: "good", Model: "claude-opus-4-8"}}},
	}
	h := NewMessagesHandler(router.New(holderForWithFallbacks(provs, models, map[string]string{"claude-opus-5": "claude-opus-4-8"})))
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-opus-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowAll(req))
	if rec.Code != 200 {
		t.Fatalf("cross-model fallback on upstream 404 should yield 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"msg_mock"`) {
		t.Fatalf("body missing fallback model's response: %s", rec.Body.String())
	}
	if got := rec.Header().Get("X-Inferplane-Model-Fallback"); got != "claude-opus-4-8" {
		t.Fatalf("x-inferplane-model-fallback = %q, want %q", got, "claude-opus-4-8")
	}
}

// D5: an upstream 400 that is NOT a Bedrock ValidationException must stay a
// client error, teed verbatim — it must never be replayed across a
// model-level fallback boundary (narrow-on-purpose isModelNotFound).
func TestMessagesModelFallbackDoesNotCrossOnPlain400(t *testing.T) {
	provs := map[string]providers.Provider{
		"bad":  statusProvider{code: 400, body: []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad max_tokens"}}`)},
		"good": mockprovider.New("claude-opus-4-8"),
	}
	models := map[string]config.ModelConfig{
		"claude-opus-5":   {Targets: []config.Target{{Provider: "bad", Model: "claude-opus-5"}}},
		"claude-opus-4-8": {Targets: []config.Target{{Provider: "good", Model: "claude-opus-4-8"}}},
	}
	h := NewMessagesHandler(router.New(holderForWithFallbacks(provs, models, map[string]string{"claude-opus-5": "claude-opus-4-8"})))
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-opus-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowAll(req))
	if rec.Code != 400 {
		t.Fatalf("a plain 400 must stay a client error, not fall back, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "bad max_tokens") {
		t.Fatalf("400 body must be teed verbatim: %s", rec.Body.String())
	}
	if got := rec.Header().Get("X-Inferplane-Model-Fallback"); got != "" {
		t.Fatalf("no model-fallback header expected, got %q", got)
	}
}

// Fills a pre-existing gap: no test asserted provider-level fallback on an
// upstream 5xx specifically (only 429/transport error). Same model, no
// model_fallbacks involved.
func TestMessagesNonStreamingFallsBackOn5xx(t *testing.T) {
	provs := map[string]providers.Provider{
		"bad":  statusProvider{code: 503, body: []byte(`{"type":"error"}`)},
		"good": mockprovider.New("claude-sonnet-4-6"),
	}
	models := map[string]config.ModelConfig{
		"claude-sonnet-4-6": {Targets: []config.Target{
			{Provider: "bad", Model: "m1"},
			{Provider: "good", Model: "claude-sonnet-4-6"},
		}},
	}
	h := NewMessagesHandler(router.New(holderFor(provs, models)))
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowAll(req))
	if rec.Code != 200 {
		t.Fatalf("fallback on upstream 503 should yield 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Inferplane-Fallback"); got != "good" {
		t.Fatalf("x-inferplane-fallback header = %q, want %q", got, "good")
	}
}

func TestMessagesGovernorQuotaBlocks429(t *testing.T) {
	lim := limiter.NewMemory()
	teams := map[string]governance.TeamPolicy{
		"platform-eng": {TokensPerDay: 1000, QuotaExceeded: "block"},
	}
	gov := governance.NewGovernor(teams, lim, budget.NewMemory(), nil)
	// Exhaust the team's daily token quota so the pre-check blocks.
	lim.DebitQuota("quota:platform-eng", 1000, 24*time.Hour)

	h := NewMessagesHandlerFull(testRouter(), nil, gov)
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`))
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "platform-eng", AllowedModels: []string{"*"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != 429 {
		t.Fatalf("quota-exhausted request must be 429, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "rate_limit_error") {
		t.Fatalf("expected anthropic rate_limit_error body: %s", rec.Body.String())
	}
}

// TestKeyPolicyOfMapsAllFields guards keyPolicyOf against a future field
// added to KeyOptions or KeyPolicy without updating the mapping (this
// function is duplicated in internal/server/openaiapi/chat.go — governance
// stays a leaf and does not import keystore, so each ingress package maps
// its own Principal → KeyPolicy; this test only proves THIS copy is correct).
func TestKeyPolicyOfMapsAllFields(t *testing.T) {
	p := keystore.Principal{KeyOptions: keystore.KeyOptions{RPM: 60, TPM: 1000, BudgetUSDMicros: 5_000_000}}
	got := keyPolicyOf(p)
	want := governance.KeyPolicy{RatePerMin: 60, TokensPerMinute: 1000, BudgetMicrosPerMonth: 5_000_000}
	if got != want {
		t.Fatalf("keyPolicyOf(%+v) = %+v, want %+v", p.KeyOptions, got, want)
	}
}

func TestMessagesGovernorKeyBudgetBlocks402EvenForUngovernedTeam(t *testing.T) {
	bud := budget.NewMemory()
	// No TeamPolicy entry for "platform-eng" at all — the team is ungoverned;
	// only the key's own budget (§8 D2) must still be enforced.
	gov := governance.NewGovernor(nil, limiter.NewMemory(), bud, nil)
	bud.Debit("budget:key:ik_over", 1_500_000, 30*24*time.Hour) // over the key's 1M cap

	h := NewMessagesHandlerFull(testRouter(), nil, gov)
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`))
	ctx := principal.With(req.Context(), keystore.Principal{
		KeyID: "ik_over", Team: "platform-eng", AllowedModels: []string{"*"},
		KeyOptions: keystore.KeyOptions{BudgetUSDMicros: 1_000_000},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != 402 {
		t.Fatalf("key-budget-exhausted request must be 402, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMessagesGovernorSettlesCostIntoAudit(t *testing.T) {
	var buf bytes.Buffer
	w, err := audit.NewWriter("inst-1", filepath.Join(t.TempDir(), "a.wal"), []audit.Sink{audit.NewWriterSink("buf", &buf, true)})
	if err != nil {
		t.Fatal(err)
	}
	teams := map[string]governance.TeamPolicy{
		"platform-eng": {TokensPerDay: 1_000_000, QuotaExceeded: "block"},
	}
	gov := governance.NewGovernor(teams, limiter.NewMemory(), budget.NewMemory(), nil)
	h := NewMessagesHandlerFull(testRouter(), w, gov)
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`))
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "platform-eng", AllowedModels: []string{"*"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	w.Close()
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	// mock provider reports input=10 output=5 → 10*1 + 5*1 = 15 µUSD.
	out := buf.String()
	if !strings.Contains(out, `"amount_usd_micros":15`) {
		t.Fatalf("audit completed record must carry settled cost: %s", out)
	}
	if !strings.Contains(out, `"pricing_missing":false`) {
		t.Fatalf("pricing present → pricing_missing must be false: %s", out)
	}
}

// TestMessagesStreamingSettlesFullUsage is the regression guard for ADR-030's
// headline bug: a STREAMING request must be billed for input and prompt-cache
// tokens, not output alone.
//
// Anthropic splits the counts across frames — input + cache ride
// message_start's nested message.usage, message_delta carries output_tokens at
// the top level. The old settlement path kept only the last frame's top-level
// usage, so every streaming request (i.e. effectively all Claude Code traffic)
// settled as output-only. With the mock's usage and govPricing's rates the
// correct cost is 52 µUSD; the pre-fix behavior yields 5.
//
// Nothing in the repo asserted the settled cost of a streaming request before
// this test, which is exactly why the bug shipped.
func TestMessagesStreamingSettlesFullUsage(t *testing.T) {
	var buf bytes.Buffer
	w, err := audit.NewWriter("inst-1", filepath.Join(t.TempDir(), "a.wal"), []audit.Sink{audit.NewWriterSink("buf", &buf, true)})
	if err != nil {
		t.Fatal(err)
	}
	gov := governance.NewGovernor(nil, limiter.NewMemory(), budget.NewMemory(), nil)
	h := NewMessagesHandlerFull(testRouter(), w, gov)

	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-6","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "platform-eng", AllowedModels: []string{"*"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	w.Close()

	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	out := buf.String()

	// 10 input + 5 output + 40 cache_read + 20 write_5m + 4 write_1h,
	// at 1 / 1 / 0.1 / 1.25 / 2 µUSD per token = 10+5+4+25+8 = 52.
	if !strings.Contains(out, `"amount_usd_micros":52`) {
		t.Errorf("streaming request must settle the FULL folded usage (want 52 µUSD; 5 means only message_delta's output was counted): %s", out)
	}
	// The audit usage must show the input and cache counts too — a cost that
	// happened to be right with a zero input count would still be a bug.
	if !strings.Contains(out, `"input_tokens":10`) {
		t.Errorf("audit usage lost input_tokens from message_start: %s", out)
	}
	if !strings.Contains(out, `"cache_read_input_tokens":40`) {
		t.Errorf("audit usage lost cache_read from message_start: %s", out)
	}
}

// TestMessagesStreamingBillsCacheWriteTiersSeparately pins the second ADR-030
// mapping bug: 1h cache writes cost 2x the input rate against 5m's 1.25x, and
// the old code funnelled the whole cache_creation total into the 5m tier. If
// the tiers were collapsed, the mock's 20/4 split would bill 24*1.25 = 30
// instead of 20*1.25 + 4*2 = 33, making the total 49 rather than 52.
func TestMessagesStreamingBillsCacheWriteTiersSeparately(t *testing.T) {
	m := metrics.New()
	gov := governance.NewGovernor(nil, limiter.NewMemory(), budget.NewMemory(), nil)
	h := NewMessagesHandlerMetrics(testRouter(), nil, gov, m)

	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-6","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowAll(req))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}

	// Both tiers must carry their own count. Collapsing cache_creation into
	// the 5m tier would report 24/0 instead of 20/4 — and before ADR-030 the
	// 1h series did not exist at all.
	got := tokenUsageByType(t, m)
	for _, c := range []struct {
		typ  string
		want float64
	}{
		{"input", 10},
		{"output", 5},
		{"cache_read", 40},
		{"cache_write_5m", 20},
		{"cache_write_1h", 4},
	} {
		if got[c.typ] != c.want {
			t.Errorf("token_usage[%s] = %v, want %v (full series: %v)", c.typ, got[c.typ], c.want, got)
		}
	}
}

// tokenUsageByType sums the gen_ai_client_token_usage_total counter per
// token_type label, ignoring series with an empty model label (the governance
// pre-check records a zero-valued placeholder before the model is resolved).
func tokenUsageByType(t *testing.T, m *metrics.Metrics) map[string]float64 {
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

func TestMessagesRecordsRequestMetric(t *testing.T) {
	m := metrics.New()
	h := NewMessagesHandlerMetrics(testRouter(), nil, nil, m)
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`))
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"*"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	// at least one inferplane_requests_total series recorded
	got, err := testutil.GatherAndCount(m.Registry(), "inferplane_requests_total")
	if err != nil {
		t.Fatal(err)
	}
	if got == 0 {
		t.Fatal("inferplane_requests_total not recorded")
	}
	// token usage recorded too (mock reports input=10 output=5)
	tok, err := testutil.GatherAndCount(m.Registry(), "gen_ai_client_token_usage_total")
	if err != nil {
		t.Fatal(err)
	}
	if tok == 0 {
		t.Fatal("gen_ai_client_token_usage_total not recorded")
	}
}

func TestMessages404DoesNotLeakModelLabel(t *testing.T) {
	m := metrics.New()
	h := NewMessagesHandlerMetrics(testRouter(), nil, nil, m)
	// 50 distinct unknown model names must NOT create 50 distinct metric series
	for i := 0; i < 50; i++ {
		body := `{"model":"attacker-` + strconv.Itoa(i) + `","messages":[]}`
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		ctx := principal.With(req.Context(), keystore.Principal{AllowedModels: []string{"*"}})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req.WithContext(ctx))
		if rec.Code != 404 {
			t.Fatalf("want 404, got %d", rec.Code)
		}
	}
	// inferplane_requests_total must have a BOUNDED number of series (the sentinel),
	// not 50. CollectAndCount counts series for the named metric.
	n := testutil.CollectAndCount(m.Registry(), "inferplane_requests_total")
	if n > 2 { // sentinel "_rejected" (+ possibly the zero-init series) — must be small, NOT ~50
		t.Fatalf("unbounded model label cardinality: %d series for 50 distinct unknown models", n)
	}
}

func TestMessagesEmitsTwoPhaseAudit(t *testing.T) {
	var buf bytes.Buffer
	w, err := audit.NewWriter("inst-1", filepath.Join(t.TempDir(), "a.wal"), []audit.Sink{audit.NewWriterSink("buf", &buf, true)})
	if err != nil {
		t.Fatal(err)
	}
	h := NewMessagesHandlerWithAudit(testRouter(), w)
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`))
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik_x", Team: "platform-eng", AllowedModels: []string{"*"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	w.Close() // flush

	out := buf.String()
	if !strings.Contains(out, `"request_started"`) || !strings.Contains(out, `"request_completed"`) {
		t.Fatalf("expected both audit phases, got: %s", out)
	}
	if !strings.Contains(out, `"ik_x"`) || !strings.Contains(out, `"platform-eng"`) {
		t.Fatalf("audit missing principal: %s", out)
	}
}

func holderFor(provs map[string]providers.Provider, models map[string]config.ModelConfig) *live.Holder {
	ids := make(map[string]string, len(provs))
	for n := range provs {
		ids[n] = n
	}
	h := &live.Holder{}
	h.Swap(live.NewState(provs, models, govPricing(), ids))
	return h
}

// holderForWithFallbacks is holderFor plus model_fallbacks (D5), via
// live.NewStateWithFallbacks.
func holderForWithFallbacks(provs map[string]providers.Provider, models map[string]config.ModelConfig, fallbacks map[string]string) *live.Holder {
	ids := make(map[string]string, len(provs))
	for n := range provs {
		ids[n] = n
	}
	h := &live.Holder{}
	h.Swap(live.NewStateWithFallbacks(provs, models, govPricing(), ids, fallbacks, false))
	return h
}

// statusProvider always answers Complete with a fixed status/body and no
// error — for exercising the "successful response with a bad status" retry
// path (serveComplete's !last 5xx/429/model-not-found check), as opposed to
// failProvider's transport-error path.
type statusProvider struct {
	code int
	body []byte
}

func (statusProvider) Name() string               { return "status" }
func (statusProvider) Models() []schema.ModelInfo { return nil }
func (p statusProvider) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	return &providers.ProxyResponse{StatusCode: p.code, RawBody: p.body}, nil
}
func (statusProvider) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	return nil, nil
}

// Task 2: a request naming an ALIAS routes to the canonical target. Alias
// normalization happens before RBAC, so an allow-list holding the canonical
// name grants access to alias requests too (F6) — and, per the code-gate HIGH
// finding on PR #25, an allow-list holding ONLY the alias is ALSO resolved to
// its canonical target rather than permanently denied: canonicalizing an
// allow-list entry is still an exact match on both sides, so there is no
// bypass risk, only a dead config entry if left unresolved (an operator who
// writes an alias into allowed_models clearly means to grant that model).
func TestMessagesAliasRoutesToCanonical(t *testing.T) {
	provs := map[string]providers.Provider{"p": mockprovider.New("claude-sonnet-4-6")}
	models := map[string]config.ModelConfig{
		"claude-sonnet-4-6": {
			Aliases: []string{"apac.anthropic.claude-sonnet-4-6"},
			Targets: []config.Target{{Provider: "p", Model: "claude-sonnet-4-6"}},
		},
	}
	h := NewMessagesHandler(router.New(holderFor(provs, models)))

	// allow-list holds the canonical name → alias request succeeds.
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"apac.anthropic.claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"claude-sonnet-4-6"}})
	h.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != 200 {
		t.Fatalf("alias must route to canonical target: got %d body %s", rec.Code, rec.Body.String())
	}

	// allow-list holds ONLY the alias (not canonical) → still allowed: the
	// allow-list entry is canonicalized too, so this isn't a permanent lockout.
	req2 := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"apac.anthropic.claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	rec2 := httptest.NewRecorder()
	ctx2 := principal.With(req2.Context(), keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"apac.anthropic.claude-sonnet-4-6"}})
	h.ServeHTTP(rec2, req2.WithContext(ctx2))
	if rec2.Code != 200 {
		t.Fatalf("alias-only allow-list must still resolve to its canonical target: got %d body %s", rec2.Code, rec2.Body.String())
	}

	// an allow-list entry naming an UNRELATED model must still deny — the
	// canonicalized-comparison fix is not a broadening of access in general.
	req3 := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"apac.anthropic.claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	rec3 := httptest.NewRecorder()
	ctx3 := principal.With(req3.Context(), keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"some-other-model"}})
	h.ServeHTTP(rec3, req3.WithContext(ctx3))
	if rec3.Code != 403 {
		t.Fatalf("unrelated allow-list entry must still deny: got %d", rec3.Code)
	}
}

// ADR-030: the pricing guard is a PRICING setting, not a governance one — a
// deployment with budgets/quotas disabled (nil governor) and
// pricing.on_missing "block" must still refuse an unpriced route rather than
// serve it and bill 0.
func TestMessagesPricingGuardBlocksWithoutGovernor(t *testing.T) {
	provs := map[string]providers.Provider{"p": mockprovider.New("claude-sonnet-4-6")}
	models := map[string]config.ModelConfig{
		"claude-sonnet-4-6": {Targets: []config.Target{{Provider: "p", Model: "claude-sonnet-4-6"}}},
	}
	ids := map[string]string{"p": "p"}
	h := &live.Holder{}
	h.Swap(live.NewState(provs, models, pricing.New(pricing.OnMissingBlock, nil), ids))

	handler := NewMessagesHandler(router.New(h)) // no governor
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, allowAll(req))
	if rec.Code != 402 {
		t.Fatalf("unpriced route with on_missing block must be refused even without a governor, got %d: %s", rec.Code, rec.Body.String())
	}
}
