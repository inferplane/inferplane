package audit

import "context"

// substitutedFromKey is the request-context key an ingress handler uses to
// carry the ORIGINAL requested model past an ADR-041 budget-tier
// substitution, down to the audit call — which by that point only sees the
// model actually served (RequestRef.ModelRequested; RBAC, pricing, and
// metrics all key off it too, per the ADR-041 design). Without this, the
// client's real request would be invisible in the audit trail.
type substitutedFromKey struct{}

// WithSubstitutedFrom returns a context carrying the original requested
// model. Call it once, right after SubstituteTier reports a substitution,
// before continuing the request with req.WithContext(ctx).
func WithSubstitutedFrom(ctx context.Context, from string) context.Context {
	return context.WithValue(ctx, substitutedFromKey{}, from)
}

// SubstitutedFrom returns the value WithSubstitutedFrom set, or "" if no
// substitution occurred for this request.
func SubstitutedFrom(ctx context.Context) string {
	from, _ := ctx.Value(substitutedFromKey{}).(string)
	return from
}

// paramsStrippedKey carries the parameter names the PROVIDER dropped before
// egress (ProxyRequest.ParamsStripped, strategy P1 "undisclosed request
// mutation") from the point the provider call returned down to the audit
// call — the same one-way plumbing as substitutedFromKey above.
type paramsStrippedKey struct{}

// WithParamsStripped returns a context carrying the stripped parameter
// names. Call it once, right after the provider call reports a strip,
// before continuing the request with req.WithContext(ctx).
func WithParamsStripped(ctx context.Context, params []string) context.Context {
	return context.WithValue(ctx, paramsStrippedKey{}, params)
}

// ParamsStrippedFrom returns the value WithParamsStripped set, or nil if
// nothing was stripped for this request.
func ParamsStrippedFrom(ctx context.Context) []string {
	params, _ := ctx.Value(paramsStrippedKey{}).([]string)
	return params
}
