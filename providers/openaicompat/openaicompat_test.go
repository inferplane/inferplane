package openaicompat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
)

func TestCompleteForwardsOpenAIVerbatimWhenIngressOpenAI(t *testing.T) {
	var gotBody []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
	}))
	defer up.Close()
	p, _ := factory(providers.Config{Type: "openai_compatible", BaseURL: up.URL, APIKey: "k"})
	// ingress openai, but Upstream differs from the client's model → model rewritten.
	raw := []byte(`{"model":"Qwen/Qwen2.5","messages":[{"role":"user","content":"hi"}]}`)
	clientRaw := []byte(`{"model":"qwen","messages":[{"role":"user","content":"hi"}]}`)
	resp, err := p.Complete(context.Background(), &providers.ProxyRequest{Model: "qwen", Upstream: "Qwen/Qwen2.5", RawBody: clientRaw, IngressProtocol: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	// forwarded verbatim except the top-level model rewritten to Upstream.
	if string(gotBody) != string(raw) {
		t.Fatalf("openai ingress → openai provider must forward verbatim w/ model rewritten:\n got: %s\nwant: %s", gotBody, raw)
	}
	if resp.Parsed == nil || resp.Parsed.Usage == nil || *resp.Parsed.Usage.InputTokens != 5 {
		t.Fatalf("usage observation: %+v", resp.Parsed)
	}
}

func TestCompleteConvertsWhenIngressAnthropic(t *testing.T) {
	var gotBody []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer up.Close()
	p, _ := factory(providers.Config{Type: "openai_compatible", BaseURL: up.URL})
	// anthropic ingress: Parsed is canonical; RawBody is Anthropic JSON (not OpenAI)
	cr := &schema.ChatRequest{Model: "claude-x", Messages: []schema.Message{{Role: "user", Content: []schema.ContentBlock{{Type: "text", Text: ptrS("hi")}}}}}
	_, err := p.Complete(context.Background(), &providers.ProxyRequest{Model: "claude-x", Upstream: "Qwen/Qwen2.5", Parsed: cr, RawBody: []byte(`{"model":"claude-x"}`), IngressProtocol: "anthropic"})
	if err != nil {
		t.Fatal(err)
	}
	// upstream must receive an OpenAI-shaped body (converted), with model=Upstream
	var m map[string]any
	if json.Unmarshal(gotBody, &m) != nil {
		t.Fatalf("upstream body not JSON: %s", gotBody)
	}
	if m["model"] != "Qwen/Qwen2.5" {
		t.Fatalf("model not rewritten to upstream: %v", m["model"])
	}
	if _, hasMessages := m["messages"]; !hasMessages {
		t.Fatalf("converted body missing messages: %s", gotBody)
	}
}

func ptrS(s string) *string { return &s }

func TestStreamForwardsOpenAISSE(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" + "data: [DONE]\n\n"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse)
	}))
	defer up.Close()
	p, _ := factory(providers.Config{Type: "openai_compatible", BaseURL: up.URL})
	seq, err := p.Stream(context.Background(), &providers.ProxyRequest{Model: "q", Upstream: "q", RawBody: []byte(`{"model":"q","stream":true}`), IngressProtocol: "openai", Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	var raw strings.Builder
	var sawContent bool
	for ev, e := range seq {
		if e != nil {
			t.Fatal(e)
		}
		raw.WriteString(string(ev.Raw))
		if ev.Chunk != nil {
			sawContent = true
		}
	}
	if !strings.Contains(raw.String(), `"content":"hi"`) || !strings.Contains(raw.String(), "[DONE]") {
		t.Fatalf("openai SSE not teed verbatim: %s", raw.String())
	}
	_ = sawContent
}

// A non-OpenAI ingress (Anthropic, Bedrock) tees RawBody to its client, so a
// 2xx completion must be re-rendered in Anthropic shape under the PUBLIC
// model name — while the OpenAI ingress keeps the verbatim OpenAI wire.
func TestCompleteRerendersRawBodyForCrossProtocolIngress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"upstream-m","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"hey"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	}))
	defer srv.Close()
	p := &provider{baseURL: srv.URL, client: srv.Client()}
	raw := `{"model":"public-m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
	var cr schema.ChatRequest
	if err := json.Unmarshal([]byte(raw), &cr); err != nil {
		t.Fatal(err)
	}
	req := &providers.ProxyRequest{Model: "public-m", Upstream: "upstream-m", RawBody: []byte(raw), Parsed: &cr, IngressProtocol: "bedrock"}
	resp, err := p.Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(resp.RawBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["type"] != "message" || body["model"] != "public-m" {
		t.Fatalf("cross-protocol RawBody not anthropic-shaped/public-model: type=%v model=%v", body["type"], body["model"])
	}

	// OpenAI ingress: verbatim OpenAI wire preserved.
	req.IngressProtocol = "openai"
	resp, err = p.Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	body = map[string]any{}
	if err := json.Unmarshal(resp.RawBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["object"] != "chat.completion" {
		t.Fatalf("openai ingress RawBody must stay verbatim OpenAI wire: %v", body)
	}
}
