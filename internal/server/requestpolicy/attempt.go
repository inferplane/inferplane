package requestpolicy

import (
	"context"
	"errors"
	"net/http"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/providers"
)

// NoteAttemptFailure records the upstream HTTP status of a retriable failure
// on the open attempt (ADR-057). Transport errors carry no status.
func NoteAttemptFailure(ctx context.Context, err error) {
	var ue *providers.UpstreamError
	if errors.As(err, &ue) {
		audit.NoteAttemptHTTP(ctx, ue.HTTPStatus())
	}
}

// RetryStatus is the closed attempt status for a retriable failure: a
// noted 4xx other than 429 at a cross-model boundary is the only
// retriable "model not found" case; 5xx, 429 and transport errors are
// upstream errors. Call it while the attempt is still open: it reads the
// status noted on the open attempt.
func RetryStatus(ctx context.Context, crossModelNext bool) string {
	h := audit.OpenAttemptHTTP(ctx)
	if crossModelNext && h >= 400 && h < 500 && h != 429 {
		return audit.AttemptModelNotFound
	}
	return audit.AttemptUpstreamError
}

// EndAttemptOnError closes the open attempt for a terminal provider error.
// The status is the upstream's own (noted from a *providers.UpstreamError),
// never the gateway's synthesized response; a deadline is a timeout.
func EndAttemptOnError(ctx context.Context, err error) {
	NoteAttemptFailure(ctx, err)
	status := audit.AttemptUpstreamError
	var ue *providers.UpstreamError
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ue) && ue.HTTPStatus() == http.StatusGatewayTimeout) {
		status = audit.AttemptTimeout
	}
	audit.EndOpenAttempt(ctx, status, 0)
}

// AttemptStatusForHTTP maps a committed upstream response status to an attempt status.
func AttemptStatusForHTTP(code int) string {
	switch {
	case code < 400:
		return audit.AttemptOK
	case code == http.StatusGatewayTimeout:
		return audit.AttemptTimeout
	default:
		return audit.AttemptUpstreamError
	}
}
