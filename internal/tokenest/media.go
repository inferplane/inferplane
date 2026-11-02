package tokenest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
)

// imagePayloads returns the quoted base64 strings of inline images at
// recognized content positions: user/assistant message blocks and their tool
// results. Opaque fields (tool inputs, schemas, signatures, quoted JSON text)
// and invalid or non-image payloads are never returned, so they keep their
// scanned cost. Unknown protocols and unparsable shapes return nothing.
func imagePayloads(protocol string, raw []byte) []json.RawMessage {
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return nil
	}
	key := "messages"
	switch protocol {
	case "anthropic", "bedrock", "bedrock-converse", "openai":
	case "responses":
		key = "input"
	default:
		return nil
	}
	if protocol == "bedrock-converse" {
		protocol = "bedrock"
	}
	var messages []map[string]json.RawMessage
	if json.Unmarshal(root[key], &messages) != nil {
		return nil
	}
	var out []json.RawMessage
	for _, message := range messages {
		role := stringValue(message["role"])
		if role != "user" && role != "assistant" {
			continue
		}
		if protocol == "responses" {
			if kind := stringValue(message["type"]); kind != "" && kind != "message" {
				continue
			}
		}
		var blocks []map[string]json.RawMessage
		if json.Unmarshal(message["content"], &blocks) != nil {
			continue // string content is text, even when it contains JSON
		}
		for _, block := range blocks {
			out = appendImage(out, protocol, block)
			if protocol == "bedrock" && block["type"] == nil {
				var result struct {
					Content []map[string]json.RawMessage `json:"content"`
				}
				if json.Unmarshal(block["toolResult"], &result) == nil {
					for _, content := range result.Content {
						out = appendImage(out, protocol, content)
					}
				}
			}
			if (protocol == "anthropic" || protocol == "bedrock") && stringValue(block["type"]) == "tool_result" {
				var results []map[string]json.RawMessage
				if json.Unmarshal(block["content"], &results) == nil {
					for _, result := range results {
						out = appendImage(out, protocol, result)
					}
				}
			}
		}
	}
	return out
}

func stringValue(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func appendImage(out []json.RawMessage, protocol string, block map[string]json.RawMessage) []json.RawMessage {
	kind := stringValue(block["type"])
	switch {
	case protocol == "bedrock" && kind == "" && block["image"] != nil:
		var image struct {
			Format string `json:"format"`
			Source struct {
				Bytes json.RawMessage `json:"bytes"`
			} `json:"source"`
		}
		if json.Unmarshal(block["image"], &image) != nil || !imageType("image/"+image.Format) ||
			!validBase64(stringValue(image.Source.Bytes)) {
			return out
		}
		return append(out, bytes.TrimSpace(image.Source.Bytes))
	case (protocol == "anthropic" || protocol == "bedrock") && kind == "image":
		var source map[string]json.RawMessage
		if json.Unmarshal(block["source"], &source) != nil || stringValue(source["type"]) != "base64" ||
			!imageType(stringValue(source["media_type"])) {
			return out
		}
		data := bytes.TrimSpace(source["data"])
		if !validBase64(stringValue(data)) {
			return out
		}
		return append(out, data)
	case protocol == "openai" && kind == "image_url":
		var image map[string]json.RawMessage
		if json.Unmarshal(block["image_url"], &image) != nil {
			return out
		}
		return appendDataURL(out, image["url"])
	case protocol == "responses" && kind == "input_image":
		return appendDataURL(out, block["image_url"])
	}
	return out
}

func imageType(media string) bool {
	switch media {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

func validBase64(data string) bool {
	if data == "" {
		return false
	}
	// Validate without retaining decoded image bytes or interpreting pixels.
	_, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(data)))
	return err == nil
}

func appendDataURL(out []json.RawMessage, raw json.RawMessage) []json.RawMessage {
	url := stringValue(raw)
	header, data, ok := strings.Cut(url, ",")
	if !ok || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") ||
		!imageType(strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")) || !validBase64(data) {
		return out
	}
	return append(out, bytes.TrimSpace(raw))
}
