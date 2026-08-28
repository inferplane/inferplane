package openai

import (
	"bufio"
	"io"
	"iter"
	"strings"

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
		for {
			line, err := br.ReadString('\n')
			trimmed := strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(trimmed, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
				if payload == "[DONE]" {
					if !yield(&providers.StreamEvent{Raw: []byte("data: [DONE]\n\n")}, nil) {
						return
					}
				} else if payload != "" {
					ev := &providers.StreamEvent{Raw: []byte("data: " + payload + "\n\n")}
					if c, cerr := ChunkToCanonical([]byte(payload)); cerr == nil {
						ev.Chunk = c
					}
					if !yield(ev, nil) {
						return
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
