package responses

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/inferplane/inferplane/pkg/schema"
)

func TestBridgeThinkingDoesNotAbort(t *testing.T) {
	for _, kind := range []string{"thinking", "redacted_thinking"} {
		t.Run(kind, func(t *testing.T) {
			s := NewStreamState("fable")
			zero, one := 0, 1
			in, out := int64(10), int64(25)
			text := "OK"
			secret := "do not expose"
			chunks := []*schema.ChatChunk{
				{Type: "message_start", Message: &schema.ChatResponse{Usage: &schema.Usage{InputTokens: &in}}},
				{Type: "content_block_start", Index: &zero, ContentBlock: &schema.ContentBlock{Type: kind, Thinking: &secret, Data: &secret}},
				{Type: "content_block_delta", Index: &zero, Delta: json.RawMessage(`{"type":"thinking_delta","thinking":"do not expose"}`)},
				{Type: "content_block_delta", Index: &zero, Delta: json.RawMessage(`{"type":"signature_delta","signature":"do not expose"}`)},
				{Type: "content_block_stop", Index: &zero},
				{Type: "content_block_start", Index: &one, ContentBlock: &schema.ContentBlock{Type: "text", Text: &text}},
				{Type: "content_block_stop", Index: &one},
				{Type: "message_delta", Usage: &schema.Usage{OutputTokens: &out}, Delta: json.RawMessage(`{"stop_reason":"end_turn"}`)},
				{Type: "message_stop"},
			}
			var wire bytes.Buffer
			for _, c := range chunks {
				events, err := s.Convert(c)
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range events {
					if err := WriteEvent(&wire, e); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := s.Finish(); err != nil {
				t.Fatal(err)
			}
			for _, wanted := range []string{`response.completed`, `"output_index":0`, `"output_tokens":25`, `OK`} {
				if !bytes.Contains(wire.Bytes(), []byte(wanted)) {
					t.Fatalf("missing %s: %s", wanted, &wire)
				}
			}
			if bytes.Contains(wire.Bytes(), []byte(secret)) {
				t.Fatal("thinking leaked")
			}
			resp := &schema.ChatResponse{Model: "fable", Content: []schema.ContentBlock{{Type: kind, Thinking: &secret, Data: &secret}, {Type: "text", Text: &text}}, Usage: &schema.Usage{InputTokens: &in, OutputTokens: &out}, StopReason: ptr("end_turn")}
			raw, err := CanonicalToResponse(resp)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte(secret)) || !bytes.Contains(raw, []byte(`"output_tokens":25`)) {
				t.Fatalf("bad complete response %s", raw)
			}
		})
	}
}

func TestBridgeIgnoredThinkingIsStillValidated(t *testing.T) {
	idx := 0
	for _, bad := range []*schema.ChatChunk{
		{Type: "content_block_start", Index: &idx, ContentBlock: &schema.ContentBlock{Type: "text"}},
		{Type: "content_block_delta", Index: &idx, Delta: json.RawMessage(`{"type":"text_delta","text":"hidden"}`)},
	} {
		s := NewStreamState("fable")
		if _, err := s.Convert(&schema.ChatChunk{Type: "content_block_start", Index: &idx, ContentBlock: &schema.ContentBlock{Type: "thinking"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Convert(bad); err == nil {
			t.Fatal("invalid ignored block accepted")
		}
	}
}

func TestBridgeThinkingLifecycleAndUnknownBlocks(t *testing.T) {
	idx := 0
	for _, action := range []string{"unclosed", "duplicate_stop", "delta_after_stop", "unknown_block"} {
		t.Run(action, func(t *testing.T) {
			s := NewStreamState("fable")
			in, out := int64(1), int64(1)
			if _, err := s.Convert(&schema.ChatChunk{Type: "message_start", Message: &schema.ChatResponse{Usage: &schema.Usage{InputTokens: &in, OutputTokens: &out}}}); err != nil {
				t.Fatal(err)
			}
			if action == "unknown_block" {
				if _, err := s.Convert(&schema.ChatChunk{Type: "content_block_start", Index: &idx, ContentBlock: &schema.ContentBlock{Type: "future_unknown"}}); err == nil {
					t.Fatal("unknown block accepted")
				}
				return
			}
			if _, err := s.Convert(&schema.ChatChunk{Type: "content_block_start", Index: &idx, ContentBlock: &schema.ContentBlock{Type: "thinking"}}); err != nil {
				t.Fatal(err)
			}
			var bad *schema.ChatChunk
			if action == "unclosed" {
				bad = &schema.ChatChunk{Type: "message_stop"}
			} else {
				if _, err := s.Convert(&schema.ChatChunk{Type: "content_block_stop", Index: &idx}); err != nil {
					t.Fatal(err)
				}
				bad = &schema.ChatChunk{Type: "content_block_stop", Index: &idx}
				if action == "delta_after_stop" {
					bad = &schema.ChatChunk{Type: "content_block_delta", Index: &idx, Delta: json.RawMessage(`{"type":"signature_delta","signature":"opaque"}`)}
				}
			}
			if _, err := s.Convert(bad); err == nil {
				t.Fatal("invalid lifecycle accepted")
			}
		})
	}
}
