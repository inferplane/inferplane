package bedrock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/inferplane/inferplane/internal/responses"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
)

func TestResponsesIngressCapability(t *testing.T) {
	p := &provider{}
	for _, protocol := range []string{"anthropic", "openai", "bedrock", "responses"} {
		if !p.SupportsIngress(protocol) {
			t.Fatalf("unsupported existing ingress %s", protocol)
		}
	}
	if p.SupportsIngress("unknown") || p.SupportsIngress("") {
		t.Fatal("claimed unknown ingress")
	}
}

func TestResponsesToolAliasCollisionAndOrder(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			b := newResponsesBackend(t, "converse")
			fixture := bridgeToolFixture()
			req := responsesRequest(t, b, string(bridgeJSON(fixture)))
			if _, err := b.invoke(req, stream); err != nil {
				t.Fatal(err)
			}
			originalNames := bridgeToolNames(t, b.request(t))
			tools := fixture["tools"].([]any)
			slices.Reverse(tools)
			fixture["tools"] = tools
			req = responsesRequest(t, b, string(bridgeJSON(fixture)))
			if _, err := b.invoke(req, stream); err != nil {
				t.Fatal(err)
			}
			reordered := bridgeToolNames(t, b.request(t))
			if !reflect.DeepEqual([]string{originalNames[3], originalNames[4], originalNames[2], originalNames[1], originalNames[0]}, reordered) {
				t.Fatalf("alias depended on tool order: %v => %v", originalNames, reordered)
			}
			// Use a literal name that equals another tool's generated alias.
			// Both declaration orders must fail, never silently bind the wrong tool.
			collision := map[string]any{"type": "function", "name": originalNames[0], "strict": false, "parameters": map[string]any{"type": "object"}}
			for _, first := range []bool{false, true} {
				colliding := slices.Clone(tools)
				if first {
					colliding = append([]any{collision}, colliding...)
				} else {
					colliding = append(colliding, collision)
				}
				fixture["tools"] = colliding
				refused := newResponsesBackend(t, "converse")
				req = responsesRequest(t, refused, string(bridgeJSON(fixture)))
				before := bridgeJSON(req)
				_, err := refused.invoke(req, stream)
				if !errors.Is(err, responses.ErrInvalid) || refused.conv.gotModelID != "" {
					t.Fatalf("collision reached upstream: %v", err)
				}
				if !bytes.Equal(before, bridgeJSON(req)) {
					t.Fatal("collision rejection mutated input")
				}
			}
			// The same collision in history is also ambiguous even when that
			// tool is no longer offered in this turn.
			fixture["tools"] = tools
			fixture["input"] = append(fixture["input"].([]any),
				map[string]any{"type": "function_call", "name": originalNames[0], "call_id": "collision", "arguments": "{}"})
			refused := newResponsesBackend(t, "converse")
			req = responsesRequest(t, refused, string(bridgeJSON(fixture)))
			_, err := refused.invoke(req, stream)
			if !errors.Is(err, responses.ErrInvalid) || refused.conv.gotModelID != "" {
				t.Fatalf("history collision reached upstream: %v", err)
			}
		})
	}
}

func TestResponsesToolNameBoundaries(t *testing.T) {
	b := newResponsesBackend(t, "converse")
	names := []string{"a", strings.Repeat("a", 64), strings.Repeat("a", 65), "9tool", "_tool", "a-b", "한글도구"}
	var tools []any
	for _, name := range names {
		tools = append(tools, map[string]any{"type": "function", "name": name, "strict": false, "parameters": map[string]any{"type": "object"}})
	}
	req := responsesRequest(t, b, string(bridgeJSON(map[string]any{"model": "public", "input": "hi", "tools": tools})))
	if _, err := b.invoke(req, false); err != nil {
		t.Fatal(err)
	}
	got := bridgeToolNames(t, b.request(t))
	if len(got) != len(names) || got[0] != names[0] || got[1] != names[1] {
		t.Fatalf("valid names changed: %v", got)
	}
	for i := 2; i < len(names); i++ {
		if got[i] == names[i] || !bedrockToolNameRE.MatchString(got[i]) || len(got[i]) > 64 {
			t.Fatalf("invalid alias: %q", got[i])
		}
	}
}

