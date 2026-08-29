package openai

import (
	"encoding/json"
	"testing"

	"github.com/inferplane/inferplane/pkg/schema"
)

func TestRequestToCanonicalBasics(t *testing.T) {
	in := []byte(`{"model":"gpt-x","max_tokens":256,"temperature":0.7,"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}]}`)
	cr, err := RequestToCanonical(in)
	if err != nil {
		t.Fatal(err)
	}
	if cr.Model != "gpt-x" || cr.MaxTokens == nil || *cr.MaxTokens != 256 {
		t.Fatalf("req: %+v", cr)
	}
	// system extracted to top-level system; user message present
	if len(cr.System) == 0 {
		t.Fatal("system not mapped")
	}
	if len(cr.Messages) != 1 || cr.Messages[0].Role != "user" {
		t.Fatalf("messages: %+v", cr.Messages)
	}
}

func TestResponseFromCanonical(t *testing.T) {
	txt := "answer"
	stop := "end_turn"
	in, out := int64(10), int64(3)
	resp := &schema.ChatResponse{ID: "msg_1", Model: "m", Role: "assistant",
		Content: []schema.ContentBlock{{Type: "text", Text: &txt}}, StopReason: &stop,
		Usage: &schema.Usage{InputTokens: &in, OutputTokens: &out}}
	oai := ResponseFromCanonical(resp)
	var m map[string]any
	json.Unmarshal(oai, &m)
	if m["object"] != "chat.completion" {
		t.Fatalf("object: %v", m["object"])
	}
	choices := m["choices"].([]any)
	c0 := choices[0].(map[string]any)
	if c0["finish_reason"] != "stop" {
		t.Fatalf("finish_reason: %v", c0["finish_reason"])
	}
	msg := c0["message"].(map[string]any)
	if msg["content"] != "answer" {
		t.Fatalf("content: %v", msg["content"])
	}
	usage := m["usage"].(map[string]any)
	if usage["prompt_tokens"].(float64) != 10 || usage["completion_tokens"].(float64) != 3 {
		t.Fatalf("usage: %v", usage)
	}
}

func TestToolCallRoundTrip(t *testing.T) {
	// OpenAI assistant tool_call → canonical tool_use → back to OpenAI
	in := []byte(`{"model":"m","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"cmd\":\"ls\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"}]}`)
	cr, err := RequestToCanonical(in)
	if err != nil {
		t.Fatal(err)
	}
	// assistant message → tool_use block; tool message → tool_result block
	foundToolUse, foundToolResult := false, false
	for _, msg := range cr.Messages {
		for _, b := range msg.Content {
			if b.Type == "tool_use" && b.Name == "bash" && b.ID == "call_1" {
				foundToolUse = true
			}
			if b.Type == "tool_result" && b.ToolUseID == "call_1" {
				foundToolResult = true
			}
		}
	}
	if !foundToolUse || !foundToolResult {
		t.Fatalf("tool mapping: use=%v result=%v\n%+v", foundToolUse, foundToolResult, cr.Messages)
	}
}

