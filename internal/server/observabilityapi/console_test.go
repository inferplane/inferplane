package observabilityapi

import (
	"errors"
	"path/filepath"
	"testing"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
)

// Decision 9: the console rewrites its file wholesale, so anything it cannot
// read back losslessly must be foreign. Each row breaks exactly one clause of
// ruleFromDoc.
func TestRuleFromDocGuard(t *testing.T) {
	obs := func() *v1alpha1.ObservabilityRule {
		return &v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}}
	}
	doc := func(name string, rules ...v1alpha1.Rule) v1alpha1.GovernancePolicy {
		d := v1alpha1.GovernancePolicy{Metadata: v1alpha1.ObjectMeta{Name: name}}
		d.Spec.Subject.Team = "platform"
		d.Spec.Rules = rules
		return d
	}
	export := func(mut func(*v1alpha1.Rule)) v1alpha1.Rule {
		r := v1alpha1.Rule{Name: "export", FailurePolicy: v1alpha1.FailOpen, Observability: obs()}
		if mut != nil {
			mut(&r)
		}
		return r
	}
	valid := export(nil)
	rows := []struct {
		name    string
		d       v1alpha1.GovernancePolicy
		foreign bool
	}{
		{"valid console document", doc("console-platform", valid), false},
		{"non-console name", doc("platform", valid), true},
		{"bare console prefix", doc("console-", valid), true},
		{"two rules", doc("console-platform", valid, valid), true},
		{"no rules", doc("console-platform"), true},
		{"other rule name", doc("console-platform", export(func(r *v1alpha1.Rule) { r.Name = "money" })), true},
		{"FailClosed", doc("console-platform", export(func(r *v1alpha1.Rule) { r.FailurePolicy = v1alpha1.FailClosed })), true},
		{"observability absent", doc("console-platform", export(func(r *v1alpha1.Rule) { r.Observability = nil })), true},
		{"plus budget", doc("console-platform", export(func(r *v1alpha1.Rule) { r.Budget = &v1alpha1.BudgetRule{} })), true},
		{"plus rate", doc("console-platform", export(func(r *v1alpha1.Rule) { r.Rate = &v1alpha1.RateRule{} })), true},
		{"plus tokenQuota", doc("console-platform", export(func(r *v1alpha1.Rule) { r.TokenQuota = &v1alpha1.TokenQuotaRule{} })), true},
		{"plus modelAccess", doc("console-platform", export(func(r *v1alpha1.Rule) { r.ModelAccess = &v1alpha1.ModelAccessRule{} })), true},
		{"plus routing", doc("console-platform", export(func(r *v1alpha1.Rule) { r.Routing = &v1alpha1.RoutingRule{} })), true},
		{"plus sensitiveData", doc("console-platform", export(func(r *v1alpha1.Rule) { r.SensitiveData = &v1alpha1.SensitiveDataRule{} })), true},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			r, err := ruleFromDoc(tc.d)
			if tc.foreign {
				if !errors.Is(err, errForeign) {
					t.Fatalf("err = %v, want errForeign", err)
				}
				return
			}
			if err != nil || r.Name != "console-platform" || r.Audit == nil || r.Audit.Exporter != "corp" || r.Subject.Team != "platform" {
				t.Fatalf("rule = %+v, err = %v", r, err)
			}
		})
	}
}

// A console file that does not parse is foreign (read-only), never read_failed.
func TestReadConsoleMalformedIsForeign(t *testing.T) {
	path := filepath.Join(t.TempDir(), "console-observability.yaml")
	writeFile(t, path, "apiVersion: [unterminated\n  kind: : :\n")
	c := New(Options{PolicyFile: path})
	if _, _, _, err := c.readConsole(); !errors.Is(err, errForeign) {
		t.Fatalf("err = %v, want errForeign", err)
	}
}
