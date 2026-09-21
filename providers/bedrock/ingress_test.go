package bedrock

import (
	"context"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/openai"
	"github.com/inferplane/inferplane/providers"
)

// An OpenAI-ingress request through Converse must keep tools, the output
// limit and the tool-call history — all of which live in Parsed, not in the
// OpenAI-shaped RawBody — and must never mutate the caller's request.
func TestOpenAIIngressConverseKeepsToolsAndLimit(t *testing.T) {
	raw := []byte(`{"model":"kimi-k3","max_completion_tokens":256,"messages":[{"role":"user","content":"echo OK"},{"role":"assistant","content":null,"tool_calls":[{"id":"call1","type":"function","function":{"name":"echo","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call1","content":"OK"}],"tools":[{"type":"function","function":{"name":"echo","parameters":{"type":"object"}}}]}`)
	parsed, err := openai.RequestToCanonical(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, stream := range []bool{false, true} {
		fc := &fakeConverser{}
		p := &provider{conv: fc}
		req := &providers.ProxyRequest{Model: "kimi-k3", Upstream: "global.moonshotai.kimi-k3", RawBody: raw, Parsed: parsed, IngressProtocol: "openai", Stream: stream}
		if stream {
			_, err = p.Stream(context.Background(), req)
		} else {
			_, err = p.Complete(context.Background(), req)
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(fc.gotReq.Tools) != 1 || fc.gotReq.Inference["maxTokens"] != int64(256) || len(fc.gotReq.Messages) != 3 || fc.gotReq.Messages[1].Content[0].Type != "tool_use" || fc.gotReq.Messages[2].Content[0].Type != "tool_result" {
			t.Fatalf("stream=%v lost OpenAI content: %+v", stream, fc.gotReq)
		}
		if string(req.RawBody) != string(raw) {
			t.Fatal("mutated source request")
		}
	}
}

// InvokeModel consumes Anthropic Messages JSON. An OpenAI-ingress request must
// reach it as the canonical render (`max_tokens`), never as the OpenAI bytes
// (`max_completion_tokens`). Native ingresses pass through untouched, and so
// does an openai-labelled request with no Parsed — the legacy contract the
// router's Converse safety gate and the Responses bridge both rely on.
func TestOpenAIInvokeConvertsWithoutMutatingNative(t *testing.T) {
	raw := []byte(`{"model":"alias","max_completion_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	parsed, err := openai.RequestToCanonical(raw)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeInvoker{respBody: []byte(`{"type":"message","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`)}
	p := &provider{inv: f}
	req := &providers.ProxyRequest{Model: "alias", Upstream: "anthropic.claude-test", RawBody: raw, Parsed: parsed, IngressProtocol: "openai"}
	if _, err := p.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(f.gotBody), `"max_tokens":64`) || strings.Contains(string(f.gotBody), "max_completion_tokens") {
		t.Fatalf("not Anthropic body: %s", f.gotBody)
	}
	if string(req.RawBody) != string(raw) {
		t.Fatal("caller RawBody mutated")
	}
	native := &providers.ProxyRequest{RawBody: []byte(`{"messages": [ {"role":"user","content":"keep spaces"} ]}`), IngressProtocol: "anthropic"}
	same, err := anthropicRequest(native)
	if err != nil || same != native {
		t.Fatal("native request converted")
	}
	legacy := &providers.ProxyRequest{IngressProtocol: "openai", RawBody: []byte(`{"messages":[{"role":"user","content":"hello"}],"max_tokens":32}`)}
	same, err = anthropicRequest(legacy)
	if err != nil || same != legacy {
		t.Fatal("openai-labelled request with no Parsed must pass through unchanged")
	}
}
