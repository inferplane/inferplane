package schema

import "encoding/json"

// ChatRequest — canonical request. Only pipeline-interpreted fields are
// typed: Model (routing/pricing), Messages (block order + cache invariant),
// Stream, MaxTokens (TPM estimate). system/tools/tool_choice/thinking/metadata
// are kept as raw JSON and promoted to typed shapes only during cross-protocol
// conversion.
type ChatRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	MaxTokens *int64    `json:"max_tokens,omitempty"`
	// *bool: preserves an explicit "stream":false (same omitempty bug class as 48d412d)
	Stream *bool `json:"stream,omitempty"`

	System     json.RawMessage `json:"system,omitempty"`
	Tools      json.RawMessage `json:"tools,omitempty"`
	ToolChoice json.RawMessage `json:"tool_choice,omitempty"`
	Thinking   json.RawMessage `json:"thinking,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`

	Extra map[string]json.RawMessage `json:"-"`
}

var chatRequestKnown = []string{
	"model", "messages", "max_tokens", "stream",
	"system", "tools", "tool_choice", "thinking", "metadata",
}

func (r *ChatRequest) UnmarshalJSON(data []byte) error {
	type plain ChatRequest
	extra, err := unmarshalWithExtra(data, (*plain)(r), chatRequestKnown...)
	r.Extra = extra
	return err
}

func (r ChatRequest) MarshalJSON() ([]byte, error) {
	type plain ChatRequest
	return marshalWithExtra(plain(r), r.Extra)
}
