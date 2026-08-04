# Control-plane usage telemetry — cost/usage visibility on inferplaned

**Date:** 2026-08-04
**Status:** Design approved (brainstorming session with operator; panel-assisted
storage decision via /co-agent decide — see Storage below)
**Related:** ADR-031 (control/data plane split), ADR-034 (sync heartbeat + leases),
ADR-030 (cache-tier billing), ADR-008 (DB-authoritative + Git export pattern),
ADR-014 (LiteLLM UX parity, secret-ref-honest)

## Context

inferplaned distributes policy and issues budget leases (ADR-034), and each mayu
reports team-level cumulative spend — but only as a lease-ledger input. There is
no place where an operator can see "team X spent $Y this month, broken down by
model/user." That view is the control plane's job (the istiod analogy: config
down, telemetry up). mayu already computes exact integer-µUSD cost per request
including prompt-cache tiers (ADR-030) and audits it locally; the data exists,
it is just not centralized.

Deployment reality that shaped the design: inferplaned runs on ECS where tasks
are replaced at will — **the container must stay stateless**; anything durable
lives outside it.

## Decisions (with alternatives considered)

### D1 — New telemetry channel, not a sync-heartbeat extension

`POST /v1alpha1/usage`, separate from `/v1alpha1/sync`. The heartbeat is the
enforcement-critical path (policy pull + lease renewal); a telemetry batch must
never be able to slow or fail it. Rejected: extending `ConsumptionReport` on the
sync request — couples observability payload growth to the enforcement path.

### D2 — Storage: in-memory always; opt-in PostgreSQL (pure-Go pgx) for durability

Panel consultation (Kiro / Codex / Agy) + chair verification:

- **DuckDB** — rejected. Kiro and Agy both flagged that go-duckdb requires cgo;
  chair verified by building under `CGO_ENABLED=0`: compile fails. Violates the
  hard pure-Go constraint. (Codex recommended it conditionally on cgo-freedom;
  the condition does not hold.)
- **InfluxDB** — rejected. Dedicated time-series server is overkill for
  tens-to-hundreds of teams at 60s batch cadence, and adds an external server
  dependency.
- **Local SQLite (keystore precedent)** — initially chosen, then **rejected on
  the stateless-container requirement**: ECS task replacement discards local
  files. Local persistence contradicts the deployment model.
- **DynamoDB** — rejected: AWS-proprietary API conflicts with CNCF
  vendor-neutrality (the project targets CNCF Sandbox).
- **PostgreSQL via `jackc/pgx/v5`** — **chosen**. Chair verified `pgx` builds
  under `CGO_ENABLED=0` (pure Go). Postgres is an open standard protocol — runs
  identically on RDS, Cloud SQL, or self-hosted, no vendor lock-in. The
  "extra server" objection is neutralized because the stateless-container
  requirement makes external state mandatory anyway.

