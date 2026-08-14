# internal Module

## Role
Private gateway internals — everything that is not a provider or a public package.
Packages are leaf-oriented to avoid import cycles (`principal`, `metrics`, `governance`
are leaves that others depend on).

## Invariants
These are the facts in this module worth knowing before touching it — the rest is
in the code and in `docs/decisions/`.

- **Pre-check before billing, settle after.** `on_exceeded` block wins on tie (`governance/`).
- **Cost is integer microUSD, never float.** Round-half-even via `math/big`.
- **`count_tokens` must never return non-200.**
- **Money units cross a boundary exactly once:** wire policy documents are milliUSD
  (1000 = $1, operator-facing); `policy/` converts to internal µUSD (×1000, exact) and
  nothing downstream re-converts. Settling coarser reopens ADR-030's zero-cost bug class.
- **Secrets never enter a DB-backed store** (`providerstore/`, `policystore/`) — only refs.
  A `policystore` constructor error is a hand-written string that never wraps the
  underlying pgx error, which can embed the DSN (and a password).
- **`key_id` never reaches a `/metrics` label**, but may reach a webhook payload body
  (`alert/`) — a label is cardinality-unbounded exposure, a payload body is not.
- Keep `principal`, `metrics`, `governance` import-cycle-free — widely depended on.
- Metric labels are config-bounded; never label with raw client input (`_rejected`
  sentinel on pre-resolution rejects).
- A policy from `policy/`/`policystore/` can only **narrow** a key's allow-list or
  budget, never widen one already set elsewhere (`router.SetPolicyGate`).
- RBAC re-check after routing: `router.FilterModelAllowed`/`FilterRegions` must run
  right after `ResolveChain` in every ingress handler — the router has no `Principal`
  in scope, so a model-fallback or region-filtered target appended *after* the
  original allow-list check is not itself RBAC-checked until the handler does it.

