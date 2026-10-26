package policy

import (
	"testing"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
)

func TestBudgetMergeKeepsHardAdmissionLimitAboveSoftThreshold(t *testing.T) {
	for _, period := range []v1alpha1.BudgetPeriod{v1alpha1.PeriodCalendarMonth, v1alpha1.PeriodCalendarDay} {
		for _, reverse := range []bool{false, true} {
			for _, user := range []string{"", "person"} {
				t.Run(string(period)+map[bool]string{false: "/forward", true: "/reverse"}[reverse]+"/"+user, func(t *testing.T) {
					subject := Subject{Team: "t", User: user}
					policies := []*Policy{
						{Name: "soft", Subject: subject, Rules: []Rule{{Name: "threshold", Budget: &Budget{
							LimitMicroUSD: 1_000, Period: period, AdminContact: "soft-contact",
						}}}},
						{Name: "hard-wide", Subject: subject, Rules: []Rule{{Name: "cap", Budget: &Budget{
							LimitMicroUSD: 6_000, Period: period, HardCap: true, AdminContact: "wide-contact",
						}}}},
						{Name: "hard-tight", Subject: subject, Rules: []Rule{{Name: "cap", Budget: &Budget{
							LimitMicroUSD: 3_000, Period: period, HardCap: true, AdminContact: "hard-contact",
						}}}},
					}
					if reverse {
						policies[0], policies[2] = policies[2], policies[0]
					}
					var limit int64
					var hard bool
					var contact string
					if user == "" {
						limits := mergeTeamLimits(policies)["t"]
						limit, hard, contact = limits.BudgetMicrosPerMonth, limits.BudgetHard, limits.AdminContact
						if period == v1alpha1.PeriodCalendarDay {
							limit, hard, contact = limits.BudgetMicrosPerDay, limits.BudgetDayHard, limits.AdminContactDay
						}
					} else {
						limits := mergeUserLimits(policies)[userKey{team: "t", user: user}]
						limit, hard, contact = limits.BudgetMicrosPerMonth, limits.BudgetHard, limits.AdminContact
						if period == v1alpha1.PeriodCalendarDay {
							limit, hard, contact = limits.BudgetMicrosPerDay, limits.BudgetDayHard, limits.AdminContactDay
						}
					}
					if limit != 3_000 || !hard || contact != "hard-contact" {
						t.Fatalf("soft threshold erased or narrowed hard cap: limit=%d hard=%v contact=%q", limit, hard, contact)
					}
				})
			}
		}
	}
}

func TestUserBudgetMergeKeepsHardCapAcrossSubjectSelectors(t *testing.T) {
	store := NewEmptyStore()
	rejected := store.ApplyWire([]v1alpha1.GovernancePolicy{
		{
			TypeMeta: v1alpha1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.KindGovernancePolicy},
			Metadata: v1alpha1.ObjectMeta{Name: "person-cap"},
			Spec: v1alpha1.GovernancePolicySpec{
				Subject: v1alpha1.Subject{User: "person"},
				Rules: []v1alpha1.Rule{{Name: "cap", FailurePolicy: v1alpha1.FailClosed,
					Budget: &v1alpha1.BudgetRule{LimitMilliUSD: 3, HardCap: true}}},
			},
		},
		{
			TypeMeta: v1alpha1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.KindGovernancePolicy},
			Metadata: v1alpha1.ObjectMeta{Name: "team-person-threshold"},
			Spec: v1alpha1.GovernancePolicySpec{
				Subject: v1alpha1.Subject{Team: "t", User: "person"},
				Rules: []v1alpha1.Rule{{Name: "threshold", FailurePolicy: v1alpha1.FailOpen,
					Budget: &v1alpha1.BudgetRule{LimitMilliUSD: 1}}},
			},
		},
	})
	if len(rejected) != 0 {
		t.Fatal(rejected)
	}
	limits, ok := store.UserLimits("t", "person")
	if !ok || limits.BudgetMicrosPerMonth != 3_000 || !limits.BudgetHard {
		t.Fatalf("selector merge lost independent hard cap: %+v, found=%v", limits, ok)
	}
}
