package bedrockapi

import (
	"encoding/json"
	"net/http"
)

func exceptionName(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "ValidationException"
	case http.StatusUnauthorized:
		return "UnauthorizedException"
	case http.StatusForbidden:
		return "AccessDeniedException"
	case http.StatusNotFound:
		return "ResourceNotFoundException"
	case http.StatusRequestTimeout:
		return "ModelTimeoutException"
	case http.StatusTooManyRequests:
		return "ThrottlingException"
	default:
		return "InternalServerException"
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	body, _ := json.Marshal(struct {
		Message string `json:"message"`
	}{Message: msg})
	w.Header().Set("X-Amzn-ErrorType", exceptionName(status))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// upstreamErrMessage extracts a client-safe message from an upstream error
// body: only the nested error envelope ({"error":{"message":...}} — the shape
// the bedrock provider synthesizes via synthError/anthropicErrorBody, the real
// Anthropic API emits, and OpenAI-wire vendors emit) is echoed. That envelope
// filter is the safety argument: the ARN-bearing class (raw AWS front-layer
// errors) is flat {"message":...} and never matches, so it falls back to the
// fixed string. Without this, a gateway-synthesized refusal (e.g. the mantle
// guardrail check's "remove the guardrail or route via converse") reached
// Bedrock-ingress clients as an undiagnosable generic error while the other
// two ingresses teed it verbatim.
func upstreamErrMessage(body []byte, fallback string) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil && env.Error.Message != "" {
		return env.Error.Message
	}
	return fallback
}
