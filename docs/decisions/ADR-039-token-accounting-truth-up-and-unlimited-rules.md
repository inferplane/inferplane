# ADR-039: TPM true-up, cache-aware quota, and explicit unlimited rules

**Date:** 2026-08-14
**Status:** Accepted (implemented)
**Related:** ADR-030 (cost settlement correctness — the pricing-path bug this
ADR closes the quota-path and rate-path equivalent of), ADR-031 (policy the
single truth shared by both binaries), ADR-032 (policy units/subjects/cadence
— where `Rate`/`Budget`'s wire shape is defined), ADR-034 (budget leases —
the mechanism that actually bounds cross-instance budget overshoot, distinct
from the TPM fix below)

## Context

A design review of the current codebase found the token-accounting layer
wrong in three independent ways, discovered together because they share one
symptom: Claude Code's traffic is cache-heavy (the same 150k+ token prefix
resent on most requests, mostly served from cache), and the flagship ECS demo
policy (`examples/policies-ecs-demo/demo.yaml`) had its `rate` rule deleted
entirely in an uncommitted change, with a comment naming the reason —
`team_token_rate_limited` 429s on ordinary cache-creation bursts. A governance
product whose own reference deployment cannot keep a rate rule enabled is a
real defect, not a tuning problem.

1. **TPM was charged once, at `PreCheck`, and never corrected.**
   `internal/governance.Governor.PreCheck` charges a coarse `len(raw)/4` byte
   estimate of the request body against the team's and the key's TPM token
   bucket (`internal/limiter.Memory.AllowRate`, which debits `cost` at check
   time — there was no second phase). Claude Code resends the full cached
   prefix on every call, so the estimate counts bytes a cache HIT serves for
   roughly a tenth of the load a cache MISS would — the estimate becomes a
   permanent overcharge with no path back to reality. This is structurally
   the same bug class ADR-030 found on the pricing path, one layer over: the
   input to the arithmetic was wrong, not the arithmetic.

2. **The daily token quota ignored cache tokens.** `Governor.Settle` debited
   `quota:<team>` by `u.Input + u.Output` only. `pricing.Usage` has carried
   `CacheRead`/`CacheWrite5m`/`CacheWrite1h` since ADR-030, and pricing already
   consumes them — quota simply never did. A cache-read-heavy team's daily
   quota usage was effectively never charged, the mirror image of finding 1.

3. **There was no way to declare "no cap, on purpose."** `internal/policy`
   requires a `rate` rule to limit at least one of rpm/tpm, and a `budget`
   rule to declare `limitMilliUSD > 0` — so the only way to represent
   "unlimited" was to delete the rule. Deleting the rule also deletes its
   observability: there is no way to distinguish, from the policy document
   alone, "we decided this dimension has no cap" from "nobody ever set a
   limit here." The demo policy's deleted rate rule is exactly this failure
   mode: the fix for problem 1 removes the pressure to delete the rule, but
   an explicit escape hatch is still worth having independently — a
   governance product should let "no cap" be a first-class, auditable
   decision.

## Decision

**Fix 1 — TPM true-up.** `LimiterStore` gains `AdjustRate(key string, delta,
burst int64)`: a post-hoc balance correction, not a second refill. `Settle`
now takes the exact estimate `PreCheck` charged (threaded through via each
ingress package's existing `pr.RawBody` — the same bytes `PreCheck` already
estimated from, so no new parameter needed on the request path) and, once
real usage is known, computes `actualTokens = Input + Output + CacheRead +
CacheWrite5m + CacheWrite1h` and calls `AdjustRate(key, estimatedTokens -
actualTokens, burst)` on both the team's and the key's TPM buckets. Positive
delta credits back an over-charge; negative debits an under-charge.

`AdjustRate` deliberately does **not** floor a debit at zero: a bucket driven
negative stays blocked until real refill time repays it. Flooring at zero
would make a chronic under-estimate free to true up every time, which is
exactly the gap a rate limiter exists to close.

**Fix 2 — quota counts cache tokens.** `Settle`'s daily-quota debit now uses
the same `actualTokens` total as fix 1, instead of `Input + Output` alone.

