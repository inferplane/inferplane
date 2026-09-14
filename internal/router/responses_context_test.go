package router

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/responses"
	"github.com/inferplane/inferplane/internal/sensitivity"
)

func responsesContextConfig() *config.Config {
	cfg := routingConfig()
	for name, pc := range cfg.Providers {
		pc.Type = "request-routing-openai_compatible"
		cfg.Providers[name] = pc
	}
	for name, mc := range cfg.Models {
		mc.ContextWindow = 4096
		mc.Capabilities = []string{"tools"}
		cfg.Models[name] = mc
	}
	return cfg
}

func responsesContextSetup(t *testing.T, cfg *config.Config, tools bool) (*Router, RequestRoutingInput) {
	t.Helper()
	r, in := routingSetup(t, cfg)
	// Entirely synthetic, exactly 8192 wire bytes: the existing ingress
	// estimate is 2048 input tokens, plus the explicit 64-token output bound.
	prefix := `{"model":"premium","input":"Contact person@example.test. `
	suffix := `","max_output_tokens":64`
	if tools {
		suffix += `,"tools":[{"type":"function","name":"read","strict":false,"parameters":{"type":"object"}}]`
	}
	suffix += `}`
	raw := []byte(prefix + strings.Repeat("x", 8192-len(prefix)-len(suffix)) + suffix)
	if err := responses.ValidateConversion(raw); err != nil {
		t.Fatalf("synthetic request is not portable: %v", err)
	}
	view, err := sensitivity.NewInspector().Inspect(context.Background(), "responses", raw)
	if err != nil || !view.Complete || view.InputTokens <= 4096 || view.OutputTokens != 64 ||
		view.HasTools != tools || view.HasVision || view.HasReasoning || view.HasStructuredOutput {
		t.Fatalf("fixture must exceed conservative capacity with portable content: %+v, %v", view, err)
	}
	in.Protocol, in.RawBody = "responses", raw
	in.Compatible = func(ChainTarget) bool { return responses.ValidateConversion(raw) == nil }
	return r, in
}

func TestResponsesRequestedContextUsesIngressEstimate(t *testing.T) {
	for _, requested := range []string{"premium", "premium-alias", ""} {
		t.Run("requested="+requested, func(t *testing.T) {
			cfg := responsesContextConfig()
			mc := cfg.Models["premium"]
			mc.Aliases = []string{"premium-alias"}
			cfg.Models["premium"] = mc
			r, in := responsesContextSetup(t, cfg, true)
			in.RequestedModel = requested
			before := bytes.Clone(in.RawBody)
			got, err := r.RouteRequest(context.Background(), in)
			if err != nil || got.Model != "premium" || len(got.Chain) != 1 || got.Chain[0].Model != "premium" {
				t.Fatalf("explicit portable request lost its route: %+v, %v", got, err)
			}
			if !bytes.Equal(in.RawBody, before) || got.State != in.State ||
				got.Decision.Inspection != "complete" || !slices.Contains(got.Decision.Categories, "email") {
				t.Fatal("capacity adjustment changed request bytes, topology or inspection evidence")
			}
		})
	}
}

func TestResponsesRequestedContextRetainsAdmissionGuards(t *testing.T) {
	for _, tt := range []struct {
		name   string
		window int64
		deny   bool
	}{
		{"exact input plus output", 2112, false},
		{"output exceeds remaining context", 2111, true},
		{"input exceeds context", 2047, true},
		{"unknown context", 0, true},
		{"missing tools capability", 4096, true},
		{"missing price", 4096, true},
		{"region denied", 4096, true},
		{"negative input estimate", 4096, true},
		{"negative output estimate", 4096, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := responsesContextConfig()
			mc := cfg.Models["premium"]
			mc.ContextWindow = tt.window
			if tt.name == "missing tools capability" {
				mc.Capabilities = nil
			}
			cfg.Models["premium"] = mc
			if tt.name == "missing price" {
				delete(cfg.Pricing.Overrides["public"], "premium-upstream")
			}
			r, in := responsesContextSetup(t, cfg, true)
			if tt.name == "region denied" {
				in.AllowedRegions = []string{"ap"}
			}
			if strings.HasPrefix(tt.name, "negative ") {
				view := sensitivity.Result{Complete: true, HasTools: true, InputTokens: 8192, OutputTokens: 64}
				if tt.name == "negative input estimate" {
					view.InputTokens = -1
				} else {
					view.OutputTokens = -1
				}
				r.SetRequestInspector(inspectFunc(func(context.Context, string, []byte) (sensitivity.Result, error) {
					return view, nil
				}))
			}
			got, err := r.RouteRequest(context.Background(), in)
			if tt.deny {
				requireDenied(t, got, err, "no_safe_route")
			} else if err != nil || len(got.Chain) != 1 {
				t.Fatalf("exact context fit was refused: %+v, %v", got, err)
			}
		})
	}
}

