package providers

import "testing"

// TestUpstreamErrorHTTPStatusClamps: every ingress tees an UpstreamError via
// HTTPStatus(); a provider bug leaving StatusCode out of range must degrade
// to 502, never reach WriteHeader (which panics on 0 — found live 2026-08-19).
func TestUpstreamErrorHTTPStatusClamps(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, 502}, {-7, 502}, {99, 502}, {600, 502},
		{100, 100}, {429, 429}, {503, 503}, {599, 599},
	} {
		e := &UpstreamError{StatusCode: tc.in}
		if got := e.HTTPStatus(); got != tc.want {
			t.Errorf("HTTPStatus(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
