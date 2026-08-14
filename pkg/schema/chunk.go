package schema

import "encoding/json"

// ChatChunk — canonical streaming event. Adopts Anthropic's event vocabulary
// verbatim (message_start/content_block_start/content_block_delta/
// content_block_stop/message_delta/message_stop/ping/error). `delta` is kept
// as raw JSON: the SSE serializer only re-emits it, and cross-protocol
// conversion promotes it to a typed shape. The message_delta carrying `usage`
// is the source of truth for settlement (drain-time settlement).
type ChatChunk struct {
	Type         string                     `json:"type"`
	Index        *int                       `json:"index,omitempty"`
	Message      *ChatResponse              `json:"message,omitempty"`
	ContentBlock *ContentBlock              `json:"content_block,omitempty"`
	Delta        json.RawMessage            `json:"delta,omitempty"`
	Usage        *Usage                     `json:"usage,omitempty"`
	Extra        map[string]json.RawMessage `json:"-"`
}

var chatChunkKnown = []string{
	"type", "index", "message", "content_block", "delta", "usage",
}

func (c *ChatChunk) UnmarshalJSON(data []byte) error {
	type plain ChatChunk
	extra, err := unmarshalWithExtra(data, (*plain)(c), chatChunkKnown...)
	c.Extra = extra
	return err
}

func (c ChatChunk) MarshalJSON() ([]byte, error) {
	type plain ChatChunk
	return marshalWithExtra(plain(c), c.Extra)
}
