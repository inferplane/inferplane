package policy

import (
	"errors"
	"testing"
	"time"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
)

func validDoc() *v1alpha1.GovernancePolicy {
	return &v1alpha1.GovernancePolicy{
		TypeMeta: v1alpha1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.KindGovernancePolicy},
		Metadata: v1alpha1.ObjectMeta{Name: "platform-eng", Generation: 3},
		Spec: v1alpha1.GovernancePolicySpec{
			Subject: v1alpha1.Subject{Team: "platform-eng"},
			Rules: []v1alpha1.Rule{
				{
					Name:          "monthly-hard-cap",
					FailurePolicy: v1alpha1.FailClosed,
					Budget: &v1alpha1.BudgetRule{
						LimitMilliUSD: 5_000_000, // $5,000
						HardCap:       true,
						Lease:         v1alpha1.LeaseSpec{GrantMilliUSD: 5_000, RenewInterval: "30s"},
					},
				},
				{
					Name:          "affinity",
					FailurePolicy: v1alpha1.FailOpen,
					Routing:       &v1alpha1.RoutingRule{OnAffinityConflict: v1alpha1.PreferAffinity},
				},
				{
					Name:          "models",
					FailurePolicy: v1alpha1.FailOpen,
					ModelAccess:   &v1alpha1.ModelAccessRule{Allow: []string{"claude-sonnet-4-6", "claude-haiku-4-5"}},
				},
				{
					Name:          "throughput",
					FailurePolicy: v1alpha1.FailOpen,
					Rate:          &v1alpha1.RateRule{RPM: 300, TPM: 2_000_000},
				},
			},
		},
	}
}

func TestFromV1Alpha1Valid(t *testing.T) {
	p, err := FromV1Alpha1(validDoc())
	if err != nil {
		t.Fatalf("FromV1Alpha1: %v", err)
	}
	if p.Name != "platform-eng" || p.Generation != 3 || p.Subject.Team != "platform-eng" {
		t.Fatalf("policy envelope mangled: %+v", p)
	}
	if len(p.Rules) != 4 {
		t.Fatalf("got %d rules, want 4", len(p.Rules))
	}
	b := p.Rules[0].Budget
	// Wire milliUSD → internal microUSD is ×1000, exact.
	if b == nil || !b.HardCap || b.LimitMicroUSD != 5_000_000_000 || b.LeaseGrantMicroUSD != 5_000_000 || b.LeaseRenewInterval != 30*time.Second {
		t.Fatalf("budget rule mangled: %+v", b)
	}
	if r := p.Rules[1].Routing; r == nil || r.Affinity == nil || r.Affinity.OnAffinityConflict != v1alpha1.PreferAffinity {
		t.Fatalf("routing rule mangled: %+v", r)
	}
	if m := p.Rules[2].ModelAccess; m == nil || len(m.Allow) != 2 {
		t.Fatalf("model access rule mangled: %+v", m)
	}
	if r := p.Rules[3].Rate; r == nil || r.RPM != 300 || r.TPM != 2_000_000 {
		t.Fatalf("rate rule mangled: %+v", r)
	}
}

// Zero lease fields take the ADR-032 defaults: renew 10s, grant 0.1% of the
// limit floored at 1 milliUSD.
func TestLeaseDefaults(t *testing.T) {
	doc := validDoc()
	doc.Spec.Rules[0].Budget.Lease = v1alpha1.LeaseSpec{}
	p, err := FromV1Alpha1(doc)
	if err != nil {
		t.Fatalf("FromV1Alpha1: %v", err)
	}
	b := p.Rules[0].Budget
	if b.LeaseRenewInterval != DefaultLeaseRenewInterval {
		t.Fatalf("renew interval = %s, want default %s", b.LeaseRenewInterval, DefaultLeaseRenewInterval)
	}
	// 0.1% of $5,000 = $5 = 5,000 milliUSD = 5,000,000 µUSD.
	if b.LeaseGrantMicroUSD != 5_000_000 {
		t.Fatalf("default grant = %d µUSD, want 5000000", b.LeaseGrantMicroUSD)
	}

	// A tiny limit floors the default grant at 1 milliUSD, never 0.
	doc.Spec.Rules[0].Budget.LimitMilliUSD = 50 // $0.05
	p, err = FromV1Alpha1(doc)
	if err != nil {
		t.Fatalf("FromV1Alpha1 tiny limit: %v", err)
	}
	if got := p.Rules[0].Budget.LeaseGrantMicroUSD; got != 1_000 {
		t.Fatalf("floored grant = %d µUSD, want 1000 (1 milliUSD)", got)
	}
}

