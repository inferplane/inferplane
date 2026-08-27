package policy

import (
	"errors"
	"testing"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
)

// A user-subject budget rule is enforceable as of ADR-042 Phase 3
// (previously rejected by checkEnforceable): both a (team, user) subject and
// a user-ONLY subject with a budget rule must load — through the file path
// AND the control-plane wire path, since each builds its own snapshot.
func TestUserSubjectBudgetRuleIsNowEnforceable(t *testing.T) {
	dir := t.TempDir()
	writePolicy(t, dir, "p.yaml", `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: team-user-cap }
spec:
  subject: { team: t, user: sub-1 }
  rules:
  - name: cap
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 1000, hardCap: true }
---
apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: user-only-cap }
spec:
  subject: { user: sub-2 }
  rules:
  - name: cap
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 500, hardCap: true }
`)
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("user-subject budget rules must be enforceable now: %v", err)
	}
	if ul, ok := s.UserLimits("t", "sub-1"); !ok || ul.BudgetMicrosPerMonth != 1_000_000 {
		t.Fatalf("UserLimits(t, sub-1) = %+v, %v; want month 1_000_000", ul, ok)
	}

	// The wire path applies the same gate and builds its own snapshot: no
	// rejection, and the users map must be populated there too.
	ws := NewEmptyStore()
	rejected := ws.ApplyWire([]v1alpha1.GovernancePolicy{{
		TypeMeta: v1alpha1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.KindGovernancePolicy},
		Metadata: v1alpha1.ObjectMeta{Name: "user-only-cap"},
		Spec: v1alpha1.GovernancePolicySpec{
			Subject: v1alpha1.Subject{User: "sub-2"},
			Rules: []v1alpha1.Rule{{
				Name:          "cap",
				FailurePolicy: v1alpha1.FailClosed,
				Budget:        &v1alpha1.BudgetRule{LimitMilliUSD: 500, HardCap: true},
			}},
		},
	}})
	if len(rejected) != 0 {
		t.Fatalf("ApplyWire rejected an enforceable user-subject budget rule: %v", rejected)
	}
	if ul, ok := ws.UserLimits("any-team", "sub-2"); !ok || ul.BudgetMicrosPerMonth != 500_000 {
		t.Fatalf("wire-fed UserLimits = %+v, %v; want month 500_000", ul, ok)
	}
}

