package providers

import (
	"encoding/json"
	"testing"
)

func TestContextOverflowCounts(t *testing.T) {
	for _, tc := range []struct {
		name, message   string
		tokens, maximum int64
		ok              bool
	}{
		{"anthropic", "prompt is too long: 250001 tokens > 200000 maximum", 250001, 200000, true},
		{"anthropic capitalized", "Prompt is too long: 250001 tokens > 200000 maximum", 250001, 200000, true},
		{"converse input length", "Input length (135043) exceeds model's maximum context length (131072).", 135043, 131072, true},
		{"vllm messages", "This model's maximum context length is 4096 tokens. However, you requested 5000 tokens in the messages, Please reduce the length of the messages.", 5000, 4096, true},
		{"vllm completion", "This model's maximum context length is 4096 tokens. However, you requested 5500 tokens (5000 in the messages, 500 in the completion). Please reduce the length of the messages or completion.", 5500, 4096, true},
		{"suffix", "Input length (135043) exceeds model's maximum context length (131072). arn:aws:private", 0, 0, false},
		{"not over", "Input length (131072) exceeds model's maximum context length (131072).", 0, 0, false},
		{"zero limit", "prompt is too long: 1 tokens > 0 maximum", 0, 0, false},
		{"overflowing number", "prompt is too long: 99999999999999999999999 tokens > 1 maximum", 0, 0, false},
		{"inconsistent split", "This model's maximum context length is 4096 tokens. However, you requested 5500 tokens (4000 in the messages, 500 in the completion). Please reduce the length of the messages or completion.", 0, 0, false},
		{"unknown wording", "context window exceeded", 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokens, maximum, ok := ContextOverflowCounts(tc.message)
			if tokens != tc.tokens || maximum != tc.maximum || ok != tc.ok {
				t.Fatalf("got (%d, %d, %t), want (%d, %d, %t)", tokens, maximum, ok, tc.tokens, tc.maximum, tc.ok)
			}
		})
	}
}

func TestPromptTooLongBody(t *testing.T) {
	for _, tc := range []struct {
		tokens, maximum int64
		message         string
	}{
		{135043, 131072, "prompt is too long: 135043 tokens > 131072 maximum"},
		{0, 0, "prompt is too long"},
	} {
		var got struct {
			Type  string `json:"type"`
			Error struct{ Type, Code, Message string }
		}
		body := PromptTooLongBody(tc.tokens, tc.maximum)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.Type != "error" || got.Error.Type != "invalid_request_error" || got.Error.Code != "context_length_exceeded" ||
			got.Error.Message != tc.message || ResponseErrorCode(body) != "context_length_exceeded" {
			t.Fatalf("unrecoverable overflow body: %s", body)
		}
	}
}
