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

func managedMintStore(t *testing.T, required bool) *keystore.SQLiteStore {
	t.Helper()
	s := newTestStore(t)
	registry, ok := any(s).(keystore.IdentityStore)
	if !ok {
		t.Fatal("SQLite identity-store implementation is required")
	}
	if err := registry.ConfigureIdentity(context.Background(), identity.Config{Organization: "org", Required: required}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestManagedMintUsesVerifiedTupleAcrossRotationAndIssuers(t *testing.T) {
	for _, required := range []bool{false, true} {
		t.Run(map[bool]string{false: "optional", true: "required"}[required], func(t *testing.T) {
			s := managedMintStore(t, required)
			var records []audit.Record
			h := MintHandler(s, time.Hour, limiter.NewMemory(), func(r audit.Record) { records = append(records, r) })
			var previous keystore.Principal
			for i, issuer := range []string{"https://a.example", "https://a.example", "https://b.example"} {
				actor := principal.AdminIdentity{Issuer: issuer, Subject: "same-subject", Teams: []string{"alpha"}, AuthMethod: "oidc"}
				req := httptest.NewRequest("POST", "/v1/auth/key", strings.NewReader(`{"issuer":"https://forged.example","subject":"victim","owner":"forged","identity":{"issuer":"https://forged.example"}}`))
				req = req.WithContext(principal.WithAdmin(req.Context(), actor))
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != 200 {
					t.Fatalf("mint status=%d", rec.Code)
				}
				var out map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				p, err := s.Resolve(context.Background(), out["key"])
				if err != nil || p.Identity == nil || p.Identity.Issuer != issuer || p.Identity.Subject != actor.Subject || p.Identity.Organization != "org" {
					t.Fatal("mint did not retain only the verified tuple")
				}
				if required && p.Owner != p.Identity.CanonicalRef() || !required && p.Owner != actor.Subject {
					t.Fatal("managed accounting reference or legacy optional owner changed")
				}
				if i == 1 && (p.KeyID == previous.KeyID || p.Identity.CanonicalRef() != previous.Identity.CanonicalRef()) {
					t.Fatal("rotation split the person or reused the credential")
				}
				if i == 2 && p.Identity.CanonicalRef() == previous.Identity.CanonicalRef() {
					t.Fatal("equal subjects across issuers collided")
				}
				previous = p
			}
			for _, r := range records {
				raw, _ := r.Canonical()
				if r.Principal.Identity == nil || r.Principal.User != nil ||
					strings.Contains(string(raw), "https://") || strings.Contains(string(raw), "same-subject") {
					t.Fatal("managed mint audit must contain digest-only identity")
				}
			}
		})
	}
}

func TestManagedMintThrottleIsIssuerQualified(t *testing.T) {
	s := managedMintStore(t, true)
	h := MintHandler(s, time.Hour, limiter.NewMemory(), nil)
	for i := 0; i < 12; i++ {
		issuer, want := "https://a.example", 200
		if i == 10 {
			want = 429
		}
		if i == 11 {
			issuer = "https://b.example"
		}
		req := httptest.NewRequest("POST", "/v1/auth/key", strings.NewReader(`{}`))
		req = req.WithContext(principal.WithAdmin(req.Context(), principal.AdminIdentity{Issuer: issuer, Subject: "same", Teams: []string{"alpha"}, AuthMethod: "oidc"}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("mint %d status=%d want=%d", i, rec.Code, want)
		}
	}
}

type unavailableIdentityStore struct{ *keystore.SQLiteStore }

func (unavailableIdentityStore) IdentityConfig(context.Context) (identity.Config, error) {
	return identity.Config{}, errors.New("private-store-error")
}

func TestManagedMintFailsClosedOnMissingTrustOrRegistryFailure(t *testing.T) {
	for _, scenario := range []string{"missing issuer", "non-oidc", "store failure"} {
		t.Run(scenario, func(t *testing.T) {
			s := managedMintStore(t, true)
			var store keystore.Store = s
			id := principal.AdminIdentity{Issuer: "https://a.example", Subject: "person", Teams: []string{"alpha"}, AuthMethod: "oidc"}
			if scenario == "missing issuer" {
				id.Issuer = ""
			}
			if scenario == "non-oidc" {
				id.AuthMethod = "break_glass"
			}
			if scenario == "store failure" {
				store = unavailableIdentityStore{s}
			}
			req := httptest.NewRequest("POST", "/v1/auth/key", strings.NewReader(`{"issuer":"https://forged.example"}`))
			req = req.WithContext(principal.WithAdmin(req.Context(), id))
			rec := httptest.NewRecorder()
			MintHandler(store, time.Hour, limiter.NewMemory(), nil).ServeHTTP(rec, req)
			keys, _ := s.List(context.Background())
			if rec.Code < 400 || len(keys) != 0 || strings.Contains(rec.Body.String(), "private-store-error") {
				t.Fatal("untrusted or failed identity lookup permitted mint or leaked details")
			}
		})
	}
}

func TestManagedSelfRevokeAuditsKeyAuthenticationAndDigestIdentity(t *testing.T) {
	s := managedMintStore(t, true)
	service, err := identity.NewService("org", "build-bot")
	if err != nil {
		t.Fatal(err)
	}
	_, p, err := s.CreateWithOptions(context.Background(), "alpha", []string{"*"}, keystore.KeyOptions{Identity: &service})
	if err != nil {
		t.Fatal(err)
	}
	var events []audit.Record
	req := httptest.NewRequest("DELETE", "/v1/auth/key", nil)
	req = req.WithContext(principal.With(req.Context(), p))
	rec := httptest.NewRecorder()
	RevokeHandler(s, func(r audit.Record) { events = append(events, r) }).ServeHTTP(rec, req)
	if rec.Code != 204 || len(events) != 1 || events[0].Principal.Identity == nil || events[0].Principal.User != nil {
		t.Fatal("managed self-revoke lost digest-only identity")
	}
	if events[0].Principal.AuthMethod == nil || *events[0].Principal.AuthMethod != "virtual_key" {
		t.Fatal("service self-revoke must not falsely claim OIDC authentication")
	}
}
