package responses

import (
	"errors"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/providers"
)

// Bedrock Mantle reports an in-stream overflow as an error event whose code is
// nested under "error" (captured live 2026-11-27 from openai.gpt-6.2-astra),
// not at the top level. Codex compacts only when the code survives.
func TestReadSSEKeepsTheNestedErrorEventCode(t *testing.T) {
	raw := "data: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"status\":\"in_progress\",\"output\":[]}}\nevent: response.created\n\n" +
		"data: {\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"private-message\",\"param\":null,\"type\":\"invalid_request_error\"},\"sequence_number\":2,\"type\":\"error\"}\nevent: error\n\n"
	var failure error
	for _, err := range ReadSSE(strings.NewReader(raw), "m") {
		if err != nil {
			failure = err
		}
	}
	var upstream *providers.UpstreamError
	if !errors.Is(failure, ErrFailed) || !errors.As(failure, &upstream) {
		t.Fatalf("failure = %v, want ErrFailed with the upstream code", failure)
	}
	if got := providers.ResponseErrorCode(upstream.Body); got != "context_length_exceeded" {
		t.Fatalf("code = %q, want context_length_exceeded", got)
	}
	if strings.Contains(string(upstream.Body), "private") {
		t.Fatal("provider message crossed into the client body")
	}
}
