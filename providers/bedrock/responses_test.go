package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/openai"
	"github.com/inferplane/inferplane/internal/responses"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
)

// Exercise real provider dispatch/conversion; only the network/SDK is replaced.
type responsesBackend struct {
	p        *provider
	inv      *fakeInvoker
	conv     *fakeConverser
	upstream string
	wire     []byte
	calls    int
	output   *schema.ChatResponse
}

func newResponsesBackend(t *testing.T, api string) *responsesBackend {
	t.Helper()
	b := &responsesBackend{
		inv: &fakeInvoker{}, conv: &fakeConverser{}, upstream: "bridge-model",
		output: parseBridgeResponse(t, `{"type":"message","role":"assistant","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":3,"cache_read_input_tokens":2}}`),
	}
	b.p = &provider{inv: b.inv, conv: b.conv, modelAPI: map[string]string{b.upstream: api}}
	if strings.HasPrefix(api, "mantle") {
		b.upstream = "openai.bridge"
		if api == "mantle-anthropic" {
			b.upstream = "anthropic.bridge"
		}
		b.p.modelAPI[b.upstream] = "mantle"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b.calls++
			b.wire, _ = io.ReadAll(r.Body)
			if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") ||
				strings.Contains(r.Header.Get("Authorization"), "client-token") ||
				r.Header.Get("X-Api-Key") != "" {
				t.Error("Mantle did not isolate client credentials")
			}
			var top map[string]json.RawMessage
			_ = json.Unmarshal(b.wire, &top)
			chat := !strings.Contains(r.URL.Path, "/messages")
			if string(top["stream"]) == "true" {
				w.Header().Set("Content-Type", "text/event-stream")
				state := openai.StreamState{IncludeUsage: true}
				for _, c := range bridgeChunks(b.output) {
					if chat {
						if raw := openai.ChunkFromCanonical(c, &state); len(raw) > 0 {
							fmt.Fprintf(w, "data: %s\n\n", raw)
						}
					} else {
						_ = schema.WriteAnthropicSSE(w, c)
					}
				}
				if chat {
					fmt.Fprint(w, "data: [DONE]\n\n")
				}
			} else if chat {
				_, _ = w.Write(openai.ResponseFromCanonical(b.output))
			} else {
				_ = json.NewEncoder(w).Encode(b.output)
			}
		}))
		t.Cleanup(srv.Close)
		b.p.man = staticMantle(t, srv)
	}
	return b
}

