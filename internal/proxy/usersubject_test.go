package proxy

// Phase 3 (ADR-042): a USER-scoped budget rule must be excluded from the
// consumption report. Such a rule has no ledger row upstream (see
// internal/controlplane), and SpentOf answers with the TEAM's cumulative
// spend — reporting that against one person's rule would be reporting the
// wrong quantity to a row that does not exist. These tests exist because
// checkEnforceable no longer rejects user-subject budget rules at load.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/policy"
)

// userSubjectSyncYAML pairs a team-only budget rule with a (team, user)
// budget rule on the same team, so the report loop has one rule it must emit
// and one it must skip.
const userSubjectSyncYAML = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: team-pol }
spec:
  subject: { team: alpha }
  rules:
  - name: team-cap
    failurePolicy: FailClosed
    budget:
      limitMilliUSD: 1000
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
      limitMilliUSD: 50
      hardCap: true
`

// newUserSubjectStore loads userSubjectSyncYAML through the same path the
// data plane uses, so the Subject under test is the one FromV1Alpha1
// actually produces.
func newUserSubjectStore(t *testing.T) *policy.Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(userSubjectSyncYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	wire, _, err := policy.LoadWirePaths(dir)
	if err != nil {
		t.Fatalf("LoadWirePaths: %v", err)
	}
	store := policy.NewEmptyStore()
	if rej := store.ApplyWire(wire); len(rej) != 0 {
		t.Fatalf("policy set rejected: %+v", rej)
	}
	return store
}

// TestSyncOnceOmitsUserSubjectRuleFromReports asserts the report LENGTH, not
// just the user rule's absence: an absence-only assertion also passes if the
// loop stopped emitting everything, which is not a filter but a break.
func TestSyncOnceOmitsUserSubjectRuleFromReports(t *testing.T) {
	cp := newRecordingCP(t)
	store := newUserSubjectStore(t)
	s := &Syncer{
		URL: cp.srv.URL, Dataplane: "dp1", Store: store, Leases: NewLeaseTable(),
		SpentOf: func(team string, period v1alpha1.BudgetPeriod) int64 { return 0 },
	}
	if _, err := s.syncOnce(context.Background()); err != nil {
		t.Fatalf("syncOnce: %v", err)
	}
	if got := len(cp.last.Reports); got != 1 {
		t.Fatalf("reports = %+v, want exactly 1 (the team rule's)", cp.last.Reports)
	}
	if got := cp.last.Reports[0].Rule; got != "team-cap" {
		t.Fatalf("surviving report names rule %q, want team-cap", got)
	}
}

// TestSyncOnceStillReportsTeamRuleSpend is the control for the test above:
// with SpentOf returning a distinctive value, the team rule's report carries
// it — proving the user-subject exclusion is a filter, not a broken reporter.
func TestSyncOnceStillReportsTeamRuleSpend(t *testing.T) {
	cp := newRecordingCP(t)
	store := newUserSubjectStore(t)
	s := &Syncer{
		URL: cp.srv.URL, Dataplane: "dp1", Store: store, Leases: NewLeaseTable(),
		SpentOf: func(team string, period v1alpha1.BudgetPeriod) int64 {
			if team != "alpha" {
				t.Errorf("SpentOf team = %q, want alpha", team)
			}
			return 777_777
		},
	}
	if _, err := s.syncOnce(context.Background()); err != nil {
		t.Fatalf("syncOnce: %v", err)
	}
	if got := len(cp.last.Reports); got != 1 {
		t.Fatalf("reports = %+v, want exactly 1", cp.last.Reports)
	}
	rep := cp.last.Reports[0]
	if rep.Rule != "team-cap" || rep.Team != "alpha" {
		t.Fatalf("report = %+v, want the team rule's for team alpha", rep)
	}
	if rep.SpentMicroUSD != 777_777 {
		t.Fatalf("team rule reported %d µUSD, want 777777", rep.SpentMicroUSD)
	}
}