// A rule may declare unlimited: true instead of a numeric limit — an
// explicit, auditable "no cap" that doesn't require deleting the rule (and
// its observability) entirely.
func TestFromV1Alpha1Unlimited(t *testing.T) {
	doc := validDoc()
	doc.Spec.Rules[0].Budget = &v1alpha1.BudgetRule{Unlimited: true}
	doc.Spec.Rules[3].Rate = &v1alpha1.RateRule{Unlimited: true}
	p, err := FromV1Alpha1(doc)
	if err != nil {
		t.Fatalf("FromV1Alpha1: %v", err)
	}
	b := p.Rules[0].Budget
	if b == nil || !b.Unlimited || b.LimitMicroUSD != 0 || b.HardCap {
		t.Fatalf("unlimited budget rule mangled: %+v", b)
	}
	r := p.Rules[3].Rate
	if r == nil || !r.Unlimited || r.RPM != 0 || r.TPM != 0 {
		t.Fatalf("unlimited rate rule mangled: %+v", r)
	}
}

// Every rejection must be an explicit *UnsupportedError — never a silent
// skip — because the data plane reports these back to the control plane.
func TestFromV1Alpha1Rejections(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*v1alpha1.GovernancePolicy)
	}{
		{"unknown apiVersion", func(d *v1alpha1.GovernancePolicy) { d.APIVersion = "inferplane.dev/v9" }},
		{"unknown kind", func(d *v1alpha1.GovernancePolicy) { d.Kind = "Mystery" }},
		{"empty subject", func(d *v1alpha1.GovernancePolicy) { d.Spec.Subject = v1alpha1.Subject{} }},
		{"missing failurePolicy", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[0].FailurePolicy = "" }},
		{"unknown failurePolicy", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[0].FailurePolicy = "Shrug" }},
		{"hard cap must be FailClosed", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[0].FailurePolicy = v1alpha1.FailOpen }},
		{"negative lease grant", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[0].Budget.Lease.GrantMilliUSD = -1 }},
		{"unparseable renew interval", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[0].Budget.Lease.RenewInterval = "soon" }},
		{"sub-floor renew interval", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[0].Budget.Lease.RenewInterval = "100ms" }},
		{"affinity conflict preference required", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[1].Routing.OnAffinityConflict = "" }},
		{"empty model allow list", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[2].ModelAccess.Allow = nil }},
		{"empty model name", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[2].ModelAccess.Allow = []string{""} }},
		{"rate with no dimension", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[3].Rate = &v1alpha1.RateRule{} }},
		{"negative rate", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[3].Rate.RPM = -1 }},
		{"unlimited combined with rpm", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules[3].Rate = &v1alpha1.RateRule{Unlimited: true, RPM: 10}
		}},
		{"unlimited budget combined with limitMilliUSD", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules[0].Budget = &v1alpha1.BudgetRule{Unlimited: true, LimitMilliUSD: 100}
		}},
		{"unlimited budget combined with hardCap", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules[0].Budget = &v1alpha1.BudgetRule{Unlimited: true, HardCap: true}
		}},
		{"unlimited budget combined with adminContact", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules[0].Budget = &v1alpha1.BudgetRule{Unlimited: true, AdminContact: "ops@example.com"}
		}},
		{"two kinds on one rule", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules[0].Routing = &v1alpha1.RoutingRule{OnAffinityConflict: v1alpha1.PreferFallback}
		}},
		{"no kind on a rule", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[0].Budget = nil }},
		{"routing rule sets both affinity and budgetTiers", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules[1].Routing.BudgetTiers = validBudgetTiersRule(d).Routing.BudgetTiers
		}},
		{"budgetTiers missing budgetRef", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules = append(d.Spec.Rules, v1alpha1.Rule{
				Name: "tiers", FailurePolicy: v1alpha1.FailOpen,
				Routing: &v1alpha1.RoutingRule{BudgetTiers: &v1alpha1.BudgetTiersRule{
					Tiers: []v1alpha1.BudgetTier{{ThresholdPercent: 80, Substitute: map[string]string{"a": "b"}}},
				}},
			})
		}},
		{"budgetTiers budgetRef not found", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules = append(d.Spec.Rules, budgetTiersRule("no-such-rule", []v1alpha1.BudgetTier{
				{ThresholdPercent: 80, Substitute: map[string]string{"a": "b"}},
			}))
		}},
		{"budgetTiers budgetRef names an unlimited budget", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules = append(d.Spec.Rules,
				v1alpha1.Rule{Name: "unlimited-budget", FailurePolicy: v1alpha1.FailOpen, Budget: &v1alpha1.BudgetRule{Unlimited: true}},
				budgetTiersRule("unlimited-budget", []v1alpha1.BudgetTier{{ThresholdPercent: 80, Substitute: map[string]string{"a": "b"}}}),
			)
		}},
		{"budgetTiers empty tiers", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules = append(d.Spec.Rules, budgetTiersRule("monthly-hard-cap", nil))
		}},
		{"budgetTiers threshold out of range", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules = append(d.Spec.Rules, budgetTiersRule("monthly-hard-cap", []v1alpha1.BudgetTier{
				{ThresholdPercent: 100, Substitute: map[string]string{"a": "b"}},
			}))
		}},
		{"budgetTiers thresholds not strictly increasing", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules = append(d.Spec.Rules, budgetTiersRule("monthly-hard-cap", []v1alpha1.BudgetTier{
				{ThresholdPercent: 80, Substitute: map[string]string{"a": "b"}},
				{ThresholdPercent: 80, Substitute: map[string]string{"a": "c"}},
			}))
		}},
		{"budgetTiers empty substitute", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules = append(d.Spec.Rules, budgetTiersRule("monthly-hard-cap", []v1alpha1.BudgetTier{
				{ThresholdPercent: 80, Substitute: map[string]string{}},
			}))
		}},
		{"budgetTiers substitute empty key", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules = append(d.Spec.Rules, budgetTiersRule("monthly-hard-cap", []v1alpha1.BudgetTier{
				{ThresholdPercent: 80, Substitute: map[string]string{"": "b"}},
			}))
		}},
		{"budgetTiers substitute maps a model to itself", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules = append(d.Spec.Rules, budgetTiersRule("monthly-hard-cap", []v1alpha1.BudgetTier{
				{ThresholdPercent: 80, Substitute: map[string]string{"a": "a"}},
			}))
		}},
		{"budgetTiers substitute has a chain (key also a value)", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules = append(d.Spec.Rules, budgetTiersRule("monthly-hard-cap", []v1alpha1.BudgetTier{
				{ThresholdPercent: 80, Substitute: map[string]string{"a": "b", "b": "c"}},
			}))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := validDoc()
			tc.mutate(doc)
			_, err := FromV1Alpha1(doc)
			var ue *UnsupportedError
			if !errors.As(err, &ue) {
				t.Fatalf("want *UnsupportedError, got %v", err)
			}
		})
	}
}

