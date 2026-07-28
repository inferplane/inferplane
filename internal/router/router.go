// Package router resolves a requested model name to a provider + upstream
// model id, with priority fallback and a per-provider circuit breaker
// (design doc §4.5). The topology (providers + routes) is read from a
// live.Holder so it can be hot-reloaded (ADR-006): ResolveChain takes one
// snapshot per call and returns it, so the caller bills the same generation.
// The breaker is keyed by provider IDENTITY (type+base_url) and persists
// across reloads for unchanged providers; RetainBreakers prunes the rest.
package router

import (
	"fmt"
	"time"

	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/metrics"
	"github.com/inferplane/inferplane/providers"
)

type Router struct {
	live    *live.Holder
	brk     *breaker
	metrics *metrics.Metrics // nil-safe: no-op when nil
}

func New(holder *live.Holder) *Router {
	// 5 consecutive failures → open, 1s base backoff (doubling, capped 30s).
	return &Router{live: holder, brk: newBreaker(5, time.Second)}
}

// SetMetrics attaches the Prometheus metrics sink. The circuit-state gauge is
// updated on every RecordResult. Pass nil (or never call) to disable.
func (r *Router) SetMetrics(m *metrics.Metrics) { r.metrics = m }

// Canonical resolves a configured model alias using one live snapshot.
func (r *Router) Canonical(model string) string {
	st := r.live.Load()
	return st.Canonical(model)
}

// ResolveModel canonicalizes an alias, then — only when the result has no
// route at all — substitutes the configured model_fallbacks/family-heuristic
// target (live.State.FallbackFor). served == requested (substituted == false)
// when nothing applies, including when the canonical name already has a
// route: a configured model is never second-guessed here, only an unrouted
// one. Call this in place of Canonical at ingress so RBAC/audit/metrics/
// pricing downstream all key off the model actually served.
func (r *Router) ResolveModel(requested string) (served string, substituted bool) {
	st := r.live.Load()
	canonical := st.Canonical(requested)
	if _, ok := st.Route(canonical); ok {
		return canonical, false
	}
	if fb := st.FallbackFor(canonical); fb != "" {
		return fb, true
	}
	return canonical, false
}

// Allows reports whether p's allow-list permits the (already-canonicalized)
// model. Ingress handlers canonicalize a REQUESTED alias before this check
// (ADR-021 F6) so an alias-only allow-list can never bypass RBAC on the
// request side. This closes the mirror-image gap: an allow-LIST entry that
// itself names an alias (an operator config mistake, or a valid alias they
// reasonably expect to grant access through) is also resolved to canonical
// before comparing, so a key configured with an alias in allowed_models
// isn't silently locked out of every request. Still exact-match once
// canonicalized — no broadening of what a key can reach.
func (r *Router) Allows(p keystore.Principal, model string) bool {
	if p.Allows(model) {
		return true
	}
	st := r.live.Load()
	for _, m := range p.AllowedModels {
		if st.Canonical(m) == model {
			return true
		}
	}
	return false
}

// ChainTarget is one resolved fallback target: the provider instance, its
// CONFIG provider name (pricing/metric key), the breaker Identity (type+base_url,
// captured from the generation this was resolved on so RecordResult records
// against the SAME generation — never a re-Loaded one), and the upstream model.
type ChainTarget struct {
	Provider     providers.Provider
	ProviderName string
	Identity     string
	Upstream     string
	// Model is the canonical model this target serves (D5's model-level
	// fallback). Equal to the model ResolveChain was called with for every
	// target of that model's own chain; differs only on a target appended
	// from a cross-model fallback (live.State.FallbackFor), which callers use
	// to detect the boundary and re-check RBAC (FilterModelAllowed) before
	// ever sending a request there.
	Model string
	// Region is the target provider's configured region label (D7, ADR-020),
	// captured from the generation this was resolved on. Empty = unlabeled.
	Region string
}

// ResolveChain returns every configured target for a model in priority order,
// skipping providers whose circuit breaker is open, and appends the targets
// of the model's configured fallback (live.State.FallbackFor) — if any — after
// them, so an upstream "model not found" can cross to a different model as
// well as a different provider (§4.5 extended). If ALL breakers are open
// across the WHOLE resulting chain it is returned anyway (better to try than
// hard-fail). Targets pointing at an unknown provider are silently skipped.
// Appended cross-model targets are NOT RBAC-checked here — the router has no
// Principal in scope; callers MUST run FilterModelAllowed on the result.
func (r *Router) ResolveChain(model string) ([]ChainTarget, *live.State, error) {
	st := r.live.Load() // one snapshot for this whole call — no mixed generations
	mc, ok := st.Route(model)
	if !ok || len(mc.Targets) == 0 {
		return nil, st, fmt.Errorf("router: no route for model %q", model)
	}
	models := []string{model}
	if fb := st.FallbackFor(model); fb != "" {
		if fbmc, ok := st.Route(fb); ok && len(fbmc.Targets) > 0 {
			models = append(models, fb)
		}
	}
	var allowed, all []ChainTarget
	for _, m := range models {
		rmc, _ := st.Route(m)
		for _, t := range rmc.Targets {
			p, ok := st.Provider(t.Provider)
			if !ok {
				continue // config drift: target points at unknown provider
			}
			id, _ := st.Identity(t.Provider)
			ct := ChainTarget{Provider: p, ProviderName: t.Provider, Identity: id, Upstream: t.Model, Model: m, Region: st.Region(t.Provider)}
			all = append(all, ct)
			if r.brk.Allow(id) {
				allowed = append(allowed, ct)
			}
		}
	}
	if len(all) == 0 {
		return nil, st, fmt.Errorf("router: model %q points at unknown provider(s)", model)
	}
	if len(allowed) == 0 {
		return all, st, nil // all breakers open → try anyway
	}
	return allowed, st, nil
}

