package principal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/identity"
	"github.com/inferplane/inferplane/internal/keystore"
)

func TestHumanIdentityRequiresVerifiedIssuerAndOIDC(t *testing.T) {
	a := AdminIdentity{Issuer: "https://issuer.example", Subject: "opaque-person", AuthMethod: "oidc"}
	got, err := a.HumanIdentity("org")
	if err != nil || got.Organization != "org" || got.Issuer != a.Issuer || got.Subject != a.Subject {
		t.Fatal("verified human tuple was not retained")
	}
	for _, bad := range []AdminIdentity{
		{Subject: a.Subject, AuthMethod: "oidc"},
		{Issuer: a.Issuer, Subject: a.Subject, AuthMethod: "break_glass"},
		{Issuer: "http://issuer.example", Subject: a.Subject, AuthMethod: "oidc"},
	} {
		if _, err := bad.HumanIdentity("org"); err == nil {
			t.Fatal("unverified human identity accepted")
		}
	}
}

func TestAuditRefUsesDigestOnlyIdentityAndPreservesLegacyShape(t *testing.T) {
	id, err := identity.NewHuman("org", "https://issuer.example", "opaque-person")
	if err != nil {
		t.Fatal(err)
	}
	ref := AuditRef(keystore.Principal{KeyID: "key", Team: "team", KeyOptions: keystore.KeyOptions{Owner: "legacy-person", Identity: &id}})
	raw, _ := json.Marshal(ref)
	if ref.Identity == nil || !strings.Contains(string(raw), id.CanonicalRef()) ||
		strings.Contains(string(raw), id.Issuer) || strings.Contains(string(raw), id.Subject) || strings.Contains(string(raw), "legacy-person") {
		t.Fatal("audit identity missing or raw attribution exposed")
	}
	legacy, _ := json.Marshal(AuditRef(keystore.Principal{KeyID: "key", Team: "team"}))
	if string(legacy) != `{"key_id":"key","team":"team"}` {
		t.Fatalf("legacy data-plane audit changed: %s", legacy)
	}
	a := AdminIdentity{Issuer: id.Issuer, Subject: id.Subject, AuthMethod: "oidc"}
	managed := AdminAuditRef(a, "org")
	if managed.Identity == nil || managed.User != nil {
		t.Fatal("managed actor audit must use digests only")
	}
	old := AdminAuditRef(a, "")
	if old.User == nil || *old.User != a.Subject || old.Identity != nil {
		t.Fatal("legacy actor behavior changed")
	}
}

type configStore struct {
	keystore.Store
	cfg identity.Config
	err error
}

func (s configStore) ConfigureIdentity(context.Context, identity.Config) error { return s.err }
func (s configStore) IdentityConfig(context.Context) (identity.Config, error)  { return s.cfg, s.err }
func (s configStore) ResolveIdentity(context.Context, identity.ID) (identity.Binding, bool, error) {
	return identity.Binding{}, false, s.err
}
func (s configStore) IdentityByRef(context.Context, string) (identity.Binding, bool, error) {
	return identity.Binding{}, false, s.err
}

func TestIdentityConfigReadFailsClosedAndKeepsZeroMode(t *testing.T) {
	if cfg, err := IdentityConfig(context.Background(), configStore{}); err != nil || cfg.Organization != "" {
		t.Fatal("zero identity config changed")
	}
	for _, s := range []configStore{
		{err: errors.New("sensitive-store-detail")},
		{cfg: identity.Config{Required: true}},
	} {
		if _, err := IdentityConfig(context.Background(), s); err == nil || strings.Contains(err.Error(), "sensitive-store-detail") {
			t.Fatal("invalid/unavailable identity config must fail closed without store details")
		}
	}
}
