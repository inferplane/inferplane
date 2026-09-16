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

func TestIdentityRejectedPolicyCannotReopenOnUnchangedHeartbeat(t *testing.T) {
	id, _ := identity.NewHuman("acme", "https://idp.example", "person")
	cfg := &identity.Config{Organization: "acme", Required: true}
	var bad v1alpha1.GovernancePolicy
	ref, _ := json.Marshal(id.CanonicalRef())
	raw := `{"apiVersion":"inferplane.dev/v1alpha1","kind":"GovernancePolicy","metadata":{"name":"personal-budget"},"spec":{"subject":{"user":` + string(ref) + `},"rules":[{"name":"money","failurePolicy":"FailClosed","budget":{"limitMilliUSD":1,"hardCap":true}},{"name":"unsupported-user-rate","failurePolicy":"FailClosed","rate":{"rpm":1}}]}}`
	if err := json.Unmarshal([]byte(raw), &bad); err != nil {
		t.Fatal(err)
	}
	correct := bad
	correct.Spec.Rules = correct.Spec.Rules[:1]
	var recovered atomic.Bool
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request policy.SyncRequest
		json.NewDecoder(r.Body).Decode(&request)
		doc := bad
		if recovered.Load() {
			doc = correct
		}
		docs := []v1alpha1.GovernancePolicy{doc}
		generation := policy.GenerationOf(docs)
		response := policy.SyncResponse{IdentityFingerprint: cfg.Fingerprint(), Generation: generation}
		if request.Generation != generation {
			response.Policies = docs
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer cp.Close()
	store := policy.NewEmptyStore() // User rates require shared mode; this is local.
	if err := store.SetIdentityConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if rejected := store.ApplyWire([]v1alpha1.GovernancePolicy{correct}); len(rejected) != 0 {
		t.Fatal(rejected)
	}
	s := &Syncer{URL: cp.URL, Dataplane: "node", Store: store, IdentityConfig: cfg}
	if _, err := s.syncOnce(context.Background()); err == nil {
		t.Fatal("unsupported policy was not rejected")
	}
	if len(store.Policies()) != 1 {
		t.Fatal("rejected required bundle discarded the previous valid budget")
	}
	if _, err := s.syncOnce(context.Background()); err == nil {
		t.Fatal("rejected user budget reopened on a body-less unchanged heartbeat")
	}
	if ok, _ := s.GovernanceReady(0); ok {
		t.Fatal("rejected policy allowed governed requests")
	}
	recovered.Store(true)
	if _, err := s.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.GovernanceReady(0); !ok || len(store.Policies()) != 1 {
		t.Fatal("corrected complete policy did not restore protection")
	}
}
