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
	for ev, err := range ReadChatSSE(strings.NewReader(body)) {
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
	for ev, err := range ReadChatSSE(strings.NewReader(body)) {
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
