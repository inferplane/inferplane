# ADR-034: Control-plane policy distribution and budget-lease protocol

## Status

Accepted (2026-07-30)

## Context

ADR-033 landed the local policy file channel. This ADR lands the control
plane channel: inferplaned distributes GovernancePolicy documents to data
planes and issues the budget leases (ADR-031 §3, cadence defaults ADR-032)
that make a GLOBAL budget enforceable across N proxies that each see only
their own traffic.

## Decision

### 1. One heartbeat, four jobs

A data plane POSTs `/v1alpha1/sync` on the control plane's requested cadence
(the tightest lease `renewInterval`, else 60s). The single round trip
carries: the policy pull (skipped when the `generation` fingerprint
matches), the cumulative consumption report, the lease renewal, and the
version-skew rejection report. The request path never waits on it —
enforcement reads atomic snapshots (policy store, lease table).

Units: policy DOCUMENTS stay operator-facing milliUSD (ADR-032); the sync
protocol is a MACHINE channel and uses exact integer µUSD — per-request
consumption is sub-milliUSD and reporting it coarser would lose spend the
same way settling coarser would (ADR-030).

### 2. Lease ledger: allowances, not deltas

The control plane keeps one ledger per lease-managed budget rule:
cumulative reported spend and cumulative granted allowance per data plane.

    remaining  = limit − Σ spent − Σ (allowance − spent of OTHER planes)
    allowance′ = spent + min(grantSize, max(0, remaining))

Grants are cumulative allowances (idempotent across retried heartbeats),
expiry is 3× the renew interval (two missed heartbeats of tolerance), and
worst-case global overshoot stays bounded by Σ outstanding grants — the
ADR-032 knobs. Consumption reports are cumulative so a lost heartbeat never
loses spend; a DECREASE is the data plane's budget window rolling over (or a
restart), so the ledger adopts the fresh counter and drops the old
allowance — carrying it forward would let every proxy re-spend its old
allowance each window.

### 3. Data-plane enforcement: clamp + gate

Within a valid lease the team-lookup closure clamps the local budget limit
to the allowance — zero network round trips on the request path. A zero
allowance can't be expressed as a limit (0 = unlimited in `TeamPolicy`), so:
hard caps at zero allowance (and hard caps whose lease EXPIRED) are blocked
outright by `Governor.SetLeaseGate`, consulted first in PreCheck — fail
closed, per-rule. Soft rules never gate: control-plane outage fails open for
them (`failurePolicy`, ADR-031). A partial allowance clamps to ≥1 µUSD,
accepting up to one request of local overshoot (the §5.3 posture).

### 4. Explicit rejection, visible skew

Distributed documents apply PER DOCUMENT on the data plane (unlike the
all-or-nothing local file path): a document this build can't enforce is
rejected, kept out of the applied set, and reported on the next heartbeat.
`GET /v1alpha1/dataplanes` shows each connected proxy's API versions,
applied generation, and rejections — the operator's pre-propagation
coverage check.

### 5. Boot and outage posture

A data plane in control-plane mode boots WITHOUT waiting for the control
plane (fault isolation: ungoverned-until-first-sync, first heartbeat fires
immediately) and keeps the last-applied policy set and leases through
outages — lease expiry, not the outage itself, is what flips hard caps to
fail-closed while the loop retries. `control_plane` and `policies` are
mutually exclusive config: one authoritative policy source.

### 6. Authentication

A shared bearer token (`INFERPLANED_TOKEN` on inferplaned;
`control_plane.token_ref` — referenced, never inline, §7 — on mayu),
compared constant-time. Without it inferplaned must stay loopback-only (it
logs an UNAUTHENTICATED warning); mTLS/OIDC-brokered channel auth is future
work alongside credential brokering.

## Consequences / known limits (this iteration)

- The ledger is in-memory: a control-plane restart re-learns spend from the
  next heartbeats' cumulative reports; grants issued moments before a crash
  are re-derived. A durable ledger arrives with the HA milestone.
- Budget windows are per-data-plane tumbling windows (the local budget
  store's); the global limit across window phases is approximate at the
  edges. A control-plane-owned window epoch lands with the durable ledger.
- Delivery is poll-based at lease cadence (10s default — well inside the
  60s/15s policy-propagation requirement); an SSE push stream is a listed
  follow-up, not a semantic change.
- `user`-subject budget/rate rules remain rejected by data planes (ADR-033
  gate) until per-user governance windows land.
- Consumption reports read ONE team-level spend counter: a team with budget
  rules in several policies reports the same cumulative spend against each
  rule, so every rule beyond the tightest is under-granted (conservative,
  never permissive). Per-rule spend tracking lands with the durable ledger.
