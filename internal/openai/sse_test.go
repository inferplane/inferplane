package openai

import (
	"strings"
	"testing"
)

// One SSE line can carry several parallel tool calls, which ChunkToCanonical
// expands into one canonical frame each. ReadChatSSE must yield them all, with
// Raw on the FIRST event only — an OpenAI-wire ingress tees Raw, so repeating
// it would duplicate the line on the client's stream.
func TestReadChatSSEFansOutParallelToolCalls(t *testing.T) {
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[" +
		"{\"index\":0,\"id\":\"call_a\",\"function\":{\"name\":\"a\",\"arguments\":\"\"}}," +
		"{\"index\":1,\"id\":\"call_b\",\"function\":{\"name\":\"b\",\"arguments\":\"\"}}" +
		"]}}]}\n\n" +
		"data: [DONE]\n\n"
	var names []string
	var rawCount int
	for ev, err := range ReadChatSSE(strings.NewReader(body), "public-m") {
		if err != nil {
			t.Fatal(err)
		}
		if len(ev.Raw) > 0 {
			rawCount++
		}
		if ev.Chunk != nil && ev.Chunk.ContentBlock != nil {
			names = append(names, ev.Chunk.ContentBlock.Name)
		}
	}
	if strings.Join(names, ",") != "a,b" {
		t.Errorf("tool-call names = %v, want [a b]", names)
	}
	// One Raw for the tool-call line, one for [DONE].
	if rawCount != 2 {
		t.Errorf("Raw emitted %d times, want 2 (the line once + [DONE])", rawCount)
	}
}

// A line that yields no canonical frame (an unparseable or unmapped chunk) must
// still tee its Raw bytes, or an OpenAI-wire client loses part of the stream.
func TestReadChatSSEKeepsRawWhenNoChunk(t *testing.T) {
	body := "data: not-json\n\n"
	var n int
	for ev, err := range ReadChatSSE(strings.NewReader(body), "public-m") {
		if err != nil {
			t.Fatal(err)
		}
		n++
		if string(ev.Raw) != "data: not-json\n\n" || ev.Chunk != nil {
			t.Errorf("event = %+v", ev)
		}
	}
	if n != 1 {
		t.Errorf("yielded %d events, want 1", n)
	}
}

// Every tool block ChunkToCanonical opens must be CLOSED before the
// stop-bearing message_delta: the OpenAI wire has no close event, but the
// Anthropic frame vocabulary the canonical consumers re-render requires a
// content_block_stop per opened block — an Anthropic client finalizes the
// accumulated input_json_delta fragments there, so an unclosed block leaves
// the tool call permanently pending client-side.
func TestReadChatSSEClosesToolBlocksBeforeStop(t *testing.T) {
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_a\",\"function\":{\"name\":\"a\",\"arguments\":\"\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"x\\\":1}\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"
	var types []string
	var stopIdx *int
	for ev, err := range ReadChatSSE(strings.NewReader(body), "public-m") {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Chunk == nil {
			continue
		}
		types = append(types, ev.Chunk.Type)
		if ev.Chunk.Type == "message_start" {
			if ev.Raw != nil {
				t.Error("synthesized message_start must carry no Raw")
			}
			if ev.Chunk.Message == nil || ev.Chunk.Message.Model != "public-m" {
				t.Errorf("message_start must carry the PUBLIC model name, got %+v", ev.Chunk.Message)
			}
		}
		if ev.Chunk.Type == "content_block_stop" {
			if ev.Raw != nil {
				t.Error("synthesized content_block_stop must carry no Raw (the OpenAI wire has no such frame to tee)")
			}
			stopIdx = ev.Chunk.Index
		}
	}
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("frame order = %v, want %v", types, want)
	}
	if stopIdx == nil || *stopIdx != 1 {
		t.Errorf("content_block_stop index = %v, want 1 (tool indices shift +1 past the text block)", stopIdx)
	}
}

// A usage-only message_delta (stream_options.include_usage's final chunk)
// does not end the message and must not close open blocks; a stream that
// ends at [DONE] without any finish_reason still must not leave them open.
func TestReadChatSSEClosesToolBlocksAtDoneWithoutFinish(t *testing.T) {
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_a\",\"function\":{\"name\":\"a\",\"arguments\":\"{}\"}}]}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":5}}\n\n" +
		"data: [DONE]\n\n"
	var types []string
	for ev, err := range ReadChatSSE(strings.NewReader(body), "public-m") {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Chunk == nil {
			continue
		}
		types = append(types, ev.Chunk.Type)
	}
	// The usage-only message_delta rides through open blocks untouched; the
	// close lands before [DONE].
	want := []string{"message_start", "content_block_start", "content_block_delta", "message_delta", "content_block_stop", "message_stop"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("frame order = %v, want %v", types, want)
	}
}