// budgetTiersRule builds a routing rule whose budgetTiers half references
// budgetRef, for use alongside validDoc()'s "monthly-hard-cap" budget rule.
func budgetTiersRule(budgetRef string, tiers []v1alpha1.BudgetTier) v1alpha1.Rule {
	return v1alpha1.Rule{
		Name:          "downgrade-subagents",
		FailurePolicy: v1alpha1.FailOpen,
		Routing: &v1alpha1.RoutingRule{
			BudgetTiers: &v1alpha1.BudgetTiersRule{BudgetRef: budgetRef, Tiers: tiers},
		},
	}
}

// validBudgetTiersRule is a well-formed budgetTiers rule against d's
// "monthly-hard-cap" budget rule.
func validBudgetTiersRule(d *v1alpha1.GovernancePolicy) v1alpha1.Rule {
	return budgetTiersRule("monthly-hard-cap", []v1alpha1.BudgetTier{
		{ThresholdPercent: 80, Substitute: map[string]string{"claude-haiku-4-5": "glm-4.7-gpu"}},
	})
}

// A budgetTiers routing rule (ADR-041) converts and validates independently
// of the affinity half, and is NOT rejected by checkEnforceable the way
// affinity rules are (see store_test.go TestUnenforceableRejected).
func TestFromV1Alpha1BudgetTiersValid(t *testing.T) {
	doc := validDoc()
	doc.Spec.Rules = append(doc.Spec.Rules, v1alpha1.Rule{
		Name:          "downgrade-subagents-at-80",
		FailurePolicy: v1alpha1.FailOpen,
		Routing: &v1alpha1.RoutingRule{
			BudgetTiers: &v1alpha1.BudgetTiersRule{
				BudgetRef: "monthly-hard-cap",
				Tiers: []v1alpha1.BudgetTier{
					{ThresholdPercent: 80, Substitute: map[string]string{"claude-haiku-4-5": "glm-4.7-gpu"}},
					{ThresholdPercent: 95, Substitute: map[string]string{"claude-haiku-4-5": "glm-4.7-gpu", "claude-sonnet-4-6": "glm-4.7-gpu"}},
				},
			},
		},
	})
	p, err := FromV1Alpha1(doc)
	if err != nil {
		t.Fatalf("FromV1Alpha1: %v", err)
	}
	r := p.Rules[len(p.Rules)-1]
	bt := r.Routing.BudgetTiers
	if bt == nil || bt.BudgetRef != "monthly-hard-cap" || len(bt.Tiers) != 2 {
		t.Fatalf("budgetTiers rule mangled: %+v", r.Routing)
	}
	if bt.Tiers[0].ThresholdPercent != 80 || bt.Tiers[0].Substitute["claude-haiku-4-5"] != "glm-4.7-gpu" {
		t.Fatalf("tier 0 mangled: %+v", bt.Tiers[0])
	}
	if bt.Tiers[1].ThresholdPercent != 95 || len(bt.Tiers[1].Substitute) != 2 {
		t.Fatalf("tier 1 mangled: %+v", bt.Tiers[1])
	}
}