func parseBridgeResponse(t *testing.T, raw string) *schema.ChatResponse {
	t.Helper()
	var out schema.ChatResponse
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

func bridgeJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func bridgeChunks(out *schema.ChatResponse) []*schema.ChatChunk {
	chunks := []*schema.ChatChunk{{Type: "message_start", Message: &schema.ChatResponse{Type: "message", Role: "assistant", Usage: out.Usage}}}
	for i, block := range out.Content {
		block, idx := block, i
		delta := map[string]string{"type": "text_delta"}
		if block.Type == "tool_use" {
			delta["type"], delta["partial_json"] = "input_json_delta", string(block.Input)
			block.Input = json.RawMessage("{}")
		} else {
			delta["text"] = *block.Text
			empty := ""
			block.Text = &empty
		}
		chunks = append(chunks,
			&schema.ChatChunk{Type: "content_block_start", Index: &idx, ContentBlock: &block},
			&schema.ChatChunk{Type: "content_block_delta", Index: &idx, Delta: bridgeJSON(delta)},
			&schema.ChatChunk{Type: "content_block_stop", Index: &idx})
	}
	return append(chunks,
		&schema.ChatChunk{Type: "message_delta", Delta: bridgeJSON(map[string]any{"stop_reason": out.StopReason}), Usage: out.Usage},
		&schema.ChatChunk{Type: "message_stop"})
}

func (b *responsesBackend) invoke(req *providers.ProxyRequest, stream bool) (*schema.ChatResponse, error) {
	b.inv.respBody = bridgeJSON(b.output)
	b.inv.streamRaw = nil
	for _, c := range bridgeChunks(b.output) {
		b.inv.streamRaw = append(b.inv.streamRaw, bridgeJSON(c))
	}
	b.conv.resp = ConverseResponse{Content: b.output.Content, StopReason: *b.output.StopReason, InputTokens: 7, OutputTokens: 3, CacheReadTokens: 2}
	b.conv.streamEv = nil
	for _, block := range b.output.Content {
		if block.Type == "tool_use" {
			b.conv.streamEv = append(b.conv.streamEv,
				ConverseStreamEvent{Kind: eventToolUseStart, ToolName: block.Name, ToolUseID: block.ID},
				ConverseStreamEvent{Kind: eventToolInputDelta, ToolDelta: string(block.Input)})
		} else {
			b.conv.streamEv = append(b.conv.streamEv, ConverseStreamEvent{Kind: eventTextDelta, TextDelta: *block.Text})
		}
		b.conv.streamEv = append(b.conv.streamEv, ConverseStreamEvent{Kind: eventBlockStop})
	}
	b.conv.streamEv = append(b.conv.streamEv,
		ConverseStreamEvent{Kind: eventMessageStop, StopReason: *b.output.StopReason},
		ConverseStreamEvent{Kind: eventUsage, InputTokens: 7, OutputTokens: 3, CacheReadTokens: 2})
	if !stream {
		out, err := b.p.Complete(context.Background(), req)
		if err != nil {
			return nil, err
		}
		if out.StatusCode/100 != 2 {
			return out.Parsed, &providers.UpstreamError{StatusCode: out.StatusCode, Body: out.RawBody}
		}
		return out.Parsed, nil
	}
	evs, err := b.p.Stream(context.Background(), req)
	if err != nil {
		return nil, err
	}
	out := &schema.ChatResponse{Type: "message", Role: "assistant"}
	for ev, err := range evs {
		if err != nil {
			return out, err
		}
		if ev == nil || ev.Chunk == nil {
			continue
		}
		c := ev.Chunk
		if c.Message != nil {
			out.Usage = schema.MergeUsage(out.Usage, c.Message.Usage)
		}
		out.Usage = schema.MergeUsage(out.Usage, c.Usage)
		if c.ContentBlock != nil {
			out.Content = append(out.Content, *c.ContentBlock)
		}
		if c.Type == "content_block_delta" && len(out.Content) > 0 {
			block := &out.Content[len(out.Content)-1]
			var delta map[string]string
			_ = json.Unmarshal(c.Delta, &delta)
			if delta["type"] == "input_json_delta" {
				if string(block.Input) == "{}" {
					block.Input = nil
				}
				block.Input = append(block.Input, delta["partial_json"]...)
			} else if block.Text != nil {
				s := *block.Text + delta["text"]
				block.Text = &s
			}
		}
		if c.Type == "message_delta" {
			var d struct {
				StopReason *string `json:"stop_reason"`
			}
			_ = json.Unmarshal(c.Delta, &d)
			if d.StopReason != nil {
				out.StopReason = d.StopReason
			}
		}
	}
	return out, nil
}

func (b *responsesBackend) request(t *testing.T) *schema.ChatRequest {
	t.Helper()
	if b.conv.gotModelID != "" {
		cr := b.conv.gotReq
		n, _ := cr.Inference["maxTokens"].(int64)
		out := &schema.ChatRequest{MaxTokens: &n, System: bridgeJSON(cr.System), ToolChoice: bridgeJSON(map[string]string{"type": cr.ToolChoice.Type, "name": cr.ToolChoice.Name})}
		for _, m := range cr.Messages {
			out.Messages = append(out.Messages, schema.Message{Role: m.Role, Content: m.Content})
		}
		var tools []map[string]any
		for _, tool := range cr.Tools {
			tools = append(tools, map[string]any{"name": tool.Name, "input_schema": tool.InputSchema})
		}
		out.Tools = bridgeJSON(tools)
		return out
	}
	raw := b.inv.gotBody
	if raw == nil {
		raw = b.wire
		if b.upstream == "openai.bridge" {
			out, err := openai.RequestToCanonical(raw)
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
	}
	return parseChat(t, string(raw))
}

func responsesRequest(t *testing.T, b *responsesBackend, raw string) *providers.ProxyRequest {
	t.Helper()
	cr, err := responses.RequestToCanonical([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return &providers.ProxyRequest{Model: "public", Upstream: b.upstream, IngressProtocol: "responses",
		RawBody: []byte(raw), Parsed: cr, Headers: http.Header{"Authorization": {"Bearer client-token"}, "X-Api-Key": {"client-token"}}}
}

var responsesAPIs = []string{"converse", "invoke_model", "mantle-chat", "mantle-anthropic"}

func TestResponsesBridgeText(t *testing.T) {
	for _, api := range responsesAPIs {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", api, stream), func(t *testing.T) {
				b := newResponsesBackend(t, api)
				req := responsesRequest(t, b, `{"model":"public","instructions":"be brief","input":"hello","store":false,"max_output_tokens":57,"client_metadata":{"private":"ignore"}}`)
				before := bridgeJSON(req)
				// RawBody is authoritative even if the observation view is stale.
				req.Parsed.Messages = nil
				before = bridgeJSON(req)
				out, err := b.invoke(req, stream)
				if err != nil {
					t.Fatal(err)
				}
				got := b.request(t)
				if got.MaxTokens == nil || *got.MaxTokens != 57 || len(got.Messages) != 1 || blocksText(got.Messages[0].Content) != "hello" || systemText(got.System) != "be brief" {
					t.Fatalf("Responses did not reach backend as bounded Anthropic canonical text: %+v", got)
				}
				if out == nil || blocksText(out.Content) != "hello" || out.Usage == nil || *out.Usage.InputTokens != 7 || *out.Usage.OutputTokens != 3 || *out.Usage.CacheReadInputTokens != 2 {
					t.Fatalf("text/usage lost: %+v", out)
				}
				if !reflect.DeepEqual(before, bridgeJSON(req)) {
					t.Fatal("mutated caller request")
				}
				for _, forbidden := range []string{`"input"`, `"store"`, `"client_metadata"`, "client-token"} {
					if strings.Contains(string(b.inv.gotBody)+string(b.wire), forbidden) {
						t.Fatalf("leaked Responses metadata: %s", forbidden)
					}
				}
			})
		}
	}
}

func TestResponsesBridgeRejectsBeforeDispatch(t *testing.T) {
	bodies := []string{
		`{`,
		`{"model":"public","input":"hi","previous_response_id":"opaque"}`,
		`{"model":"public","input":[{"type":"reasoning","encrypted_content":"opaque"}]}`,
		`{"model":"public","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://invalid.test"}]}]}`,
		`{"model":"public","input":"hi","tools":[{"type":"web_search"}]}`,
		`{"model":"public","input":"hi","tools":[{"type":"function","name":"f","parameters":{},"strict":true}]}`,
		`{"model":"public","input":"hi","tools":[{"type":"function","name":"f","parameters":{}}]}`,
		`{"model":"public","input":"hi","tools":[{"type":"namespace","name":"ns","tools":[{"type":"function","name":"f","parameters":{}}]}]}`,
		`{"model":"public","input":"hi","tools":[{"type":"function","name":"f","parameters":{},"strict":"false"}]}`,
		`{"model":"public","input":"hi","tools":[{"type":"function","name":"f","parameters":{},"Strict":false}]}`,
		`{"model":"public","input":"hi","tools":[{"type":"function","name":"f","parameters":null,"strict":false}]}`,
		`{"model":"public","input":[{"type":"function_call","call_id":"bad","name":"f","arguments":"[]"}]}`,
		`{"model":"public","input":[{"type":"function_call_output","call_id":"bad","output":[{"type":"input_image","image_url":"https://invalid.test"}]}]}`,
		`{"model":"public","input":"hi","reasoning":{"effort":"high"}}`,
		`{"model":"public","input":"hi","parallel_tool_calls":false}`,
		`{"model":"public","input":"hi","store":true}`,
		`{"model":"public","input":"hi","text":{"format":{"type":"json_schema","schema":{}}}}`,
	}
	for _, api := range responsesAPIs {
		for _, stream := range []bool{false, true} {
			for i, raw := range bodies {
				t.Run(fmt.Sprintf("%s/stream=%t/%d", api, stream, i), func(t *testing.T) {
					b := newResponsesBackend(t, api)
					_, err := b.invoke(&providers.ProxyRequest{Upstream: b.upstream, IngressProtocol: "responses", RawBody: []byte(raw)}, stream)
					if err == nil {
						t.Fatal("accepted unsupported Responses input")
					}
					if b.calls != 0 || b.inv.gotModelID != "" || b.conv.gotModelID != "" {
						t.Fatal("dispatched invalid input")
					}
					if !errors.Is(err, responses.ErrInvalid) && !errors.Is(err, responses.ErrUnsupported) {
						t.Fatalf("lost conversion error: %v", err)
					}
				})
			}
		}
	}
}

func bridgeToolFixture() map[string]any {
	long := strings.Repeat("mcp_very_long_name_", 5)
	tools := []any{
		map[string]any{"type": "function", "name": long + "a", "strict": false, "parameters": map[string]any{"type": "object", "properties": map[string]any{"phase": map[string]string{"type": "string"}}}},
		map[string]any{"type": "function", "name": long + "b", "strict": false, "parameters": map[string]any{"type": "object"}},
		map[string]any{"type": "custom", "name": "shell.exec", "format": map[string]string{"type": "text"}},
		map[string]any{"type": "namespace", "name": "aws-sdk", "tools": []any{
			map[string]any{"type": "function", "name": "getObject", "strict": false, "parameters": map[string]any{"type": "object"}},
			map[string]any{"type": "custom", "name": "run-script"},
		}},
	}
	input := []any{
		map[string]any{"role": "user", "content": "run these tools"},
		map[string]any{"role": "assistant", "phase": "commentary", "content": "checking"},
		map[string]any{"type": "function_call", "call_id": "old_0", "name": long + "a", "arguments": `{"phase":"application data"}`},
		map[string]any{"type": "function_call", "call_id": "old_1", "name": long + "b", "arguments": `{}`},
		map[string]any{"type": "custom_tool_call", "call_id": "old_2", "name": "shell.exec", "input": "printf hello"},
		map[string]any{"type": "function_call", "call_id": "old_3", "namespace": "aws-sdk", "name": "getObject", "arguments": `{}`},
		map[string]any{"type": "custom_tool_call", "call_id": "old_4", "namespace": "aws-sdk", "name": "run-script", "input": "echo namespaced"},
	}
	for i := range 5 {
		kind := "function_call_output"
		if i == 2 || i == 4 {
			kind = "custom_tool_call_output"
		}
		input = append(input, map[string]any{"type": kind, "call_id": fmt.Sprintf("old_%d", i), "output": fmt.Sprintf("result %d", i)})
	}
	return map[string]any{"model": "public", "input": input, "tools": tools}
}

func bridgeToolNames(t *testing.T, cr *schema.ChatRequest) []string {
	t.Helper()
	var tools []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(cr.Tools, &tools); err != nil && len(cr.Tools) > 0 {
		t.Fatal(err)
	}
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Name
	}
	return names
}