Config (same opt-in shape as ADR-008's `provider_store`):

```json
"usage_store": { "type": "postgres", "dsn_ref": {"env": "INFERPLANED_USAGE_DSN"} }
```

Absent → in-memory only (default; demo/single-instance). DSN is a secret ref,
never inline (§7).

### D3 — Data model: window batches at team+user+model granularity

mayu accumulates per-(team, user, model) counters over a 60s window and POSTs:

```json
{
  "dataplane": "ec2-local-mayu",
  "window_start": "2026-08-04T12:00:00Z",
  "window_end":   "2026-08-04T12:01:00Z",
  "entries": [
    {
      "team": "demo", "user": "intern-01", "model": "claude-opus-5",
      "spent_micro_usd": 1234,
      "input_tokens": 500, "output_tokens": 120,
      "cache_read_tokens": 300,
      "cache_write_5m_tokens": 50, "cache_write_1h_tokens": 0
    }
  ]
}
```

- Storage granularity is always the finest (team+user+model); coarser views are
  query-time GROUP BY. Money is integer µUSD end to end — never float.
- Cache fields are filled from the existing `schema.Usage` fold +
  `CacheWriteTiers()` (ADR-030) — no new interpretation logic. The 5m/1h split
  is preserved because the tiers bill differently.
- Idempotency key: `(dataplane, window_start)` — the control plane upserts, so
  a retried batch never double-counts.

Postgres schema (portable SQL types only, so standard `pg_dump`/RDS snapshots
are the backup story — inferplaned implements no bespoke backup):

```sql
CREATE TABLE usage_windows (
  dataplane      TEXT NOT NULL,
  window_start   TIMESTAMPTZ NOT NULL,
  window_end     TIMESTAMPTZ NOT NULL,
  team           TEXT NOT NULL,
  "user"         TEXT NOT NULL DEFAULT '',
  model          TEXT NOT NULL,
  spent_micro_usd        BIGINT NOT NULL,
  input_tokens           BIGINT NOT NULL,
  output_tokens          BIGINT NOT NULL,
  cache_read_tokens      BIGINT NOT NULL,
  cache_write_5m_tokens  BIGINT NOT NULL,
  cache_write_1h_tokens  BIGINT NOT NULL,
  PRIMARY KEY (dataplane, window_start, team, "user", model)
);
```

### D4 — Query API + minimal read-only web view

`GET /v1alpha1/usage` with `team`, `user`, `model`, `since`, `until`,
`group_by=team|user|model` filters; returns totals + breakdown. Auth: the
existing `INFERPLANED_TOKEN` bearer (same middleware as `/v1alpha1/dataplanes`).

Minimal web console on inferplaned, mayu-console style: vanilla JS, `go:embed`,
CSP `default-src 'self'`, token in JS memory only. First version: team list →
period spend table → model breakdown table. No charts (YAGNI).

**No config-write UI in this scope.** The dashboard is read-only, so the
YAML-vs-UI conflict does not arise here. When policy editing lands later, it
must follow the ADR-008 pattern: whole-dimension opt-in ownership transfer
(file XOR store per dimension), seed-once with a durable marker, divergence
logged — never field-level merges.

### D5 — Export

- `GET /v1alpha1/usage/export?since=&until=&format=csv|json` — aggregated usage
  dump for analysis/migration convenience. Not a DR mechanism (that's D2's
  standard-Postgres-tooling story).
- `GET /v1alpha1/config/export` — renders the effective policy set as
  `GovernancePolicy` YAML documents, the exact format all three delivery
  channels already share (file / control-plane push / Helm ConfigMap). This
  makes server replication and org-to-org handover "copy the YAML, point at a
  new endpoint." Secret-free by construction (policies carry refs only).
- `POST /v1alpha1/config/import` — **interface reserved, not implemented now**:
  until a UI-editable policy store exists, import == placing files in
  `--policies`. The endpoint lands with that store, following ADR-008
  seed-once semantics.

## Failure posture

- **mayu push failure**: telemetry never blocks the request path. Failed
  windows stay in a bounded local buffer and retry next cycle (window batches +
  upsert idempotency make retries safe). On buffer overflow, drop oldest and
  surface the drop via log + metric — never silently.
- **Postgres outage**: in-memory aggregation keeps serving queries; writes queue
  with retry. The usage store is a fully separate failure domain from policy
  distribution and leases — its outage must not touch them.
- **Auth**: 401s not audited (existing posture).

## Testing

- Unit: window fold, idempotent upsert, GROUP BY queries — one suite run against
  both the in-memory and Postgres implementations (shared interface; Postgres
  cases skip without a server, tagged).
- Integration: httptest round trip mayu-push → aggregate → query API. No
  network, no credentials, no real DB (project test mandate).
- Web view: CSP-compliance harness test (existing `tests/` pattern).
- Regression: fixture proving cache 5m/1h tiers survive intact from
  `schema.Usage` through the batch to the stored row (ADR-030 class of bug).

## Out of scope

- inferplaned HA / multi-replica write coordination (open question, separate
  design; Kiro/Agy both flagged single-writer as the deferred cost).
- Credential brokering, OTel-standard telemetry pipeline (ADR-031 roadmap).
- Policy/budget editing UI (future; must reuse ADR-008 pattern).
- Charts/graphs in the web view.
- mayu user-facing CLI `status` command (item 2 of the original ask; separate
  design).
