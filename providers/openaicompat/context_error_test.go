package openaicompat

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/pricing"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/internal/server/anthropicapi"
	"github.com/inferplane/inferplane/providers"
)

type contextErrorTransport func(*http.Request) (*http.Response, error)

func (f contextErrorTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// Exercise the provider and the consuming ingress together: a raw OpenAI error
// must not obscure Claude's context recovery or replay history into a fallback.
func TestAnthropicContextErrorConversionIsSafeAndTerminal(t *testing.T) {
	for _, tc := range []struct {
		name, body, wantMessage string
		context                 bool
	}{
		{"typed-private-message", `{"error":{"code":"context_length_exceeded","message":"private prompt and credential"}}`, "prompt is too long", true},
		{"vllm-completion", `{"error":{"type":"BadRequestError","message":"This model's maximum context length is 4096 tokens. However, you requested 5500 tokens (5000 in the messages, 500 in the completion). Please reduce the length of the messages or completion."}}`, "prompt is too long: 5500 tokens > 4096 maximum", true},
		{"vllm-messages", `{"error":{"type":"BadRequestError","message":"This model's maximum context length is 4096 tokens. However, you requested 5000 tokens in the messages, Please reduce the length of the messages."}}`, "prompt is too long: 5000 tokens > 4096 maximum", true},
		{"vllm-numeric-status-code", `{"error":{"type":"BadRequestError","code":400,"message":"This model's maximum context length is 4096 tokens. However, you requested 5000 tokens in the messages, Please reduce the length of the messages."}}`, "prompt is too long: 5000 tokens > 4096 maximum", true},
		{"vllm-http-envelope", `{"object":"error","type":"BadRequestError","code":400,"param":null,"message":"This model's maximum context length is 4096 tokens. However, you requested 5000 tokens in the messages, Please reduce the length of the messages."}`, "prompt is too long: 5000 tokens > 4096 maximum", true},
		{"vllm-http-completion-envelope", `{"object":"error","type":"BadRequestError","code":400,"param":null,"message":"This model's maximum context length is 4096 tokens. However, you requested 5500 tokens (5000 in the messages, 500 in the completion). Please reduce the length of the messages or completion."}`, "prompt is too long: 5500 tokens > 4096 maximum", true},
		{"vllm-wrong-object", `{"object":"private","type":"BadRequestError","code":400,"message":"This model's maximum context length is 4096 tokens. However, you requested 5000 tokens in the messages, Please reduce the length of the messages."}`, "upstream request rejected", false},
		{"vllm-wrong-type", `{"object":"error","type":"private","code":400,"message":"This model's maximum context length is 4096 tokens. However, you requested 5000 tokens in the messages, Please reduce the length of the messages."}`, "upstream request rejected", false},
		{"vllm-wrong-status-code", `{"object":"error","type":"BadRequestError","code":500,"message":"This model's maximum context length is 4096 tokens. However, you requested 5000 tokens in the messages, Please reduce the length of the messages."}`, "upstream request rejected", false},
		{"vllm-string-status-code", `{"object":"error","type":"BadRequestError","code":"400","message":"This model's maximum context length is 4096 tokens. However, you requested 5000 tokens in the messages, Please reduce the length of the messages."}`, "upstream request rejected", false},
		{"vllm-unrecognized-message", `{"object":"error","type":"BadRequestError","code":400,"message":"private prompt is too long"}`, "upstream request rejected", false},
		{"vllm-top-level-context-code", `{"object":"error","type":"BadRequestError","code":"context_length_exceeded","message":"private"}`, "upstream request rejected", false},
		{"generic-bad-request", `{"error":{"type":"BadRequestError","message":"private prompt is too long"}}`, "upstream request rejected", false},
		{"unknown-code", `{"error":{"code":"private_unknown","message":"private"}}`, "upstream request rejected", false},
		{"suffix-private-message", `{"error":{"type":"BadRequestError","message":"This model's maximum context length is 4096 tokens. However, you requested 5000 tokens in the messages, Please reduce the length of the messages. private"}}`, "upstream request rejected", false},
		{"reversed-counts", `{"error":{"message":"This model's maximum context length is 5000 tokens. However, you requested 4096 tokens in the messages, Please reduce the length of the messages."}}`, "upstream request rejected", false},
		{"inconsistent-total", `{"error":{"message":"This model's maximum context length is 4096 tokens. However, you requested 5500 tokens (4000 in the messages, 500 in the completion). Please reduce the length of the messages or completion."}}`, "upstream request rejected", false},
		{"numeric-overflow", `{"error":{"message":"This model's maximum context length is 4096 tokens. However, you requested 999999999999999999999999 tokens in the messages, Please reduce the length of the messages."}}`, "upstream request rejected", false},
		{"zero-limit", `{"error":{"message":"This model's maximum context length is 0 tokens. However, you requested 5000 tokens in the messages, Please reduce the length of the messages."}}`, "upstream request rejected", false},
		{"wrong-code-path", `{"code":"context_length_exceeded","error":{"message":"private"}}`, "upstream request rejected", false},
		{"oversized-envelope", `{"error":{"code":"context_length_exceeded","message":"` + strings.Repeat("private", 10000) + `"}}`, "upstream request rejected", false},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				calls, fallbackCalls := 0, 0
				makeProvider := func(counter *int) providers.Provider {
					p, err := factory(providers.Config{BaseURL: "http://fixture", HTTPClient: &http.Client{
						Transport: contextErrorTransport(func(*http.Request) (*http.Response, error) {
							*counter++
							return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/json"}},
								Body: io.NopCloser(strings.NewReader(tc.body))}, nil
						}),
					}})
					if err != nil {
						t.Fatal(err)
					}
					return p
				}
				provs := map[string]providers.Provider{"primary": makeProvider(&calls), "fallback": makeProvider(&fallbackCalls)}
				models := map[string]config.ModelConfig{
					"large": {Targets: []config.Target{{Provider: "primary", Model: "up"}}},
					"small": {Targets: []config.Target{{Provider: "fallback", Model: "up"}}},
				}
				holder := &live.Holder{}
				holder.Swap(live.NewStateWithFallbacks(provs, models, pricing.New(pricing.OnMissingAllow, nil),
					map[string]string{"primary": "primary", "fallback": "fallback"}, map[string]string{"large": "small"}, false))
				h := anthropicapi.NewMessagesHandler(router.New(holder))
				req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(fmt.Sprintf(
					`{"model":"large","stream":%t,"max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`, stream)))
				req = req.WithContext(principal.With(req.Context(), keystore.Principal{AllowedModels: []string{"*"}}))
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != 400 || calls != 1 || fallbackCalls != 0 {
					t.Fatalf("validation replayed or status changed: status=%d primary=%d fallback=%d body=%s", rec.Code, calls, fallbackCalls, rec.Body.String())
				}
				var got struct {
					Type  string `json:"type"`
					Error struct {
						Type, Code, Message string
					} `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				wantCode := ""
				if tc.context {
					wantCode = "context_length_exceeded"
				}
				if got.Type != "error" || got.Error.Type != "invalid_request_error" || got.Error.Message != tc.wantMessage ||
					got.Error.Code != wantCode {
					t.Fatalf("unsafe or unreadable Anthropic recovery error: %s", rec.Body.String())
				}
			})
		}
	}
}

func TestOpenAIContextErrorResponseRemainsByteExact(t *testing.T) {
	body := " {\"error\": {\"code\":\"context_length_exceeded\", \"message\":\"private\"}} \n"
	p, err := factory(providers.Config{BaseURL: "http://fixture", HTTPClient: &http.Client{
		Transport: contextErrorTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	req := &providers.ProxyRequest{IngressProtocol: "openai", RawBody: []byte(`{"model":"m","messages":[]}`)}
	resp, err := p.Complete(t.Context(), req)
	if err != nil || resp.StatusCode != 400 || string(resp.RawBody) != body {
		t.Fatalf("native Complete changed bytes/status: response=%+v err=%v", resp, err)
	}
	_, err = p.Stream(t.Context(), req)
	upstream, ok := err.(*providers.UpstreamError)
	if !ok || upstream.StatusCode != 400 || string(upstream.Body) != body {
		t.Fatalf("native Stream changed bytes/status: %v", err)
	}
}