// Rate keeps its team-only subject restriction: user-only and (team, user)
// rate rules are both still rejected, with the rate-specific message.
func TestUserSubjectRateRuleStillRejected(t *testing.T) {
	const wantReason = "rate rules require a team-only subject in this build (user-scoped rate is not yet enforceable; user subjects support budget and modelAccess)"
	cases := []struct{ name, body string }{
		{"user-only rate", `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: u }
spec:
  subject: { user: sub-1 }
  rules:
  - name: r-user
    failurePolicy: FailOpen
    rate: { rpm: 10 }
`},
		{"team-and-user rate", `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: tu }
spec:
  subject: { team: t, user: sub-1 }
  rules:
  - name: r-user
    failurePolicy: FailOpen
    rate: { rpm: 10 }
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writePolicy(t, dir, "p.yaml", tc.body)
			_, err := NewStore(dir)
			var ue *UnsupportedError
			if !errors.As(err, &ue) {
				t.Fatalf("want *UnsupportedError, got %v", err)
			}
			if ue.Rule != "r-user" {
				t.Fatalf("Rule = %q, want %q", ue.Rule, "r-user")
			}
			if ue.Reason != wantReason {
				t.Fatalf("Reason = %q, want %q", ue.Reason, wantReason)
			}
		})
	}
}

// Most-restrictive-wins is computed PER WINDOW across every policy matching
// one (team, user): the hard 50_000 day cap beats the soft 90_000 one, and
// the month window folds independently.
func TestUserLimitsMostRestrictiveWinsPerWindow(t *testing.T) {
	dayHard := &Policy{Name: "day-hard", Subject: Subject{Team: "t", User: "sub-1"},
		Rules: []Rule{{Name: "day-cap", Budget: &Budget{LimitMicroUSD: 50_000, HardCap: true, Period: v1alpha1.PeriodCalendarDay}}}}
	monthSoft := &Policy{Name: "month-soft", Subject: Subject{Team: "t", User: "sub-1"},
		Rules: []Rule{{Name: "month-cap", Budget: &Budget{LimitMicroUSD: 1_000_000}}}}
	daySoft := &Policy{Name: "day-soft", Subject: Subject{Team: "t", User: "sub-1"},
		Rules: []Rule{{Name: "day-loose", Budget: &Budget{LimitMicroUSD: 90_000, Period: v1alpha1.PeriodCalendarDay}}}}

	got := mergeUserLimits([]*Policy{dayHard, monthSoft, daySoft})
	ul, ok := got[userKey{team: "t", user: "sub-1"}]
	if !ok {
		t.Fatal("(t, sub-1) missing from merge result")
	}
	if ul.BudgetMicrosPerDay != 50_000 || !ul.BudgetDayHard {
		t.Fatalf("day window merge wrong: %+v", ul)
	}
	if ul.BudgetMicrosPerMonth != 1_000_000 || ul.BudgetHard {
		t.Fatalf("month window merge wrong: %+v", ul)
	}
}

// The guard for narrow's `o.<window> == 0` cases: a user-only MONTH cap and a
// (team, user) DAY cap meet only in Store.UserLimits' two-entry fold, and
// BOTH windows must survive it. Without the guard, the window the other view
// doesn't carry is zeroed out — a silently deleted money control.
func TestUserLimitsDayOnlyPolicyDoesNotEraseMonthCap(t *testing.T) {
	dir := t.TempDir()
	writePolicy(t, dir, "p.yaml", `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: user-month }
spec:
  subject: { user: sub-1 }
  rules:
  - name: month-cap
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 1000, hardCap: true }
---
apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: team-user-day }
spec:
  subject: { team: t, user: sub-1 }
  rules:
  - name: day-cap
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 50, hardCap: true, period: CalendarDay }
`)
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ul, ok := s.UserLimits("t", "sub-1")
	if !ok {
		t.Fatal("(t, sub-1) not found")
	}
	if ul.BudgetMicrosPerMonth != 1_000_000 {
		t.Fatalf("day-only view erased the month cap: %+v", ul)
	}
	if ul.BudgetMicrosPerDay != 50_000 {
		t.Fatalf("month-only view erased the day cap: %+v", ul)
	}
}

// An explicit unlimited rule must never narrow OR widen a real limit —
// regardless of processing order — and a subject whose ONLY budget rule is
// unlimited still gets an (all-zero) entry, so it can shadow a default the
// way a numeric rule would.
func TestUserLimitsUnlimitedNeverWidens(t *testing.T) {
	real := &Policy{Name: "real", Subject: Subject{Team: "t", User: "sub-1"},
		Rules: []Rule{{Name: "day-cap", Budget: &Budget{LimitMicroUSD: 50_000, HardCap: true, Period: v1alpha1.PeriodCalendarDay}}}}
	unlimited := &Policy{Name: "declared-unlimited", Subject: Subject{Team: "t", User: "sub-1"},
		Rules: []Rule{{Name: "no-cap", Budget: &Budget{Unlimited: true}}}}

	for _, order := range [][]*Policy{{real, unlimited}, {unlimited, real}} {
		got := mergeUserLimits(order)
		ul, ok := got[userKey{team: "t", user: "sub-1"}]
		if !ok {
			t.Fatalf("order %v: (t, sub-1) missing from merge result", order)
		}
		if ul.BudgetMicrosPerDay != 50_000 || !ul.BudgetDayHard {
			t.Fatalf("order %v: unlimited rule corrupted the real day budget: %+v", order, ul)
		}
	}

	only := &Policy{Name: "declared-unlimited", Subject: Subject{Team: "t", User: "sub-2"},
		Rules: []Rule{{Name: "no-cap", Budget: &Budget{Unlimited: true}}}}
	got := mergeUserLimits([]*Policy{only})
	ul, ok := got[userKey{team: "t", user: "sub-2"}]
	if !ok {
		t.Fatal("an explicit unlimited declaration must still contribute a merge entry")
	}
	if ul.BudgetMicrosPerMonth != 0 || ul.BudgetMicrosPerDay != 0 {
		t.Fatalf("unlimited-only merge should be all-zero: %+v", ul)
	}
}

// A user-only subject reaches the user in EVERY team, and folds
// most-restrictive with a (team, user) subject where both match.
func TestUserLimitsFoldsUserOnlyAndTeamScoped(t *testing.T) {
	dir := t.TempDir()
	writePolicy(t, dir, "p.yaml", `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: user-global }
spec:
  subject: { user: sub-1 }
  rules:
  - name: month-cap
    failurePolicy: FailOpen
    budget: { limitMilliUSD: 1000 }
---
apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: user-in-t }
spec:
  subject: { team: t, user: sub-1 }
  rules:
  - name: month-tight
    failurePolicy: FailOpen
    budget: { limitMilliUSD: 200 }
