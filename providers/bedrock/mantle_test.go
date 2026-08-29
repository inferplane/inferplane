package bedrock

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
)

// Mantle's routes are strictly vendor-partitioned (probed live 2026-08-28):
// anthropic.* only on /anthropic/v1/messages, openai.*/xai.* only on
// /openai/v1/chat/completions, every other vendor only on
// /v1/chat/completions — each rejects the others' models with a 400.
func TestMantlePathFor(t *testing.T) {
	cases := map[string]string{
		"anthropic.claude-opus-5":  "/anthropic/v1/messages",
		"anthropic.claude-haiku-4-5": "/anthropic/v1/messages",
		"openai.gpt-5.4":           "/openai/v1/chat/completions",
		"openai.gpt-5.6-sol":       "/openai/v1/chat/completions",
		"xai.grok-4.3":             "/openai/v1/chat/completions",
		"deepseek.v3.2":            "/v1/chat/completions",
		"zai.glm-5":                "/v1/chat/completions",
		"moonshotai.kimi-k2.5":     "/v1/chat/completions",
	}
	for upstream, want := range cases {
		if got := mantlePathFor(upstream); got != want {
			t.Errorf("mantlePathFor(%q) = %q, want %q", upstream, got, want)
		}
	}
}

func TestMantleAnthropicBody(t *testing.T) {
	raw := []byte(`{"anthropic_version":"bedrock-2023-05-31","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	body, err := toMantleAnthropicBody(raw, "anthropic.claude-opus-5", true)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if _, has := out["anthropic_version"]; has {
		t.Error("anthropic_version must be dropped (a body field only InvokeModel takes)")
	}
	if out["model"] != "anthropic.claude-opus-5" {
		t.Errorf("model = %v", out["model"])
	}
	if out["stream"] != true {
		t.Errorf("stream = %v, want true", out["stream"])
	}
	if out["max_tokens"] != float64(64) {
		t.Errorf("max_tokens = %v", out["max_tokens"])
	}
}

func TestMantleChatBody(t *testing.T) {
	pr := parsedReq(t, `{"model":"x","max_tokens":64,"temperature":0.7,"top_p":0.9,"stop_sequences":["END"],"messages":[{"role":"user","content":"hi"}]}`)

	// gpt-5.4 keeps sampling params; max_tokens becomes max_completion_tokens.
	body, err := toMantleChatBody(pr, "openai.gpt-5.4", false)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if _, has := out["max_tokens"]; has {
		t.Error("max_tokens must be renamed to max_completion_tokens on mantle")
	}
	if out["max_completion_tokens"] != float64(64) {
		t.Errorf("max_completion_tokens = %v", out["max_completion_tokens"])
	}
	if out["model"] != "openai.gpt-5.4" {
		t.Errorf("model = %v", out["model"])
	}
	if out["temperature"] != 0.7 || out["top_p"] != 0.9 {
		t.Errorf("gpt-5.4 must keep sampling params: %v %v", out["temperature"], out["top_p"])
	}
	if _, has := out["stream"]; has {
		t.Errorf("non-streaming body must not carry stream: %v", out["stream"])
	}

	// gpt-5.6 rejects temperature/top_p/stop — they must be stripped.
	body, err = toMantleChatBody(pr, "openai.gpt-5.6-sol", true)
	if err != nil {
		t.Fatal(err)
	}
	out = map[string]any{}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"temperature", "top_p", "stop"} {
		if _, has := out[k]; has {
			t.Errorf("gpt-5.6: %q must be stripped", k)
		}
	}
	if out["stream"] != true {
		t.Errorf("stream = %v, want true", out["stream"])
	}
	if so, ok := out["stream_options"].(map[string]any); !ok || so["include_usage"] != true {
		t.Errorf("streaming body must request include_usage: %v", out["stream_options"])
	}
}

func parsedReq(t *testing.T, raw string) *providers.ProxyRequest {
	t.Helper()
	req := &providers.ProxyRequest{RawBody: []byte(raw)}
	req.Parsed = parseChat(t, raw)
	return req
}

func parseChat(t *testing.T, raw string) *schema.ChatRequest {
	t.Helper()
	var cr schema.ChatRequest
	if err := json.Unmarshal([]byte(raw), &cr); err != nil {
		t.Fatal(err)
	}
	return &cr
}

func blocksText(blocks []schema.ContentBlock) string {
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type == "text" && blk.Text != nil {
			b.WriteString(*blk.Text)
		}
	}
	return b.String()
}

func staticMantle(t *testing.T, srv *httptest.Server) *mantleClient {
	t.Helper()
	return newMantleClient(srv.URL, "us-east-1",
		aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "AKID", SecretAccessKey: "SECRET"}, nil
		}), srv.Client())
}

func TestMantleCompleteAnthropicPath(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"message","role":"assistant","model":"anthropic.claude-opus-5","content":[{"type":"text","text":"hey"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`))
	}))
	defer srv.Close()
	mc := staticMantle(t, srv)
	raw := `{"anthropic_version":"bedrock-2023-05-31","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
	resp, err := mc.Complete(context.Background(), &providers.ProxyRequest{
		Model: "mantle.opus", Upstream: "anthropic.claude-opus-5",
		RawBody: []byte(raw), Parsed: parseChat(t, raw),
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/anthropic/v1/messages" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotAuth, "AWS4-HMAC-SHA256") {
		t.Errorf("request not SigV4 signed: %q", gotAuth)
	}
	if gotBody["model"] != "anthropic.claude-opus-5" {
		t.Errorf("upstream body model = %v", gotBody["model"])
	}
	if resp.StatusCode != 200 || resp.Parsed == nil || *resp.Parsed.Usage.OutputTokens != 2 {
		t.Fatalf("resp: %+v", resp)
	}
	// The client asked for the PUBLIC name; Mantle answered with the upstream
	// id. Both the parsed response and the teed RawBody must carry the public
	// one, or the client sees a model it never requested.
	if resp.Parsed.Model != "mantle.opus" {
		t.Errorf("parsed model = %q, want the public name mantle.opus", resp.Parsed.Model)
	}
	var teed map[string]any
	if err := json.Unmarshal(resp.RawBody, &teed); err != nil {
		t.Fatal(err)
	}
	if teed["model"] != "mantle.opus" {
		t.Errorf("teed body model = %v, want mantle.opus", teed["model"])
	}
	if teed["stop_reason"] != "end_turn" {
		t.Errorf("re-render lost stop_reason: %v", teed["stop_reason"])
	}
}

func TestMantleCompleteChatPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"openai.gpt-5.4","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"hey"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	}))
	defer srv.Close()
	mc := staticMantle(t, srv)
	raw := `{"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
	resp, err := mc.Complete(context.Background(), &providers.ProxyRequest{
		Model: "mantle.gpt-5.4", Upstream: "openai.gpt-5.4",
		RawBody: []byte(raw), Parsed: parseChat(t, raw),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || resp.Parsed == nil {
		t.Fatalf("resp: %+v", resp)
	}
	if got := blocksText(resp.Parsed.Content); got != "hey" {
		t.Errorf("text = %q", got)
	}
	if resp.Parsed.Usage == nil || *resp.Parsed.Usage.OutputTokens != 2 {
		t.Errorf("usage: %+v", resp.Parsed.Usage)
	}
	// The Bedrock ingress tees RawBody to the client, so a chat-route
	// response must be re-rendered in Anthropic shape under the PUBLIC model
	// name — raw OpenAI wire must never reach an Anthropic-speaking client.
	var body map[string]any
	if err := json.Unmarshal(resp.RawBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["type"] != "message" || body["model"] != "mantle.gpt-5.4" {
		t.Errorf("RawBody not anthropic-shaped/public-model: type=%v model=%v", body["type"], body["model"])
	}
}

func TestMantleStreamChatPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != true {
			t.Errorf("stream flag missing in upstream body: %v", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"he\"}}]}\n\n" +
			"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer srv.Close()
	mc := staticMantle(t, srv)
	raw := `{"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
	evs, err := mc.Stream(context.Background(), &providers.ProxyRequest{
		Model: "mantle.gpt-5.4", Upstream: "openai.gpt-5.4",
		RawBody: []byte(raw), Parsed: parseChat(t, raw),
	})
	if err != nil {
		t.Fatal(err)
	}
	var sawText, sawUsage bool
	for ev, serr := range evs {
		if serr != nil {
			t.Fatal(serr)
		}
		if ev.Chunk == nil {
			continue
		}
		if strings.Contains(string(ev.Chunk.Delta), "he") {
			sawText = true
		}
		if ev.Chunk.Usage != nil && ev.Chunk.Usage.OutputTokens != nil {
			sawUsage = true
		}
	}
	if !sawText || !sawUsage {
		t.Fatalf("stream missing text(%v) or usage(%v)", sawText, sawUsage)
	}
}

// api "mantle" must actually route to the mantle client — the invoke_model
// fallback silently sent mantle-only models to the wrong endpoint.
func TestAPIForMantleNoLongerFallsBack(t *testing.T) {
	p := &provider{modelAPI: map[string]string{"openai.gpt-5.4": "mantle"}}
	if got := p.apiFor("openai.gpt-5.4"); got != "mantle" {
		t.Fatalf("apiFor = %q, want mantle", got)
	}
}
