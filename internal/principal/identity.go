package principal

import (
	"context"
	"errors"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/identity"
	"github.com/inferplane/inferplane/internal/keystore"
)

// IdentityConfig reads managed state without importing gateway configuration.
// A legacy Store need not implement IdentityStore; an implementing store's
// errors must never be mistaken for disabled managed identity.
func IdentityConfig(ctx context.Context, store keystore.Store) (identity.Config, error) {
	s, ok := store.(keystore.IdentityStore)
	if !ok {
		return identity.Config{}, nil
	}
	cfg, err := s.IdentityConfig(ctx)
	if err != nil || cfg.Validate() != nil {
		return identity.Config{}, errors.New("identity configuration unavailable")
	}
	return cfg, nil
}

// HumanIdentity consumes only middleware-verified OIDC attribution. The
// organization must come from the identity store, never client JSON.
func (a AdminIdentity) HumanIdentity(organization string) (identity.ID, error) {
	if a.AuthMethod != "oidc" {
		return identity.ID{}, errors.New("verified OIDC identity required")
	}
	return identity.NewHuman(organization, a.Issuer, a.Subject)
}

// AuditRef is the adapter for inference ingresses. audit remains independent of
// keystore and server; no raw identity or legacy account reference is appended.
func AuditRef(p keystore.Principal) audit.PrincipalRef {
	ref := audit.PrincipalRef{KeyID: p.KeyID, Team: p.Team}
	if p.Identity != nil && p.Identity.Validate() == nil {
		evidence := p.Identity.Audit()
		ref.Identity = &evidence
	}
	return ref
}

// AdminAuditRef preserves legacy actor encoding when identity is unconfigured.
// Managed OIDC actors are represented by digest-only evidence. Missing verified
// attribution must not turn into raw subject data in a managed audit record.
func AdminAuditRef(a AdminIdentity, organization string) audit.PrincipalRef {
	method := a.AuthMethod
	ref := audit.PrincipalRef{AuthMethod: &method}
	if organization != "" && a.AuthMethod == "oidc" {
		if id, err := a.HumanIdentity(organization); err == nil {
			evidence := id.Audit()
			ref.Identity = &evidence
		}
	} else {
		sub := a.Subject
		ref.User = &sub
	}
	return ref
}
