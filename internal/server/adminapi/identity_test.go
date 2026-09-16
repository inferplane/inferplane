package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/identity"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
)

func configureAdminIdentity(t *testing.T, s *keystore.SQLiteStore, cfg identity.Config) {
	t.Helper()
	registry, ok := any(s).(keystore.IdentityStore)
	if !ok {
		t.Fatal("SQLite identity-store implementation is required")
	}
	if err := registry.ConfigureIdentity(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
}

func TestManagedAdminIssuanceTrustAndRegisteredReferences(t *testing.T) {
	person, err := identity.NewHuman("org", "https://issuer.example", "victim")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		actor      principal.AdminIdentity
		status     int
	}{
		{"owner only", `{"team":"alpha","owner":"legacy-victim"}`, adminID, 400},
		{"missing identity", `{"team":"alpha"}`, adminID, 400},
		{"unknown reference", `{"team":"alpha","account_ref":"missing"}`, adminID, 400},
		{"registered reference", `{"team":"alpha","account_ref":"legacy-victim"}`, adminID, 200},
		{"member reference impersonation", `{"team":"alpha","account_ref":"legacy-victim"}`, principal.AdminIdentity{Issuer: "https://issuer.example", Subject: "member", Teams: []string{"alpha"}, AuthMethod: "oidc"}, 403},
		{"member service impersonation", `{"team":"alpha","service_account":"bot"}`, principal.AdminIdentity{Issuer: "https://issuer.example", Subject: "member", Teams: []string{"alpha"}, AuthMethod: "oidc"}, 403},
		{"member own verified identity", `{"team":"alpha","issuer":"https://forged.example","identity":{"kind":"service","subject":"victim","issuer":"inferplane://org/service-account"}}`, principal.AdminIdentity{Issuer: "https://issuer.example", Subject: "member", Teams: []string{"alpha"}, AuthMethod: "oidc"}, 200},
		{"member missing issuer", `{"team":"alpha","issuer":"https://issuer.example"}`, memberID, 403},
		{"service", `{"team":"alpha","service_account":"build-bot"}`, adminID, 200},
		{"ambiguous selectors", `{"team":"alpha","service_account":"bot","account_ref":"legacy-victim"}`, adminID, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			cfg := identity.Config{Organization: "org", Required: true, Bindings: []identity.Binding{{Identity: person, AccountRef: "legacy-victim"}}}
			configureAdminIdentity(t, s, cfg)
			rec := doAs(t, NewKeysHandler(s, nil), &tc.actor, "POST", "/admin/keys", tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status=%d want=%d", rec.Code, tc.status)
			}
			keys, err := s.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if tc.status != 200 {
				if len(keys) != 0 {
					t.Fatal("refused issuance persisted a key")
				}
				return
			}
			if len(keys) != 1 || keys[0].Identity == nil {
				t.Fatal("managed issuance omitted identity")
			}
			p := keys[0]
			switch tc.name {
			case "registered reference":
				if *p.Identity != person || p.Owner != "legacy-victim" {
					t.Fatal("registered reference did not retain immutable identity/account")
				}
			case "member own verified identity":
				if p.Identity.Kind != identity.Human || p.Identity.Issuer != tc.actor.Issuer || p.Identity.Subject != tc.actor.Subject {
					t.Fatal("member controlled the identity tuple")
				}
			case "service":
				want, _ := identity.NewService("org", "build-bot")
				if *p.Identity != want {
					t.Fatal("service issuer was not derived from organization")
				}
			}
			var dto map[string]json.RawMessage
			_ = json.Unmarshal(rec.Body.Bytes(), &dto)
			if len(dto["identity"]) == 0 || strings.Contains(string(dto["identity"]), p.Identity.Issuer) ||
				strings.Contains(string(dto["identity"]), `"subject":`) {
				t.Fatal("new identity DTO must be present and digest-only")
			}
		})
	}
}

type failedIdentityStore struct{ *keystore.SQLiteStore }

func (failedIdentityStore) IdentityConfig(context.Context) (identity.Config, error) {
	return identity.Config{}, errors.New("sensitive-registry-error")
}

func TestAdminIdentityReadFailureRefusesBeforeMutation(t *testing.T) {
	s := newTestStore(t)
	rec := doAs(t, NewKeysHandler(failedIdentityStore{s}, nil), &adminID, "POST", "/admin/keys", `{"team":"alpha"}`)
	keys, _ := s.List(context.Background())
	if rec.Code != 503 || len(keys) != 0 || strings.Contains(rec.Body.String(), "sensitive-registry-error") {
		t.Fatal("identity configuration failure was not closed/sanitized")
	}
}

func TestManagedServiceKeysRetainIdentityAcrossRotation(t *testing.T) {
	s := newTestStore(t)
	configureAdminIdentity(t, s, identity.Config{Organization: "org", Required: true})
	h := NewKeysHandler(s, nil)
	for i := 0; i < 2; i++ {
		if rec := doAs(t, h, &adminID, "POST", "/admin/keys", `{"team":"alpha","service_account":"build-bot"}`); rec.Code != 200 {
			t.Fatalf("service mint=%d", rec.Code)
		}
	}
	keys, _ := s.List(context.Background())
	if len(keys) != 2 || keys[0].KeyID == keys[1].KeyID || keys[0].Owner != keys[1].Owner || *keys[0].Identity != *keys[1].Identity {
		t.Fatal("rotating service keys did not retain one stable identity/account")
	}
}
