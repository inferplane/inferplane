package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/controlplane"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/tier"
)

// The distributed set carries one enforceable policy and one this data-plane
// build must reject (a routing rule) — the rejection must be reported on the
// NEXT heartbeat, never dropped.
const syncPolicyYAML = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: team-a }
spec:
  subject: { team: alpha }
  rules:
  - name: cap
    failurePolicy: FailClosed
    budget:
      limitMilliUSD: 100
      hardCap: true
      lease: { grantMilliUSD: 10, renewInterval: "5s" }
---
apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: team-b-pin }
spec:
  subject: { team: beta }
  rules:
  - name: pin
    failurePolicy: FailOpen
    routing: { onAffinityConflict: PreferAffinity }
`

// syncTierPolicyYAML adds an ADR-041 budgetTiers routing rule on top of the
// "cap" budget rule.
const syncTierPolicyYAML = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: team-a }
spec:
  subject: { team: alpha }
  rules:
  - name: cap
    failurePolicy: FailClosed
    budget:
      limitMilliUSD: 100
      hardCap: true
      lease: { grantMilliUSD: 10, renewInterval: "5s" }
  - name: downgrade-at-80
    failurePolicy: FailOpen
    routing:
      budgetTiers:
        budgetRef: cap
        tiers:
        - thresholdPercent: 80
          substitute: { claude-haiku-4-5: glm-4.7-gpu }
`