// A user-only subject is as valid as a team subject: user-level and
// department-level governance are equal citizens (ADR-032).
func TestUserSubject(t *testing.T) {
	doc := validDoc()
	doc.Spec.Subject = v1alpha1.Subject{User: "junseok"}
	p, err := FromV1Alpha1(doc)
	if err != nil {
		t.Fatalf("FromV1Alpha1: %v", err)
	}
	if p.Subject.User != "junseok" || p.Subject.Team != "" {
		t.Fatalf("subject mangled: %+v", p.Subject)
	}
}

func TestSupports(t *testing.T) {
	if !Supports(v1alpha1.APIVersion) {
		t.Fatalf("Supports(%q) = false", v1alpha1.APIVersion)
	}
	if Supports("inferplane.dev/v9") {
		t.Fatal("Supports of unknown version = true")
	}
}

// Review findings: duplicate rule names alias lease state later; oversized
// milliUSD amounts would overflow the ×1000 µUSD conversion into negatives.
func TestRejectDuplicateRuleNamesAndOverflow(t *testing.T) {
	dup := validDoc()
	dup.Spec.Rules[1].Name = dup.Spec.Rules[0].Name
	if _, err := FromV1Alpha1(dup); err == nil {
		t.Fatal("duplicate rule names accepted")
	}

	over := validDoc()
	over.Spec.Rules[0].Budget.LimitMilliUSD = maxWireMilliUSD + 1
	if _, err := FromV1Alpha1(over); err == nil {
		t.Fatal("overflowing limitMilliUSD accepted")
	}
	overGrant := validDoc()
	overGrant.Spec.Rules[0].Budget.Lease.GrantMilliUSD = maxWireMilliUSD + 1
	if _, err := FromV1Alpha1(overGrant); err == nil {
		t.Fatal("overflowing grantMilliUSD accepted")
	}
	atMax := validDoc()
	atMax.Spec.Rules[0].Budget.LimitMilliUSD = maxWireMilliUSD
	p, err := FromV1Alpha1(atMax)
	if err != nil {
		t.Fatalf("boundary value rejected: %v", err)
	}
	if p.Rules[0].Budget.LimitMicroUSD <= 0 {
		t.Fatalf("boundary conversion overflowed: %d", p.Rules[0].Budget.LimitMicroUSD)
	}
}

// period selects a budget rule's calendar window. Empty normalizes to
// CalendarMonth — the window every budget rule enforced before the field
// existed — so Budget.Period is never empty after conversion and every
// pre-existing document keeps its exact meaning.
func TestBudgetPeriodConversion(t *testing.T) {
	cases := []struct {
		name string
		wire v1alpha1.BudgetPeriod
		want v1alpha1.BudgetPeriod
	}{
		{"omitted defaults to CalendarMonth", "", v1alpha1.PeriodCalendarMonth},
		{"CalendarDay", v1alpha1.PeriodCalendarDay, v1alpha1.PeriodCalendarDay},
		{"CalendarMonth", v1alpha1.PeriodCalendarMonth, v1alpha1.PeriodCalendarMonth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := validDoc()
			doc.Spec.Rules[0].Budget.Period = tc.wire
			p, err := FromV1Alpha1(doc)
			if err != nil {
				t.Fatalf("FromV1Alpha1: %v", err)
			}
			if got := p.Rules[0].Budget.Period; got != tc.want {
				t.Fatalf("Budget.Period = %q, want %q", got, tc.want)
			}
		})
	}
}

