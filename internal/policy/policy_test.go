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
		Metadata: v1alpha1.ObjectMeta{Name: "team-a", Generation: 3},
		Spec: v1alpha1.GovernancePolicySpec{Rules: []v1alpha1.Rule{
			{
				Name:          "team-a-hard-cap",
				FailurePolicy: v1alpha1.FailClosed,
				Budget: &v1alpha1.BudgetRule{
					LimitMicroUSD: 500_000_000, // $500
					HardCap:       true,
					Lease:         v1alpha1.LeaseSpec{GrantMicroUSD: 5_000_000, RenewInterval: "30s"},
				},
			},
			{
				Name:          "team-a-affinity",
				FailurePolicy: v1alpha1.FailOpen,
				Routing:       &v1alpha1.RoutingRule{OnAffinityConflict: v1alpha1.PreferAffinity},
			},
		}},
	}
}

func TestFromV1Alpha1Valid(t *testing.T) {
	rules, err := FromV1Alpha1(validDoc())
	if err != nil {
		t.Fatalf("FromV1Alpha1: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(rules))
	}
	b := rules[0].Budget
	if b == nil || !b.HardCap || b.LeaseGrantMicroUSD != 5_000_000 || b.LeaseRenewInterval != 30*time.Second {
		t.Fatalf("budget rule mangled: %+v", b)
	}
	r := rules[1].Routing
	if r == nil || r.OnAffinityConflict != v1alpha1.PreferAffinity {
		t.Fatalf("routing rule mangled: %+v", r)
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
		{"missing failurePolicy", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[0].FailurePolicy = "" }},
		{"unknown failurePolicy", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[0].FailurePolicy = "Shrug" }},
		{"hard cap must be FailClosed", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[0].FailurePolicy = v1alpha1.FailOpen }},
		{"lease grant required", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[0].Budget.Lease.GrantMicroUSD = 0 }},
		{"lease renew interval required", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[0].Budget.Lease.RenewInterval = "" }},
		{"affinity conflict preference required", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[1].Routing.OnAffinityConflict = "" }},
		{"budget and routing on one rule", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules[0].Routing = &v1alpha1.RoutingRule{OnAffinityConflict: v1alpha1.PreferFallback}
		}},
		{"neither budget nor routing", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[0].Budget = nil }},
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

func TestSupports(t *testing.T) {
	if !Supports(v1alpha1.APIVersion) {
		t.Fatalf("Supports(%q) = false", v1alpha1.APIVersion)
	}
	if Supports("inferplane.dev/v9") {
		t.Fatal("Supports of unknown version = true")
	}
}