func TestResponsesRequestedContextKeepsConservativeInspectionSignals(t *testing.T) {
	r, in := responsesContextSetup(t, responsesContextConfig(), true)
	p := contextPolicy("context", v1alpha1.Shadow, "private")
	p.Rules[0].Routing.Context.MaxSimpleInputTokens = 4096
	installRoutingPolicies(r, p)
	got, err := r.RouteRequest(context.Background(), in)
	if err != nil || got.Model != "premium" || got.Decision.Reason != "context_shadow" ||
		got.Decision.ProposedModel != "premium" || len(got.Decision.Recommendations) != 1 ||
		got.Decision.Recommendations[0].Reason != "input_threshold" {
		t.Fatalf("capacity estimate leaked into context classification: %+v, %v", got, err)
	}
}

func TestResponsesRequestedContextDoesNotRelaxModelFallback(t *testing.T) {
	cfg := responsesContextConfig()
	cfg.ModelFallbacks["premium"] = "private"
	r, in := responsesContextSetup(t, cfg, true)
	got, err := r.RouteRequest(context.Background(), in)
	if err != nil || len(got.Chain) != 1 || got.Chain[0].Model != "premium" {
		t.Fatalf("undersized alternate survived beside explicit model: %+v, %v", got, err)
	}
	// An ingress-promoted fallback must not inherit the requested-model estimate.
	in.Chain = in.Chain[1:]
	got, err = r.RouteRequest(context.Background(), in)
	requireDenied(t, got, err, "no_safe_route")
}

func TestResponsesAutomaticContextKeepsConservativeCapacity(t *testing.T) {
	for _, window := range []int64{4096, 20000} {
		t.Run(map[int64]string{4096: "undersized preference", 20000: "undersized original fallback"}[window], func(t *testing.T) {
			cfg := responsesContextConfig()
			mc := cfg.Models["economy"]
			mc.ContextWindow = window
			cfg.Models["economy"] = mc
			cfg.ModelFallbacks["economy"] = "premium"
			r, in := responsesContextSetup(t, cfg, false)
			p := contextPolicy("context", v1alpha1.Enforce, "economy")
			p.Rules[0].Routing.Context.MaxSimpleInputTokens = 20000
			installRoutingPolicies(r, p)
			got, err := r.RouteRequest(context.Background(), in)
			wantModel, wantReason := "premium", "context_unavailable"
			if window == 20000 {
				wantModel, wantReason = "economy", "context_selected"
			}
			if err != nil || got.Model != wantModel || got.Decision.Reason != wantReason ||
				len(got.Chain) != 1 || got.Chain[0].Model != wantModel {
				t.Fatalf("automatic chain used the explicit-request estimate: %+v, %v", got, err)
			}
		})
	}
}

func TestResponsesRestrictedTargetsKeepConservativeCapacity(t *testing.T) {
	for _, name := range []string{"legacy substitute", "strict original", "strict substitute", "internal original", "internal substitute"} {
		t.Run(name, func(t *testing.T) {
			cfg := responsesContextConfig()
			if name == "internal original" {
				pc := cfg.Providers["public"]
				pc.DataBoundary = "internal"
				cfg.Providers["public"] = pc
			}
			r, in := responsesContextSetup(t, cfg, true)
			reason := "no_safe_route"
			switch name {
			case "legacy substitute", "strict substitute":
				in.Model = "private"
				in.Chain, in.State, _ = r.ResolveChain(in.Model)
				if name == "strict substitute" {
					setBudgetConstraint(t, r, map[string]string{"premium": "private"})
					reason = "budget_target_unavailable"
				}
			case "strict original":
				setBudgetConstraint(t, r, map[string]string{"premium": "premium"})
				reason = "budget_target_unavailable"
			case "internal original":
				installRoutingPolicies(r, privatePolicy("privacy", "premium"))
			case "internal substitute":
				installRoutingPolicies(r, privatePolicy("privacy", "private"))
			}
			got, err := r.RouteRequest(context.Background(), in)
			requireDenied(t, got, err, reason)
		})
	}
}
