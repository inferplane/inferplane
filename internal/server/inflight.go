package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/inferplane/inferplane/internal/metrics"
)

// inflightRetryAfterSeconds is the polling hint for a refused request: one
// large body usually finishes within seconds.
const inflightRetryAfterSeconds = "2"

// inflightBudget bounds the request-body bytes held across all data-plane
// requests (server.max_inflight_request_bytes). It never queues.
type inflightBudget struct {
	mu    sync.Mutex
	used  int64
	limit int64
}

// acquire reserves n bytes, reporting the new total, or refuses.
func (b *inflightBudget) acquire(n int64) (int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > b.limit-b.used {
		return b.used, false
	}
	b.used += n
	return b.used, true
}

func (b *inflightBudget) release(n int64) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.used -= n
	return b.used
}

func countPath(path string) bool {
	return strings.HasSuffix(path, "/count_tokens") || strings.HasSuffix(path, "/count-tokens")
}

// inflightMiddleware sits inside KeyAuth, so only authenticated callers hold
// budget. A request reserves its declared Content-Length, or the whole
// per-request cap when the length is undeclared, until its handler returns —
// a stream until it ends, since its body is kept for fallback. Ingress
// processing holds several copies of each body (≈5.5× measured 2026-10-27),
// so size the budget from the memory limit, not the body sizes. Generation
// that does not fit is refused with a protocol-shaped 503 and Retry-After.
// A count that does not fit stays HTTP 200: it is answered locally at four
// bytes per token, without buffering the body or calling a provider.
func inflightMiddleware(b *inflightBudget, requestLimit int64, m *metrics.Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := r.ContentLength
		if n < 0 {
			n = requestLimit
		}
		if n == 0 {
			next.ServeHTTP(w, r)
			return
		}
		used, ok := b.acquire(n)
		if !ok {
			if countPath(r.URL.Path) {
				m.IncInflightRefusal("count")
				writeLocalCount(w, r)
				return
			}
			m.IncInflightRefusal("generation")
			w.Header().Set("Retry-After", inflightRetryAfterSeconds)
			writeIngressError(w, r.URL.Path, http.StatusServiceUnavailable, "overloaded_error", "server_error", "gateway_overloaded",
				"gateway is processing too many large requests; retry shortly")
			return
		}
		m.SetInflightRequestBytes(used)
		defer func() { m.SetInflightRequestBytes(b.release(n)) }()
		next.ServeHTTP(w, r)
	})
}

// writeLocalCount answers a count request without holding its body: the
// declared length, or the bytes drained from a bounded undeclared body.
func writeLocalCount(w http.ResponseWriter, r *http.Request) {
	size := r.ContentLength
	if size < 0 {
		size, _ = io.Copy(io.Discard, r.Body)
	}
	field := "input_tokens"
	if strings.HasPrefix(r.URL.Path, "/model/") {
		field = "inputTokens" // Bedrock CountTokens shape
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]int64{field: max(int64(1), size/4)})
}
