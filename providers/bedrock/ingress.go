package bedrock

import (
	"encoding/json"
	"fmt"

	"github.com/inferplane/inferplane/providers"
)

// anthropicRequest re-renders a cross-protocol request for the Anthropic-shaped
// Bedrock wires. InvokeModel and Converse both consume Anthropic Messages JSON;
// an OpenAI-ingress request arrives with the client's OpenAI bytes in RawBody
// (the ingress keeps them for the cache invariant) and the canonical form in
// Parsed. Forwarding RawBody as-is would put `max_completion_tokens`,
// `tool_calls` and `tools[].function` on a wire that has no such fields.
//
// Only the openai ingress is converted; every other ingress keeps RawBody
// verbatim (cache safety). The chat ingress always supplies Parsed
// (openaiapi/chat.go builds the ProxyRequest from the canonical it routed on);
// a nil Parsed under the openai label is a caller that chose to send
// Anthropic-shaped bytes itself — the legacy text-only contract the ADR-043
// router's Converse safety gate admits — and is passed through unchanged.
// The caller's request is never mutated — a clone with the re-rendered body
// is returned so fallback to a different provider still sees the original
// bytes.
func anthropicRequest(req *providers.ProxyRequest) (*providers.ProxyRequest, error) {
	if req.IngressProtocol != "openai" || req.Parsed == nil {
		return req, nil
	}
	canonical := *req.Parsed
	canonical.Model = req.Model
	stream := req.Stream
	canonical.Stream = &stream
	raw, err := json.Marshal(&canonical)
	if err != nil {
		return nil, fmt.Errorf("bedrock: render canonical request: %w", err)
	}
	clone := *req
	clone.RawBody = raw
	return &clone, nil
}
