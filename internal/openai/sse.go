package openai

import (
	"bufio"
	"encoding/json"
	"io"
	"iter"
	"maps"
	"slices"
	"strings"

	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
)

// ReadChatSSE parses an OpenAI Chat Completions SSE stream (sequences of
// `data: {...}` lines terminated by `data: [DONE]`). Each event's Raw is the
// provider-native OpenAI SSE bytes ("data: {...}\n\n") so an OpenAI ingress can
// tee them verbatim; Chunk is the canonical (Anthropic) view parsed via
// ChunkToCanonical for observation and cross-protocol re-serialization
// (a cross-protocol ingress IGNORES Raw and re-renders from Chunk). The [DONE]
// terminator yields Raw="data: [DONE]\n\n" with Chunk=nil. Shared by the
// openaicompat provider (vLLM/Ollama) and the Bedrock Mantle chat-completions
// routes.
func ReadChatSSE(r io.Reader) iter.Seq2[*providers.StreamEvent, error] {
	return func(yield func(*providers.StreamEvent, error) bool) {
		br := bufio.NewReader(r)
		// Tool blocks ChunkToCanonical opened (content_block_start) that no
		// frame has closed yet. The OpenAI wire has no close event — a tool
		// call just stops producing argument fragments — but the Anthropic
		// frame vocabulary the canonical consumers re-render requires a
		// content_block_stop per opened block before the message-level stop
		// (an Anthropic client finalizes the accumulated partial_json there).
		// ChunkToCanonical is stateless per chunk, so the closes are
		// synthesized here, where the whole stream passes through.
		openTools := map[int]bool{}
		closeOpenTools := func() bool {
			for _, idx := range slices.Sorted(maps.Keys(openTools)) {
				i := idx
				// Raw stays nil: an OpenAI-wire ingress tees Raw verbatim and
				// its wire has no such frame; only canonical consumers see it.
				if !yield(&providers.StreamEvent{Chunk: &schema.ChatChunk{Type: "content_block_stop", Index: &i}}, nil) {
					return false
				}
			}
			clear(openTools)
			return true
		}
		for {
			line, err := br.ReadString('\n')
			trimmed := strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(trimmed, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
				if payload == "[DONE]" {
					// A stream that ended without a finish_reason still must
					// not leave blocks open.
					if !closeOpenTools() {
						return
					}
					if !yield(&providers.StreamEvent{Raw: []byte("data: [DONE]\n\n")}, nil) {
						return
					}
				} else if payload != "" {
					raw := []byte("data: " + payload + "\n\n")
					chunks, cerr := ChunkToCanonical([]byte(payload))
					if cerr != nil || len(chunks) == 0 {
						if !yield(&providers.StreamEvent{Raw: raw}, nil) {
							return
						}
					} else {
						// One SSE line can carry several canonical frames
						// (parallel tool calls). Raw rides the FIRST event
						// only: an OpenAI-wire ingress tees Raw, so repeating
						// it would duplicate the line on the client's stream.
						// The else is structural — it must stay impossible to
						// emit raw AND frames for one line.
						for i, c := range chunks {
							if c.Type == "content_block_start" && c.Index != nil {
								openTools[*c.Index] = true
							}
							// Close every open tool block BEFORE the
							// stop-bearing message_delta (finish_reason);
							// usage-only message_delta frames (Delta == "{}")
							// don't end the message and close nothing.
							if c.Type == "message_delta" && stopBearing(c.Delta) && !closeOpenTools() {
								return
							}
							ev := &providers.StreamEvent{Chunk: c}
							if i == 0 {
								ev.Raw = raw
							}
							if !yield(ev, nil) {
								return
							}
						}
					}
				}
			}
			if err == io.EOF {
				return
			}
			if err != nil {
				yield(nil, err)
				return
			}
		}
	}
}

// stopBearing reports whether a message_delta's delta payload carries a
// stop_reason — i.e. it is the finish frame, not an include_usage-only frame.
func stopBearing(delta json.RawMessage) bool {
	var d struct {
		StopReason *string `json:"stop_reason"`
	}
	return json.Unmarshal(delta, &d) == nil && d.StopReason != nil
}
