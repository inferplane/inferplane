package openaicompat

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/inferplane/inferplane/providers"
)

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
			// Exact vLLM v0.8.5 wording (and the other anchored shapes
			// providers.ContextOverflowCounts knows): only validated numeric
			// counts cross protocols, never provider text or suffixes.
			if tokens, maximum, ok := providers.ContextOverflowCounts(upstreamMessage); ok {
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