func newControlPlaneWithTiers(t *testing.T, token string) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(syncTierPolicyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cp, err := controlplane.NewServer(token, dir)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	cp.Mount(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func newControlPlane(t *testing.T, token string) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(syncPolicyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cp, err := controlplane.NewServer(token, dir)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	cp.Mount(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestSyncerAppliesPoliciesLeasesAndReportsRejections(t *testing.T) {
	ts := newControlPlane(t, "tok")
	store := policy.NewEmptyStore()
	leases := NewLeaseTable()
	spent := int64(0)
	s := &Syncer{
		URL: ts.URL, Token: "tok", Dataplane: "dp1",
		Store: store, Leases: leases,
		SpentOf: func(team string) int64 { return spent },
	}

	next, err := s.syncOnce(context.Background())
	if err != nil {
		t.Fatalf("syncOnce: %v", err)
	}
	if next != 5*time.Second {
		t.Fatalf("next interval = %s, want 5s (control-plane cadence)", next)
	}
	// The enforceable policy applied; the routing one was rejected.
	if tl, ok := store.TeamLimits("alpha"); !ok || tl.BudgetMicrosPerMonth != 100_000 || !tl.BudgetHard {
		t.Fatalf("distributed budget not applied: %+v", tl)
	}
	if len(s.pending) != 1 || s.pending[0].Policy != "team-b-pin" {
		t.Fatalf("rejection not recorded: %+v", s.pending)
	}
	// Lease landed: fresh dp, zero spend → allowance = one grant.
	l, ok := leases.Get("alpha")
	if !ok || l.AllowanceMicroUSD != 10_000 || !l.HardCap {
		t.Fatalf("lease not applied: %+v", l)
	}
	if blocked, _ := leases.Blocked("alpha"); blocked {
		t.Fatal("valid lease with allowance must not block")
	}

	// Second heartbeat: reports spend, delivers the pending rejection, and
	// the control plane raises the allowance to spent+grant.
	spent = 60_000
	if _, err := s.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.pending) != 0 {
		t.Fatalf("rejections not cleared after delivery: %+v", s.pending)
	}
	l, _ = leases.Get("alpha")
	if l.AllowanceMicroUSD != 70_000 {
		t.Fatalf("allowance after report = %d, want 70000", l.AllowanceMicroUSD)
	}
}

func TestLeaseTableGate(t *testing.T) {
	lt := NewLeaseTable()

	// Hard cap, expired → blocked.
	lt.set([]policy.LeaseGrant{{Team: "a", AllowanceMicroUSD: 10, ExpiresAt: time.Now().Add(-time.Second), HardCap: true}})
	if blocked, _ := lt.Blocked("a"); !blocked {
		t.Fatal("expired hard-cap lease must block")
	}
	// Hard cap, zero allowance → blocked (global budget exhausted).
	lt.set([]policy.LeaseGrant{{Team: "a", AllowanceMicroUSD: 0, ExpiresAt: time.Now().Add(time.Minute), HardCap: true}})
	if blocked, _ := lt.Blocked("a"); !blocked {
		t.Fatal("zero-allowance hard-cap lease must block")
	}
	// Soft lease: never blocks, even expired (fails open per rule).
	lt.set([]policy.LeaseGrant{{Team: "a", AllowanceMicroUSD: 0, ExpiresAt: time.Now().Add(-time.Second), HardCap: false}})
	if blocked, _ := lt.Blocked("a"); blocked {
		t.Fatal("soft lease must fail open")
	}
	// No lease at all: not blocked.
	if blocked, _ := lt.Blocked("unknown"); blocked {
		t.Fatal("lease-less team must not block")
	}

	// Per-team merge is most-restrictive.
	lt.set([]policy.LeaseGrant{
		{Team: "m", AllowanceMicroUSD: 500, ExpiresAt: time.Now().Add(time.Hour), HardCap: false},
		{Team: "m", AllowanceMicroUSD: 200, ExpiresAt: time.Now().Add(time.Minute), HardCap: true},
	})
	l, _ := lt.Get("m")
	if l.AllowanceMicroUSD != 200 || !l.HardCap {
		t.Fatalf("merge not most-restrictive: %+v", l)
	}
}

// A dead control plane must not clear the last-applied policy set or leases —
// expiry is what degrades them, per rule.
func TestSyncerOutageKeepsLastState(t *testing.T) {
	ts := newControlPlane(t, "")
	store := policy.NewEmptyStore()
	leases := NewLeaseTable()
	s := &Syncer{URL: ts.URL, Dataplane: "dp1", Store: store, Leases: leases, SpentOf: func(string) int64 { return 0 }}
	if _, err := s.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	ts.Close()
	if _, err := s.syncOnce(context.Background()); err == nil {
		t.Fatal("sync against a dead control plane must error")
	}
	if _, ok := store.TeamLimits("alpha"); !ok {
		t.Fatal("policy set lost on outage")
	}
	if _, ok := leases.Get("alpha"); !ok {
		t.Fatal("leases lost on outage")
	}
}

// The syncer applies the control plane's ActiveTiers into the Tiers table
// (ADR-041), the same way it applies Leases.
func TestSyncerAppliesActiveTiers(t *testing.T) {
	ts := newControlPlaneWithTiers(t, "")
	store := policy.NewEmptyStore()
	leases := NewLeaseTable()
	tiers := tier.NewTable()
	s := &Syncer{
		URL: ts.URL, Dataplane: "dp1",
		Store: store, Leases: leases, Tiers: tiers,
		SpentOf: func(string) int64 { return 90_000 }, // 90% of the 100,000 µUSD limit
	}
	// First heartbeat applies the policy set (the report loop below reads
	// s.Store.Policies(), which is still empty before this); the second
	// heartbeat's report then carries the 90% spend to the ledger.
	if _, err := s.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := tiers.Get("alpha")
	if got["claude-haiku-4-5"] != "glm-4.7-gpu" {
		t.Fatalf("tier not applied: %v", got)
	}
}

// A dead control plane must not clear the last-applied tier state either —
// same keep-last-state posture as leases and policies.
func TestSyncerOutageKeepsLastTierState(t *testing.T) {
	ts := newControlPlaneWithTiers(t, "")
	store := policy.NewEmptyStore()
	leases := NewLeaseTable()
	tiers := tier.NewTable()
	s := &Syncer{
		URL: ts.URL, Dataplane: "dp1",
		Store: store, Leases: leases, Tiers: tiers,
		SpentOf: func(string) int64 { return 90_000 },
	}
	if _, err := s.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tiers.Get("alpha")["claude-haiku-4-5"] != "glm-4.7-gpu" {
		t.Fatal("tier not applied before outage")
	}
	ts.Close()
	if _, err := s.syncOnce(context.Background()); err == nil {
		t.Fatal("sync against a dead control plane must error")
	}
	if tiers.Get("alpha")["claude-haiku-4-5"] != "glm-4.7-gpu" {
		t.Fatal("tier state lost on outage")
	}
}
