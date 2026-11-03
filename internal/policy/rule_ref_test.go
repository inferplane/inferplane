package policy

import (
	"testing"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
)

// Each merged team limit names the rule whose value it holds (ADR-057). A hard
// cap names its rule over a smaller soft meter; a tie keeps the first rule.
func TestTeamLimitsNameBindingRules(t *testing.T) {
	policies := []*Policy{
		{Name: "wide", Generation: 2, Subject: Subject{Team: "t"}, Rules: []Rule{
			{Name: "rate", Rate: &Rate{RPM: 100, TPM: 5000}},
			{Name: "soft-day", Budget: &Budget{LimitMicroUSD: 1_000, Period: v1alpha1.PeriodCalendarDay}},
		}},
		{Name: "tight", Generation: 7, Subject: Subject{Team: "t"}, Rules: []Rule{
			{Name: "rpm", Rate: &Rate{RPM: 10}},
			{Name: "tpm-tie", Rate: &Rate{TPM: 5000}},
			{Name: "hard-day", Budget: &Budget{LimitMicroUSD: 9_000, HardCap: true, Period: v1alpha1.PeriodCalendarDay}},
			{Name: "month", Budget: &Budget{LimitMicroUSD: 50_000, HardCap: true, Period: v1alpha1.PeriodCalendarMonth}},
		}},
	}
	tl := mergeTeamLimits(policies)["t"]
	want := map[string]RuleRef{
		"rpm":   {Policy: "tight", Generation: 7, Rule: "rpm"},
		"tpm":   {Policy: "wide", Generation: 2, Rule: "rate"},
		"day":   {Policy: "tight", Generation: 7, Rule: "hard-day"},
		"month": {Policy: "tight", Generation: 7, Rule: "month"},
	}
	for name, got := range map[string]RuleRef{"rpm": tl.RPMRule, "tpm": tl.TPMRule, "day": tl.BudgetDayRule, "month": tl.BudgetRule} {
		if got != want[name] {
			t.Errorf("%s rule = %+v, want %+v", name, got, want[name])
		}
	}
	if tl.RPM != 10 || tl.TPM != 5000 || tl.BudgetMicrosPerDay != 9_000 || !tl.BudgetDayHard || tl.BudgetMicrosPerMonth != 50_000 {
		t.Fatalf("merged values changed: %+v", tl)
	}
}

// User limits name their rule through both folds: rules inside one subject
// and the user-only view narrowed by the (team, user) view.
func TestUserLimitsNameBindingRules(t *testing.T) {
	dir := t.TempDir()
	writePolicy(t, dir, "p.yaml", `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: user-global, generation: 4 }
spec:
  subject: { user: sub-1 }
  rules:
  - name: month-cap
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 1000, hardCap: true }
  - name: day-cap
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 300, hardCap: true, period: CalendarDay }
---
apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: user-in-t, generation: 9 }
spec:
  subject: { team: t, user: sub-1 }
  rules:
  - name: month-tight
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 200, hardCap: true }
`)
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ul, ok := s.UserLimits("t", "sub-1")
	if !ok || ul.BudgetMicrosPerMonth != 200_000 || ul.BudgetMicrosPerDay != 300_000 {
		t.Fatalf("UserLimits = %+v, %v", ul, ok)
	}
	if ul.BudgetRule != (RuleRef{Policy: "user-in-t", Generation: 9, Rule: "month-tight"}) {
		t.Fatalf("month rule = %+v", ul.BudgetRule)
	}
	if ul.BudgetDayRule != (RuleRef{Policy: "user-global", Generation: 4, Rule: "day-cap"}) {
		t.Fatalf("day rule = %+v", ul.BudgetDayRule)
	}
}
