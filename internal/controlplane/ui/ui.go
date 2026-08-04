// Package ui serves inferplaned's minimal read-only usage console. Same
// posture as mayu's adminui (ADR-001/002): the three static assets are
// data-free — no spend, no team names, no token — and therefore safe to
// serve unauthenticated; every data fetch the page performs goes through the
// bearer-gated /v1alpha1/usage API with a token the operator enters once and
// the page holds in JS memory only (never localStorage/sessionStorage/
// cookies). This resolves the "UI behind bearer" paradox the P2 gate raised:
// the shell is public, the data is not.
package ui

import (
	"embed"
	"net/http"
)

//go:embed static
var static embed.FS

// contentTypes pins the served types explicitly (with nosniff, the browser
// must not have to guess).
var contentTypes = map[string]string{
	"index.html": "text/html; charset=utf-8",
	"app.js":     "text/javascript; charset=utf-8",
	"style.css":  "text/css; charset=utf-8",
}

// Handler serves the console at the mount root: "/", "/app.js",
// "/style.css". Anything else is 404. Every response carries the strict CSP
// (default-src 'self' — no inline style/handlers, no external loads).
func Handler() http.Handler {
	const csp = "default-src 'self'; frame-ancestors 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := r.URL.Path
		if name == "/" || name == "" {
			name = "index.html"
		} else {
			name = name[1:]
		}
		ct, ok := contentTypes[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		body, err := static.ReadFile("static/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", ct)
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		_, _ = w.Write(body)
	})
}
