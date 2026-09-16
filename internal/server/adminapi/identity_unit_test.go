package adminapi

import (
	"context"
	"errors"
	"testing"

	"github.com/inferplane/inferplane/internal/identity"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
)

type selectionRegistry struct {
	keystore.Store
	binding identity.Binding
	found   bool
	err     error
	lookups int
}

func (*selectionRegistry) ConfigureIdentity(context.Context, identity.Config) error { return nil }
func (*selectionRegistry) IdentityConfig(context.Context) (identity.Config, error) {
	return identity.Config{Organization: "org", Required: true}, nil
}
func (s *selectionRegistry) IdentityByRef(context.Context, string) (identity.Binding, bool, error) {
	s.lookups++
	return s.binding, s.found, s.err
}
func (s *selectionRegistry) ResolveIdentity(context.Context, identity.ID) (identity.Binding, bool, error) {
	return s.binding, s.found, s.err
}

func TestAdminSelectionRefusesInconsistentOrFailedRegistry(t *testing.T) {
	id, _ := identity.NewHuman("org", "https://issuer.example", "person")
	foreign, _ := identity.NewHuman("other-org", "https://issuer.example", "person")
	for _, tc := range []struct {
		name     string
		registry selectionRegistry
		status   int
	}{
		{"missing", selectionRegistry{}, 400},
		{"failed", selectionRegistry{err: errors.New("sensitive-details")}, 503},
		{"foreign organization", selectionRegistry{found: true, binding: identity.Binding{Identity: foreign, AccountRef: "registered"}}, 503},
		{"different reference", selectionRegistry{found: true, binding: identity.Binding{Identity: id, AccountRef: "different"}}, 503},
		{"invalid identity", selectionRegistry{found: true, binding: identity.Binding{AccountRef: "registered"}}, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := keystore.KeyOptions{}
			h := NewKeysHandler(&tc.registry, nil)
			status := h.applyMintIdentity(context.Background(), identity.Config{Organization: "org", Required: true},
				adminID, keyOptionsBody{AccountRef: "registered"}, &opts)
			if status != tc.status || opts.Identity != nil {
				t.Fatal("inconsistent registry yielded a usable mint identity")
			}
		})
	}
}

func TestAdminSelectionOptionalModePreservesLegacyOwnerWithoutFalseAttribution(t *testing.T) {
	actor := principal.AdminIdentity{Issuer: "https://issuer.example", Subject: "self", AuthMethod: "oidc", IsAdmin: true}
	registry := &selectionRegistry{}
	h := NewKeysHandler(registry, nil)
	opts := keystore.KeyOptions{Owner: "other-legacy-owner"}
	body := keyOptionsBody{Owner: opts.Owner}
	cfg := identity.Config{Organization: "org"}
	if status := h.applyMintIdentity(context.Background(), cfg, actor, body, &opts); status != 0 ||
		opts.Owner != "other-legacy-owner" || opts.Identity != nil {
		t.Fatal("optional admin owner-only path falsely bound its target to the actor")
	}
	actor.IsAdmin = false
	if status := h.applyMintIdentity(context.Background(), cfg, actor, body, &opts); status != 0 ||
		opts.Owner != "self" || opts.Identity == nil || opts.Identity.Subject != "self" {
		t.Fatal("optional member could impersonate another owner")
	}
	opts = keystore.KeyOptions{}
	if status := h.applyMintIdentity(context.Background(), cfg, actor, keyOptionsBody{AccountRef: "self"}, &opts); status != 403 ||
		registry.lookups != 0 {
		t.Fatal("member account selection reached privileged registry lookup")
	}
}
