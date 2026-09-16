package policy

import (
	"encoding/json"
	"testing"
	"time"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/identity"
)

func identityPolicy(t *testing.T, ref string) []v1alpha1.GovernancePolicy {
	t.Helper()
	var doc v1alpha1.GovernancePolicy
	raw := `{"apiVersion":"inferplane.dev/v1alpha1","kind":"GovernancePolicy","metadata":{"name":"identity-budget"},"spec":{"subject":{"user":` + string(mustIdentityJSON(t, ref)) + `},"rules":[{"name":"month","failurePolicy":"FailClosed","budget":{"limitMilliUSD":1000,"hardCap":true}}]}}`
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	return []v1alpha1.GovernancePolicy{doc}
}

func mustIdentityJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRequiredIdentityPolicyReferences(t *testing.T) {
	id, err := identity.NewHuman("acme", "https://idp.example", "subject")
	if err != nil {
		t.Fatal(err)
	}
	cfg := identity.Config{Organization: "acme", Required: true, Bindings: []identity.Binding{{Identity: id, AccountRef: "legacy-sub"}}}
	fresh, _ := identity.NewHuman("acme", "https://idp.example", "new-person")
	for _, tc := range []struct {
		ref string
		ok  bool
	}{{"legacy-sub", true}, {id.CanonicalRef(), false}, {fresh.CanonicalRef(), true}, {"unregistered-owner", false}, {"identity-v1:invalid", false}} {
		t.Run(tc.ref, func(t *testing.T) {
			err := ValidateIdentitySubjects(identityPolicy(t, tc.ref), &cfg)
			if (err == nil) != tc.ok {
				t.Fatalf("identity reference accepted=%v, want %v", err == nil, tc.ok)
			}
		})
	}
}

func TestIdentityBindingPreservesBudgetAndWindowKeys(t *testing.T) {
	id, _ := identity.NewHuman("acme", "https://idp.example", "subject")
	cfg := identity.Config{Organization: "acme", Required: true, Bindings: []identity.Binding{{Identity: id, AccountRef: "legacy-sub"}}}
	docs := identityPolicy(t, "legacy-sub")
	now := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	before, err := AuthorityBudgets(docs, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateIdentitySubjects(docs, &cfg); err != nil {
		t.Fatal(err)
	}
	after, err := AuthorityBudgets(docs, now)
	if err != nil {
		t.Fatal(err)
	}
	if string(mustIdentityJSON(t, before)) != string(mustIdentityJSON(t, after)) {
		t.Fatal("identity binding changed existing account or window definitions")
	}
}
