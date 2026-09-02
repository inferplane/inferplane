package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/principal"
)

// maxAdminBearerLen mirrors the adminauth shape cap: anything larger is
// rejected before splitting/parsing/hashing (DoS guard, ADR-004).
const maxAdminBearerLen = 8 * 1024

// OIDCVerifier is the narrow seam to internal/adminauth.Verifier so tests can
// fake the OIDC path without a network.
type OIDCVerifier interface {
	Verify(ctx context.Context, raw string) (adminauth.Claims, error)
}

// AdminAuth is the unified admin-plane authenticator (ADR-004): static
// break-glass tokens AND externally-acquired OIDC ID tokens on the same
// Authorization: Bearer header. Routing is a TOTAL rule — verifier configured
// AND adminauth.IsOIDCBearerShape(bearer) ⇒ OIDC path; everything else ⇒
// static path. The same predicate gates config load (a JWT-shaped static
// token is a load error), so the two paths are mutually exclusive: a shaped
// bearer is never compared against static hashes (auth-bypass/timing-oracle
// guard) and a non-shaped bearer never reaches the verifier.
//
// 401 = bad/absent credential (never audited — unauthenticated flood must not
// grow the audit chain). 403 = authenticated but mapped to nothing; that IS a
// governance event and goes through auditDenied (may be nil to skip).
func AdminAuth(tokens []string, verifier OIDCVerifier, mapping adminauth.MappingConfig, auditDenied func(r *http.Request, subject string), next http.Handler) http.Handler {
	hashes := make([][32]byte, len(tokens))
	for i, t := range tokens {
		hashes[i] = sha256.Sum256([]byte(t))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if bearer == "" || len(bearer) > maxAdminBearerLen {
			writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "admin credential required")
			return
		}

		if verifier != nil && adminauth.IsOIDCBearerShape(bearer) {
			claims, err := verifier.Verify(r.Context(), bearer)
			if err != nil {
				writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "invalid OIDC token")
				return
			}
			roleGated := len(mapping.RoleMappings) > 0
			roles := adminauth.ResolveRoles(claims.Groups, mapping)
			teams, isAdmin, ok := adminauth.Resolve(claims.Groups, mapping)
			if !ok {
				// Under an active role config, a role-holding identity with
				// no team mapping is still a legitimate authentication — an
				// auditor need not belong to any team. Without role config
				// the pre-roles behavior stands, byte-identical.
				if !(roleGated && len(roles) > 0) {
					if auditDenied != nil {
						auditDenied(r, claims.Subject)
					}
					writeAnthropicError(w, http.StatusForbidden, "permission_error", "identity maps to no team")
					return
				}
			}
			id := principal.AdminIdentity{Subject: claims.Subject, Issuer: claims.Issuer, Teams: teams, IsAdmin: isAdmin, AuthMethod: "oidc", Roles: roles, RoleGated: roleGated}
			next.ServeHTTP(w, r.WithContext(principal.WithAdmin(r.Context(), id)))
			return
		}

		if len(hashes) == 0 {
			writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "admin token required")
			return
		}
		gh := sha256.Sum256([]byte(bearer))
		ok := false
		for _, h := range hashes {
			if subtle.ConstantTimeCompare(gh[:], h[:]) == 1 {
				ok = true
			}
		}
		if !ok {
			writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "invalid admin token")
			return
		}
		// Break-glass is platform-admin by definition — under role gating it
		// carries the role explicitly so HasRole answers uniformly.
		id := principal.AdminIdentity{Subject: "break-glass", IsAdmin: true, AuthMethod: "break_glass", RoleGated: len(mapping.RoleMappings) > 0, Roles: breakGlassRoles(mapping)}
		next.ServeHTTP(w, r.WithContext(principal.WithAdmin(r.Context(), id)))
	})
}

func breakGlassRoles(mapping adminauth.MappingConfig) []string {
	if len(mapping.RoleMappings) == 0 {
		return nil
	}
	return []string{adminauth.RolePlatformAdmin}
}

// AdminTokenAuth guards the admin plane. Tokens are compared by SHA-256 +
// constant-time (so length/content don't leak via timing), and multiple tokens
// are accepted for rotation (§5.5). An empty token set denies everything
// (defense-in-depth, like KeyAuth). Separate credential from data-plane keys.
func AdminTokenAuth(tokens []string, next http.Handler) http.Handler {
	hashes := make([][32]byte, len(tokens))
	for i, t := range tokens {
		hashes[i] = sha256.Sum256([]byte(t))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" || len(hashes) == 0 {
			writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "admin token required")
			return
		}
		gh := sha256.Sum256([]byte(got))
		ok := false
		for _, h := range hashes {
			if subtle.ConstantTimeCompare(gh[:], h[:]) == 1 {
				ok = true
			}
		}
		if !ok {
			writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "invalid admin token")
			return
		}
		next.ServeHTTP(w, r)
	})
}
