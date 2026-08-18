# ADR-036: Control-plane usage telemetry — cost/usage visibility on inferplaned

**Date:** 2026-08-04
**Status:** Accepted — implemented (T1–T12 on `feat/control-plane-usage-telemetry`).
Design hardened through THREE `/co-agent:consensus` P2 plan-gate rounds (codex
gpt-5.6-sol, kiro-cli claude-opus-4.8, agy Gemini 3.1 Pro) plus per-task gates
during implementation; chair verified every code-level claim before accepting.
**Related:** ADR-031 (control/data plane split — this is the "telemetry
aggregation" half), ADR-034 (sync heartbeat; this channel is deliberately
separate), ADR-030 (cache-tier billing — tiers preserved end to end), ADR-008
(export posture), ADR-037 (console SSO — replaces this ADR's single shared
`INFERPLANED_TOKEN` console login with per-human OIDC identity, on the same
`/ui/` console this ADR shipped).

## Context

mayu settles exact integer-µUSD cost per request (ADR-030) but only locally;
the control plane received team-level cumulative spend solely as a
lease-ledger input. No central view answered "team X spent $Y this month, by
model/user" — the istiod-analogy gap (config down worked; telemetry up did
not). Deployment reality: inferplaned runs on ECS where tasks are replaced at
will — the container must stay stateless.

## Decision

1. **A separate telemetry channel** — `POST /v1alpha1/usage` — not a sync
   extension: the heartbeat is enforcement-critical (policy + leases) and a
   telemetry payload must never slow it. mayu folds per-settle
   `(team, owner, upstream-model, usage, cost)` into a window `Collector`
   (60s), pushed by a `UsagePusher` with a bounded 60-window retry FIFO.
2. **mayu's FIFO is the single retry store.** The control plane acks only
   what is stored: a durable-store failure returns 503 and the data plane
   retries. There is NO control-plane-side write queue (round-2 gate: an
   async CP queue acks before durability and silently loses its depth on an
   ECS task replacement). Failure classification: 4xx permanent (drop +
   `inferplane_usage_windows_dropped_total`) except 408/429; 5xx/network
   retryable; 3xx permanent (redirects never followed — a 301'd POST becomes
   a GET).
3. **Storage: always-on bounded memory (24h retention) + opt-in Postgres
   write-through** (`INFERPLANED_USAGE_DSN`). PG commit first, then memory,
   then ack; queries prefer PG (full history) and fall back to memory
   **marked `degraded: true`** during an outage — a billing view silently
   missing history is worse than an error. Lazy PG connect (an outage never
   blocks boot); DSN never appears in any error; portable SQL types so
   pg_dump/RDS snapshots ARE the backup story.
4. **Query + console**: `GET /v1alpha1/usage` (team/user/model filters,
   group_by) behind the existing bearer; a read-only console at `/ui/`
   (mayu ADR-001/002 posture: data-free unauthenticated shell, token in JS
   memory only, CSP `default-src 'self'`).
5. **Exports**: `GET /v1alpha1/usage/export` (CSV/JSON, required
   since/until, streamed row-by-row with a per-write `ResponseController`
   deadline so the server's 30s WriteTimeout cannot silently truncate) and
   `GET /v1alpha1/config/export` (the distributed policy set as
   GovernancePolicy YAML — replication/handover is "save this, point a new
   `--policies` at it"). Config IMPORT is reserved until a UI-editable
   policy store exists.

### Spec amendments recorded here (P2 gate)

- The spec's `usage_store {type, dsn_ref}` config block became the fixed env
  var `INFERPLANED_USAGE_DSN`: inferplaned has no config file, and the
  `INFERPLANED_TOKEN` precedent is a fixed env name (the secret-ref posture
  holds — the value never rides a flag).
- Postgres composes BEHIND memory (write-through), never memory-XOR-Postgres.

## Considered alternatives

- **Extend the sync heartbeat** — rejected: couples observability payload
  growth to the enforcement path.
- **DuckDB** — rejected on a *verified* hard constraint: go-duckdb requires
  cgo (`CGO_ENABLED=0 go build` fails — chair-reproduced), violating the
  pure-Go mandate. (One panel member recommended it conditionally; the
  condition does not hold.)
- **Local SQLite (keystore precedent)** — rejected: ECS task replacement
  discards local files; container must stay stateless.
- **DynamoDB** — rejected: AWS-proprietary API conflicts with CNCF
  vendor-neutrality. Postgres is an open protocol (RDS/Cloud SQL/self-hosted
  identically); `jackc/pgx` is pure Go and already a direct dependency
  (bodystore precedent).
- **InfluxDB** — rejected: a dedicated time-series server is overkill for
  tens-to-hundreds of teams at 60s cadence.
- **CP-side write queue for PG outages** — adopted in plan r2, **deleted in
  r3**: unanimous round-2 gate finding (ack-before-durable data loss).

## Consequences

- Operators get `GET /v1alpha1/usage`, two exports, and `/ui/` — the
  control plane now does both halves of the istio analogy.
- Accepted limitation (recorded, not fixed): no batch splitting for >4 MiB
  pathological-cardinality windows — dropped and counted.
- Known deferral: per-rule spend in consumption reports (ADR-034 known
  limits) and inferplaned HA are unchanged by this ADR — see ADR-038
  §Consequences for why "stateless container" here does not mean
  horizontally scalable: the budget-lease ledger is still per-process.
- `internal/telemetry` is now live code (was an ADR-031 placeholder): wire
  types, collector, aggregators shared by both binaries.
