package openaicompat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"

	"github.com/inferplane/inferplane/providers"
)

// These exact Chat Completions messages come from vLLM v0.8.5's
// serving_engine.py context validation. Anchor the whole message: only validated
// numeric counts may cross protocols, never arbitrary provider text or suffixes.
var vllmMessagesOverflow = regexp.MustCompile(`^This model's maximum context length is ([0-9]+) tokens\. However, you requested ([0-9]+) tokens in the messages, Please reduce the length of the messages\.$`)
var vllmCompletionOverflow = regexp.MustCompile(`^This model's maximum context length is ([0-9]+) tokens\. However, you requested ([0-9]+) tokens \(([0-9]+) in the messages, ([0-9]+) in the completion\)\. Please reduce the length of the messages or completion\.$`)

func anthropicRequestError(req *providers.ProxyRequest, status int, body []byte, header http.Header) ([]byte, http.Header) {
	if req.IngressProtocol != "anthropic" || status != http.StatusBadRequest {
		return body, header
	}
	code, message := "", "upstream request rejected"
	if len(body) <= 64<<10 {
		var envelope struct {
			Object  string          `json:"object"`
			Type    string          `json:"type"`
			Code    json.RawMessage `json:"code"`
			Message string          `json:"message"`
			Error   *struct {
				Code    json.RawMessage `json:"code"`
				Message string          `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil {
			upstreamMessage := ""
			if envelope.Error != nil {
				// Only an exact string is OpenAI protocol error-code evidence.
				var upstreamCode string
				_ = json.Unmarshal(envelope.Error.Code, &upstreamCode)
				if upstreamCode == "context_length_exceeded" {
					code, message = "context_length_exceeded", "prompt is too long"
				}
				upstreamMessage = envelope.Error.Message
			} else if envelope.Object == "error" && envelope.Type == "BadRequestError" {
				// vLLM v0.8.5 api_server.py returns ErrorResponse directly:
				// object/type/message plus a numeric HTTP code (protocol.py).
				// This code alone says nothing about context overflow.
				var statusCode int
				if json.Unmarshal(envelope.Code, &statusCode) == nil && statusCode == http.StatusBadRequest {
					upstreamMessage = envelope.Message
				}
			}
			if tokens, maximum, ok := vllmOverflowCounts(upstreamMessage); ok {
				code = "context_length_exceeded"
				message = fmt.Sprintf("prompt is too long: %d tokens > %d maximum", tokens, maximum)
			}
		}
	}
	converted, _ := json.Marshal(struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code,omitempty"`
			Message string `json:"message"`
		} `json:"error"`
	}{
		Type: "error",
		Error: struct {
			Type    string `json:"type"`
			Code    string `json:"code,omitempty"`
			Message string `json:"message"`
		}{Type: "invalid_request_error", Code: code, Message: message},
	})
	header = header.Clone()
	if header == nil {
		header = make(http.Header)
	}
	header.Set("Content-Type", "application/json")
	header.Del("Content-Length")
	header.Del("Content-Encoding")
	return converted, header
}

func vllmOverflowCounts(message string) (tokens, maximum int64, ok bool) {
	match := vllmMessagesOverflow.FindStringSubmatch(message)
	if match == nil {
		match = vllmCompletionOverflow.FindStringSubmatch(message)
	}
	if match == nil {
		return 0, 0, false
	}
	values := make([]int64, len(match)-1)
	for i, raw := range match[1:] {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			return 0, 0, false
		}
		values[i] = value
	}
	maximum, tokens = values[0], values[1]
	if tokens <= maximum {
		return 0, 0, false
	}
	// Validate the provider's split without overflowing addition.
	if len(values) == 4 && (values[2] >= tokens || tokens-values[2] != values[3]) {
		return 0, 0, false
	}
	return tokens, maximum, true
}