// FilterModelAllowed drops every target whose Model the caller is not
// permitted to reach (per `allowed`, typically func(m) bool { return
// router.Allows(p, m) }). ResolveChain appends cross-model fallback targets
// AFTER the ingress allow-list check has already run against the originally
// requested model, so those targets must be re-checked here or a key allowed
// only model A would silently reach model B. Never empties the chain: the
// primary model's targets were already allowed before ResolveChain ran.
func FilterModelAllowed(chain []ChainTarget, allowed func(model string) bool) []ChainTarget {
	if len(chain) == 0 {
		return chain
	}
	primary := chain[0].Model
	out := chain[:0:0]
	for _, ct := range chain {
		if ct.Model == primary || allowed(ct.Model) {
			out = append(out, ct)
		}
	}
	return out
}

// FilterRegions drops every target whose Region is not in allowed (D7,
// ADR-020). A target with an EMPTY Region is always dropped when allowed is
// non-empty — an unlabeled provider cannot prove residency, so a
// region-restricted team fails closed rather than silently reaching it. An
// empty allowed means unrestricted: the chain passes through unchanged (and is
// not even copied, so a no-policy team pays no allocation cost).
func FilterRegions(chain []ChainTarget, allowed []string) []ChainTarget {
	if len(allowed) == 0 {
		return chain
	}
	set := make(map[string]bool, len(allowed))
	for _, r := range allowed {
		set[r] = true
	}
	var out []ChainTarget
	for _, ct := range chain {
		if ct.Region != "" && set[ct.Region] {
			out = append(out, ct)
		}
	}
	return out
}

// RecordResult feeds a per-provider call outcome to the circuit breaker, keyed
// by the breaker IDENTITY captured when the request resolved (passed via the
// ChainTarget, NOT re-Loaded here) so the outcome is always recorded against
// the generation the call actually ran on. The metric label is the config
// provider name (cardinality-bounded). A stale identity whose provider was
// pruned by a concurrent reload is never consulted by ResolveChain (which only
// checks current-generation identities) and is reaped by the next reload's
// RetainBreakers, so recording against it is harmless.
func (r *Router) RecordResult(providerName, identity string, ok bool) {
	if ok {
		r.brk.RecordSuccess(identity)
	} else {
		r.brk.RecordFailure(identity)
	}
	r.metrics.SetCircuitState(providerName, r.brk.State(identity))
}

// RetainBreakers drops breaker entries whose identity is absent from the given
// generation (config name → identity), so a removed (or re-pointed) provider
// leaves no stale circuit state. Called by the reloader after a Swap.
func (r *Router) RetainBreakers(identities map[string]string) {
	keep := make(map[string]bool, len(identities))
	for _, id := range identities {
		keep[id] = true
	}
	r.brk.Retain(keep)
}

// Resolve returns the provider and upstream model id for a requested model.
func (r *Router) Resolve(model string) (providers.Provider, string, error) {
	st := r.live.Load()
	mc, ok := st.Route(model)
	if !ok || len(mc.Targets) == 0 {
		return nil, "", fmt.Errorf("router: no route for model %q", model)
	}
	t := mc.Targets[0]
	p, ok := st.Provider(t.Provider)
	if !ok {
		return nil, "", fmt.Errorf("router: model %q points at unknown provider %q", model, t.Provider)
	}
	return p, t.Model, nil
}

// ResolveProvider is like Resolve but also returns the CONFIG provider name
// (the key under `providers:` in config, e.g. "anthropic-direct"), which is the
// first element of the pricing table key. The provider's own Name() reports its
// TYPE ("anthropic"/"bedrock"), not the config name, so callers that key
// pricing must use this config name to stay consistent with Bundled() and
// config overrides.
func (r *Router) ResolveProvider(model string) (prov providers.Provider, providerName, upstream string, err error) {
	st := r.live.Load()
	mc, ok := st.Route(model)
	if !ok || len(mc.Targets) == 0 {
		return nil, "", "", fmt.Errorf("router: no route for model %q", model)
	}
	t := mc.Targets[0]
	p, ok := st.Provider(t.Provider)
	if !ok {
		return nil, "", "", fmt.Errorf("router: model %q points at unknown provider %q", model, t.Provider)
	}
	return p, t.Provider, t.Model, nil
}

// AllModels returns every configured model name (for /v1/models; the ingress
// filters by the virtual key's allow-list).
func (r *Router) AllModels() []string {
	return r.live.Load().ModelNames()
}
