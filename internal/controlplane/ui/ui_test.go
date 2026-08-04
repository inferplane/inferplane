package ui

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAssetsServedWithCSP(t *testing.T) {
	h := Handler()
	for path, wantCT := range map[string]string{
		"/":          "text/html",
		"/app.js":    "text/javascript",
		"/style.css": "text/css",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
			t.Fatalf("%s missing strict CSP: %q", path, csp)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, wantCT) {
			t.Fatalf("%s content-type %q, want prefix %q", path, got, wantCT)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s missing nosniff", path)
		}
	}
	// Unknown path 404s.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/etc/passwd", nil))
	if rec.Code != 404 {
		t.Fatalf("traversal-ish path must 404, got %d", rec.Code)
	}
}

// The token-storage discipline, mechanically checked: the served JS must
// never touch persistent browser storage, and the served bytes carry no
// secret. Inline handlers/styles would violate the CSP — check those too.
func TestServedBytesDiscipline(t *testing.T) {
	h := Handler()
	for _, path := range []string{"/", "/app.js", "/style.css"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		body := rec.Body.String()
		for _, banned := range []string{"localStorage", "sessionStorage", "document.cookie"} {
			if strings.Contains(body, banned) {
				t.Fatalf("%s uses %s — the token must live in JS memory only", path, banned)
			}
		}
		lower := strings.ToLower(body)
		if path == "/" {
			for _, banned := range []string{"onclick=", "style=\"", "<style"} {
				if strings.Contains(lower, banned) {
					t.Fatalf("%s contains inline %q — CSP default-src 'self' forbids it", path, banned)
				}
			}
		}
	}
}
