package bedrock

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithy "github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// TestUpstreamErrorThrottling pins the bug fix: a Bedrock ThrottlingException
// must surface as 429/rate_limit_error, not a generic 502 — the CLI needs
// this signal to know to back off and retry instead of treating it as an
// opaque failure.
func TestUpstreamErrorThrottling(t *testing.T) {
	err := fmt.Errorf("bedrock: invoke: %w", &brtypes.ThrottlingException{Message: aws.String("nope")})
	ue := upstreamError(err)
	if ue.StatusCode != 429 {
		t.Fatalf("status = %d, want 429", ue.StatusCode)
	}
	if !strings.Contains(string(ue.Body), "rate_limit_error") {
		t.Fatalf("body missing rate_limit_error: %s", ue.Body)
	}
}

// TestUpstreamErrorNeverLeaksErrorMessage: ErrorMessage() can carry an
// account id/ARN (same concern as health.go's credentialErrorCodes comment)
// — the synthesized body must carry only the error CODE, never the message.
func TestUpstreamErrorNeverLeaksErrorMessage(t *testing.T) {
	err := &smithy.GenericAPIError{Code: "ValidationException", Message: "arn:aws:iam::123456789012:role/secret"}
	ue := upstreamError(err)
	if strings.Contains(string(ue.Body), "123456789012") || strings.Contains(string(ue.Body), "arn:aws") {
		t.Fatalf("body leaked ErrorMessage: %s", ue.Body)
	}
	if ue.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", ue.StatusCode)
	}
}

// TestUpstreamErrorOriginalStatusCodeWinsOverTable: ModelStreamErrorException
// carries the real upstream status Bedrock observed (e.g. 429 mid-stream) —
// that must win over the generic table entry (424) for the same error code.
func TestUpstreamErrorOriginalStatusCodeWinsOverTable(t *testing.T) {
	err := &brtypes.ModelStreamErrorException{OriginalStatusCode: aws.Int32(429)}
	ue := upstreamError(err)
	if ue.StatusCode != 429 {
		t.Fatalf("status = %d, want 429 (OriginalStatusCode should win over the 424 table entry)", ue.StatusCode)
	}
}

// TestUpstreamErrorModelErrorExceptionFallsBackToTable: without an
// OriginalStatusCode, ModelErrorException falls back to its table entry.
func TestUpstreamErrorModelErrorExceptionFallsBackToTable(t *testing.T) {
	err := &brtypes.ModelErrorException{}
	ue := upstreamError(err)
	if ue.StatusCode != 424 {
		t.Fatalf("status = %d, want 424", ue.StatusCode)
	}
}

// TestUpstreamErrorCredentialCodesReuseHealthMap: a credential/signature
// rejection code (already classified by health.go's credentialErrorCodes)
// must map to 403 here too — the two classifications must never drift apart.
func TestUpstreamErrorCredentialCodesReuseHealthMap(t *testing.T) {
	err := &smithy.GenericAPIError{Code: "ExpiredTokenException"}
	ue := upstreamError(err)
	if ue.StatusCode != 403 {
		t.Fatalf("status = %d, want 403", ue.StatusCode)
	}
}

// TestUpstreamErrorUnclassifiedFallsBackTo502: a plain transport error (no
// smithy.APIError in the chain) has no status to recover — 502 is the
// correct, honest fallback.
func TestUpstreamErrorUnclassifiedFallsBackTo502(t *testing.T) {
	err := errors.New("dial tcp: connection refused")
	ue := upstreamError(err)
	if ue.StatusCode != 502 {
		t.Fatalf("status = %d, want 502", ue.StatusCode)
	}
	if !strings.Contains(string(ue.Body), "api_error") {
		t.Fatalf("body missing api_error: %s", ue.Body)
	}
}

// TestUpstreamErrorWrappedMultipleTimes: the %w chains client.go already uses
// (e.g. "bedrock: converse stream %q: %w") must not block errors.As.
func TestUpstreamErrorWrappedMultipleTimes(t *testing.T) {
	inner := &brtypes.ServiceUnavailableException{}
	err := fmt.Errorf("bedrock: converse stream %q: %w", "claude-x", fmt.Errorf("bedrock: converse: %w", inner))
	ue := upstreamError(err)
	if ue.StatusCode != 503 {
		t.Fatalf("status = %d, want 503", ue.StatusCode)
	}
}

// TestUpstreamErrorInvalidTransportStatusFallsBackTo502 pins the live panic
// found on 2026-08-19: a smithy ResponseError whose captured HTTP status is 0
// (or any out-of-range value) flowed UNVALIDATED into UpstreamError.StatusCode,
// and every ingress that tees that status verbatim (bedrockapi.writeErr,
// openaiapi's WriteHeader) then panicked with "invalid WriteHeader code 0",
// resetting the client's connection mid-request. An unclassifiable transport
// status must degrade to 502, exactly like an unclassifiable error.
func TestUpstreamErrorInvalidTransportStatusFallsBackTo502(t *testing.T) {
	for _, status := range []int{0, -1, 99, 600} {
		err := fmt.Errorf("bedrock: converse stream: %w", &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
			Err:      errors.New("stream open failed"),
		})
		ue := upstreamError(err)
		if ue.StatusCode != 502 {
			t.Fatalf("transport status %d: UpstreamError.StatusCode = %d, want 502", status, ue.StatusCode)
		}
	}
}

// TestUpstreamErrorValidTransportStatusStillWins: the fix must not break the
// legitimate path — a real captured 503 still surfaces as 503.
func TestUpstreamErrorValidTransportStatusStillWins(t *testing.T) {
	err := fmt.Errorf("bedrock: invoke: %w", &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: 503}},
		Err:      errors.New("service unavailable"),
	})
	if ue := upstreamError(err); ue.StatusCode != 503 {
		t.Fatalf("status = %d, want 503", ue.StatusCode)
	}
}