**Fix 3 — explicit `unlimited` sentinel.** `api/v1alpha1.RateRule` and
`BudgetRule` each gain an `unlimited` boolean. `internal/policy.FromV1Alpha1`
accepts it as an alternative to a numeric limit (rejecting the combination of
`unlimited` with `rpm`/`tpm`, or with `limitMilliUSD`/`hardCap`/`lease`/
`adminContact` — an unlimited rule that also tries to be a normal one is a
contradiction, not a preference). Internally, `Unlimited: true` still
produces the pre-existing zero-value sentinel every consumer already treats
as "no cap" (`governance.TeamPolicy.TokensPerMinute == 0`, etc.) — the flag
exists purely so a policy diff/audit can distinguish a decision from an
absence, not to introduce a new enforcement code path.

Two merge-layer bugs surfaced by adding a legitimate `LimitMicroUSD: 0`/
`LeaseRenewInterval: 0` rule (previously impossible, since a real budget rule
always had `LimitMilliUSD > 0`):

- `internal/policy.mergeTeamLimits`'s most-restrictive-wins comparison used
  `r.Budget.LimitMicroUSD < tl.BudgetMicrosPerMonth` to decide whether a new
  rule narrows the binding budget. An unlimited rule (`LimitMicroUSD: 0`)
  processed *after* a real one satisfied that comparison and silently erased
  the real cap. Fixed by giving `Unlimited` its own branch that never
  compares against or overwrites the accumulator — Rate's equivalent merge
  was already safe, because `minNonZero(a, 0)` returns `a` in both argument
  orders.
- `internal/controlplane`'s `applyWire` built a lease-ledger entry for every
  budget rule and used `minRenew == 0 || r.Budget.LeaseRenewInterval <
  minRenew` to compute the heartbeat interval — a rule with
  `LeaseRenewInterval: 0` always wins that comparison (`0` is the sentinel for
  "not yet set"), so an unlimited rule could silently discard a real rule's
  renew interval. Fixed by skipping unlimited rules before the ledger loop
  entirely: they have nothing to lease (no cap to protect locally), so they
  should not participate in ledger construction or interval computation at
  all — not merely contribute a value that happens to be harmless.

The CRD manifest (`deploy/crd/inferplane.dev_governancepolicies.yaml`, ADR-035)
gained matching CEL validations so `kubectl apply` enforces the same
constraints the Go validator does.

## Consequences

### Positive
- Cache-heavy traffic (Claude Code's normal shape) no longer accumulates a
  permanent TPM overcharge, closing the actual production failure that forced
  the demo policy's rate rule to be deleted.
- Daily quota now reflects real token footprint, closing the same bug class
  ADR-030 fixed on the pricing path.
- An operator can declare "no cap, deliberately" without losing the rule's
  place in the document (and therefore its audit trail) — `git diff`ing a
  policy no longer conflates "we removed a rule" with "we widened a rule to
  no cap," which read identically before this change.

### Negative
- `Governor.Settle`'s signature gained a parameter (`estimatedTokens int64`),
  a small but real coupling between `PreCheck`'s estimate and `Settle`'s
  correction that every future ingress must thread through correctly (all
  three existing ingresses already reuse `pr.RawBody`, the same bytes
  `PreCheck` estimated from, so this is a compile-time-checked constraint, not
  a runtime one — but it is a constraint a fourth ingress must remember).
- `AdjustRate`'s debt semantics mean a single request whose actual usage
  wildly exceeds its estimate can push a team's TPM bucket negative enough
  that unrelated, well-estimated subsequent requests block until it refills —
  the same trade-off any token-bucket limiter makes, now also reachable via a
  large true-up rather than only via burst traffic.
- The `unlimited` sentinel does not, by itself, change who can set it: see
  the open item below.

### Follow-up (explicitly out of scope here)
`PUT /v1alpha1/policies/{name}` (ADR-038) accepts any authenticated
`inferplaned` identity, with no separate write-authorization tier and no
per-write audit of who submitted a change. `unlimited: true` makes a cap
easier to quietly widen than deleting a rule did (one field flips instead of
a whole rule disappearing from the diff), which raises the stakes on that
existing gap without doing anything to close it. A follow-up ADR should
address write authorization and change attribution on that endpoint.
