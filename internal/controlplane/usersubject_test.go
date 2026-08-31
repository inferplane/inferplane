package controlplane

// Phase 3 (ADR-042): a USER-scoped budget rule must be excluded from the
// lease channel. A ruleLedger is keyed by TEAM and its grant clamps every
// data plane serving that team, so admitting a (team, user) rule would
// throttle the whole team to one individual's cap. These tests exist because
// checkEnforceable no longer rejects user-subject budget rules at load — the
// old `Subject.Team == ""` skip alone is not enough anymore.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/inferplane/inferplane/internal/policy"
)

// userSubjectPolicyYAML pairs a team-only budget rule with a (team, user)
// budget rule on the SAME team. The limits differ by 20x (1,000,000 vs
// 50,000 µUSD) so a ledger row built from the wrong rule — or the user rule
// REPLACING the team rule — is unmistakable in the failure message.
const userSubjectPolicyYAML = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: team-pol }
spec:
  subject: { team: alpha }
  rules:
  - name: team-cap
    failurePolicy: FailClosed
    budget:
      limitMilliUSD: 1000          # $1 = 1,000,000 µUSD
      hardCap: true
---
apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: user-pol }
spec:
  subject: { team: alpha, user: sub-1 }
  rules:
  - name: user-cap
    failurePolicy: FailClosed
    budget:
      limitMilliUSD: 50            # $0.05 = 50,000 µUSD — one PERSON's cap
      hardCap: true
`

func newUserSubjectServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(userSubjectPolicyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer("", dir)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	mux := http.NewServeMux()
	s.Mount(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return s, ts
}

// TestApplyWireSkipsUserSubjectBudgetRule asserts the surviving row's
// limitMicro VALUE, not merely that a row exists: an implementation where
// the user rule replaced the team rule still has "a row for team alpha", but
// its limit would be the individual's 50,000 instead of 1,000,000.
func TestApplyWireSkipsUserSubjectBudgetRule(t *testing.T) {
	s, _ := newUserSubjectServer(t)

	if _, ok := s.ledger[ruleKey{policy: "user-pol", rule: "user-cap"}]; ok {
		t.Fatal("a (team, user) budget rule must not create a ledger row — its grant would clamp the whole team to one person's cap")
	}
	if got := len(s.ledger); got != 1 {
		t.Fatalf("ledger rows = %d, want exactly 1 (the team rule's)", got)
	}
	l, ok := s.ledger[ruleKey{policy: "team-pol", rule: "team-cap"}]
	if !ok {
		t.Fatal("the team-only budget rule's ledger row must still exist")
	}
	if l.limitMicro != 1_000_000 {
		t.Fatalf("team rule limitMicro = %d, want 1000000 — 50000 means the USER rule's cap replaced the team's", l.limitMicro)
	}
	if l.team != "alpha" {
		t.Fatalf("ledger row team = %q, want alpha", l.team)
	}
}

// TestSyncGrantsNoLeaseForUserSubjectRule drives one heartbeat and asserts
// the response carries no grant naming the user rule — and asserts the grant
// COUNT, so an extra grant cannot hide behind a name-only check.
func TestSyncGrantsNoLeaseForUserSubjectRule(t *testing.T) {
	_, ts := newUserSubjectServer(t)

	resp := doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp1", APIVersions: policy.SupportedAPIVersions})
	if got := len(resp.Leases); got != 1 {
		t.Fatalf("leases = %+v, want exactly 1 (the team rule's)", resp.Leases)
	}
	for _, l := range resp.Leases {
		if l.Rule == "user-cap" {
			t.Fatalf("a user-subject budget rule must never be leased: %+v", l)
		}
	}
	if resp.Leases[0].Rule != "team-cap" || resp.Leases[0].Team != "alpha" {
		t.Fatalf("surviving lease = %+v, want the team rule's grant for team alpha", resp.Leases[0])
	}
}
