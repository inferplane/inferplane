package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/identity"
	"github.com/inferplane/inferplane/internal/policy"
)

func TestIdentityCannotValidateInnerAndApplyDifferentOuterPolicies(t *testing.T) {
	id, _ := identity.NewHuman("acme", "https://idp.example", "person")
	cfg := &identity.Config{Organization: "acme", Required: true}
	makeDoc := func(limit int) v1alpha1.GovernancePolicy {
		var doc v1alpha1.GovernancePolicy
		raw := fmt.Sprintf(`{"apiVersion":"inferplane.dev/v1alpha1","kind":"GovernancePolicy","metadata":{"name":"budget"},"spec":{"subject":{"user":%q},"rules":[{"name":"cap","failurePolicy":"FailClosed","budget":{"limitMilliUSD":%d,"hardCap":true}}]}}`, id.CanonicalRef(), limit)
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	protected := []v1alpha1.GovernancePolicy{makeDoc(1)}
	for _, mode := range []string{"empty", "omitted", "weaker"} {
		t.Run(mode, func(t *testing.T) {
			store := policy.NewEmptyStore()
			if err := store.SetIdentityConfig(cfg); err != nil {
				t.Fatal(err)
			}
			if rejected := store.ApplyWire(protected); len(rejected) > 0 {
				t.Fatal(rejected)
			}
			cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				response := policy.SyncResponse{
					IdentityFingerprint: cfg.Fingerprint(), Generation: policy.GenerationOf(protected),
					Authority: &policy.AuthorityResponse{IdentityFingerprint: cfg.Fingerprint(), Policies: protected},
				}
				if mode == "empty" {
					response.IdentityPoliciesComplete = true
					response.Policies = []v1alpha1.GovernancePolicy{}
				} else if mode == "weaker" {
					response.Policies = []v1alpha1.GovernancePolicy{makeDoc(1_000_000)}
				}
				json.NewEncoder(w).Encode(response)
			}))
			defer cp.Close()
			s := &Syncer{URL: cp.URL, Dataplane: "node", Store: store, IdentityConfig: cfg}
			if _, err := s.syncOnce(context.Background()); err == nil {
				t.Fatal("unsolicited authority envelope bypassed policy-set validation")
			}
			limits, ok := store.UserLimits("", id.CanonicalRef())
			if !ok || limits.BudgetMicrosPerMonth != 1000 {
				t.Fatal("outer payload weakened the protected budget")
			}
			if ready, _ := s.GovernanceReady(0); ready {
				t.Fatal("unexpected authority cleared identity quarantine")
			}
		})
	}
}