// An unknown period is an explicit *UnsupportedError carrying the offending
// rule's name, never a silent fallback — and case matters: this package does
// not coerce a value the schema doesn't spell exactly.
func TestBudgetPeriodUnknownRejected(t *testing.T) {
	for _, bad := range []v1alpha1.BudgetPeriod{"Weekly", "calendarday"} {
		t.Run(string(bad), func(t *testing.T) {
			doc := validDoc()
			doc.Spec.Rules[0].Budget.Period = bad
			_, err := FromV1Alpha1(doc)
			var ue *UnsupportedError
			if !errors.As(err, &ue) {
				t.Fatalf("want *UnsupportedError, got %v", err)
			}
			if ue.Rule != "monthly-hard-cap" {
				t.Fatalf("UnsupportedError.Rule = %q, want %q", ue.Rule, "monthly-hard-cap")
			}
		})
	}
}

// unlimited combined with period is rejected — "no cap" is a statement about
// the budget dimension as a whole and has no window — while an unlimited rule
// alone still gets a normalized Period, keeping Budget's "never empty after
// conversion" contract true.
func TestBudgetPeriodUnlimited(t *testing.T) {
	combined := validDoc()
	combined.Spec.Rules[0].Budget = &v1alpha1.BudgetRule{Unlimited: true, Period: v1alpha1.PeriodCalendarDay}
	_, err := FromV1Alpha1(combined)
	var ue *UnsupportedError
	if !errors.As(err, &ue) {
		t.Fatalf("want *UnsupportedError, got %v", err)
	}

	alone := validDoc()
	alone.Spec.Rules[0].Budget = &v1alpha1.BudgetRule{Unlimited: true}
	p, err := FromV1Alpha1(alone)
	if err != nil {
		t.Fatalf("FromV1Alpha1: %v", err)
	}
	b := p.Rules[0].Budget
	if b == nil || !b.Unlimited || b.Period != v1alpha1.PeriodCalendarMonth {
		t.Fatalf("unlimited budget rule mangled: %+v", b)
	}
}

// The design's day+month shape: two rules in one document, one per window,
// each carrying its own limit and hardCap. This round trip — right periods,
// exact ×1000 milliUSD→µUSD limits, right hardCap flags — is what the rest of
// the daily-budget phase is built on.
func TestBudgetDayAndMonthRules(t *testing.T) {
	doc := &v1alpha1.GovernancePolicy{
		TypeMeta: v1alpha1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.KindGovernancePolicy},
		Metadata: v1alpha1.ObjectMeta{Name: "demo-team"},
		Spec: v1alpha1.GovernancePolicySpec{
			Subject: v1alpha1.Subject{Team: "demo"},
			Rules: []v1alpha1.Rule{
				{
					Name:          "daily-soft-cap",
					FailurePolicy: v1alpha1.FailOpen,
					Budget: &v1alpha1.BudgetRule{
						Period:        v1alpha1.PeriodCalendarDay,
						LimitMilliUSD: 50_000, // $50 / day
					},
				},
				{
					Name:          "monthly-hard-cap",
					FailurePolicy: v1alpha1.FailClosed,
					Budget: &v1alpha1.BudgetRule{
						LimitMilliUSD: 1_000_000, // $1000 / month; period omitted = CalendarMonth
						HardCap:       true,
					},
				},
			},
		},
	}
	p, err := FromV1Alpha1(doc)
	if err != nil {
		t.Fatalf("FromV1Alpha1: %v", err)
	}
	if len(p.Rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(p.Rules))
	}
	day := p.Rules[0].Budget
	if day == nil || day.Period != v1alpha1.PeriodCalendarDay || day.LimitMicroUSD != 50_000_000 || day.HardCap {
		t.Fatalf("day rule mangled: %+v", day)
	}
	month := p.Rules[1].Budget
	if month == nil || month.Period != v1alpha1.PeriodCalendarMonth || month.LimitMicroUSD != 1_000_000_000 || !month.HardCap {
		t.Fatalf("month rule mangled: %+v", month)
	}
}
