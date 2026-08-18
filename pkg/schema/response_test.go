package schema

import (
	"encoding/json"
	"testing"
)

func TestChatResponseRoundTrip(t *testing.T) {
	in := `{
	  "id": "msg_01ABC",
	  "type": "message",
	  "role": "assistant",
	  "model": "claude-sonnet-4-6",
	  "content": [
	    {"type":"thinking","thinking":"reasoning...","signature":"EuYB"},
	    {"type":"text","text":"done"}
	  ],
	  "stop_reason": "end_turn",
	  "stop_sequence": null,
	  "usage": {
	    "input_tokens": 1200,
	    "output_tokens": 850,
	    "cache_read_input_tokens": 45000,
	    "cache_creation_input_tokens": 1024,
	    "cache_creation": {"ephemeral_5m_input_tokens":1024,"ephemeral_1h_input_tokens":0}
	  }
	}`
	var r ChatResponse
	if err := json.Unmarshal([]byte(in), &r); err != nil {
		t.Fatal(err)
	}
	if r.Usage == nil || r.Usage.CacheReadInputTokens == nil || *r.Usage.CacheReadInputTokens != 45000 {
		t.Fatalf("usage not typed: %+v", r.Usage)
	}
	if r.Usage.CacheCreation == nil || r.Usage.CacheCreation.Ephemeral5mInputTokens == nil || *r.Usage.CacheCreation.Ephemeral5mInputTokens != 1024 {
		t.Fatalf("cache_creation TTL detail not typed: %+v", r.Usage.CacheCreation)
	}
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONSemanticEqual(t, []byte(in), out)
}

func TestUsagePinsPartialAndExplicitZero(t *testing.T) {
	// A message_delta sometimes carries only output_tokens (must not add a
	// key), and an explicit cache_creation_input_tokens:0 must be preserved.
	for _, in := range []string{
		`{"output_tokens":850}`,
		`{"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":0}`,
	} {
		var u Usage
		if err := json.Unmarshal([]byte(in), &u); err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(u)
		if err != nil {
			t.Fatal(err)
		}
		assertJSONSemanticEqual(t, []byte(in), out)
	}
}

func TestChatResponseSkeletonConstruction(t *testing.T) {
	// The contract for building a message_start skeleton in code: nil
	// StopReason/StopSequence marshal as explicit null, and empty Content
	// marshals as [].
	in := int64(4805)
	out := int64(2)
	r := ChatResponse{
		ID: "msg_01S", Type: "message", Role: "assistant",
		Model:   "claude-sonnet-4-6",
		Content: []ContentBlock{},
		Usage:   &Usage{InputTokens: &in, OutputTokens: &out},
	}
	got, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"msg_01S","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":4805,"output_tokens":2}}`
	assertJSONSemanticEqual(t, []byte(want), got)
}

func TestUsageExtraRoundTrip(t *testing.T) {
	// An unknown billing field must never be dropped from a settlement input.
	for _, in := range []string{
		`{"input_tokens":1,"output_tokens":2,"server_tool_use":{"web_search_requests":3}}`,
		`{"output_tokens":2,"cache_creation":{"ephemeral_5m_input_tokens":1,"ephemeral_30m_input_tokens":7}}`,
	} {
		var u Usage
		if err := json.Unmarshal([]byte(in), &u); err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(u)
		if err != nil {
			t.Fatal(err)
		}
		assertJSONSemanticEqual(t, []byte(in), got)
	}
}
