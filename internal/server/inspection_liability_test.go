package server

import (
	"context"
	"testing"

	"github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/sensitivity"
	"github.com/inferplane/inferplane/internal/server/routingtest"
)

type liabilityInspectionScenario struct {
	ingress policyIngress
	mode    string
}

func liabilityInspectionScenarios(ingresses []policyIngress) []liabilityInspectionScenario {
	var cases []liabilityInspectionScenario
	for _, ingress := range ingresses {
		for _, mode := range []string{"none", "shadow", "privacy-shadow"} {
			cases = append(cases, liabilityInspectionScenario{ingress, mode})
		}
	}
	return cases
}

type liabilityModeInspector struct{ modes []sensitivity.InspectionMode }

func (i *liabilityModeInspector) Inspect(ctx context.Context, protocol string, raw []byte) (sensitivity.Result, error) {
	return sensitivity.NewInspector().Inspect(ctx, protocol, raw)
}
func (i *liabilityModeInspector) InspectWithMode(ctx context.Context, protocol string, raw []byte, mode sensitivity.InspectionMode) (sensitivity.Result, error) {
	i.modes = append(i.modes, mode)
	return sensitivity.NewInspector().(sensitivity.SelectiveInspector).InspectWithMode(ctx, protocol, raw, mode)
}

func qualifyLiabilityInspection(f *routingtest.Fixture, mode string) *liabilityModeInspector {
	if mode != "none" {
		f.Policies = []*policy.Policy{routingtest.Context(v1alpha1.Shadow)}
	}
	if mode == "privacy-shadow" {
		f.Policies = append(f.Policies, routingtest.Privacy(v1alpha1.Block))
	}
	i := &liabilityModeInspector{}
	f.Router.SetRequestInspector(i)
	return i
}

func requireLiabilityInspection(t *testing.T, i *liabilityModeInspector, protocol, mode string) {
	t.Helper()
	if mode == "none" && protocol != "responses" {
		if len(i.modes) != 0 {
			t.Fatal("unprotected legacy routing unexpectedly inspected")
		}
		return
	}
	want := sensitivity.StructureOnly
	if mode == "privacy-shadow" {
		want = sensitivity.DetectSensitiveData
	}
	if len(i.modes) != 1 || i.modes[0] != want {
		t.Fatalf("accounting scenario used wrong inspection: %v, want %v", i.modes, want)
	}
}
