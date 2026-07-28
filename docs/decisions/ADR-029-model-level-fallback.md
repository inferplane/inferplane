# ADR-029: model-level fallback (unrouted-model and cross-model-404)

**Date:** 2026-07-28
**Status:** Accepted
**Related:** §4.5 (priority fallback chain / circuit breaker), ADR-021 (model
aliases — the `Canonical` resolution step this ADR extends)

## Context

Claude Code / OpenCode hardcode a model id per release (e.g. `claude-opus-5`).
When a client requests a model the operator hasn't added to `models` yet, the
gateway 404s at `ResolveChain` and the client is broken until someone edits
config and redeploys. The same happens one layer in when the model *is*
configured but the specific upstream provider rejects it as unknown (Anthropic
`not_found_error`, or Bedrock `ValidationException` for a model not enabled in
that region).

Today's fallback (§4.5) is **provider-level only**: an ordered `targets` array
per model, retried on transport error / 429 / 5xx. It has no notion of
substituting a *different model*.

## Decision

Two triggers, implemented at two different layers, both operator-configurable
via `model_fallbacks` (requested → served model, one hop, same posture as
model aliases) and `model_fallback_family` (default true).

**Case 1 — unknown model, substitute before RBAC.** `router.Router.ResolveModel`
canonicalizes the request, then — only when the canonical name has no route at
all — substitutes `live.State.FallbackFor`'s result: the explicit
`model_fallbacks` entry if any, else the highest configured version strictly
below the requested one within the same name family (`claude-opus-5` →
`claude-opus-4-8`; a name with no numeric version tail, e.g.
`claude-sonnet-4-6-bedrock`, is never a family candidate). Ingress handlers
call `ResolveModel` in place of `Canonical` at the top of the request, so
everything downstream — the allow-list check, `ResolveChain`, metrics label,
audit record, pricing — keys off the *served* model with no further change. A
key whose allow-list names only the unconfigured requested model is denied
(403), never silently downgraded — fail-closed, consistent with every other
allow-list mismatch.

**Case 2 — configured model, upstream says "not found", extend the chain.**
`router.ChainTarget` gains a `Model` field. `ResolveChain` appends the
fallback model's targets after the primary model's own targets (same
breaker filtering). Because the allow-list check already ran against the
primary model alone before `ResolveChain`, the appended cross-model targets
are **not** RBAC-checked by the router — it has no `Principal` in scope. Every
ingress handler calls the new pure function `router.FilterModelAllowed(chain,
allowedFn)` immediately after `ResolveChain` (mirroring the existing
`FilterRegions` pattern) to re-check those targets' model before ever sending
a request there. In the retry loop, a "model not found" response (a bare 404,
or a 400 whose body contains `ValidationException`) becomes retriable **only**
across a model boundary (`crossModelNext`) — a same-model provider fallback
retries on 429/5xx exactly as before; an unrelated 400 always stays a client
error, teed verbatim. On a cross-model transition, the response carries
`x-inferplane-model-fallback: <served model>` (mirrors the existing
`x-inferplane-fallback: <provider>` header) and `ObserveFallback` records
reason `model_not_found` instead of `upstream_error`.

## Why this shape

- **No provider diff.** `providers/anthropic`'s `buildUpstream` already
  patches only the top-level `model` field via `rewriteTopLevelModel(RawBody,
  Upstream)`, so §4.4's verbatim/cache invariant survives a model substitution
  unmodified.
- **No pricing/audit-schema diff.** `governance.Settle` already keys cost off
  `(providerName, upstream)`, not the canonical model name, so cost is correct
  across a model boundary for free.
- **RBAC re-check is structural, not optional.** Skipping
  `FilterModelAllowed` would let a key allowed only model A silently reach a
  cross-model fallback model B — the one genuinely new security surface this
  ADR introduces, closed the same way §4.5's region lock closes its analogous
  gap (`FilterRegions`).
- **The 400-vs-404 narrowing is deliberate.** Only Bedrock returns 400 for an
  unknown/disabled model; treating an *arbitrary* 400 as retriable would
  replay unrelated client errors (bad `max_tokens`, malformed content) across
  provider/model boundaries, wasting a governance pre-check's budget debit on
  every retry for no chance of success.

## Consequences

- `/v1/models` does **not** advertise fallback keys (listing
  `claude-opus-5` as available when it's actually served by
  `claude-opus-4-8` would be misleading).
- No chained fallback (A→B→C) — one hop only, same as model aliases.
- `count_tokens` (never allowed to return non-200) and the Bedrock
  URL-path model resolution both apply the Case 1 substitution the same way,
  but neither exercises the RBAC-recheck path: `count_tokens` has no
  allow-list check today (pre-existing, unrelated to this ADR), and Bedrock's
  own allow-list check runs against the *already-substituted* served model.
