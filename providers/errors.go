package providers

import (
	"fmt"
	"net/http"
)

// UpstreamError carries a non-2xx upstream response on the streaming path,
// where the (iter.Seq2, error) signature can't return a ProxyResponse. The
// ingress type-asserts this (errors.As) to tee the real status/body/headers
// to the client verbatim instead of fabricating a gateway error — symmetric
// with Complete's ProxyResponse for non-streaming (design doc §4.4 tee).
type UpstreamError struct {
	StatusCode int
	Body       []byte
	Header     http.Header
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("upstream returned status %d", e.StatusCode)
}

// HTTPStatus returns StatusCode clamped to a writable HTTP status. Every
// ingress that tees an UpstreamError verbatim MUST go through this: a
// provider bug that leaves StatusCode 0 (or any out-of-range value) would
// otherwise reach http.ResponseWriter.WriteHeader, which panics with
// "invalid WriteHeader code 0" and resets the client's connection mid-request
// (found live 2026-08-19 on the bedrock ConverseStream error path). An
// unclassifiable status degrades to 502, the same fallback the providers use.
func (e *UpstreamError) HTTPStatus() int {
	if e.StatusCode < 100 || e.StatusCode > 599 {
		return http.StatusBadGateway
	}
	return e.StatusCode
}
