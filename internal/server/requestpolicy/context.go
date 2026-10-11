package requestpolicy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
)

// ContextTextEstimate is only a local context preflight heuristic. Inline
// image base64 is a transport representation, not text tokens: exclude it at
// recognized protocol content positions. Image tokenization and the combined
// context limit remain the upstream's responsibility. This does NOT establish
// that an image fits, inspect it for privacy, or estimate its financial usage.
// Never use this value for governance, accounting, or automatic target selection.
// Unrecognized/invalid content retains the existing raw-byte estimate.
func ContextTextEstimate(protocol string, raw []byte) int64 {
	full := int64(len(raw))
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return max(int64(1), full/4)
	}
	key := "messages"
	switch protocol {
	case "anthropic", "bedrock", "openai":
	case "responses":
		key = "input"
	default:
		return max(int64(1), full/4)
	}
	var messages []map[string]json.RawMessage
	if json.Unmarshal(root[key], &messages) != nil {
		return max(int64(1), full/4)
	}
	var excluded int64
	for _, message := range messages {
		role := contextString(message["role"])
		if role != "user" && role != "assistant" {
			continue
		}
		if protocol == "responses" {
			kind := contextString(message["type"])
			if kind != "" && kind != "message" {
				continue
			}
		}
		var blocks []map[string]json.RawMessage
		if json.Unmarshal(message["content"], &blocks) != nil {
			continue // string content is text, even when it contains JSON
		}
		for _, block := range blocks {
			excluded += contextImageBytes(protocol, block)
			if (protocol == "anthropic" || protocol == "bedrock") && contextString(block["type"]) == "tool_result" {
				var results []map[string]json.RawMessage
				if json.Unmarshal(block["content"], &results) == nil {
					for _, result := range results {
						excluded += contextImageBytes(protocol, result)
					}
				}
			}
		}
	}
	if excluded < 0 || excluded > full {
		return max(int64(1), full/4)
	}
	return max(int64(1), (full-excluded)/4)
}

func contextString(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func contextImageBytes(protocol string, block map[string]json.RawMessage) int64 {
	kind := contextString(block["type"])
	switch {
	case (protocol == "anthropic" || protocol == "bedrock") && kind == "image":
		var source map[string]json.RawMessage
		if json.Unmarshal(block["source"], &source) != nil || contextString(source["type"]) != "base64" || !contextImageType(contextString(source["media_type"])) {
			return 0
		}
		data := bytes.TrimSpace(source["data"])
		if !contextBase64(contextString(data)) {
			return 0
		}
		return int64(len(data) - 2) // exact serialized payload, excluding quotes
	case protocol == "openai" && kind == "image_url":
		var image map[string]json.RawMessage
		if json.Unmarshal(block["image_url"], &image) != nil {
			return 0
		}
		return contextDataURLBytes(image["url"])
	case protocol == "responses" && kind == "input_image":
		return contextDataURLBytes(block["image_url"])
	}
	return 0
}

func contextImageType(media string) bool {
	switch media {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

func contextBase64(data string) bool {
	if data == "" {
		return false
	}
	// Validate without retaining decoded image bytes or interpreting pixels.
	_, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(data)))
	return err == nil
}

func contextDataURLBytes(raw json.RawMessage) int64 {
	url := contextString(raw)
	header, data, ok := strings.Cut(url, ",")
	if !ok || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") ||
		!contextImageType(strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")) || !contextBase64(data) {
		return 0
	}
	encoded := bytes.TrimSpace(raw)
	comma := bytes.IndexByte(encoded, ',')
	if comma < 0 {
		return 0 // escaped delimiter: retain the conservative raw estimate
	}
	return int64(len(encoded) - comma - 2) // keep URL header and closing quote
}
