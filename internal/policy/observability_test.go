package policy

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
)

func obsDoc(name, team, user string, rule *v1alpha1.ObservabilityRule) v1alpha1.GovernancePolicy {
	return v1alpha1.GovernancePolicy{
		TypeMeta: v1alpha1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.KindGovernancePolicy},
		Metadata: v1alpha1.ObjectMeta{Name: name},
		Spec: v1alpha1.GovernancePolicySpec{
			Subject: v1alpha1.Subject{Team: team, User: user},
			Rules:   []v1alpha1.Rule{{Name: "export", FailurePolicy: v1alpha1.FailOpen, Observability: rule}},
		},
	}
}

func obsBudgetDoc(name, team string) v1alpha1.GovernancePolicy {
	return v1alpha1.GovernancePolicy{
		TypeMeta: v1alpha1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.KindGovernancePolicy},
		Metadata: v1alpha1.ObjectMeta{Name: name},
		Spec: v1alpha1.GovernancePolicySpec{
			Subject: v1alpha1.Subject{Team: team},
			Rules: []v1alpha1.Rule{{Name: "money", FailurePolicy: v1alpha1.FailOpen,
				Budget: &v1alpha1.BudgetRule{LimitMilliUSD: 10_000}}},
		},
	}
}

func percent(v int) *int { return &v }

func TestObservabilityRuleConversion(t *testing.T) {
	doc := obsDoc("obs", "eng", "", &v1alpha1.ObservabilityRule{
		Traces:  &v1alpha1.TraceExport{Exporter: "corp", SamplePercent: percent(25)},
		Audit:   &v1alpha1.SignalExport{Exporter: "corp"},
		Content: &v1alpha1.SignalExport{Exporter: "vault"},
	})
	p, err := FromV1Alpha1(&doc)
	if err != nil {
		t.Fatalf("FromV1Alpha1: %v", err)
	}
	got := p.Rules[0].Observability
	if got == nil || got.Traces == nil || *got.Traces != (TraceExport{Exporter: "corp", SamplePercent: 25}) ||
		got.Audit != "corp" || got.Content != "vault" {
		t.Fatalf("converted rule = %+v", got)
	}

	defaulted := obsDoc("obs", "eng", "", &v1alpha1.ObservabilityRule{Traces: &v1alpha1.TraceExport{Exporter: "corp"}})
	p, err = FromV1Alpha1(&defaulted)
	if err != nil {
		t.Fatalf("FromV1Alpha1 default percent: %v", err)
	}
	if p.Rules[0].Observability.Traces.SamplePercent != 100 {
		t.Fatalf("omitted samplePercent must mean 100, got %d", p.Rules[0].Observability.Traces.SamplePercent)
	}
}

func TestObservabilityRuleRejections(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*v1alpha1.GovernancePolicy)
		reason string
	}{
		{"fail closed", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[0].FailurePolicy = v1alpha1.FailClosed }, "FailOpen"},
		{"empty rule", func(d *v1alpha1.GovernancePolicy) { d.Spec.Rules[0].Observability = &v1alpha1.ObservabilityRule{} }, "at least one"},
		{"empty exporter", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules[0].Observability.Audit = &v1alpha1.SignalExport{}
		}, "exporter"},
		{"bad exporter name", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules[0].Observability.Content = &v1alpha1.SignalExport{Exporter: "Bad_Name"}
		}, "exporter"},
		{"percent zero", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules[0].Observability.Traces.SamplePercent = percent(0)
		}, "samplePercent"},
		{"percent over", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules[0].Observability.Traces.SamplePercent = percent(101)
		}, "samplePercent"},
		{"two kinds", func(d *v1alpha1.GovernancePolicy) {
			d.Spec.Rules[0].Rate = &v1alpha1.RateRule{RPM: 1}
		}, "exactly one"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := obsDoc("obs", "eng", "", &v1alpha1.ObservabilityRule{Traces: &v1alpha1.TraceExport{Exporter: "corp"}})
			tc.mutate(&doc)
			_, err := FromV1Alpha1(&doc)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("err = %v, want reason containing %q", err, tc.reason)
			}
		})
	}
}

func TestApplyWireRejectsObservabilityOutsideCeiling(t *testing.T) {
	s := NewEmptyStore()
	s.SetObservabilityCeiling(func(exporter, signal string) error {
		if exporter == "corp" && signal == SignalTraces {
			return nil
		}
		return fmt.Errorf("exporter %q does not allow %s", exporter, signal)
	})
	docs := []v1alpha1.GovernancePolicy{
		obsDoc("content-on-traces-only", "eng", "", &v1alpha1.ObservabilityRule{Content: &v1alpha1.SignalExport{Exporter: "corp"}}),
		obsDoc("unknown-exporter", "eng", "", &v1alpha1.ObservabilityRule{Traces: &v1alpha1.TraceExport{Exporter: "other"}}),
		obsDoc("ok", "eng", "", &v1alpha1.ObservabilityRule{Traces: &v1alpha1.TraceExport{Exporter: "corp"}}),
		obsBudgetDoc("money", "eng"),
	}
	rej := s.ApplyWire(docs)
	if len(rej) != 2 || rej[0].Policy != "content-on-traces-only" || rej[1].Policy != "unknown-exporter" {
		t.Fatalf("rejections = %+v", rej)
	}
	if rej[0].Rule != "export" || !strings.Contains(rej[0].Reason, "content") {
		t.Fatalf("rejection must name the rule and signal: %+v", rej[0])
	}
	if _, ok := s.TeamLimits("eng"); !ok {
		t.Fatal("budget document in the same distribution must still apply")
	}
	got := s.Observability("eng", "")
	if len(got.Traces) != 1 || got.Traces[0] != (TraceExport{Exporter: "corp", SamplePercent: 100}) || got.Content != "" || len(got.Audit) != 0 {
		t.Fatalf("decision = %+v", got)
	}
}