func TestResponsesToolChoiceRefusesMissingDeclaration(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, choice := range []string{`"required"`, `{"type":"function","name":"history_only"}`} {
			b := newResponsesBackend(t, "converse")
			req := responsesRequest(t, b, `{"model":"public","input":[{"type":"function_call","name":"history_only","call_id":"old","arguments":"{}"},{"type":"function_call_output","call_id":"old","output":"ok"}],"tool_choice":`+choice+`}`)
			_, err := b.invoke(req, stream)
			if !errors.Is(err, responses.ErrInvalid) || b.conv.gotModelID != "" {
				t.Fatalf("missing choice declaration reached dispatch: %v", err)
			}
		}
	}
}

func TestResponsesUnknownToolRetainsUsage(t *testing.T) {
	for _, api := range responsesAPIs {
		for _, stream := range []bool{false, true} {
			for _, choice := range []string{"auto", "none"} {
				t.Run(fmt.Sprintf("%s/%t/%s", api, stream, choice), func(t *testing.T) {
					b := newResponsesBackend(t, api)
					req := responsesRequest(t, b, `{"model":"public","input":"hi","tools":[{"type":"function","name":"known","parameters":{},"strict":false}],"tool_choice":"`+choice+`"}`)
					name := "unknown_alias"
					if choice == "none" {
						name = "known"
					}
					b.output.Content = []schema.ContentBlock{{Type: "tool_use", ID: "bad", Name: name, Input: json.RawMessage("{}")}}
					out, err := b.invoke(req, stream)
					if err == nil || out == nil || out.Usage == nil || out.Usage.InputTokens == nil || *out.Usage.InputTokens != 7 || *out.Usage.OutputTokens != 3 {
						t.Fatalf("refusal discarded usage: out=%+v err=%v", out, err)
					}
					for _, block := range out.Content {
						if block.Type == "tool_use" {
							t.Fatal("refusal exposed unauthorized tool")
						}
					}
				})
			}
		}
	}
}

func TestResponsesGuardrails(t *testing.T) {
	for _, api := range responsesAPIs {
		for _, stream := range []bool{false, true} {
			for _, override := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%t/%t", api, stream, override), func(t *testing.T) {
					b := newResponsesBackend(t, api)
					b.p.defaultGuardrail = Guardrail{ID: "default-gr", Version: "1"}
					req := responsesRequest(t, b, `{"model":"public","input":"hi"}`)
					want := b.p.defaultGuardrail
					if override {
						req.GuardrailID, req.GuardrailVersion = "team-gr", "7"
						want = Guardrail{ID: "team-gr", Version: "7"}
					}
					out, err := b.invoke(req, stream)
					if strings.HasPrefix(api, "mantle") {
						var upstream *providers.UpstreamError
						if !errors.As(err, &upstream) || upstream.StatusCode != 400 || b.calls != 0 {
							t.Fatalf("Mantle bypassed guardrail: %v", err)
						}
						return
					}
					if err != nil || out == nil {
						t.Fatal(err)
					}
					got := b.inv.gotGuardrail
					if api == "converse" {
						got = b.conv.gotReq.Guardrail
					}
					if got != want {
						t.Fatalf("guardrail lost: got=%+v want=%+v", got, want)
					}
				})
			}
		}
	}
}

