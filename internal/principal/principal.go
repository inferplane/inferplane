// Package principal carries the authenticated Principal across the request
// context. Separate from internal/server to avoid an import cycle
// (server imports anthropicapi; anthropicapi needs the principal accessor).
package principal

import (
	"context"

	"github.com/inferplane/inferplane/internal/keystore"
)

type ctxKey int

const (
	key      ctxKey = 0
	adminKey ctxKey = 1 // separate key: admin identity never shadows the data-plane Principal
)

func With(ctx context.Context, p keystore.Principal) context.Context {
	return context.WithValue(ctx, key, p)
}

func From(ctx context.Context) (keystore.Principal, bool) {
	p, ok := ctx.Value(key).(keystore.Principal)
	return p, ok
}

// AdminIdentity is the admin-plane caller (§5.1 Identity→Principal, ADR-004).
// PII-minimal by design (P2 gate): only the opaque OIDC `sub` — never email,
// never raw IdP groups — enters the request context; groups are consumed by
// the middleware's mapping step and dropped. Break-glass static tokens inject
// the sentinel {Subject: "break-glass", IsAdmin: true}.
type AdminIdentity struct {
	Subject    string
	Issuer     string   // verified OIDC issuer (empty for break-glass) — with Subject, the durable identity (Phase 0b)
	Teams      []string // teams this identity may issue/revoke keys for (nil for admins)
	IsAdmin    bool     // admin_groups member or break-glass: entitled to every team
	AuthMethod string   // "oidc" | "break_glass" — recorded in audit
	// Roles are the duty-separation roles (Phase 0b-3) resolved from the
	// verified groups claim; RoleGated says whether role gating is active
	// for this deployment (RoleMappings configured). With RoleGated false,
	// Roles is nil and every capability check passes — opt-in.
	Roles     []string
	RoleGated bool
}

// HasRole reports whether the identity holds the role. platform-admin (and
// break-glass, which authenticates as it) implies every role.
func (a AdminIdentity) HasRole(role string) bool {
	for _, r := range a.Roles {
		if r == role || r == "platform-admin" {
			return true
		}
	}
	return false
}

// UserID returns the canonical durable identity `issuer + "#" + subject`
// (Phase 0b design spec §3.1): "#" cannot appear in an https issuer URL and
// is opaque inside sub, so the FIRST "#" is an unambiguous split point.
// Empty when there is no verified issuer (break-glass) — a durable identity
// is never fabricated.
func (a AdminIdentity) UserID() string {
	if a.Issuer == "" || a.Subject == "" {
		return ""
	}
	return a.Issuer + "#" + a.Subject
}

// Entitled reports whether the identity may act on the given team.
// Fail-closed: a zero identity (or empty team) is entitled to nothing.
func (a AdminIdentity) Entitled(team string) bool {
	if a.IsAdmin {
		return true
	}
	if team == "" {
		return false
	}
	for _, t := range a.Teams {
		if t == team {
			return true
		}
	}
	return false
}

// WithAdmin attaches the admin-plane identity to the context.
func WithAdmin(ctx context.Context, a AdminIdentity) context.Context {
	return context.WithValue(ctx, adminKey, a)
}

// AdminFrom retrieves the admin-plane identity, if any.
func AdminFrom(ctx context.Context) (AdminIdentity, bool) {
	a, ok := ctx.Value(adminKey).(AdminIdentity)
	return a, ok
}
