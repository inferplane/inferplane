// Shared bearer-auth middleware for Server and UsageServer (D3, inferplaned
// console SSO plan): one implementation instead of two byte-identical
// duplicated auth methods.
package controlplane

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/inferplane/inferplane/internal/adminauth"
)

// OIDCVerifier is the narrow seam to internal/adminauth.Verifier so tests can
// fake the OIDC path without a network. A separate interface from
// internal/server's OIDCVerifier — controlplane is a peer package, not a
// dependent of server, and must not import it.
type OIDCVerifier interface {
	Verify(ctx context.Context, raw string) (adminauth.Claims, error)
}

// authOptions is the OIDC config Server/UsageServer accept via Option.
type authOptions struct {
	verifier OIDCVerifier
	mapping  adminauth.MappingConfig
}

// Option configures optional cross-cutting behavior on Server/UsageServer.
type Option func(*authOptions)

// WithOIDC enables console-SSO bearer auth alongside the static token.
// Routing is the same TOTAL rule as internal/server's AdminAuth: verifier
// non-nil AND the bearer is JWT-shaped (adminauth.IsOIDCBearerShape) ⇒ OIDC
// path; everything else ⇒ static path. That total rule is what keeps mayu's
// usage-pusher token (never JWT-shaped) from ever reaching the verifier, and
// keeps a JWT-shaped bearer from ever being compared against the static
// token (auth-bypass/timing-oracle guard — same rationale as ADR-004).
func WithOIDC(verifier OIDCVerifier, mapping adminauth.MappingConfig) Option {
	return func(o *authOptions) {
		o.verifier = verifier
		o.mapping = mapping
	}
}

func newAuthOptions(opts []Option) authOptions {
	var o authOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// authn is the shared middleware. There is no request-context principal and
// no per-team scoping on the control plane (D4/D5): a resolved OIDC identity
// and the static token both grant the SAME whole-console access the one
// shared token already grants today — SSO is a strict improvement in
// per-human identity and short-lived tokens, not a new authorization axis.
//
// token == "" AND no verifier configured ⇒ unauthenticated, byte-identical
// to today's loopback-only posture. Otherwise a request needs EITHER a
// verified OIDC token whose groups resolve to something, OR the exact static
// token — an SSO-only deploy (empty token, verifier configured) is a
// legitimate posture, not a break-glass gap.
func authn(token string, opts authOptions, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token == "" && opts.verifier == nil {
			next(w, r)
			return
		}

		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if bearer == "" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		if opts.verifier != nil && adminauth.IsOIDCBearerShape(bearer) {
			claims, err := opts.verifier.Verify(r.Context(), bearer)
			if err != nil {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			if _, _, ok := adminauth.Resolve(claims.Groups, opts.mapping); !ok {
				http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
				return
			}
			next(w, r)
			return
		}

		if token == "" {
			// SSO-only deploy: a non-shaped bearer can never authenticate.
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if subtle.ConstantTimeCompare([]byte(bearer), []byte(token)) != 1 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
