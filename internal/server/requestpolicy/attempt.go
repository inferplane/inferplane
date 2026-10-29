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

func canceled(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) || ctx.Err() == context.Canceled
}

// EndRetriedAttempt notes the upstream status of a retriable failure and
// ends the open attempt. modelNotFound is the ingress's own verdict
// (cross-model boundary and its model-not-found predicate); status is
// otherwise canceled, timeout or upstream_error.
func EndRetriedAttempt(ctx context.Context, err error, httpStatus int, modelNotFound bool) {
	code := 0
	var ue *providers.UpstreamError
	if errors.As(err, &ue) {
		code = ue.HTTPStatus()
	} else if httpStatus > 0 {
		code = httpStatus
	}
	if code > 0 {
		audit.NoteAttemptHTTP(ctx, code)
	}
	status := audit.AttemptUpstreamError
	switch {
	case canceled(ctx, err):
		status = audit.AttemptCanceled
	case modelNotFound:
		status = audit.AttemptModelNotFound
	case errors.Is(err, context.DeadlineExceeded) || code == http.StatusGatewayTimeout:
		status = audit.AttemptTimeout
	}
	audit.EndOpenAttempt(ctx, status, 0)
}

// EndStreamAttempt ends the open attempt of a committed stream: ok when it
// completed cleanly, canceled when the request was canceled, else stream_failed.
func EndStreamAttempt(ctx context.Context, completed bool, err error) {
	switch {
	case canceled(ctx, err):
		audit.EndOpenAttempt(ctx, audit.AttemptCanceled, http.StatusOK)
	case completed && err == nil:
		audit.EndOpenAttempt(ctx, audit.AttemptOK, http.StatusOK)
	default:
		audit.EndOpenAttempt(ctx, audit.AttemptStreamFailed, http.StatusOK)
	}
}

// EndAttemptOnError closes the open attempt for a terminal provider error.
// The status is the upstream's own (noted from a *providers.UpstreamError),
// never the gateway's synthesized response; a deadline is a timeout.
func EndAttemptOnError(ctx context.Context, err error) {
	NoteAttemptFailure(ctx, err)
	status := audit.AttemptUpstreamError
	var ue *providers.UpstreamError
	if canceled(ctx, err) {
		status = audit.AttemptCanceled
	} else if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ue) && ue.HTTPStatus() == http.StatusGatewayTimeout) {
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
