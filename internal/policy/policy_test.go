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
	if r := p.Rules[1].Routing; r == nil || r.OnAffinityConflict != v1alpha1.PreferAffinity {
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
		{"two kinds on one rule", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules[0].Routing = &v1alpha1.RoutingRule{OnAffinityConflict: v1alpha1.PreferFallback}
		}},
		{"no kind on a rule", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[0].Budget = nil }},
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
