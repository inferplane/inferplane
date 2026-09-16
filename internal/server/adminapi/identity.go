package adminapi

import (
	"context"
	"net/http"

	"github.com/inferplane/inferplane/internal/identity"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
)

// applyMintIdentity never accepts a human tuple from JSON. Full administrators
// can select a registered account or create a service identity; OIDC members
// can only enroll their own verified identity.
func (h *KeysHandler) applyMintIdentity(ctx context.Context, cfg identity.Config, actor principal.AdminIdentity, body keyOptionsBody, opts *keystore.KeyOptions) int {
	if body.AccountRef != "" && body.ServiceAccount != "" {
		return http.StatusBadRequest
	}
	if (body.AccountRef != "" || body.ServiceAccount != "") && !actor.IsAdmin {
		return http.StatusForbidden
	}
	if cfg.Organization == "" {
		if body.AccountRef != "" || body.ServiceAccount != "" {
			return http.StatusBadRequest
		}
		if actor.AuthMethod == "oidc" && !actor.IsAdmin {
			opts.Owner = actor.Subject
		}
		return 0
	}
	if cfg.Required && body.Owner != "" {
		return http.StatusBadRequest // use explicit account_ref, never owner-only attribution
	}
	if body.AccountRef != "" {
		store, ok := h.store.(keystore.IdentityStore)
		if !ok {
			return http.StatusServiceUnavailable
		}
		binding, found, err := store.IdentityByRef(ctx, body.AccountRef)
		if err != nil {
			return http.StatusServiceUnavailable
		}
		if !found {
			return http.StatusBadRequest
		}
		if binding.Validate() != nil || binding.Identity.Organization != cfg.Organization || binding.AccountRef != body.AccountRef {
			return http.StatusServiceUnavailable
		}
		opts.Identity = &binding.Identity
		if cfg.Required {
			opts.Owner = ""
		} else {
			opts.Owner = binding.AccountRef // explicit selection, not a silent migration
		}
		return 0
	}
	if body.ServiceAccount != "" {
		service, err := identity.NewService(cfg.Organization, body.ServiceAccount)
		if err != nil {
			return http.StatusBadRequest
		}
		opts.Identity = &service
		return 0
	}
	if actor.AuthMethod == "oidc" {
		// Optional mode still permits the existing full-admin owner-only
		// path, but it must not attribute that other owner to this human.
		if !cfg.Required && actor.IsAdmin && opts.Owner != "" && opts.Owner != actor.Subject {
			return 0
		}
		human, err := actor.HumanIdentity(cfg.Organization)
		if err != nil {
			return http.StatusForbidden
		}
		opts.Identity = &human
		if cfg.Required {
			opts.Owner = ""
		} else if !actor.IsAdmin {
			opts.Owner = actor.Subject
		}
		return 0
	}
	if cfg.Required {
		return http.StatusBadRequest
	}
	return 0
}
