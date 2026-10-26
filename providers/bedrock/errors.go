package bedrock

import (
	"encoding/json"
	"errors"
	"log"
	"regexp"
	"strconv"

	smithy "github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/inferplane/inferplane/providers"
)

// bedrockErrorStatus maps a Bedrock smithy API error CODE to the HTTP status
// it represents. Covers every typed exception in
// aws-sdk-go-v2/service/bedrockruntime/types plus health.go's
// credentialErrorCodes (SigV4/credential rejection — 403, same map reused so
// the two classifications never drift apart).
var bedrockErrorStatus = map[string]int{
	"ThrottlingException":           429,
	"ServiceQuotaExceededException": 429,
	// The SDK auto-retries this up to 5 times; if it still surfaces, 429 is
	// the accurate client action (wait and retry) even though Bedrock's own
	// docs class it separately from throttling.
	"ModelNotReadyException":      429,
	"ValidationException":         400,
	"AccessDeniedException":       403,
	"ResourceNotFoundException":   404,
	"ModelTimeoutException":       408,
	"ServiceUnavailableException": 503,
	"ModelErrorException":         424, // only reached when OriginalStatusCode is absent
	"ModelStreamErrorException":   424, // only reached when OriginalStatusCode is absent
	"InternalServerException":     500,
	"ConflictException":           409,
}

// anthropicErrType maps an HTTP status to the Anthropic Messages API's own
// error `type` vocabulary (the same shape internal/server/anthropicapi's
// writeErr produces), so a synthesized error body reads like a real
// Anthropic error to the client.
func anthropicErrType(status int) string {
	switch status {
	case 400:
		return "invalid_request_error"
	case 401:
		return "authentication_error"
	case 403:
		return "permission_error"
	case 404:
		return "not_found_error"
	case 429:
		return "rate_limit_error"
	case 529:
		return "overloaded_error"
	default:
		return "api_error"
	}
}

// upstreamError classifies a Bedrock SDK error and converts it into a
// providers.UpstreamError carrying the ACTUAL upstream status (429 for a
// throttled model, 503 for an unavailable one, ...) instead of letting it
// fall through to a generic transport-error 502. The ingress (anthropicapi/
// openaiapi) already knows how to tee an UpstreamError verbatim — this is the
// only piece bedrock was missing.
//
// Classification order: a typed exception's own OriginalStatusCode (the most
// specific signal Bedrock gives) > the ErrorCode table above > the HTTP
// transport status smithy captured > 502 (unclassifiable).
//
// The synthesized body never echoes ErrorMessage() — it can carry an
// account id or ARN (see health.go's credentialErrorCodes comment) — only the
// error CODE and strictly recognized numeric prompt-overflow evidence.
func upstreamError(err error) *providers.UpstreamError {
	if overflow := promptOverflow(err); overflow != nil {
		return overflow
	}
	status := 502
	code := ""

	var mse *brtypes.ModelStreamErrorException
	var me *brtypes.ModelErrorException
	var apiErr smithy.APIError
	var re *smithyhttp.ResponseError

	switch {
	case errors.As(err, &mse) && mse.OriginalStatusCode != nil && validStatus(*mse.OriginalStatusCode):
		status = int(*mse.OriginalStatusCode)
		code = mse.ErrorCode()
	case errors.As(err, &me) && me.OriginalStatusCode != nil && validStatus(*me.OriginalStatusCode):
		status = int(*me.OriginalStatusCode)
		code = me.ErrorCode()
	case errors.As(err, &apiErr):
		code = apiErr.ErrorCode()
		if s, ok := bedrockErrorStatus[code]; ok {
			status = s
		} else if credentialErrorCodes[code] {
			status = 403
		} else if errors.As(err, &re) && validStatus(int32(re.HTTPStatusCode())) {
			status = re.HTTPStatusCode()
		}
	case errors.As(err, &re) && validStatus(int32(re.HTTPStatusCode())):
		// An out-of-range transport status (0 when smithy captured no real
		// response) keeps the 502 default: teeing it verbatim panicked the
		// ingress with "invalid WriteHeader code 0" (found live 2026-08-19).
		status = re.HTTPStatusCode()
	}

	msg := "bedrock upstream error"
	if code != "" {
		msg = "bedrock upstream error (" + code + ")"
	}
	// The full SDK error text goes to the SERVER log only — same principle as
	// anthropicapi's stream-interrupt log: it can carry an account id/ARN, so
	// it never reaches the client, but without it an operator has no way to
	// learn WHY the upstream refused (a ValidationException is "context
	// exceeded", "unsupported field", or "bad tool schema" indistinguishably
	// from the client's side). Logs are operator-only.
	log.Printf("bedrock: upstream error status=%d code=%s: %v", status, code, err)
	return synthError(status, msg)
}

var promptOverflowPattern = regexp.MustCompile(`(?i)^prompt is too long: ([0-9]+) tokens > ([0-9]+) maximum$`)

// Only the SDK's typed validation exception with this complete numeric shape
// is recovery evidence. Extra text, unknown wording and generic errors retain
// the normal scrubbed error; no provider string becomes a client message.
func promptOverflow(err error) *providers.UpstreamError {
	var validation *brtypes.ValidationException
	if !errors.As(err, &validation) {
		return nil
	}
	match := promptOverflowPattern.FindStringSubmatch(validation.ErrorMessage())
	if match == nil {
		return nil
	}
	input, inputErr := strconv.ParseInt(match[1], 10, 64)
	limit, limitErr := strconv.ParseInt(match[2], 10, 64)
	if inputErr != nil || limitErr != nil || limit <= 0 || input <= limit {
		return nil
	}
	body, _ := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]string{"type": "invalid_request_error", "code": "context_length_exceeded",
			"message": "prompt is too long: " + strconv.FormatInt(input, 10) + " tokens > " + strconv.FormatInt(limit, 10) + " maximum"},
	})
	return &providers.UpstreamError{StatusCode: 400, Body: body}
}

// synthError builds an UpstreamError with an Anthropic-shaped body, for the
// cases where the gateway itself is the one refusing (no SDK error to classify).
// The message must never carry an account id, ARN, or upstream error text —
// same rule as upstreamError's deliberate omission of ErrorMessage().
func synthError(status int, msg string) *providers.UpstreamError {
	body, _ := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": anthropicErrType(status), "message": msg},
	})
	return &providers.UpstreamError{StatusCode: status, Body: body}
}

func validStatus(code int32) bool {
	return code >= 100 && code <= 599
}