func TestResponsesBridgeToolsAndReplay(t *testing.T) {
	for _, api := range responsesAPIs {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", api, stream), func(t *testing.T) {
				b := newResponsesBackend(t, api)
				fixture := bridgeToolFixture()
				req := responsesRequest(t, b, string(bridgeJSON(fixture)))
				before := bridgeJSON(req)
				if _, err := b.invoke(req, stream); err != nil {
					t.Fatal(err)
				}
				sent := b.request(t)
				names, originals := bridgeToolNames(t, sent), bridgeToolNames(t, req.Parsed)
				if len(names) != 5 {
					t.Fatalf("silently dropped tools: %v", names)
				}
				seen := map[string]bool{}
				for _, name := range names {
					if !bedrockToolNameRE.MatchString(name) || len(name) > 64 || seen[name] {
						t.Fatalf("invalid or colliding backend alias %q", name)
					}
					seen[name] = true
				}
				if sent.MaxTokens == nil || *sent.MaxTokens != 4096 {
					t.Fatalf("missing bounded default: %+v", sent.MaxTokens)
				}
				wantMessages := 3
				if api == "mantle-chat" {
					wantMessages = 7 // Chat has one role:tool message per result.
				}
				if len(sent.Messages) != wantMessages || sent.Messages[1].Role != "assistant" || len(sent.Messages[1].Content) != 6 {
					t.Fatalf("parallel calls/results not grouped into turns: %+v", sent.Messages)
				}
				var results []schema.ContentBlock
				for _, msg := range sent.Messages[2:] {
					if msg.Role != "user" {
						t.Fatalf("bad result role: %s", msg.Role)
					}
					results = append(results, msg.Content...)
				}
				if len(results) != 5 {
					t.Fatalf("lost parallel results: %+v", results)
				}
				for i, block := range sent.Messages[1].Content[1:] {
					if block.Name != names[i] || block.ID != fmt.Sprintf("old_%d", i) {
						t.Fatalf("history alias/id mismatch: %+v", block)
					}
				}
				if !strings.Contains(string(sent.Messages[1].Content[1].Input), `"phase":"application data"`) {
					t.Fatal("mutated tool arguments")
				}
				for i, result := range results {
					if result.ToolUseID != fmt.Sprintf("old_%d", i) {
						t.Fatalf("result lost call id: %+v", result)
					}
				}
				for _, msg := range sent.Messages {
					if len(msg.Extra["phase"]) != 0 {
						t.Fatal("phase leaked in envelope")
					}
					for _, block := range msg.Content {
						if len(block.Extra["phase"]) != 0 {
							t.Fatal("block phase leaked in envelope")
						}
					}
				}
				// Echo backend aliases with parallel custom/function output.
				b.output.Content = nil
				stop := "tool_use"
				b.output.StopReason = &stop
				for i, name := range names {
					args := json.RawMessage(`{"phase":"application data"}`)
					if i == 2 || i == 4 {
						args = json.RawMessage(`{"input":"echo hello\nprintf done"}`)
					}
					b.output.Content = append(b.output.Content, schema.ContentBlock{Type: "tool_use", ID: fmt.Sprintf("new_%d", i), Name: name, Input: args})
				}
				out, err := b.invoke(req, stream)
				if err != nil {
					t.Fatal(err)
				}
				if len(out.Content) != 5 {
					t.Fatalf("parallel outputs lost: %+v", out.Content)
				}
				for i, block := range out.Content {
					if block.Name != originals[i] {
						t.Fatalf("alias not reversed: got %q want %q", block.Name, originals[i])
					}
				}
				rendered, err := responses.CanonicalToResponseForRequest(out, req.Parsed)
				if err != nil {
					t.Fatal(err)
				}
				var wire struct {
					Output []map[string]any `json:"output"`
				}
				if err := json.Unmarshal(rendered, &wire); err != nil {
					t.Fatal(err)
				}
				for i, item := range wire.Output {
					wantKind := "function_call"
					if i == 2 || i == 4 {
						wantKind = "custom_tool_call"
					}
					if item["type"] != wantKind {
						t.Fatalf("custom identity lost: %s", rendered)
					}
					if i >= 3 && item["namespace"] != "aws-sdk" {
						t.Fatalf("namespace lost: %s", rendered)
					}
					if i == 2 && item["input"] != "echo hello\nprintf done" {
						t.Fatalf("custom input lost: %s", rendered)
					}
				}
				if !reflect.DeepEqual(before, bridgeJSON(req)) {
					t.Fatal("mutated original canonical request")
				}
				input := fixture["input"].([]any)
				for _, item := range wire.Output {
					input = append(input, item)
				}
				for i := range 5 {
					kind := "function_call_output"
					if i == 2 || i == 4 {
						kind = "custom_tool_call_output"
					}
					input = append(input, map[string]any{"type": kind, "call_id": fmt.Sprintf("new_%d", i), "output": "ok"})
				}
				fixture["input"] = input
				replay := responsesRequest(t, b, string(bridgeJSON(fixture)))
				if _, err := b.invoke(replay, stream); err != nil {
					t.Fatal(err)
				}
				if got := bridgeToolNames(t, b.request(t)); !reflect.DeepEqual(names, got) {
					t.Fatalf("aliases changed on replay: %v => %v", names, got)
				}
			})
		}
	}
}

