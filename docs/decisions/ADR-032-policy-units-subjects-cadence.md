# ADR-032: Policy money unit, user/team subjects, and governance cadences

## Status

Accepted (2026-07-30)

## Context

ADR-031 landed the `api/v1alpha1` GovernancePolicy schema with three open
edges: the money unit exposed to operators, who a policy can select, and the
lease/distribution cadences (★1). This ADR fixes all three.

## Decision

### 1. Wire money unit is integer milliUSD (1000 = $1); internal stays microUSD

Operators found microUSD too small to read and write. Every operator-facing
surface — v1alpha1 fields (`limitMilliUSD`, `grantMilliUSD`), future CLI and
report output — uses **milliUSD**.

Internal cost accounting **stays integer microUSD** and this is not
negotiable: per-token costs are sub-milliUSD (Haiku-class input at $0.25/MTok
is 0.25 µUSD per token — 0.00025 milliUSD), so settling in milliUSD rounds
small requests to zero, which is exactly the ADR-030 bug class ("settlement
produced 0 µUSD on real traffic"). The boundary conversion (×1000) is exact
and lives in `internal/policy` only.

### 2. Subjects: user-level and team-level governance are equal citizens

`spec.subject` selects `team` (the organizational unit — a department maps
here) and/or `user` (OIDC `sub` or a virtual key's `owner`); at least one is
required. Both selectors carry the same rule set (budget, modelAccess, rate,
routing) — per-user model restriction and per-user budget are first-class,
not encodings over per-key limits. This also fixes the ADR-028 gap where
CLI-minted rotating keys could not carry per-key budgets: a user-subject
budget windows on the user, not the rotating `key_id`.

Several policies may match one request (team + user). Enforcement applies
all of them; the most restrictive outcome wins — block beats warn, the same
tie rule two-phase governance already uses.

### 3. Cadences: near-real-time cost control, minute-scale policy delivery

There are two distinct cadences, deliberately different:

| Cadence | Default | Floor | What it bounds |
|---|---|---|---|
| Lease renew (consumption report + regrant) | **10s** | 1s | Budget overshoot: worst case = grant × connected data planes |
| Lease grant size | **0.1% of limit** (≥ 1 milliUSD) | — | Same bound, per data plane |
| Policy distribution reconcile poll | **60s** | 15s | Staleness of rules when the push stream is down |

Cost control is the near-real-time one: with defaults, a $5,000/month team
budget leases $5 per proxy per ~10s, so worst-case overshoot with 100
connected proxies is $500 — and shrinking either knob tightens it further.
Policy distribution is push-based over the control-plane stream (effectively
immediate); the 60s/15s poll exists only as the reconcile fallback for
disconnected proxies. Constants live in `internal/policy` (`DefaultLease*`,
`DefaultPolicySyncInterval`, `MinPolicySyncInterval`).

Lease fields are now optional (defaults apply); an explicit sub-floor renew
interval is rejected, not clamped — clamping silently changes what the
operator asked for.

### 4. New rule kinds: modelAccess and rate

`modelAccess.allow` (post-canonicalization match, `"*"` wildcard, empty list
rejected — deny-all must be written deliberately) and `rate.rpm`/`rate.tpm`
(0 = unlimited dimension, at least one must be positive) join budget and
routing. Exactly one kind per rule.

## Consequences

- `internal/policy.FromV1Alpha1` returns a `*Policy` (subject + generation +
  rules), no longer a bare rule slice.
- The same document remains applicable as a K8s CRD, a local file (hot
  reload), or an inferplaned push — delivery channels unchanged (ADR-031).
- ADR-031 open question 1 (lease defaults) is resolved here; open question 3
  (server-cache TTL tracking) remains open.
- Existing standalone config (`teams` / `virtual_keys`, human-USD floats) is
  untouched; a config→policy bridge is future work.
