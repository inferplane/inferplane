package providers

import "testing"

type plainProvider struct{ Provider }

type authorityProvider struct {
	Provider
	upstream string
}

func (a authorityProvider) EnforcesContextWindow(upstream string) bool { return upstream == a.upstream }

func TestEnforcesContextWindowIsAnOptionalPerUpstreamCapability(t *testing.T) {
	if EnforcesContextWindow(nil, "m") || EnforcesContextWindow(plainProvider{}, "m") {
		t.Fatal("a provider without the capability must keep the gateway's own window check")
	}
	a := authorityProvider{upstream: "m"}
	if !EnforcesContextWindow(a, "m") || EnforcesContextWindow(a, "other") {
		t.Fatal("the capability must be answered per upstream model")
	}
}
