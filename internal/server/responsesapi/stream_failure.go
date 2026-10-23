package responsesapi

import (
	"context"
	"errors"

	"github.com/inferplane/inferplane/providers"
)

var (
	errStreamIncomplete     = errors.New("stream incomplete")
	errResponseIncompatible = errors.New("response incompatible")
	errUsageMissing         = errors.New("usage missing")
	errClientWriteFailed    = errors.New("client write failed")
)

// streamFailureCode retains only typed, bounded evidence. Error messages and
// arbitrary provider codes can contain prompts or credentials and are ignored.
func streamFailureCode(cause error) string {
	var upstream *providers.UpstreamError
	if errors.As(cause, &upstream) {
		if code := providers.ResponseErrorCode(upstream.Body); code != "" {
			return code
		}
	}
	switch {
	case errors.Is(cause, context.Canceled):
		return "request_canceled"
	case errors.Is(cause, context.DeadlineExceeded):
		return "upstream_timeout"
	case errors.Is(cause, errStreamIncomplete):
		return "stream_incomplete"
	case errors.Is(cause, errResponseIncompatible):
		return "response_incompatible"
	case errors.Is(cause, errUsageMissing):
		return "usage_missing"
	case errors.Is(cause, errClientWriteFailed):
		return "client_write_failed"
	default:
		return "upstream_error"
	}
}