## Key Packages
- `server/` — HTTP data plane + admin plane. Ingress handlers: `anthropicapi/`, `openaiapi/`, `bedrockapi/` (ADR-024); `usageapi/` self-service `GET /v1/usage` (ADR-021); `authapi/` opt-in CLI login (ADR-028); `adminapi/`, `configapi/`, `auditapi/`, `analyticsapi/`; `adminui/` embedded console (ADR-001, SSO ADR-026, i18n ADR-027). A mid-stream upstream failure surfaces as a wire-appropriate error frame (SSE `error` event / eventstream exception), never a silent truncation, and the audit record is marked `partial: true`.
- `router/` — model→provider resolution (`ResolveChain`), priority fallback, per-provider circuit breaker. `Canonical`/`Allows` (ADR-021) canonicalize model aliases before RBAC. `ResolveModel` (ADR-029) substitutes an unrouted model via `model_fallbacks` or same-family default before the allow-list check. `FilterRegions`/`FilterModelAllowed` are the pure RBAC re-check functions ingress handlers must call after `ResolveChain` (see Invariants). `SetPolicyGate` (ADR-033) ANDs an optional GovernancePolicy check onto every `Allows` decision.
- `governance/` — `Governor` (`PreCheck`/`Settle`). `SetLeaseGate` (ADR-034) checks a budget lease first in `PreCheck`, denying 402 before any counter is charged. `UsageOf` (ADR-021) is the read-only companion behind `GET /v1/usage` — no debit, no state change. `SetTeamLookup` (ADR-016) prefers a DB team record over the static config map. `SetBudgetNotify`/`SetKeyBudgetNotify` (ADR-017) fire post-debit alert hooks.
- `keystore/` — virtual-key `Store` (SQLite), `Principal`, RBAC `Allows()`. `TeamStore` (ADR-016) is the `teams` table (allow-list, RPM/TPM, budget, guardrail override ADR-019, region lock ADR-020). `KeyEnsurer.EnsureKey` (ADR-023) upserts a caller-supplied plaintext so a config-declared key survives a wiped store. `ErrKeyExpired` (ADR-028) is distinct from `ErrKeyNotFound` so an expired CLI-minted key gets a different 401 message — still 401 either way.
- `audit/` — single-writer hash-chain writer, WAL, `verify.go`. `audit/s3anchor/` is opt-in S3 Object Lock anchoring (ADR-012). `Record.BodyRef`/`RecordRef` (ADR-018) are appended at the struct end (omitempty) so mixed-version chains still verify byte-identically.
- `bodystore/` — opt-in encrypted request/response body capture (ADR-018), OUTSIDE the audit chain. Envelope AEAD; `Fetch` is fail-closed (any decrypt/miss → 410 tombstone, never plaintext). SQLite/Postgres backends; key rotation via `bodies rewrap-key`.
- `analytics/` — derived usage read-model backing `GET /admin/logs`; `pgstore/` is the shared-Postgres backend (ADR-015).
- `pricing/` — integer microUSD table, round-half-even. **ADR-030**: rate key is `(config provider name, UPSTREAM model id)`, never the canonical ingress name — the single most common way to end up billing 0. `HasRate` backs both boot validation and the runtime guard, so they can't drift apart. Cache rates derive from the input rate (0.1x read / 1.25x 5m-write / 2x 1h-write) unless explicitly overridden.
- `limiter/`, `budget/` — in-memory two-phase governance stores with injectable clocks. Per-instance state (see ADR-013 in Design Debt below).
- `alert/` — budget-alert webhook emitter (ADR-017). Fire-and-forget JSON POST on each newly-crossed utilization threshold; per-team and per-key paths dedupe independently.
- `metrics/` — Prometheus registry + GenAI collectors + nil-safe hooks.
- `openai/` — OpenAI ⇄ canonical conversion.
- `adminauth/` — admin-plane OIDC identity leaf (ADR-004): shared bearer-shape predicate, groups→team mapping, go-oidc verifier.
- `live/` — reloadable topology (providers + routes + pricing) behind one atomic `Holder` (ADR-006). `BuildState` also runs pricing-coverage validation (ADR-030): `on_missing: block` refuses to boot on an unpriced route; `allow` (default) logs loudly and continues. `Canonical`/`FallbackFor` back the router's alias and model-fallback resolution.
- `providerstore/` — opt-in DB-authoritative provider/model topology (ADR-008). Refs only, no secret column. UI writes build-once-swap-once under one `reloadMu`.
- `config/` — config loading + secret-ref resolution (inline secrets rejected). OIDC block validation; `CLILogin` requires a client ID distinct from the console's own (ADR-028). `ModelFallbacks`/`ModelFallbackFamily` (ADR-029) validated at both the file-load and UI-write paths.
- `principal/` — request-scoped principal context (leaf, breaks import cycles).
- `tracing/` — opt-in OpenTelemetry seam (ADR-011): OTLP exporter + GenAI-semconv spans + W3C propagation + `trace_id` in audit. No-op by default.
- `filter/` — request-transform filter seam (ADR-009): `RequestFilter` interface + registry. Concrete filters live under `plugins/<name>/` and register via blank import.
- `policy/` — rule + budget-lease schema shared by BOTH binaries (ADR-031/032/033) — the single source of truth. Converts `api/v1alpha1` wire docs and explicitly rejects anything unsupported (`UnsupportedError`, reported upstream, never silently ignored). Subjects (`team`/`user`) are equal citizens; most-restrictive-wins on multi-match. Exactly one rule kind per rule; `failurePolicy` required, no default. The local file channel (ADR-033) rejects rules this build can't enforce (routing; user-subject budget/rate) rather than accepting and ignoring them.
- `policystore/` — opt-in Postgres-authoritative GovernancePolicy store for `inferplaned` (ADR-038). The document, not the rule, is the CRUD unit. File-loaded set = seed, DB = authoritative once seeded (marker-gated, never row-count).
- `controlplane/` — `inferplaned`'s distribution core (ADR-034): `POST /v1alpha1/sync` is one heartbeat (policy pull + consumption report + lease renewal + rejection report). Budget-lease ledger is in-memory. `applyWire` (ADR-038) is the one install path both the file loader and the DB loader go through, so ledger semantics never diverge by source. `policies.go` mounts read/write policy routes behind the same auth as sync (ADR-038 accepted limitation: no separate write authorization).
- `proxy/` — mayu's control-plane `Syncer` (heartbeat client) and `LeaseTable` (the `Governor` lease gate: hard cap + expired/zero allowance fails closed).
- `cache/` — `VolatileStore` interface for in-memory-only payload caching / cache-affinity routing. **Not implemented** — no importers today; the CRD's `routing` rule kind exists in the wire schema ahead of this package.
- `telemetry/` — usage wire types + window `Collector` + `Aggregator` (memory/postgres/durable), live (ADR-036). `proxy.UsagePusher` drains it to `inferplaned` on a channel separate from the enforcement-critical sync heartbeat.

## Design Debt
Worth knowing before assuming a caveat is temporary:
- **Multi-replica HA is designed, not built (ADR-013, still Proposed).** `keystore`/`providerstore` are SQLite-only; `limiter`/`budget` are memory-only; there is no Redis/Valkey dependency anywhere. Every "per-instance state" note above is a standing limitation, not a transitional one.
- **`cache/` (VolatileStore) is unimplemented** — see above.
