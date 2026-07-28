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