func TestResponsesConverseRetainsUncertainUsage(t *testing.T) {
	fc := &fakeConverser{resp: ConverseResponse{UsageUncertain: true, InputTokens: 7, OutputTokens: 3}}
	p := &provider{conv: fc}
	req := &providers.ProxyRequest{IngressProtocol: "responses", RawBody: []byte(`{"model":"m","input":"hi"}`)}
	out, err := p.Complete(context.Background(), req)
	if err != nil || out.Parsed.Usage == nil || !out.Parsed.Usage.AccountingUncertain {
		t.Fatalf("uncertainty lost: %v", err)
	}
	for _, evs := range [][]ConverseStreamEvent{
		{{Kind: eventMessageStop, StopReason: "end_turn"}},
		{{Kind: eventMessageStop, StopReason: "end_turn"}, {Kind: eventUsage, InputTokens: 7, OutputTokens: 3, UsageUncertain: true}},
	} {
		fc.streamEv = evs
		seq, err := p.Stream(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		var usage *schema.Usage
		for ev, err := range seq {
			if err != nil {
				t.Fatal(err)
			}
			if ev != nil && ev.Chunk != nil {
				usage = schema.MergeUsage(usage, ev.Chunk.Usage)
			}
		}
		if usage == nil || !usage.AccountingUncertain {
			t.Fatal("stream invented known usage")
		}
	}
}

func TestResponsesUpstreamErrorStatus(t *testing.T) {
	for _, api := range []string{"converse", "invoke_model"} {
		for _, stream := range []bool{false, true} {
			b := newResponsesBackend(t, api)
			b.inv.err = &brtypes.ThrottlingException{Message: aws.String("secret upstream detail")}
			b.conv.err = b.inv.err
			req := responsesRequest(t, b, `{"model":"public","input":"hi"}`)
			_, err := b.invoke(req, stream)
			var upstream *providers.UpstreamError
			if !errors.As(err, &upstream) || upstream.StatusCode != 429 || strings.Contains(string(upstream.Body), "secret") {
				t.Fatalf("lost upstream status or disclosed message: %v", err)
			}
		}
	}
	for _, stream := range []bool{false, true} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "11")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"message":"busy"}}`)
		}))
		p := &provider{man: staticMantle(t, srv), modelAPI: map[string]string{"openai.bridge": "mantle"}}
		req := &providers.ProxyRequest{Upstream: "openai.bridge", IngressProtocol: "responses", RawBody: []byte(`{"model":"m","input":"hi"}`)}
		if stream {
			_, err := p.Stream(context.Background(), req)
			var upstream *providers.UpstreamError
			if !errors.As(err, &upstream) || upstream.StatusCode != 429 || upstream.Header.Get("Retry-After") != "11" {
				t.Fatalf("lost stream error contract: %v", err)
			}
		} else {
			out, err := p.Complete(context.Background(), req)
			if err != nil || out.StatusCode != 429 || out.Headers.Get("Retry-After") != "11" {
				t.Fatalf("lost complete error contract: %v", err)
			}
		}
		srv.Close()
	}
}

type contextInvoker struct {
	*fakeInvoker
	want context.Context
	t    *testing.T
}

func (f *contextInvoker) Invoke(ctx context.Context, id string, body []byte, g Guardrail) ([]byte, error) {
	if ctx != f.want {
		f.t.Error("changed request context")
	}
	return f.fakeInvoker.Invoke(ctx, id, body, g)
}
func (f *contextInvoker) InvokeStream(ctx context.Context, id string, body []byte, g Guardrail) (iter.Seq2[[]byte, error], error) {
	if ctx != f.want {
		f.t.Error("changed stream context")
	}
	return f.fakeInvoker.InvokeStream(ctx, id, body, g)
}

func TestResponsesStreamConsumerStopsAndContext(t *testing.T) {
	for _, api := range responsesAPIs {
		t.Run(api, func(t *testing.T) {
			b := newResponsesBackend(t, api)
			req := responsesRequest(t, b, `{"model":"public","input":"hi","tools":[{"type":"function","name":"known","strict":false,"parameters":{}}]}`)
			b.output.Content = []schema.ContentBlock{{Type: "tool_use", ID: "tool", Name: "known", Input: json.RawMessage(`{"a":1}`)}}
			if _, err := b.invoke(req, false); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if api == "invoke_model" {
				b.p.inv = &contextInvoker{fakeInvoker: b.inv, want: ctx, t: t}
			}
			for stopAfter := 1; stopAfter <= 6; stopAfter++ {
				seq, err := b.p.Stream(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				count := 0
				for _, err := range seq {
					if err != nil {
						t.Fatal(err)
					}
					count++
					if count == stopAfter {
						break
					}
				}
				if count != stopAfter {
					t.Fatalf("stream stopped at %d, want %d", count, stopAfter)
				}
			}
		})
	}
}

func TestResponsesMantleCancellation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			started, stopped := make(chan struct{}), make(chan struct{})
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(started)
				select {
				case <-r.Context().Done():
					close(stopped)
				case <-release:
				}
			}))
			defer srv.Close()
			defer close(release)
			p := &provider{man: staticMantle(t, srv), modelAPI: map[string]string{"openai.bridge": "mantle"}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				req := &providers.ProxyRequest{Upstream: "openai.bridge", IngressProtocol: "responses", RawBody: []byte(`{"model":"m","input":"hi"}`)}
				if stream {
					_, err := p.Stream(ctx, req)
					done <- err
				} else {
					_, err := p.Complete(ctx, req)
					done <- err
				}
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("request not started")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("provider did not cancel")
			}
			select {
			case <-stopped:
			case <-time.After(3 * time.Second):
				t.Fatal("upstream did not cancel")
			}
		})
	}
}

func TestResponsesBridgeKeepsLegacyInvokeRaw(t *testing.T) {
	raw := []byte(" { \"type\":\"message\", \"content\":[{\"type\":\"tool_use\",\"id\":\"one\",\"name\":\"unknown-native\",\"input\":{}}], \"usage\":{\"input_tokens\":7,\"output_tokens\":3} } ")
	for _, ingress := range []string{"anthropic", "openai", "bedrock", ""} {
		fi := &fakeInvoker{respBody: raw}
		p := &provider{inv: fi}
		out, err := p.Complete(context.Background(), &providers.ProxyRequest{Upstream: "anthropic.claude", IngressProtocol: ingress, RawBody: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)})
		if err != nil || !bytes.Equal(out.RawBody, raw) {
			t.Fatalf("native %s response rewritten: %v", ingress, err)
		}
		if out.Parsed.Content[0].Name != "unknown-native" {
			t.Fatal("native tool name changed")
		}
	}
}

func TestResponsesMantleParallelInterleavedStream(t *testing.T) {
	var aliases []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Error(err)
			return
		}
		if len(wire.Tools) != 5 {
			t.Errorf("dropped parallel tools: %+v", wire.Tools)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		var starts, fragments []any
		for i, tool := range wire.Tools {
			aliases = append(aliases, tool.Function.Name)
			starts = append(starts, map[string]any{"index": i, "id": fmt.Sprintf("call_%d", i), "type": "function",
				"function": map[string]any{"name": tool.Function.Name, "arguments": "{"}})
			args := `"value":2}`
			if i == 2 || i == 4 {
				args = `"input":"echo interleaved"}`
			}
			fragments = append(fragments, map[string]any{"index": i, "function": map[string]any{"arguments": args}})
		}
		emit := func(calls []any) {
			fmt.Fprintf(w, "data: %s\n\n", bridgeJSON(map[string]any{"choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{"tool_calls": calls},
			}}}))
		}
		emit(starts)
		slices.Reverse(fragments)
		emit(fragments)
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":3,\"prompt_tokens_details\":{\"cached_tokens\":2}}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	p := &provider{man: staticMantle(t, srv), modelAPI: map[string]string{"openai.bridge": "mantle"}}
	req := responsesRequest(t, &responsesBackend{upstream: "openai.bridge"}, string(bridgeJSON(bridgeToolFixture())))
	state := responses.NewStreamState("public", req.Parsed)
	seq, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var completed json.RawMessage
	var raw bytes.Buffer
	for event, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		raw.Write(event.Raw)
		events, err := state.Convert(event.Chunk)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			if e.Type == "response.completed" {
				completed = e.Data
			}
		}
	}
	if _, err := state.Finish(); err != nil {
		t.Fatal(err)
	}
	var done struct {
		Response struct {
			Output []struct{ Type, Name, Namespace, Input, Arguments string } `json:"output"`
			Usage  struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal(completed, &done); err != nil {
		t.Fatal(err)
	}
	if len(done.Response.Output) != 5 {
		t.Fatalf("lost parallel outputs: %s", completed)
	}
	if done.Response.Usage.InputTokens != 9 || done.Response.Usage.OutputTokens != 3 {
		t.Fatalf("usage lost: %s", completed)
	}
	for i, item := range done.Response.Output {
		if i == 2 || i == 4 {
			if item.Type != "custom_tool_call" || item.Input != "echo interleaved" {
				t.Fatalf("custom input crossed streams: %+v", item)
			}
		} else if item.Type != "function_call" || item.Arguments != `{"value":2}` {
			t.Fatalf("function arguments crossed streams: %+v", item)
		}
		if i >= 3 && item.Namespace != "aws-sdk" {
			t.Fatalf("namespace lost: %+v", item)
		}
	}
	for _, alias := range aliases {
		if strings.Contains(raw.String(), alias) {
			t.Fatalf("backend alias leaked in translated Raw: %s", alias)
		}
	}
}
