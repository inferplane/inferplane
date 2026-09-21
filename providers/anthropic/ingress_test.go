package anthropic

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/openai"
	"github.com/inferplane/inferplane/providers"
)

// An OpenAI-ingress request carries the client's OpenAI bytes in RawBody and
// the canonical form in Parsed. The Anthropic wire must receive the canonical
// render (Anthropic tools/max_tokens shape), not the OpenAI bytes — and the
// caller's RawBody must stay untouched for fallback.
func TestOpenAIIngressRendersAnthropicTools(t *testing.T) {
	raw := []byte(`{"model":"alias","max_completion_tokens":256,"messages":[{"role":"user","content":"echo"}],"tools":[{"type":"function","function":{"name":"echo","parameters":{"type":"object"}}}]}`)
	parsed, err := openai.RequestToCanonical(raw)
	if err != nil {
		t.Fatal(err)
	}
	p := &provider{baseURL: "https://example.invalid", apiKey: "test-upstream"}
	req := &providers.ProxyRequest{Model: "alias", Upstream: "claude-test", RawBody: raw, Parsed: parsed, IngressProtocol: "openai", Stream: true}
	up, err := p.buildUpstream(context.Background(), "/v1/messages", req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(up.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{`"model":"claude-test"`, `"max_tokens":256`, `"stream":true`, `"input_schema"`} {
		if !strings.Contains(string(body), part) {
			t.Fatalf("missing %s: %s", part, body)
		}
	}
	for _, part := range []string{"max_completion_tokens", `"function"`} {
		if strings.Contains(string(body), part) {
			t.Fatalf("OpenAI shape leaked to Anthropic wire (%s): %s", part, body)
		}
	}
	if up.Header.Get("Anthropic-Version") != defaultAnthropicVersion {
		t.Fatalf("Anthropic-Version = %q, want %q", up.Header.Get("Anthropic-Version"), defaultAnthropicVersion)
	}
	if string(req.RawBody) != string(raw) {
		t.Fatal("caller mutated")
	}
}

// The anthropic ingress is NOT re-rendered through the canonical schema: only
// the pre-existing top-level model rewrite applies, so an unknown top-level
// field and the content string survive, and the client's own
// Anthropic-Version header wins — no version is injected on this ingress.
func TestAnthropicIngressNotReRendered(t *testing.T) {
	raw := []byte(`{"model":"alias","max_tokens":5,"messages":[{"role":"user","content":"keep   spacing"}],"x_unknown_passthrough":{"a":1}}`)
	p := &provider{baseURL: "https://example.invalid", apiKey: "test-upstream"}
	req := &providers.ProxyRequest{Model: "alias", Upstream: "claude-test", RawBody: raw, IngressProtocol: "anthropic"}
	req.Headers = map[string][]string{"Anthropic-Version": {"2099-01-01"}}
	up, err := p.buildUpstream(context.Background(), "/v1/messages", req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(up.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{`"keep   spacing"`, `"x_unknown_passthrough":{"a":1}`, `"model":"claude-test"`} {
		if !strings.Contains(string(body), part) {
			t.Fatalf("anthropic ingress body altered beyond the model rewrite (missing %s): %s", part, body)
		}
	}
	if up.Header.Get("Anthropic-Version") != "2099-01-01" {
		t.Fatalf("client Anthropic-Version overridden: %q", up.Header.Get("Anthropic-Version"))
	}
	delete(req.Headers, "Anthropic-Version")
	up, err = p.buildUpstream(context.Background(), "/v1/messages", req)
	if err != nil {
		t.Fatal(err)
	}
	if v := up.Header.Get("Anthropic-Version"); v != "" {
		t.Fatalf("version injected on the anthropic ingress: %q", v)
	}
}