func TestReloadRejectsObservabilityOutsideCeiling(t *testing.T) {
	body, err := json.Marshal(obsDoc("obs", "eng", "", &v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "missing"}}))
	if err != nil {
		t.Fatal(err)
	}
	path := writePolicy(t, t.TempDir(), "obs.yaml", string(body))
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("syntax-only initial load must succeed before a ceiling is installed: %v", err)
	}
	s.SetObservabilityCeiling(func(exporter, signal string) error { return fmt.Errorf("exporter %q is not declared", exporter) })
	if err := s.Reload(); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("Reload err = %v, want rejection naming the exporter", err)
	}
}

func TestObservabilityDecisionMerge(t *testing.T) {
	s := NewEmptyStore()
	docs := []v1alpha1.GovernancePolicy{
		obsDoc("team", "eng", "", &v1alpha1.ObservabilityRule{
			Traces:  &v1alpha1.TraceExport{Exporter: "corp", SamplePercent: percent(10)},
			Audit:   &v1alpha1.SignalExport{Exporter: "corp"},
			Content: &v1alpha1.SignalExport{Exporter: "vault"},
		}),
		obsDoc("user", "eng", "alice", &v1alpha1.ObservabilityRule{
			Traces: &v1alpha1.TraceExport{Exporter: "corp", SamplePercent: percent(50)},
			Audit:  &v1alpha1.SignalExport{Exporter: "siem"},
		}),
		obsDoc("bob-content", "", "bob", &v1alpha1.ObservabilityRule{Content: &v1alpha1.SignalExport{Exporter: "other-vault"}}),
		obsDoc("other-team", "ops", "", &v1alpha1.ObservabilityRule{Traces: &v1alpha1.TraceExport{Exporter: "ops"}}),
	}
	if rej := s.ApplyWire(docs); len(rej) != 0 {
		t.Fatalf("unexpected rejections without a ceiling: %+v", rej)
	}

	alice := s.Observability("eng", "alice")
	if len(alice.Traces) != 1 || alice.Traces[0] != (TraceExport{Exporter: "corp", SamplePercent: 50}) {
		t.Fatalf("alice traces = %+v, want union with max percent", alice.Traces)
	}
	if strings.Join(alice.Audit, ",") != "corp,siem" {
		t.Fatalf("alice audit = %v, want sorted union", alice.Audit)
	}
	if alice.Content != "vault" {
		t.Fatalf("alice content = %q, want the single agreed exporter", alice.Content)
	}

	bob := s.Observability("eng", "bob")
	if bob.Content != "" {
		t.Fatalf("bob content = %q: disagreeing content exporters must disable content", bob.Content)
	}
	if strings.Join(bob.Audit, ",") != "corp" {
		t.Fatalf("bob audit = %v", bob.Audit)
	}

	if none := s.Observability("sales", ""); len(none.Traces) != 0 || len(none.Audit) != 0 || none.Content != "" {
		t.Fatalf("unmatched subject must get a zero decision, got %+v", none)
	}
}

func TestObservabilityDecisionEmptyStore(t *testing.T) {
	var nilStore *Store
	if d := nilStore.Observability("eng", "alice"); len(d.Traces) != 0 || len(d.Audit) != 0 || d.Content != "" {
		t.Fatalf("nil store decision = %+v", d)
	}
	if d := NewEmptyStore().Observability("eng", "alice"); len(d.Traces) != 0 {
		t.Fatalf("empty store decision = %+v", d)
	}
}

func TestPoliciesCopyDoesNotAliasObservability(t *testing.T) {
	s := NewEmptyStore()
	s.ApplyWire([]v1alpha1.GovernancePolicy{obsDoc("obs", "eng", "", &v1alpha1.ObservabilityRule{Traces: &v1alpha1.TraceExport{Exporter: "corp", SamplePercent: percent(10)}})})
	s.Policies()[0].Rules[0].Observability.Traces.SamplePercent = 99
	if got := s.Observability("eng", "").Traces[0].SamplePercent; got != 10 {
		t.Fatalf("mutating Policies() changed the live snapshot: percent = %d", got)
	}
}

// The shipped example must stay a valid document (docs link to it).
func TestObservabilityExampleParses(t *testing.T) {
	policies, _, err := LoadPaths("../../examples/observability/policy.yaml")
	if err != nil {
		t.Fatalf("example: %v", err)
	}
	o := policies[0].Rules[0].Observability
	if o == nil || o.Traces.Exporter != "corp" || o.Traces.SamplePercent != 20 || o.Audit != "corp" || o.Content != "vault" {
		t.Fatalf("example rule = %+v", o)
	}
}
