// Package mockprovider is a deterministic in-memory Provider for tests.
// It needs no network and emits a fixed Anthropic-shaped message + stream.
package mockprovider

import (
	"context"
	"encoding/json"
	"iter"

	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
)

type mock struct{ model string }

// New returns a mock provider serving exactly one model id.
func New(model string) providers.Provider { return &mock{model: model} }

func (m *mock) Name() string { return "mock" }

func (m *mock) Models() []schema.ModelInfo {
	return []schema.ModelInfo{{Type: "model", ID: m.model, DisplayName: m.model}}
}

func ptrStr(s string) *string { return &s }

func (m *mock) Complete(_ context.Context, _ *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	in, out := int64(10), int64(5)
	resp := &schema.ChatResponse{
		ID: "msg_mock", Type: "message", Role: "assistant", Model: m.model,
		Content:    []schema.ContentBlock{{Type: "text", Text: ptrStr("ok")}},
		StopReason: ptrStr("end_turn"),
		Usage:      &schema.Usage{InputTokens: &in, OutputTokens: &out},
	}
	raw, _ := json.Marshal(resp)
	return &providers.ProxyResponse{StatusCode: 200, RawBody: raw, Parsed: resp}, nil
}

// Stream splits the usage counts across frames the way the real Anthropic wire
// does — and this faithfulness is load-bearing, not cosmetic. The mock used to
// put input AND output on message_delta's top-level usage, which no real
// upstream does; that is why the settlement bug ADR-030 fixes (reading only the
// last frame's top-level usage, so streaming requests billed output only) was
// invisible to every streaming test in the repo.
//
// Real shape, preserved here:
//   - message_start carries input + prompt-cache counts, NESTED under
//     message.usage, and an output_tokens placeholder.
//   - message_delta carries the final output_tokens at the TOP level, and
//     nothing else.
//
// A settlement path that folds both frames bills 10 input + 5 output + the
// cache counts; one that overwrites bills 5 output and nothing else.
func (m *mock) Stream(_ context.Context, _ *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	in, outStart, out := int64(10), int64(1), int64(5)
	cacheRead, cacheWrite5m, cacheWrite1h := int64(40), int64(20), int64(4)
	events := []*providers.StreamEvent{
		{Raw: []byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n"),
			Chunk: &schema.ChatChunk{Type: "message_start", Message: &schema.ChatResponse{
				ID: "msg_mock", Type: "message", Role: "assistant", Model: m.model,
				Content: []schema.ContentBlock{},
				Usage: &schema.Usage{
					InputTokens:          &in,
					OutputTokens:         &outStart,
					CacheReadInputTokens: &cacheRead,
					CacheCreation: &schema.CacheCreation{
						Ephemeral5mInputTokens: &cacheWrite5m,
						Ephemeral1hInputTokens: &cacheWrite1h,
					},
				},
			}}},
		{Raw: []byte("event: message_delta\ndata: {\"type\":\"message_delta\"}\n\n"),
			Chunk: &schema.ChatChunk{Type: "message_delta", Usage: &schema.Usage{OutputTokens: &out}}},
		{Raw: []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
			Chunk: &schema.ChatChunk{Type: "message_stop"}},
	}
	return func(yield func(*providers.StreamEvent, error) bool) {
		for _, ev := range events {
			if !yield(ev, nil) {
				return
			}
		}
	}, nil
}
