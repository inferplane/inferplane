package openaiapi

// Agent wire-shape fixtures (Core Purpose #1): the OpenAI-compat ingress is
// the path Codex (wire_api = "chat") and OpenCode (openai-compatible
// providers) ride, and until these tests existed nothing in the tree
// exercised their actual request shapes — system prompt + agentic function
// tools + streaming, tool-call round-trip turns, and OpenCode's
// stream_options.include_usage.
//
// Provenance, honestly stated: the fixtures are CONSTRUCTED from the
// codex-rs chat-completions client (wire_api "chat") and OpenCode's
// openai-compatible provider payloads as of 2026 — not recorded captures.
// They pin the load-bearing shape elements (null assistant content beside
// tool_calls, role:"tool" turns keyed by tool_call_id, strict/
// additionalProperties tool schemas, stream_options) so a regression on any
// of them fails here first; replacing them with recorded captures from real
// clients upgrades roadmap Purpose #1 from "fixture-verified" to
// "client-verified" and is still wanted.
//
// Each fixture runs against BOTH provider wires: openai (verbatim tee — the
// vLLM/GPU path) and anthropic (schema translation — the "Codex user served
// a Claude model" cost-substitution path, Core Purpose #3).

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// oaiWireRouterFor mirrors oaiWireRouter but routes the fixtures' "gpt-x".
func oaiWireRouterFor() *router.Router {
	provs := map[string]providers.Provider{"p": oaiWireProvider{}}
	models := map[string]config.ModelConfig{
		"gpt-x": {Targets: []config.Target{{Provider: "p", Model: "gpt-x"}}},
	}
	return router.New(holderFor(provs, models))
}

func anthropicWireRouterFor() *router.Router {
	provs := map[string]providers.Provider{"p": mockprovider.New("gpt-x")}
	models := map[string]config.ModelConfig{
		"gpt-x": {Targets: []config.Target{{Provider: "p", Model: "gpt-x"}}},
	}
	return router.New(holderFor(provs, models))
}

func serveAgentFixture(t *testing.T, r *router.Router, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewChatHandler(r)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"*"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

func TestChatCodexShapedRequestStreamsVerbatimOnOpenAIWire(t *testing.T) {
	rec := serveAgentFixture(t, oaiWireRouterFor(), fixture(t, "codex_chat_stream.json"))
	if rec.Code != 200 {
		t.Fatalf("codex-shaped stream on openai wire: status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"chat.completion.chunk"`) {
		t.Fatalf("missing chunk shape: %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("missing terminal [DONE] — codex's stream reader hangs without it: %s", body)
	}
}

func TestChatCodexShapedRequestConvertsOnAnthropicWire(t *testing.T) {
	// The cost-substitution path: a Codex client served an anthropic-wire
	// model must get back OpenAI chunks, with tools/system converted on the
	// way in rather than rejected.
	rec := serveAgentFixture(t, anthropicWireRouterFor(), fixture(t, "codex_chat_stream.json"))
	if rec.Code != 200 {
		t.Fatalf("codex-shaped stream on anthropic wire: status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"chat.completion.chunk"`) {
		t.Fatalf("stream not converted to OpenAI chunk shape: %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("missing terminal [DONE]: %s", body)
	}
	if strings.Contains(body, "event: message_start") {
		t.Fatalf("anthropic SSE leaked into a codex-facing stream: %s", body)
	}
}

func TestChatCodexToolResultTurnAcceptedOnBothWires(t *testing.T) {
	// Turn 2 of an agentic loop: assistant tool_calls with null content,
	// then a role:"tool" result keyed by tool_call_id. A gateway that 4xxs
	// this shape breaks Codex after its FIRST shell command.
	for name, r := range map[string]*router.Router{
		"openai-wire":    oaiWireRouterFor(),
		"anthropic-wire": anthropicWireRouterFor(),
	} {
		rec := serveAgentFixture(t, r, fixture(t, "codex_chat_toolresult.json"))
		if rec.Code != 200 {
			t.Fatalf("%s: codex tool-result turn rejected: status %d: %s", name, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "data: [DONE]") {
			t.Fatalf("%s: missing terminal [DONE]: %s", name, rec.Body.String())
		}
	}
}

func TestChatOpenCodeShapedRequestIncludeUsageOnAnthropicWire(t *testing.T) {
	// OpenCode opts into stream_options.include_usage; on a converted
	// (anthropic-wire) stream the final chunk must carry usage, or
	// OpenCode's cost display reads zero for every governed request.
	rec := serveAgentFixture(t, anthropicWireRouterFor(), fixture(t, "opencode_chat_stream.json"))
	if rec.Code != 200 {
		t.Fatalf("opencode-shaped stream: status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"prompt_tokens"`) {
		t.Fatalf("stream_options.include_usage honored nowhere — no usage chunk in converted stream: %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("missing terminal [DONE]: %s", body)
	}
}

func TestChatOpenCodeShapedRequestStreamsVerbatimOnOpenAIWire(t *testing.T) {
	rec := serveAgentFixture(t, oaiWireRouterFor(), fixture(t, "opencode_chat_stream.json"))
	if rec.Code != 200 {
		t.Fatalf("opencode-shaped stream on openai wire: status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Fatalf("missing terminal [DONE]: %s", rec.Body.String())
	}
}
