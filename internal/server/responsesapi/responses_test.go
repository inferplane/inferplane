package responsesapi

// The request fixture is a REAL capture from Codex 0.152.1 (wire_api
// "responses" against a recording stub; large prompts trimmed, structure
// untouched) — see docs/verification/coding-agents.md. These tests pin the
// wire current Codex actually speaks, not a documented reconstruction.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestToChatRequestFromRealCodexCapture(t *testing.T) {
	raw, err := os.ReadFile("testdata/codex_responses_request.json")
	if err != nil {
		t.Fatal(err)
	}
	var rr responsesRequest
	if err := json.Unmarshal(raw, &rr); err != nil {
		t.Fatal(err)
	}
	body, dropped, err := toChatRequest(&rr)
	if err != nil {
		t.Fatalf("toChatRequest: %v", err)
	}
	var chat map[string]any
	if err := json.Unmarshal(body, &chat); err != nil {
		t.Fatal(err)
	}
	msgs := chat["messages"].([]any)
	// instructions → system[0]; the developer message is system too; then users.
	first := msgs[0].(map[string]any)
	if first["role"] != "system" || !strings.Contains(first["content"].(string), "coding agent") {
		t.Fatalf("instructions must become the leading system message: %+v", first)
	}
	if second := msgs[1].(map[string]any); second["role"] != "system" {
		t.Fatalf("a developer input message maps to the system role, got %+v", second)
	}
	if last := msgs[len(msgs)-1].(map[string]any); last["role"] != "user" {
		t.Fatalf("trailing user message lost: %+v", last)
	}
	// Codex always streams; the adapter must ask the chat pipeline for the
	// final usage chunk so response.completed can carry token counts.
	if chat["stream"] != true || chat["stream_options"].(map[string]any)["include_usage"] != true {
		t.Fatalf("stream translation: %v / %v", chat["stream"], chat["stream_options"])
	}
	// The flat function tool becomes a nested chat tool; namespace and
	// web_search have no chat equivalent and are DROPPED WITH DISCLOSURE.
	tools := chat["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("want exactly the function tool translated, got %d", len(tools))
	}
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "exec_command" || fn["parameters"] == nil {
		t.Fatalf("exec_command tool mangled: %+v", fn)
	}
	joined := strings.Join(dropped, ",")
	for _, want := range []string{"tools.namespace", "tools.web_search", "reasoning", "include", "prompt_cache_key"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("dropped params must disclose %q, got %v", want, dropped)
		}
	}
	if chat["tool_choice"] != "auto" {
		t.Fatalf("tool_choice auto must pass through, got %v", chat["tool_choice"])
	}
}

func TestFunctionCallRoundTripItems(t *testing.T) {
	rr := &responsesRequest{Model: "m", Input: json.RawMessage(`[
	  {"type":"function_call","call_id":"call_1","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"},
	  {"type":"function_call_output","call_id":"call_1","output":"file.txt"}
	]`)}
	body, _, err := toChatRequest(rr)
	if err != nil {
		t.Fatal(err)
	}
	var chat struct {
		Messages []map[string]any `json:"messages"`
	}
	json.Unmarshal(body, &chat)
	if len(chat.Messages) != 2 {
		t.Fatalf("want 2 messages, got %+v", chat.Messages)
	}
	tc := chat.Messages[0]["tool_calls"].([]any)[0].(map[string]any)
	if chat.Messages[0]["role"] != "assistant" || tc["id"] != "call_1" ||
		tc["function"].(map[string]any)["name"] != "exec_command" {
		t.Fatalf("function_call item mangled: %+v", chat.Messages[0])
	}
	if chat.Messages[1]["role"] != "tool" || chat.Messages[1]["tool_call_id"] != "call_1" ||
		chat.Messages[1]["content"] != "file.txt" {
		t.Fatalf("function_call_output item mangled: %+v", chat.Messages[1])
	}
}

// A fake chat ingress: replays a canned chat SSE stream (content deltas, a
// tool-call delta pair, the include_usage final chunk) or a canned JSON.
func fakeChat(stream bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !stream {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hello","tool_calls":[{"id":"call_9","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":\"ls\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, d := range []string{
			`{"choices":[{"delta":{"role":"assistant","content":"hel"}}]}`,
			`{"choices":[{"delta":{"content":"lo"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","function":{"name":"exec_command","arguments":"{\"cmd\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]}},{"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
			`[DONE]`,
		} {
			w.Write([]byte("data: " + d + "\n\n"))
		}
	})
}

func TestServeJSONTranslatesChatResponse(t *testing.T) {
	h := New(fakeChat(false))
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","input":"hi"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var resp struct {
		Object string           `json:"object"`
		Status string           `json:"status"`
		Output []map[string]any `json:"output"`
		Usage  map[string]any   `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Object != "response" || resp.Status != "completed" || len(resp.Output) != 2 {
		t.Fatalf("response envelope: %s", rec.Body)
	}
	msg, call := resp.Output[0], resp.Output[1]
	if msg["type"] != "message" || msg["content"].([]any)[0].(map[string]any)["text"] != "hello" {
		t.Fatalf("message item: %+v", msg)
	}
	if call["type"] != "function_call" || call["call_id"] != "call_9" || call["name"] != "exec_command" {
		t.Fatalf("function_call item: %+v", call)
	}
	if resp.Usage["input_tokens"].(float64) != 10 || resp.Usage["output_tokens"].(float64) != 5 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

func TestServeStreamTranslatesChatSSE(t *testing.T) {
	h := New(fakeChat(true))
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","input":"hi","stream":true}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d ct %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, want := range []string{
		`event: response.created`,
		`"type":"response.output_text.delta"`,
		`"delta":"hel"`,
		`event: response.output_item.done`,
		`"call_id":"call_9"`,
		`"arguments":"{\"cmd\":\"ls\"}"`, // split across two deltas, reassembled
		`event: response.completed`,
		`"input_tokens":10`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream missing %q:\n%s", want, body)
		}
	}
	// The full text must appear in the final message item.
	if !strings.Contains(body, `"text":"hello"`) {
		t.Fatalf("accumulated text missing:\n%s", body)
	}
}

// An error from the pipeline (e.g. an invalid key's 401) passes through as
// its original JSON error body — never re-wrapped into a fake 200 stream.
func TestErrorPassthrough(t *testing.T) {
	deny := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"message":"invalid API key","type":"authentication_error"}}`))
	})
	for _, stream := range []bool{false, true} {
		h := New(deny)
		body := `{"model":"m","input":"hi"}`
		if stream {
			body = `{"model":"m","input":"hi","stream":true}`
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
		if rec.Code != 401 || !strings.Contains(rec.Body.String(), "invalid API key") {
			t.Fatalf("stream=%v: error must pass through verbatim, got %d %s", stream, rec.Code, rec.Body)
		}
	}
}