`)
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if ul, ok := s.UserLimits("t", "sub-1"); !ok || ul.BudgetMicrosPerMonth != 200_000 {
		t.Fatalf("UserLimits(t, sub-1) = %+v, %v; want the tighter 200_000", ul, ok)
	}
	if ul, ok := s.UserLimits("other-team", "sub-1"); !ok || ul.BudgetMicrosPerMonth != 1_000_000 {
		t.Fatalf("UserLimits(other-team, sub-1) = %+v, %v; want the global 1_000_000", ul, ok)
	}
	if _, ok := s.UserLimits("t", "nobody"); ok {
		t.Fatal("unmatched user reported limits")
	}
}

// A modelAccess-only user policy must NOT manufacture an all-zero (=
// unlimited) UserLimits entry — the same shadowing hazard
// TestModelAccessOnlyContributesNoTeamLimits pins for teams.
func TestUserLimitsIgnoresModelAccessOnlyPolicy(t *testing.T) {
	dir := t.TempDir()
	writePolicy(t, dir, "p.yaml", `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: user-models }
spec:
  subject: { user: sub-1 }
  rules:
  - name: haiku-only
    failurePolicy: FailOpen
    modelAccess: { allow: ["claude-haiku-4-5"] }
`)
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if ul, ok := s.UserLimits("t", "sub-1"); ok {
		t.Fatalf("modelAccess-only policy manufactured UserLimits %+v", ul)
	}
}

// An empty user never matches — an unauthenticated/ownerless principal must
// not accidentally inherit a user-only policy stored under the zero team, and
// must never inherit its TEAM's cap as a personal one.
//
// The team-only policy in this fixture is load-bearing and was added after
// mutation testing: with only the user-only policy present, mergeUserLimits'
// team-only skip could be deleted AND UserLimits' empty-user guard deleted with
// it, and this test still passed — nothing would ever have been stored under
// userKey{team: "t", user: ""} for the lookup to find. The team-only rule is
// what makes that key populatable, so the two guards are now genuinely pinned
// rather than merely asserted. Do not simplify the fixture back to one policy.
func TestUserLimitsEmptyUserIsNeverAMatch(t *testing.T) {
	dir := t.TempDir()
	writePolicy(t, dir, "p.yaml", `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: user-global }
spec:
  subject: { user: sub-1 }
  rules:
  - name: month-cap
    failurePolicy: FailOpen
    budget: { limitMilliUSD: 1000 }
---
apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: team-only }
spec:
  subject: { team: t }
  rules:
  - name: team-month-cap
    failurePolicy: FailOpen
    budget: { limitMilliUSD: 7000 }
`)
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if ul, ok := s.UserLimits("t", ""); ok {
		t.Fatalf("empty user matched a policy: %+v", ul)
	}
	// The control: the team-only rule DID load and is enforced where it
	// belongs, so the assertion above is about the user lookup rejecting an
	// empty user — not about an empty policy set.
	tl, ok := s.TeamLimits("t")
	if !ok || tl.BudgetMicrosPerMonth != 7_000_000 {
		t.Fatalf("team-only rule did not land: ok=%v limits=%+v", ok, tl)
	}
}

// One person's cap must not become their whole team's cap: a (team, user)
// budget policy contributes NOTHING to TeamLimits — no entry at all when it
// is the only policy, and an unchanged entry when a team policy coexists.
func TestTeamLimitsUnaffectedByUserPolicies(t *testing.T) {
	dir := t.TempDir()
	writePolicy(t, dir, "p.yaml", `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: user-in-t }
spec:
  subject: { team: t, user: sub-1 }
  rules:
  - name: personal-cap
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 50, hardCap: true }
`)
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if tl, ok := s.TeamLimits("t"); ok {
		t.Fatalf("a (team, user) budget policy leaked into TeamLimits: %+v", tl)
	}

	writePolicy(t, dir, "team.yaml", `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: team-cap }
spec:
  subject: { team: t }
  rules:
  - name: team-cap
    failurePolicy: FailOpen
    budget: { limitMilliUSD: 1000 }
`)
	if err := s.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tl, ok := s.TeamLimits("t")
	if !ok {
		t.Fatal("team policy missing from TeamLimits")
	}
	if tl.BudgetMicrosPerMonth != 1_000_000 || tl.BudgetHard {
		t.Fatalf("the personal 50_000 hard cap must not narrow the team's 1_000_000 soft cap: %+v", tl)
	}
}
