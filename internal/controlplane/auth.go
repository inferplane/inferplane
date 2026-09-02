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
	return authnCap(token, opts, nil, next)
}

// actorKey carries the authenticated actor down to mutation-audit records
// (Phase 0b-4): the durable identity `issuer#sub` on the OIDC path,
// "static-token" on the shared-token path, "" when unauthenticated
// (loopback-only posture).
type actorKey struct{}

// Actor returns the authenticated actor authnCap stamped, if any.
func Actor(ctx context.Context) string {
	a, _ := ctx.Value(actorKey{}).(string)
	return a
}

// authnCap is authn plus an optional capability requirement (Phase 0b-4):
// when requiredRoles is non-empty AND role gating is configured
// (mapping.RoleMappings non-empty), an OIDC identity must hold one of the
// roles (or platform-admin); the static token is platform-admin break-glass
// and always passes. With no RoleMappings configured, requiredRoles is
// inert — pre-roles authority, byte-identical (the mayu-plane opt-in rule).
func authnCap(token string, opts authOptions, requiredRoles []string, next http.HandlerFunc) http.HandlerFunc {
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
			roles := adminauth.ResolveRoles(claims.Groups, opts.mapping)
			if _, _, ok := adminauth.Resolve(claims.Groups, opts.mapping); !ok {
				// Under active role gating a role-holding identity with no
				// group→access mapping still authenticates (mirrors the
				// mayu-plane rule: an auditor need not map to a team).
				if !(len(opts.mapping.RoleMappings) > 0 && len(roles) > 0) {
					http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
					return
				}
			}
			if len(requiredRoles) > 0 && len(opts.mapping.RoleMappings) > 0 {
				allowed := false
				for _, have := range roles {
					if have == adminauth.RolePlatformAdmin {
						allowed = true
						break
					}
					for _, want := range requiredRoles {
						if have == want {
							allowed = true
							break
						}
					}
				}
				if !allowed {
					http.Error(w, `{"error":"missing capability"}`, http.StatusForbidden)
					return
				}
			}
			actor := claims.Issuer + "#" + claims.Subject
			next(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, actor)))
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
		// The static token is platform-admin break-glass: never role-gated.
		next(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, "static-token")))
	}
}
