package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/identity"
	"github.com/inferplane/inferplane/internal/policy"
)

func TestIdentityMismatchGatesUntilMatchingRecovery(t *testing.T) {
	cfg := &identity.Config{Organization: "acme", Required: true}
	var fingerprint atomic.Value
	fingerprint.Store("")
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req policy.SyncRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.IdentityFingerprint != cfg.Fingerprint() {
			t.Error("identity requirement was not advertised")
		}
		json.NewEncoder(w).Encode(policy.SyncResponse{
			IdentityFingerprint: fingerprint.Load().(string), Generation: policy.GenerationOf([]v1alpha1.GovernancePolicy{}),
			IdentityPoliciesComplete: true,
			Policies:                 []v1alpha1.GovernancePolicy{}, SyncIntervalSeconds: 10,
		})
	}))
	defer source.Close()
	s := &Syncer{URL: source.URL, Dataplane: "node", Store: policy.NewEmptyStore(), IdentityConfig: cfg}
	if _, err := s.syncOnce(context.Background()); err == nil {
		t.Fatal("legacy identity-blind response accepted")
	}
	if ok, _ := s.GovernanceReady(0); ok {
		t.Fatal("identity mismatch left generation ready")
	}
	fingerprint.Store(cfg.Fingerprint())
	if _, err := s.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.GovernanceReady(0); !ok {
		t.Fatal("matching recovery did not restore readiness")
	}
	fingerprint.Store("different-declaration")
	if _, err := s.syncOnce(context.Background()); err == nil {
		t.Fatal("changed declaration accepted")
	}
	if ok, _ := s.GovernanceReady(0); ok {
		t.Fatal("last-success time bypassed identity mismatch")
	}
}

func TestUnboundIdentityPolicyCannotReplacePreviousSnapshot(t *testing.T) {
	cfg := &identity.Config{Organization: "acme", Required: true}
	doc := v1alpha1.GovernancePolicy{}
	if err := json.Unmarshal([]byte(`{"apiVersion":"inferplane.dev/v1alpha1","kind":"GovernancePolicy","metadata":{"name":"unbound"},"spec":{"subject":{"user":"unbound-owner"},"rules":[{"name":"access","modelAccess":{"allowed":["model"]}}]}}`), &doc); err != nil {
		t.Fatal(err)
	}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(policy.SyncResponse{
			IdentityFingerprint: cfg.Fingerprint(), Generation: policy.GenerationOf([]v1alpha1.GovernancePolicy{doc}),
			Policies: []v1alpha1.GovernancePolicy{doc},
		})
	}))
	defer source.Close()
	store := policy.NewEmptyStore()
	s := &Syncer{URL: source.URL, Dataplane: "node", Store: store, IdentityConfig: cfg}
	if _, err := s.syncOnce(context.Background()); err == nil {
		t.Fatal("unbound identity policy accepted")
	}
	if len(store.Policies()) != 0 {
		t.Fatal("unbound policy became visible")
	}
	if ok, _ := s.GovernanceReady(0); ok {
		t.Fatal("unbound identity policy left generation ready")
	}
}
