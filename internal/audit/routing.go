package audit

import (
	"context"
	"slices"
)

// RoutingRef is a scalar-only projection of the routing decision. It must not
// contain a provider, topology, credentials, prompt, or detected value.
// This package deliberately has no dependency on router, config or server.
type RoutingRef struct {
	RequestedModel  string                  `json:"requested_model"`
	SelectedModel   string                  `json:"selected_model,omitempty"`
	ProposedModel   string                  `json:"proposed_model,omitempty"`
	Mode            string                  `json:"mode,omitempty"`
	Reason          string                  `json:"reason"`
	Inspection      string                  `json:"inspection"`
	Privacy         string                  `json:"privacy,omitempty"`
	Categories      []string                `json:"categories,omitempty"`
	Policies        []RoutingPolicyRef      `json:"policies,omitempty"`
	Recommendations []RoutingRecommendation `json:"recommendations,omitempty"`
	PlannedProvider string                  `json:"planned_provider,omitempty"`
	PlannedBoundary string                  `json:"planned_boundary,omitempty"`
	ActualModel     string                  `json:"actual_model,omitempty"`
	ActualProvider  string                  `json:"actual_provider,omitempty"`
	ActualBoundary  string                  `json:"actual_boundary,omitempty"`
	Masked          bool                    `json:"masked,omitempty"`
	Classifiers     []ClassifierRef         `json:"classifiers,omitempty"`
	// Append-only ADR-029 evidence: an unrouted requested model (the client's
	// string, bounded) was served by a configured model, the record's
	// request.model_requested. Source is "model_fallbacks" or "family".
	ModelFallbackFrom   string `json:"model_fallback_from,omitempty"`
	ModelFallbackSource string `json:"model_fallback_source,omitempty"`
}

type RoutingPolicyRef struct {
	Name       string `json:"name"`
	Generation int64  `json:"generation"`
	Rule       string `json:"rule"`
}

type RoutingRecommendation struct {
	Policy RoutingPolicyRef `json:"policy"`
	Model  string           `json:"model"`
	Reason string           `json:"reason"`
}

// ClassifierRef is the bounded evidence of one PII classifier entry. It never
// carries text, windows, probabilities, digests, endpoints or tokens.
type ClassifierRef struct {
	Name          string `json:"name"`
	Revision      string `json:"revision,omitempty"`
	Boundary      string `json:"boundary"`
	Mode          string `json:"mode"`
	Result        string `json:"result"`
	WouldRestrict bool   `json:"would_restrict,omitempty"`
}

type routingKey struct{}
type modelFallbackKey struct{}

// maxFallbackRequested bounds the client-supplied model name kept as evidence.
const maxFallbackRequested = 256

// WithModelFallback records an applied model fallback for every record of the
// request. RoutingFrom attaches it to the routing decision; a request without
// one (no applicable policy) gets a routing ref whose reason is
// "model_fallback". served is the model the request is now resolved to.
func WithModelFallback(ctx context.Context, requested, served, source string) context.Context {
	if len(requested) > maxFallbackRequested {
		requested = requested[:maxFallbackRequested]
	}
	return context.WithValue(ctx, modelFallbackKey{}, &RoutingRef{
		RequestedModel: requested, SelectedModel: served, Reason: "model_fallback", Inspection: "not_inspected",
		ModelFallbackFrom: requested, ModelFallbackSource: source,
	})
}

func cloneRouting(r *RoutingRef) *RoutingRef {
	if r == nil {
		return nil
	}
	c := *r
	c.Categories = slices.Clone(r.Categories)
	c.Policies = slices.Clone(r.Policies)
	c.Recommendations = slices.Clone(r.Recommendations)
	c.Classifiers = slices.Clone(r.Classifiers)
	return &c
}

// WithRouting takes a copy, so neither the caller nor an attempt can change the
// decision carried by started records waiting in the asynchronous writer.
func WithRouting(ctx context.Context, r *RoutingRef) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, routingKey{}, cloneRouting(r))
}

// RoutingFrom returns an owned projection for asynchronous record submission.
func RoutingFrom(ctx context.Context) *RoutingRef {
	r, _ := ctx.Value(routingKey{}).(*RoutingRef)
	fallback, _ := ctx.Value(modelFallbackKey{}).(*RoutingRef)
	if r == nil {
		return cloneRouting(fallback)
	}
	out := cloneRouting(r)
	if out.ModelFallbackSource == "" && fallback != nil {
		out.ModelFallbackFrom, out.ModelFallbackSource = fallback.ModelFallbackFrom, fallback.ModelFallbackSource
	}
	return out
}

// WithRoutingAttempt derives a NEW context for one actual provider call. Never
// attach actual fields during routing, masking, context checks or admission.
func WithRoutingAttempt(ctx context.Context, model, provider, boundary string) context.Context {
	r := RoutingFrom(ctx)
	if r == nil {
		return ctx
	}
	if boundary == "" {
		boundary = "unknown"
	}
	r.ActualModel, r.ActualProvider, r.ActualBoundary = model, provider, boundary
	return context.WithValue(ctx, routingKey{}, r)
}