func TestChunkFromCanonicalTextDelta(t *testing.T) {
	idx := 0
	delta := []byte(`{"type":"text_delta","text":"hi"}`)
	c := &schema.ChatChunk{Type: "content_block_delta", Index: &idx, Delta: delta}
	st := &StreamState{}
	out := ChunkFromCanonical(c, st)
	if out == nil {
		t.Fatal("text_delta should produce an OpenAI chunk")
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	if m["object"] != "chat.completion.chunk" {
		t.Fatalf("object: %v", m["object"])
	}
	ch := m["choices"].([]any)[0].(map[string]any)
	d := ch["delta"].(map[string]any)
	if d["content"] != "hi" {
		t.Fatalf("delta content: %v", d)
	}
}

func TestChunkFromCanonicalMessageStopFinish(t *testing.T) {
	stop := "end_turn"
	delta := []byte(`{"stop_reason":"end_turn","stop_sequence":null}`)
	_ = stop
	c := &schema.ChatChunk{Type: "message_delta", Delta: delta}
	st := &StreamState{}
	out := ChunkFromCanonical(c, st)
	if out == nil {
		t.Fatal("message_delta should produce a finish chunk")
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	ch := m["choices"].([]any)[0].(map[string]any)
	if ch["finish_reason"] != "stop" {
		t.Fatalf("finish_reason: %v", ch["finish_reason"])
	}
}

// ADR-030: OpenAI's prompt_tokens INCLUDES cached_tokens, while canonical
// InputTokens and CacheReadInputTokens are disjoint (the pricing table bills
// them at different rates). Without subtracting, every cached prompt token was
// charged at the full input rate — roughly 10x the cache-read rate.
func TestUsageFromOAI(t *testing.T) {
	i := func(v int64) *int64 { return &v }

	t.Run("nil usage", func(t *testing.T) {
		if usageFromOAI(nil) != nil {
			t.Fatal("nil in, nil out")
		}
	})

	t.Run("no cache details leaves prompt tokens intact", func(t *testing.T) {
		u := usageFromOAI(&oaiUsage{PromptTokens: i(100), CompletionTokens: i(10)})
		if *u.InputTokens != 100 || *u.OutputTokens != 10 {
			t.Fatalf("got in=%d out=%d, want 100/10", *u.InputTokens, *u.OutputTokens)
		}
		if u.CacheReadInputTokens != nil {
			t.Errorf("cache_read must stay nil: %+v", u.CacheReadInputTokens)
		}
	})

	t.Run("cached tokens are subtracted out of input", func(t *testing.T) {
		u := usageFromOAI(&oaiUsage{
			PromptTokens:        i(1000),
			CompletionTokens:    i(10),
			PromptTokensDetails: &oaiPromptTokensDetails{CachedTokens: i(900)},
		})
		if *u.InputTokens != 100 {
			t.Errorf("input = %d, want 100 (1000 total - 900 cached); 1000 means cached tokens are billed at the full rate", *u.InputTokens)
		}
		if u.CacheReadInputTokens == nil || *u.CacheReadInputTokens != 900 {
			t.Errorf("cache_read = %+v, want 900", u.CacheReadInputTokens)
		}
	})

	t.Run("cached exceeding prompt clamps at zero", func(t *testing.T) {
		u := usageFromOAI(&oaiUsage{
			PromptTokens:        i(10),
			PromptTokensDetails: &oaiPromptTokensDetails{CachedTokens: i(50)},
		})
		if *u.InputTokens != 0 {
			t.Errorf("input = %d, want 0 (never negative)", *u.InputTokens)
		}
	})
}

// The client-facing shape must mirror OpenAI's: prompt_tokens inclusive, with
// the cached portion broken out underneath.
func TestUsageFromCanonical_reExposesCacheSplit(t *testing.T) {
	i := func(v int64) *int64 { return &v }
	got := usageFromCanonical(&schema.Usage{
		InputTokens:          i(100),
		OutputTokens:         i(10),
		CacheReadInputTokens: i(900),
	})
	if got["prompt_tokens"] != int64(1000) {
		t.Errorf("prompt_tokens = %v, want 1000 (inclusive of cached, as OpenAI reports it)", got["prompt_tokens"])
	}
	if got["total_tokens"] != int64(1010) {
		t.Errorf("total_tokens = %v, want 1010", got["total_tokens"])
	}
	details, ok := got["prompt_tokens_details"].(map[string]any)
	if !ok || details["cached_tokens"] != int64(900) {
		t.Errorf("prompt_tokens_details = %v, want cached_tokens 900", got["prompt_tokens_details"])
	}

	// Without cache reads the payload must stay byte-identical to before.
	plain := usageFromCanonical(&schema.Usage{InputTokens: i(100), OutputTokens: i(10)})
	if _, present := plain["prompt_tokens_details"]; present {
		t.Error("prompt_tokens_details must be omitted when there are no cache reads")
	}
}

// CanonicalToRequest must carry tool definitions, tool_choice, and sampling
// params to the OpenAI wire — without them a coding assistant routed through
// an OpenAI-schema upstream (openai_compatible, Bedrock Mantle) silently
// loses tool calling and sampling entirely.
func TestCanonicalToRequestCarriesToolsAndSampling(t *testing.T) {
	raw := []byte(`{"model":"m","max_tokens":64,
		"temperature":0.7,"top_p":0.9,"stop_sequences":["END","STOP"],
		"tools":[{"name":"get_time","description":"tells time","input_schema":{"type":"object","properties":{"tz":{"type":"string"}}}}],
		"tool_choice":{"type":"auto"},
		"messages":[{"role":"user","content":"hi"}]}`)
	var cr schema.ChatRequest
	if err := json.Unmarshal(raw, &cr); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(CanonicalToRequest(&cr), &out); err != nil {
		t.Fatal(err)
	}
	tools, ok := out["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools not carried: %v", out["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Fatalf("tool type = %v, want function", tool["type"])
	}
	fn := tool["function"].(map[string]any)
	if fn["name"] != "get_time" || fn["description"] != "tells time" {
		t.Fatalf("function fields: %v", fn)
	}
	params, ok := fn["parameters"].(map[string]any)
	if !ok || params["type"] != "object" {
		t.Fatalf("input_schema not mapped to parameters: %v", fn["parameters"])
	}
	if out["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %v, want auto", out["tool_choice"])
	}
	if out["temperature"] != 0.7 || out["top_p"] != 0.9 {
		t.Fatalf("sampling params not carried: temp=%v top_p=%v", out["temperature"], out["top_p"])
	}
	stop, ok := out["stop"].([]any)
	if !ok || len(stop) != 2 || stop[0] != "END" {
		t.Fatalf("stop_sequences not mapped to stop: %v", out["stop"])
	}
}

func TestCanonicalToRequestToolChoiceVariants(t *testing.T) {
	mk := func(tc string) map[string]any {
		raw := []byte(`{"model":"m","max_tokens":8,"tool_choice":` + tc + `,
			"tools":[{"name":"f","input_schema":{"type":"object"}}],
			"messages":[{"role":"user","content":"hi"}]}`)
		var cr schema.ChatRequest
		if err := json.Unmarshal(raw, &cr); err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(CanonicalToRequest(&cr), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := mk(`{"type":"any"}`)["tool_choice"]; got != "required" {
		t.Errorf("any → %v, want required", got)
	}
	if got := mk(`{"type":"none"}`)["tool_choice"]; got != "none" {
		t.Errorf("none → %v, want none", got)
	}
	specific := mk(`{"type":"tool","name":"f"}`)["tool_choice"]
	sm, ok := specific.(map[string]any)
	if !ok || sm["type"] != "function" || sm["function"].(map[string]any)["name"] != "f" {
		t.Errorf("tool → %v, want function/f", specific)
	}
}

// Cache WRITE tokens are part of what the prompt consumed too. Folding only
// cache reads left prompt_tokens under-reporting every cache-writing request.
func TestUsageFromCanonical_foldsCacheWrites(t *testing.T) {
	i := func(v int64) *int64 { return &v }
	got := usageFromCanonical(&schema.Usage{
		InputTokens:  i(100),
		OutputTokens: i(10),
		CacheCreation: &schema.CacheCreation{
			Ephemeral5mInputTokens: i(300),
			Ephemeral1hInputTokens: i(200),
		},
	})
	if got["prompt_tokens"] != int64(600) {
		t.Errorf("prompt_tokens = %v, want 600 (100 input + 300 5m + 200 1h)", got["prompt_tokens"])
	}
	if got["total_tokens"] != int64(610) {
		t.Errorf("total_tokens = %v, want 610", got["total_tokens"])
	}
	if _, present := got["prompt_tokens_details"]; present {
		t.Error("cache writes are not cached_tokens — details must stay omitted")
	}

	// The split wins over the flat total; summing both would double-count.
	both := usageFromCanonical(&schema.Usage{
		InputTokens:              i(100),
		CacheCreationInputTokens: i(500),
		CacheCreation:            &schema.CacheCreation{Ephemeral5mInputTokens: i(500)},
	})
	if both["prompt_tokens"] != int64(600) {
		t.Errorf("prompt_tokens = %v, want 600 (no double-count)", both["prompt_tokens"])
	}
}

// A tool-call chunk must survive the OpenAI→canonical direction: before this,
// delta.tool_calls parsed to nothing and ChunkToCanonical returned (nil, nil),
// so a Bedrock-ingress client streaming through an OpenAI-wire provider saw
// text only and every tool call vanished mid-stream.
func TestChunkToCanonicalToolCalls(t *testing.T) {
	opens, err := ChunkToCanonical([]byte(`{"choices":[{"index":0,"delta":{"content":null,"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(opens) != 1 {
		t.Fatalf("tool_calls opener dropped: %+v", opens)
	}
	open := opens[0]
	if open.Type != "content_block_start" || open.ContentBlock == nil {
		t.Fatalf("opener: %+v", open)
	}
	if open.ContentBlock.Type != "tool_use" || open.ContentBlock.ID != "call_1" || open.ContentBlock.Name != "get_weather" {
		t.Errorf("content_block: %+v", open.ContentBlock)
	}
	if string(open.ContentBlock.Input) != `{}` {
		t.Errorf("input = %s, want {} for an empty arguments opener", open.ContentBlock.Input)
	}
	if open.Index == nil || *open.Index != 0 {
		t.Errorf("index: %v", open.Index)
	}

	frags, err := ChunkToCanonical([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"city\":"}}]},"finish_reason":null}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 1 || frags[0].Type != "content_block_delta" {
		t.Fatalf("argument fragment: %+v", frags)
	}
	frag := frags[0]
	if frag.Index == nil || *frag.Index != 1 {
		t.Errorf("fragment index: %v", frag.Index)
	}
	var d struct {
		Type        string `json:"type"`
		PartialJSON string `json:"partial_json"`
	}
	if err := json.Unmarshal(frag.Delta, &d); err != nil {
		t.Fatal(err)
	}
	if d.Type != "input_json_delta" || d.PartialJSON != `{"city":` {
		t.Errorf("delta = %+v", d)
	}
}

// Parallel tool calls arrive as several entries in ONE chunk. Converting only
// the first dropped every additional call, and usage repeated across the
// fan-out would be folded more than once by the ingress.
func TestChunkToCanonicalParallelToolCalls(t *testing.T) {
	chunks, err := ChunkToCanonical([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[` +
		`{"index":0,"id":"call_a","type":"function","function":{"name":"a","arguments":""}},` +
		`{"index":1,"id":"call_b","type":"function","function":{"name":"b","arguments":""}}` +
		`]},"finish_reason":null}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("want 2 canonical frames, got %d: %+v", len(chunks), chunks)
	}
	for i, want := range []struct {
		id, name string
		index    int
	}{{"call_a", "a", 0}, {"call_b", "b", 1}} {
		c := chunks[i]
		if c.Type != "content_block_start" || c.ContentBlock == nil {
			t.Fatalf("frame %d: %+v", i, c)
		}
		if c.ContentBlock.ID != want.id || c.ContentBlock.Name != want.name {
			t.Errorf("frame %d block = %+v", i, c.ContentBlock)
		}
		if c.Index == nil || *c.Index != want.index {
			t.Errorf("frame %d index = %v, want %d", i, c.Index, want.index)
		}
	}
	if chunks[0].Usage == nil {
		t.Error("usage must ride the first frame")
	}
	if chunks[1].Usage != nil {
		t.Error("usage repeated on a later frame — the ingress would double-count it")
	}
}
