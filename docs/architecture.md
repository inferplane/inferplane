# Architecture

## System Overview

inferplane is split into two binaries: **`mayu`**, a node-local data plane that
sits between coding agents (Claude Code, OpenCode) and upstream model providers
(Anthropic, Amazon Bedrock, self-hosted vLLM/Ollama), and **`inferplaned`**, a
control plane that distributes policy and budget leases but never carries
inference traffic (ADR-031). `mayu` authenticates virtual keys, enforces
per-team RBAC, rate limits, quotas, and budgets, forwards each request to a
real provider, and writes a tamper-evident audit record — all with no external
SaaS dependency. Both binaries are static, cgo-free, and Kubernetes-native.
See the [README](../README.md#architecture) for the control-plane/data-plane
topology diagram; this document covers `mayu`'s internal component
architecture.

`mayu` is built around two design invariants: a **canonical schema** (an
Anthropic-superset that preserves thinking blocks and `cache_control`) for
cross-protocol conversion, and **verbatim body forwarding** when the ingress
protocol matches the upstream protocol, so prompt-cache hits are never
corrupted.

## Components

### Ingress Layer (`internal/server`)
- **Data plane (`:8080`)** -- three ingresses: Anthropic Messages (`/v1/messages`, `/v1/messages/count_tokens`, `/v1/models`), OpenAI Chat Completions (`/v1/chat/completions`, `/v1/models`), and Bedrock InvokeModel passthrough (`/model/{modelId}/invoke`, `/invoke-with-response-stream`, `/count-tokens`, ADR-024). `KeyAuth` resolves the virtual key before routing.
- **Admin plane (`:9090`)** -- `/healthz`, `/readyz`, unauthenticated `/metrics`, the token-authenticated `/admin/keys` API, and the minimal embedded key console at `/admin/ui/` (data-free static assets, ADR-001; OIDC SSO login button, ADR-026).
- **TLS (`tls.go`)** -- optional self-terminated TLS on the data plane for non-Kubernetes deployments; the admin plane stays plaintext (cluster-internal).

### Governance Layer (`internal/governance`, `limiter`, `budget`, `pricing`)
- **Governor** -- two-phase: `PreCheck` runs BEFORE billing (rate/quota/budget), `Settle` runs AFTER (debits quota tokens and budget microUSD, records cost).
- **Limiter / Budget stores** -- in-memory, two-phase (optimistic check + post-debit), injectable clocks; a shared-store backend (Postgres/Redis) is planned to slot behind the same interfaces for multi-replica HA (ADR-013, not yet implemented).
- **Pricing** -- integer microUSD, round-half-even via `math/big`, TTL-tiered cache-write rates, `on_missing: allow` (self-hosted chargeback) | `block`.

### Provider Layer (`providers/*`)
- **anthropic** -- Messages API passthrough; verbatim body, gateway-injected `x-api-key`.
- **bedrock** -- Claude via InvokeModel (native Anthropic body, cache-safe top-level model rewrite, event-stream → Anthropic SSE); non-Claude via Converse. SDK isolated behind invoker/converser interfaces.
- **openaicompat** -- vLLM/Ollama/any OpenAI endpoint; order-preserving model rewrite.
- The `Provider` interface (`Name`, `Models`, `Complete`, `Stream`, optional `TokenCounter`) is the single extension point — a new provider is one package.

### Routing Layer (`internal/router`)
- Resolves model → provider target, walks the priority fallback chain, and guards each provider with a circuit breaker (consecutive-failure → open → backoff → half-open). Failover is **pre-TTFT only**; a mid-stream failure is never retried.
- **Model-level fallback (ADR-029)** — an unrouted requested model (e.g. a hardcoded client on a model the operator hasn't added yet) substitutes for a configured `model_fallbacks` entry, or by default the highest configured version below it in the same name family, BEFORE the allow-list check (`Router.ResolveModel`). A *configured* model whose upstream rejects it as unknown (404, or Bedrock 400 `ValidationException`) also crosses to its fallback model within the existing chain (`ResolveChain` appends the fallback model's own targets); because that append happens after the ingress allow-list check already ran, every ingress handler re-checks those targets via `FilterModelAllowed` before ever dispatching to them. Either path is fail-closed on RBAC and sets `x-inferplane-model-fallback`.

### Persistence Layer (`internal/keystore`, `internal/audit`)
- **Key store** -- SQLite (`modernc.org/sqlite`, cgo-free), Postgres-portable schema; keys SHA-256 hashed at rest behind a `Store` interface.
- **Audit** -- single-writer goroutine, per-instance SHA-256 hash chain, disk-backed WAL (`buffer_then_block`), `audit verify` CLI, ULID record IDs.

### Observability Layer (`internal/metrics`)
- Prometheus registry with OpenTelemetry GenAI semantic-convention naming (`gen_ai_*`) plus `inferplane_*` operational series. Cardinality is config-bounded; a sentinel `_rejected` model label protects pre-resolution 403/404 paths.

### Control-Plane Telemetry (`internal/telemetry`, `internal/controlplane`, ADR-036)
- The "usage up" channel of the split (ADR-031): each mayu folds settled usage — team/user/model, integer µUSD, cache 5m/1h tiers — into 60s windows (`telemetry.Collector`) pushed to inferplaned's `POST /v1alpha1/usage`, deliberately separate from the enforcement-critical policy/lease sync heartbeat. The data plane's bounded FIFO is the single retry store (the control plane acks only what is stored — 503 otherwise); storage is always-on bounded memory plus opt-in Postgres write-through (`INFERPLANED_USAGE_DSN`), queried via `GET /v1alpha1/usage`, streamed exports, and the read-only `/ui/` console (SSO login button, ADR-037).

### Security Layer (cross-cutting)
- Virtual-key auth + team RBAC (`Principal.Allows`), inline-secret rejection, client/upstream key isolation, no secret leakage on `/metrics`.

## mayu Component Diagram

```
┌──────────────────────────────────────────────────────────────┐
│                         Clients                                │
│   Claude Code (Anthropic API)      OpenCode (OpenAI API)       │
└───────────────┬─────────────────────────────┬─────────────────┘
                │ ik_... virtual key           │ ik_... virtual key
                ▼                             ▼
┌──────────────────────────────────────────────────────────────┐
│                   Data Plane  :8080  (internal/server)         │
│   ┌────────────┐   KeyAuth (RBAC)   ┌────────────────────┐     │
│   │ /v1/messages│──────┐     ┌──────│ /v1/chat/completions│    │
│   └────────────┘       ▼     ▼      └────────────────────┘     │
│                  ┌──────────────────┐                          │
│                  │    Governor       │  PreCheck (rate/quota/  │
│                  │ (governance)      │   budget) BEFORE bill   │
│                  └────────┬─────────┘                          │
│                           ▼                                    │
│                  ┌──────────────────┐                          │
│                  │     Router        │ fallback chain +        │
│                  │ + circuit breaker │ breaker (pre-TTFT)      │
│                  └────────┬─────────┘                          │
└───────────────────────────┼────────────────────────────────────┘
                            ▼
┌──────────────────────────────────────────────────────────────┐
│                   Provider Layer  (providers/*)                │
│   ┌──────────┐    ┌──────────┐    ┌────────────────────┐       │
│   │ anthropic│    │ bedrock   │    │ openai_compatible  │       │
│   └────┬─────┘    └────┬─────┘    └─────────┬──────────┘       │
└────────┼──────────────┼────────────────────┼──────────────────┘
         ▼              ▼                    ▼
   Anthropic API   Amazon Bedrock      vLLM / Ollama
         │              │                    │
         └──────────────┴────────────────────┘
                        │ Settle (debit quota/budget, record cost)
                        ▼
┌──────────────────────────────────────────────────────────────┐
│  Persistence + Observability                                   │
│  ┌───────────┐  ┌──────────────────┐  ┌───────────────────┐   │
│  │ key store │  │ audit hash chain  │  │ Prometheus /metrics│  │
│  │ (SQLite)  │  │ (WAL, verify)     │  │ :9090 (admin plane)│  │
│  └───────────┘  └──────────────────┘  └───────────────────┘   │
└──────────────────────────────────────────────────────────────┘
```

## Data Flow Summary

```
Client -> KeyAuth(RBAC) -> Governor.PreCheck -> Router(fallback+breaker) -> Provider -> Upstream
                                                                                  |
                              ┌───────────────────────────────────────────────────┘
                              ▼
                       Governor.Settle -> quota/budget debit + Pricing(microUSD) -> Audit(hash chain) -> /metrics
```

## Infrastructure

### Deployment
- Container: multi-stage build, `CGO_ENABLED=0` static binary → `distroless/static:nonroot`.
- Kubernetes: Helm chart at `charts/inferplane` (ConfigMap-rendered config, optional IRSA ServiceAccount for Bedrock, `existingSecret` reference — the chart never creates secrets). Optional `Ingress` (off by default); the admin plane stays off Ingress even when enabled unless `ingress.admin.enabled` is set explicitly, since it carries key-issuance/governance actions. Optional PVC for the key store (`persistence.enabled`, default off, ADR-023) — without it `/var/lib/inferplane` is an `emptyDir` and the key store/audit WAL are wiped on every restart; declaring `virtual_keys` in config gives clients a restart-durable key without needing the PVC. `NOTES.txt` prints post-install next steps (port-forward/Ingress host, first key, pointing a client at it).

### Modules / Resources
| Component | Path | Description |
|-----------|------|-------------|
| Data-plane binary | `cmd/mayu` | serve / keys / audit / report / bodies / pricing / login / token / logout subcommands |
| Control-plane binary | `cmd/inferplaned` | distributes GovernancePolicy documents and budget leases (ADR-034); no subcommands, flags + env only |
| Helm chart | `charts/inferplane` | Deployment, Service (data+admin), ServiceAccount, ConfigMap, optional Ingress, optional PVC (ADR-023), NOTES.txt |
| Dashboard | `deploy/grafana/inferplane.json` | 9-panel Prometheus dashboard |

### Deployed Endpoints
- Data plane: `:8080` (`/v1/messages`, `/v1/chat/completions`)
- Admin plane: `:9090` (`/healthz`, `/readyz`, `/metrics`, `/admin/keys`, `/admin/ui/`)

- **Config hot-reload (`internal/live`, ADR-006)** -- the provider/model/pricing topology is one immutable `live.State` behind an atomic pointer; `SIGHUP` validates + atomically swaps a new generation. Governance counters, keystore, audit chain, and circuit-breaker state persist across reloads.

- **UI-write provider registration (`internal/providerstore`, ADR-008)** -- an opt-in `provider_store` makes the DB authoritative for the reloadable topology (providers + model routes); `PUT`/`DELETE /admin/providers|models` register changes build-once-swap-once through the same `reload()` mechanism (validate the candidate generation, persist, swap the validated state, all under one `reloadMu`). **Secrets never enter the gateway** -- only the ref (env var name / file path) is stored; `GET /admin/config/export` emits a secret-free config fragment for Git. Absent `provider_store` → file-authoritative, writes 405 (ADR-005).

- **Opt-in PII masking filter (`plugins/piimask`, ADR-009)** -- a request-filter
  chain (`internal/filter`) with an opt-in, per-team PII masker. Masking
  re-serializes the body, abandoning verbatim forwarding → it **destroys the
  prompt cache** for masked traffic (up to 10× cost) — so it is opt-in and the
  cost is made explicit (boot warning + `inferplane_pii_mask_redactions_total` +
  audit `pii_masked`). One-way (no vault, no PII at rest); fails CLOSED (a masker
  error rejects, never forwards unmasked; the OpenAI ingress refuses masked teams
  in v1). A new filter = one package under `plugins/` + one blank import.

- **Opt-in OpenTelemetry tracing (`internal/tracing`, ADR-011)** -- a configured
  `otel` block installs an OTLP exporter (http/grpc) + GenAI-semconv spans on the
  generative endpoints + W3C trace-context propagation (joins the client trace,
  correlates the upstream call) + `trace_id` in the audit chain. **No-op by
  default** (no `otel` → no spans, deps inert, request path byte-identical). One
  span per request owned across the fallback loop (`defer End`, error-only-on-
  terminal); best-effort — never on the critical path. Pure-Go, exports to the
  operator's own collector (no SaaS).

## Key Design Decisions

- **Canonical schema = Anthropic-superset, not OpenAI** -- preserves thinking blocks and `cache_control` that the OpenAI shape cannot represent; same-protocol round-trips stay lossless.
- **Verbatim body forwarding on protocol match** -- corrupting `cache_control` turns a 96%-hit prompt cache into a 10× cost regression, so a matching protocol tees `RawBody` byte-for-byte instead of re-serializing.
- **Instance-local governance + SQLite default** -- a single binary boots in 5 minutes with no external DB; the `Store` interface keeps a shared-store HA backend as a future swap, not a rewrite (ADR-013, design-only today).
- **Per-instance segmented audit chain** -- a hash chain per process run survives legitimate restarts without reading as tampering, while remaining verifiable offline.
- **Pre-TTFT-only failover** -- once the first token streams, the response is committed; retrying mid-stream would duplicate or corrupt output.
- **Cost as integer microUSD** -- float accumulation drifts; round-half-even on `math/big` keeps billing exact and overflow-free.

## Operations
- Deployment: see [docs/runbooks/.template.md](runbooks/.template.md) (create `deploy-production.md` from it).
- Decisions: see [docs/decisions/](decisions/).
- Reference: see [docs/reference/INDEX.md](reference/INDEX.md).