func TestResponsesBridgeToolChoice(t *testing.T) {
	for _, api := range responsesAPIs {
		for _, stream := range []bool{false, true} {
			for _, choice := range []string{"auto", "none", "required", "specific", "namespace"} {
				t.Run(fmt.Sprintf("%s/%t/%s", api, stream, choice), func(t *testing.T) {
					b := newResponsesBackend(t, api)
					fixture := bridgeToolFixture()
					fixture["tool_choice"] = choice
					if choice == "specific" {
						fixture["tool_choice"] = map[string]any{"type": "custom", "name": "shell.exec"}
					}
					if choice == "namespace" {
						fixture["tool_choice"] = map[string]any{"type": "function", "name": "getObject", "namespace": "aws-sdk"}
					}
					req := responsesRequest(t, b, string(bridgeJSON(fixture)))
					if _, err := b.invoke(req, stream); err != nil {
						t.Fatal(err)
					}
					sent := b.request(t)
					names := bridgeToolNames(t, sent)
					var tc struct{ Type, Name string }
					_ = json.Unmarshal(sent.ToolChoice, &tc)
					switch choice {
					case "none":
						if len(names) != 0 {
							t.Fatalf("none still emitted tool schemas: %v", names)
						}
					case "required":
						if tc.Type != "any" || len(names) != 5 {
							t.Fatalf("required weakened: %s", sent.ToolChoice)
						}
					case "specific", "namespace":
						idx := 2
						if choice == "namespace" {
							idx = 3
						}
						if len(names) != 5 || tc.Type != "tool" || tc.Name != names[idx] {
							t.Fatalf("specific choice lost: %s tools=%v", sent.ToolChoice, names)
						}
					}
				})
			}
		}
	}
}

func TestResponsesBridgeUnknownToolRefuses(t *testing.T) {
	for _, api := range responsesAPIs {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", api, stream), func(t *testing.T) {
				b := newResponsesBackend(t, api)
				req := responsesRequest(t, b, `{"model":"public","input":"hi","tools":[{"type":"function","name":"known","parameters":{},"strict":false}]}`)
				b.output.Content = []schema.ContentBlock{{Type: "tool_use", ID: "bad", Name: "unknown_alias", Input: json.RawMessage("{}")}}
				_, err := b.invoke(req, stream)
				if err == nil {
					t.Fatal("unknown upstream alias passed as a valid tool")
				}
			})
		}
	}
}
