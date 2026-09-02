# ADR-043: Global rate limits via control-plane rate shares

Status: Proposed (2026-09-01, EWMA half added 2026-09-02) · Implements roadmap ① (equal split + demand-following EWMA split)

## Context

`rpm`/`tpm` rate rules enforce against per-proxy in-memory buckets
(`limiter.NewMemory`): a team capped at 300 rpm with 20 connected data
planes can actually reach ~6,000 rpm. Budgets were globalized by leases
(ADR-034); rate was not. This is roadmap Purpose row #4c — the one
dimension where a central gateway with Redis (LiteLLM's answer) is more
accurate than inferplane today (`docs/comparison.md` §2).

The budget lease design does not transfer as-is: a budget is a stock
(cumulative, settles later), rate is a flow (per-minute, must be right
*now*). Cumulative allowances mean nothing for a flow — what can be
divided is the rate itself.

## Decision

The control plane divides each team rate rule's global rpm/tpm among
currently-live data planes and hands each its share in the existing
heartbeat. Each data plane clamps its locally-enforced limit to its share.

**Wire (additive, omitempty — old builds interop unchanged):**
`SyncResponse` gains `rateShares: [{policy, rule, team, rpm, tpm,
expiresAt}]`. No new request fields in v1.

**Split policy (v1): equal split with a floor of 1.** For each
non-unlimited team rate rule, `share = limit / N` per dimension (floor
division; a computed 0 becomes 1 so no plane is ever starved outright),
where N is the number of data planes whose last heartbeat is within
3× the heartbeat interval — the same liveness horizon leases use.
Σ shares ≤ limit whenever `limit ≥ N`; when `limit < N` the min-1 floor
deliberately admits up to N (documented: a 5 rpm limit across 20 planes
cannot be both non-starving and globally exact; non-starvation wins).

**Enforcement seam:** the same team-lookup closure that clamps budget to
the lease allowance (`cmd/mayu/gateway.go`) clamps the policy-declared
rpm/tpm to the share, BEFORE `PolicyFromLimits`, so the token-bucket burst
follows the clamped rate automatically. The clamp applies only to
dimensions the policy layer actually declares (`tl.RPM > 0` / `tl.TPM > 0`)
— a stale share can never constrain a config-only or keystore-only rate
after the policy rule is removed. **A share can only NARROW the local
limit, never widen it** (`min(policy limit, share)`), so a compromised or
buggy control plane cannot raise a data plane's admission rate above what
the policy document grants — the same narrows-only posture as
`SetPolicyGate` and `SubstituteTier`.

**Failure semantics: FailOpen, keep-last.** On control-plane outage the
share table keeps its last values (never widening back to the global
limit, never dropping to zero) — a rate limit protects throughput, not
money, so there is no hard-cap analogue and no fail-closed mode.
`expiresAt` rides the wire for observability but v1 does not act on it.

**Rebalance cadence = the heartbeat.** A plane going quiet stops counting
toward N within the 3×-interval horizon, and every other plane's share
grows on its next heartbeat — the same release mechanism as budget grants.

## Alternatives rejected

- **Shared counter store (Redis/Valkey)** — exact, but reintroduces a
  network round trip on the request path and an infrastructure dependency;
  both violate the node-local data plane's reason to exist (ADR-031,
  Core Purpose #5). ADR-013's direction is Postgres-only and data-plane
  HA, a different problem.
- **Token forwarding (planes borrowing from each other)** — peer-to-peer
  coordination between data planes adds a mesh where the architecture has
  a star; the control plane already owns global state.

## Consequences

- A team's fleet-aggregate admission rate is bounded by the configured
  limit (± the min-1 floor case) instead of N× it. The 429 appears at the
  global limit.
- The split now FOLLOWS DEMAND (second half, shipped 2026-09-02): mayu
  differentiates the governor's cumulative settled-traffic counters per
  heartbeat, EWMA-smooths (α=0.5), and reports per-minute flow in the
  additive `SyncRequest.flows` — a deliberate deviation from the original
  sketch's `recentRPM`/`recentTPM` inside `ConsumptionReport`, because
  reports exist per lease-managed BUDGET rule and a team with only a rate
  rule would have had nowhere to report. The control plane reserves HALF
  each rule's limit as the equal floor (an idle plane can always start
  working without waiting a rebalance) and divides the other half
  proportionally to reported flow: Σ shares ≤ limit by construction, the
  min-1 floor kept from v1. A flow-less plane (older build, cold fleet)
  degrades to the floor, and no flow anywhere degrades to exactly the v1
  equal split. Settled traffic only — a denied request earns no share.
- Per-KEY rate limits and standalone mode are unchanged (per-plane, as
  before); user-subject rate rules stay rejected (`checkEnforceable`)
  until shares can be user-keyed.
- The two-gateway e2e (`cmd/mayu/rateshare_e2e_test.go`) pins the
  headline: two planes against one control plane admit ≤ the global limit
  in aggregate, not 2×.

## Security

No new secrets, tokens, or channels — shares ride the existing
authenticated heartbeat. The narrows-only clamp (above) bounds the blast
radius of a compromised control plane to *reducing* throughput.
