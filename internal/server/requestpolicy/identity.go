package requestpolicy

import (
	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
)

// AuditPrincipal keeps historical records byte-compatible and adds only
// digest-based evidence when the credential carries a verified identity.
func AuditPrincipal(p keystore.Principal) audit.PrincipalRef {
	return principal.AuditRef(p)
}

// TelemetrySubject is stable across credential rotation and never emits a raw
// issuer or subject for managed identities. Financial lookup keeps AccountSubject.
func TelemetrySubject(p keystore.Principal) string {
	if p.Identity != nil && p.Identity.Validate() == nil {
		return p.Identity.CanonicalRef()
	}
	return p.AccountSubject()
}
