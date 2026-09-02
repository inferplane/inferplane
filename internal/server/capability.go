package server

// Capability gating (Phase 0b-3, design spec §4): each management route
// CLASS requires a capability, satisfied by the fixed roles below.
// platform-admin (which break-glass authenticates as) satisfies everything.
// Gating is OPT-IN: an identity from a deployment with no RoleMappings has
// RoleGated=false and every check passes — pre-roles behavior,
// byte-identical. A denial is an audited 403 naming the capability
// (the adminDenialEmitter posture: authenticated-but-unauthorized IS a
// governance event; 401s never grow the chain).

import (
	"net/http"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/principal"
)

// The management capabilities (route classes).
const (
	CapKeys      = "keys"       // virtual-key issue/list/revoke, whoami self-service
	CapTeams     = "teams"      // team governance records (budgets/limits/regions)
	CapProviders = "providers"  // provider/model topology writes + connection probe
	CapAuditRead = "audit-read" // audit verify, logs, analytics, captured bodies
	CapDebugRead = "debug-read" // governance debug snapshot
)

// capabilityRoles maps each capability to the roles that satisfy it
// (platform-admin implicitly satisfies all via HasRole).
var capabilityRoles = map[string][]string{
	CapKeys:      {adminauth.RoleTeamAdmin},
	CapTeams:     {adminauth.RoleTeamAdmin, adminauth.RoleBudgetAdmin},
	CapProviders: {adminauth.RoleProviderAdmin},
	CapAuditRead: {adminauth.RoleAuditor},
	CapDebugRead: {adminauth.RoleAuditor},
}

// RequireCapability wraps a management handler class: with role gating
// active, the identity must hold a satisfying role (or platform-admin);
// without it, the check is a no-op. Runs INSIDE AdminAuth, so an identity
// is always present; a missing one fails closed anyway.
func RequireCapability(capability string, auditDenied func(r *http.Request, subject string), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := principal.AdminFrom(r.Context())
		if !ok {
			writeAnthropicError(w, http.StatusForbidden, "permission_error", "no admin identity")
			return
		}
		if id.RoleGated {
			allowed := id.HasRole(adminauth.RolePlatformAdmin)
			for _, role := range capabilityRoles[capability] {
				if allowed {
					break
				}
				allowed = id.HasRole(role)
			}
			if !allowed {
				if auditDenied != nil {
					auditDenied(r, id.Subject)
				}
				writeAnthropicError(w, http.StatusForbidden, "permission_error", "missing capability: "+capability)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
