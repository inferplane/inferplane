package ui

import (
	"net/http/httptest"
	"regexp"
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

// allowedSessionStorageKeys are the ONLY sessionStorage keys the console may
// ever use — the three short-lived PKCE values (ADR-037 console SSO,
// ported from mayu's adminui/ADR-026) that must survive the browser
// round-trip to the IdP and back (a plain JS variable does not survive a
// full-page navigation). The id_token itself must NEVER be persisted
// anywhere — it stays memory-only, same as today.
var allowedSessionStorageKeys = []string{"ip_sso_verifier", "ip_sso_state", "ip_sso_nonce"}

// sessionStorageCallRE mirrors adminui_test.go's regex exactly: it is NOT a
// blanket ban and NOT a blanket "ip_sso_" prefix exemption (that would let
// e.g. "ip_sso_token" smuggle a persisted bearer token past this check) —
// every other sessionStorage usage remains banned.
var sessionStorageCallRE = regexp.MustCompile(`sessionStorage\.(?:setItem|getItem|removeItem)\(\s*["'](\w+)["']`)

func sessionStorageUsesOnlyAllowedKeys(body string) (ok bool, offendingKey string) {
	for _, m := range sessionStorageCallRE.FindAllStringSubmatch(body, -1) {
		key := m[1]
		allowed := false
		for _, want := range allowedSessionStorageKeys {
			if key == want {
				allowed = true
				break
			}
		}
		if !allowed {
			return false, key
		}
	}
	return true, ""
}

// The token-storage discipline, mechanically checked: the served JS must
// never touch persistent browser storage EXCEPT the three named PKCE keys
// above, and the served bytes carry no secret. Inline handlers/styles would
// violate the CSP — check those too.
func TestServedBytesDiscipline(t *testing.T) {
	h := Handler()
	for _, path := range []string{"/", "/app.js", "/style.css"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		body := rec.Body.String()
		// "sessionStorage[" (bracket/computed-key access) and
		// "sessionStorage.clear" are banned outright — they bypass the
		// named-key allowlist below, which only understands
		// sessionStorage.{set,get,remove}Item("literal").
		for _, banned := range []string{"localStorage", "document.cookie", "sessionStorage[", "sessionStorage.clear"} {
			if strings.Contains(body, banned) {
				t.Fatalf("%s uses %s — the token must live in JS memory only", path, banned)
			}
		}
		if ok, offending := sessionStorageUsesOnlyAllowedKeys(body); !ok {
			t.Fatalf("%s uses sessionStorage with disallowed key %q (only %v are permitted)", path, offending, allowedSessionStorageKeys)
		}
		lower := strings.ToLower(body)
		if path == "/" {
			for _, banned := range []string{"onclick=", "style=\"", "<style"} {
				if strings.Contains(lower, banned) {
					t.Fatalf("%s contains inline %q — CSP default-src 'self' forbids it", path, banned)
				}
			}
		}
		// The budget form does not render period, so saveCard's builder must
		// carry the fetched rule's period through explicitly — otherwise a
		// console round trip silently downgrades a CalendarDay cap to a
		// CalendarMonth one. A text pin is the only guard a Go test can give
		// browser-side code.
		if path == "/app.js" && !strings.Contains(body, "prev.period") {
			t.Fatalf("%s no longer carries prev.period through a budget save — a console round trip must not silently turn a CalendarDay cap into a CalendarMonth one", path)
		}
	}
}

func TestHandlerConnectSrc(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if csp := rec.Header().Get("Content-Security-Policy"); strings.Contains(csp, "connect-src") {
		t.Fatalf("no extraConnectSrc must be byte-identical to the pre-SSO CSP, got %q", csp)
	}

	rec2 := httptest.NewRecorder()
	Handler("https://idp.example.com", "https://console.example.com").ServeHTTP(rec2, httptest.NewRequest("GET", "/", nil))
	csp := rec2.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "connect-src 'self' https://idp.example.com https://console.example.com") {
		t.Fatalf("CSP must widen connect-src with every supplied origin, got %q", csp)
	}
}
