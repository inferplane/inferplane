package responses

import (
	"errors"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/providers"
)

func TestReadSSEPreservesOnlyBoundedNativeRecoveryCodes(t *testing.T) {
	for _, kind := range []string{"response.failed", "error"} {
		for _, code := range []string{"context_length_exceeded", "invalid_encrypted_content", "private-value"} {
			t.Run(kind+"/"+code, func(t *testing.T) {
				payload := `{"type":"error","code":"` + code + `","message":"private-message"}`
				if kind == "response.failed" {
					payload = `{"type":"response.failed","response":{"status":"failed","error":{"code":"` + code + `","message":"private-message"},"usage":{"input_tokens":10,"output_tokens":2}}}`
				}
				raw := "event: " + kind + "\ndata: " + payload + "\n\n"
				var observedUsage bool
				var failure error
				for event, err := range ReadSSE(strings.NewReader(raw), "public") {
					if event != nil {
						if len(event.Raw) != 0 {
							t.Fatal("unsafe native failure frame was forwarded")
						}
						observedUsage = observedUsage || event.Chunk != nil && event.Chunk.Usage != nil
					}
					if err != nil {
						failure = err
					}
				}
				if !errors.Is(failure, ErrFailed) {
					t.Fatalf("missing terminal failure: %v", failure)
				}
				var upstream *providers.UpstreamError
				got := ""
				if errors.As(failure, &upstream) {
					got = providers.ResponseErrorCode(upstream.Body)
					if strings.Contains(string(upstream.Body), "private") {
						t.Fatal("untrusted failure contents leaked")
					}
				}
				want := code
				if code == "private-value" {
					want = ""
				}
				if got != want || observedUsage != (kind == "response.failed") {
					t.Fatalf("code=%q want=%q usage=%v", got, want, observedUsage)
				}
			})
		}
	}
}

func TestNativeRecoveryCodesRequireTheirDocumentedEnvelope(t *testing.T) {
	for _, raw := range []string{
		`{"type":"response.failed","code":"context_length_exceeded"}`,
		`{"type":"error","response":{"error":{"code":"context_length_exceeded"}}}`,
	} {
		_, err := ParseEvent([]byte(raw))
		var upstream *providers.UpstreamError
		if !errors.Is(err, ErrFailed) || errors.As(err, &upstream) {
			t.Fatalf("wrong envelope became recovery evidence: %v", err)
		}
	}
}
