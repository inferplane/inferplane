package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/inferplane/inferplane/internal/server/bedrockapi"
)

// writeIngressError renders a refusal made before any ingress handler runs in
// the envelope that route's clients parse: AWS JSON with X-Amzn-ErrorType on
// /model/*, Anthropic's envelope on /v1/messages*, and the OpenAI envelope
// (Chat Completions, Responses) everywhere else. An empty code is null.
func writeIngressError(w http.ResponseWriter, path string, status int, anthropicType, openaiType, code, message string) {
	if strings.HasPrefix(path, "/model/") {
		bedrockapi.WriteError(w, status, message)
		return
	}
	var body any
	if strings.HasPrefix(path, "/v1/messages") {
		body = map[string]any{"type": "error", "error": map[string]string{"type": anthropicType, "message": message}}
	} else {
		var openaiCode any
		if code != "" {
			openaiCode = code
		}
		body = map[string]any{"error": map[string]any{"type": openaiType, "message": message, "param": nil, "code": openaiCode}}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
