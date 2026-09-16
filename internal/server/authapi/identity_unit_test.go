package authapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/identity"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/principal"
)

type capturingIdentityStore struct {
	keystore.Store
	cfg     identity.Config
	err     error
	options []keystore.KeyOptions
}

func (s *capturingIdentityStore) ConfigureIdentity(context.Context, identity.Config) error {
	return s.err
}
func (s *capturingIdentityStore) IdentityConfig(context.Context) (identity.Config, error) {
	return s.cfg, s.err
}
func (s *capturingIdentityStore) ResolveIdentity(context.Context, identity.ID) (identity.Binding, bool, error) {
	return identity.Binding{}, false, s.err
}
func (s *capturingIdentityStore) IdentityByRef(context.Context, string) (identity.Binding, bool, error) {
	return identity.Binding{}, false, s.err
}
func (s *capturingIdentityStore) CreateWithOptions(_ context.Context, team string, models []string, opts keystore.KeyOptions) (string, keystore.Principal, error) {
	s.options = append(s.options, opts)
	return "synthetic-key", keystore.Principal{KeyID: "key", Team: team, AllowedModels: models, KeyOptions: opts}, nil
}

func TestMintBoundaryLeavesRequiredOwnerToStoreAndNeverTrustsJSON(t *testing.T) {
	for _, required := range []bool{false, true} {
		s := &capturingIdentityStore{cfg: identity.Config{Organization: "configured-org", Required: required}}
		var events []audit.Record
		h := MintHandler(s, time.Hour, limiter.NewMemory(), func(r audit.Record) { events = append(events, r) })
		actor := principal.AdminIdentity{Issuer: "https://verified.example", Subject: "verified-subject", Teams: []string{"alpha"}, AuthMethod: "oidc"}
		req := httptest.NewRequest("POST", "/v1/auth/key", strings.NewReader(`{"organization":"attacker","issuer":"https://attacker.example","subject":"victim","owner":"victim","identity":{"kind":"service"}}`))
		req = req.WithContext(principal.WithAdmin(req.Context(), actor))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 || len(s.options) != 1 {
			t.Fatal("verified mint did not reach the store")
		}
		opts := s.options[0]
		if opts.Identity == nil || opts.Identity.Organization != "configured-org" || opts.Identity.Issuer != actor.Issuer || opts.Identity.Subject != actor.Subject {
			t.Fatal("request JSON controlled typed identity")
		}
		if required && opts.Owner != "" || !required && opts.Owner != actor.Subject {
			t.Fatal("required owner was not delegated, or optional legacy owner changed")
		}
		raw, _ := json.Marshal(events)
		if len(events) != 1 || events[0].Principal.Identity == nil || strings.Contains(string(raw), actor.Issuer) || strings.Contains(string(raw), actor.Subject) {
			t.Fatal("mint audit lost digest-only attribution")
		}
	}
}

func TestMintBoundaryRegistryFailureCannotFallBackToLegacy(t *testing.T) {
	s := &capturingIdentityStore{err: errors.New("sensitive-error")}
	actor := principal.AdminIdentity{Issuer: "https://verified.example", Subject: "person", Teams: []string{"alpha"}, AuthMethod: "oidc"}
	req := httptest.NewRequest("POST", "/v1/auth/key", strings.NewReader(`{}`))
	req = req.WithContext(principal.WithAdmin(req.Context(), actor))
	rec := httptest.NewRecorder()
	MintHandler(s, time.Hour, limiter.NewMemory(), nil).ServeHTTP(rec, req)
	if rec.Code != 503 || len(s.options) != 0 || strings.Contains(rec.Body.String(), "sensitive-error") {
		t.Fatal("failed identity mode lookup downgraded issuance or leaked details")
	}
}
