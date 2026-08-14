# inferplane — LLM Consumption Governance Gateway Design

- Status: Historical design record (v0.1). The two-plane split (`mayu` data
  plane + `inferplaned` control plane, ADR-031) and most of the v0.2 items
  below have since shipped — see [docs/architecture.md](../architecture.md)
  for the current architecture and [docs/decisions/](../decisions/) for the
  ADRs that superseded or extended this document. Section numbers below are
  kept stable because ADRs and code comments cite them (`§4.4`, `§5.2`,
  `§5.3`, `§7`, `§8`, `Appendix A`); this revision translates the original
  Korean draft to English and updates content to match what actually shipped,
  without renumbering.
- Date: 2026-06-10 (r3 — second round of multi-model consensus review, 12 items incorporated)
- License: Apache 2.0
- Language: Go
- Original target release: v0.1

---

## 1. Positioning and Vision

### 1.1 One-sentence definition

> Envoy-family gateways require a platform team's own infrastructure project;
> LiteLLM/Bifrost paywall the governance core.
> **inferplane puts RBAC, quota, budget, and tamper-evident audit — all of
> it, free — into a single binary an AI team can stand up in five minutes.**

inferplane does not compete on data-plane performance or inference
optimization. Its core is **governance of LLM consumption** — controlling
and recording who uses which model, how much, and at what cost.

### 1.2 Competitive analysis summary (as of 2026-06)

| Project | Position | Governance limitation |
|---|---|---|
| LiteLLM | Python LLM proxy, de-facto standard | SSO, audit log, and project-level RBAC are Enterprise-paid |
| Bifrost | Go LLM gateway, the most direct competitor | Virtual keys/budget are free; RBAC, SSO, and immutable audit are Enterprise-gated |
| Higress | CNCF Sandbox (2026-03), enterprise AI gateway | The standalone edition is officially documented as "not production-validated, for local/test use" |
| kgateway | CNCF Sandbox, Envoy-based | Presupposes adopting an Envoy control plane — a platform-team-scale infrastructure project |
| Envoy AI Gateway | Official Envoy Gateway AI extension | Same — presupposes Envoy operational capability |
| llm-d | CNCF Sandbox (2026-03), distributed inference framework | Not a competitor — a **backend** candidate: a self-hosted upstream for inferplane |
| Inference Gateway | Under Sandbox review (cncf/sandbox#486) | Focused on multi-provider integration, no governance depth |

**Market observation:** the Envoy family is strong on a feature matrix but has
low market visibility, because the buyer is different — most LLM gateway
demand is an AI team's bottom-up "wire up my team's keys this afternoon"
need, and the comparison baseline is the **LiteLLM/Bifrost onboarding
experience**, not Envoy. A single binary is therefore treated not as a
differentiator but as a **precondition for market entry**.

**The empty slot:** "all governance free + tamper-evident audit." This is
inferplane's wedge. (Terminology discipline: as long as the verifier runs on
the same node, "tamper-*prevention*" does not hold — a hash chain provides
tamper-*evidence*; external anchoring, v0.2, raises the guarantee. See §5.4.)

### 1.3 CNCF strategy

- End goal: CNCF Sandbox (a realistic timeline is roughly 8–14 months after first release).
- Position explicitly as **complementary** to existing Sandbox projects:
  "kgateway/Higress route inference traffic; inferplane governs LLM
  consumption. llm-d/vLLM are inferplane's backends."
- From the first commit: DCO enforced, Apache 2.0, a vendor-neutral
  GOVERNANCE.md (no company named), MAINTAINERS.md, SECURITY.md,
  CODE_OF_CONDUCT.md, OTel GenAI semantic-convention naming.
- Public release: hold public release and external promotion until
  legal/policy review completes. Design and code are kept in a publishable
  state in the meantime.

### 1.4 Target clients and v0.1 success criteria

The initial target is AI coding-tool traffic:

- **Claude Code**: uses only the Anthropic Messages API (`ANTHROPIC_BASE_URL`
  swaps the host only; the body stays Anthropic Messages as-is). No OpenAI
  format support.
- **OpenCode and similar**: openai-compatible (`/v1/chat/completions`) is the
  most stable target.

**v0.1 success criterion:** Claude Code and OpenCode authenticate through
inferplane with a virtual key, team quotas aggregate correctly, every request
lands in the audit log, and **prompt-cache hit rate does not degrade versus a
direct connection.**

---

## 2. Architecture Overview

### 2.1 Request flow

*(Historical note: this diagram describes the single-binary v0.1 shape. The
current system splits this into a node-local data plane, `mayu`, and a
control plane, `inferplaned`, that distributes policy and budget leases but
never sits on the request path — see [docs/architecture.md](../architecture.md)
and ADR-031. The pipeline steps below still describe `mayu`'s internal
request handling.)*

```
 Claude Code                OpenCode
     │ Anthropic Messages       │ OpenAI Chat Completions
     ▼                          ▼
┌─────────────────────────────────────────────────────────────┐
│ Ingress Adapters                                             │
│   /v1/messages, count_tokens, /v1/models    /v1/chat/...,   │
│   (Anthropic shape)                         /v1/models      │
│   → canonical conversion (lossless invariant, §2.2)          │
├─────────────────────────────────────────────────────────────┤
│ Governance Pipeline (typed filter chain)                      │
│   ① assign a request ID                                       │
│   ② auth: resolve virtual key → principal (team)               │
│   ③ model resolution: model name → target chain (incl. allow-list check) │
│   ④ rate limit (TPM/RPM, local counters, pre-block)            │
│   ⑤ optimistic quota pre-check                                 │
│   ⑥ (v0.2+) plugin filters: e.g. PII masking                   │
├─────────────────────────────────────────────────────────────┤
│ Router                                                         │
│   priority fallback chain + circuit breaker (passive health check) │
├─────────────────────────────────────────────────────────────┤
│ Provider Layer                                                 │
│   anthropic │ bedrock (invoke/converse/mantle) │ openai_compat │
│   → returns a canonical chunk iterator; SSE serialization is core's job │
├─────────────────────────────────────────────────────────────┤
│ Response path (concurrent with stream relay)                    │
│   ⑦ egress: serialize canonical → the ingress protocol's shape │
│   ⑧ finalize usage → post-debit quota, aggregate budget         │
│   ⑨ write the audit record (after the response completes)       │
│   ⑩ update metrics                                              │
└─────────────────────────────────────────────────────────────┘
     │                    │                    │
     ▼                    ▼                    ▼
 Anthropic API      Amazon Bedrock      vLLM / Ollama / llm-d
```

Core design principles:

- **Quota is two-phase**: an optimistic pre-check, then a post-debit against
  actual tokens once the response completes.
- **Audit is two-phase**: `request_started` right after the authorization
  decision (denials included), `request_completed` after usage settles — a
  crash still leaves a trace of any request that passed auth (§5.4).
- **Stateless across requests**: no session affinity between requests — any
  replica can handle any request. Persistent state lives in the quota store
  and the key/team metadata store. Rate-limit counters, the circuit breaker,
  the audit chain head, and the WAL buffer are **instance-local**, however;
  restart-reset behavior and multi-replica limits are documented in §5.3,
  §4.5, and §5.4.

### 2.2 Canonical schema — decision

The initial premise, "canonical = the OpenAI shape," was incompatible with
the following requirements and was revised:

- Preserve thinking / redacted_thinking / tool_use / tool_result blocks
- Pass `cache_control` blocks through unmodified (§4.4)
- Preserve protocol-specific metadata such as `anthropic-beta`

**Decision:** the canonical schema is a bespoke Go type set in `pkg/schema` —
a **protocol-neutral superset** covering both OpenAI and Anthropic.

Invariants:

1. **Same-protocol round-trip is lossless**: on the Anthropic-ingress →
   Anthropic-family-provider path, content blocks, block order, and
   `cache_control` are re-serialized as semantically identical. (This is the
   lifeline of the Claude Code path.)
2. **Cross-protocol conversion is best-effort**: a path such as OpenAI
   ingress → a Claude provider converts only what is mappable, and lossy
   spots are documented (§3.3's conversion-fidelity matrix).
3. Unknown provider fields are never dropped — they are preserved under an
   `x_provider_extensions` namespace.

### 2.3 HTTP stack

- Standard `net/http` + Go 1.22+'s built-in `ServeMux`. No framework. (Fiber
  is banned — fasthttp is incompatible with `http.Flusher`-based SSE and the
  net/http middleware ecosystem.)
- Separate listeners for the data plane (`:8080`) and the admin plane
  (`:9090` — admin API, `/metrics`, health checks).
- **TLS**: `server.tls` (cert/key ref) supports self-termination. On
  Kubernetes, terminating at the ingress or service mesh is recommended, but
  for a non-K8s single-binary deployment, enabling self-TLS is called out as
  an operational requirement — a plaintext-HTTP configuration for virtual
  keys is not the default posture.

### 2.4 Filter chain interface

*(Historical note: this shipped as `internal/filter`'s `RequestFilter`
interface plus concrete filters under `plugins/<name>/` — ADR-009 — rather
than as a `pkg/plugin` public package. The design intent below is preserved
for context.)*

The shared extension point between plugins and built-in governance steps.
Operates **on the parsed canonical request/response**, not raw HTTP.

```go
// pkg/plugin (design intent; shipped as internal/filter.RequestFilter)
type Filter interface {
    Name() string
    // Transform or reject. On rejection, return a typed error.
    OnRequest(ctx *RequestContext, req *schema.ChatRequest) error
    // Inspect/transform a streaming chunk. Cache-safety note: the request prefix is immutable.
    OnResponseChunk(ctx *RequestContext, chunk *schema.ChatChunk) error
    // Called after usage is finalized. For debiting/aggregation/logging.
    OnComplete(ctx *RequestContext, usage *schema.Usage)
}
```

- Plugins are compiled-in Go interfaces plus a registry (the CoreDNS
  pattern). `plugins/<name>/` + registration via `init()`.
- Freezing a plugin ABI (external process / Wasm) is deferred until v1.0.

---

## 3. Ingress Specification

Full spec compatibility is not the goal. The aim is to **faithfully
implement the surface target clients actually use.**

### 3.1 Anthropic ingress — v0.1 required scope

| Endpoint | Requirement |
|---|---|
| `POST /v1/messages` | Non-streaming + SSE streaming. Reproduce the Anthropic SSE event structure exactly (`message_start`, `content_block_start/delta/stop`, `message_delta`, `message_stop`, `ping`, `error`) |
| `POST /v1/messages/count_tokens` | **Must always return a valid response.** There is a known history of a bare 501/403 crashing Claude Code (a truncated-JSON crash). Never terminate with a 5xx/501 |
| `GET /v1/models` | Called by Claude Code v2.1.129+'s gateway model-discovery feature (`CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY`). Return only the virtual key's **allow-listed models**, in the Anthropic model-list response shape |

count_tokens handling strategy:

- Target resolves to Anthropic Direct → forward to the upstream count_tokens.
- Target is Bedrock → use the Bedrock CountTokens API (when available).
- Otherwise / upstream failure → respond with a conservative estimate (the
  exact strategy was an open question — see §10). In every case, return a
  valid JSON response.

Content preservation (guaranteed by invariant §2.2-1):

- Preserve `tool_use` / `tool_result` / `thinking` / `redacted_thinking`
  blocks losslessly, including order.
- Pass `cache_control` blocks through unmodified (§4.4 design constraint).

Header handling:

- `anthropic-version`, `anthropic-beta`: Anthropic Direct passes headers
  through as-is. The Bedrock path is not a protocol that carries headers
  through verbatim, so it follows the conversion table in §4.3.
- `x-api-key` / `Authorization`: resolved as the gateway's virtual key (the
  gateway's own credential is used upstream, §5.2).

### 3.2 OpenAI ingress — v0.1 scope

| Endpoint | Requirement |
|---|---|
| `POST /v1/chat/completions` | Non-streaming + SSE streaming (`data: {...}` / `data: [DONE]`), including tool calling |
| `GET /v1/models` | Return only the models on the virtual key's **allow-list** — used by OpenCode's model picker |

### 3.3 Cross-protocol conversion fidelity

| Path | Fidelity |
|---|---|
| Anthropic ingress → an Anthropic-family provider (Direct, Bedrock invoke/mantle) | Lossless (invariant) |
| OpenAI ingress → an openai_compatible provider | Lossless |
| OpenAI ingress → an Anthropic-family provider | Best-effort: messages/tools mapping, thinking not exposed |
| Anthropic ingress → an openai_compatible provider | Best-effort: thinking-block drop is documented, cache_control is ignored (with a warning log) |

This matrix is published verbatim in the v0.1 docs.

---

## 4. Provider Layer

### 4.1 Provider interface

```go
// providers
type Provider interface {
    Name() string
    Models() []schema.ModelInfo
    Complete(ctx context.Context, req *schema.ChatRequest) (*schema.ChatResponse, error)
    // A canonical chunk iterator. SSE serialization is core's (egress's) job.
    Stream(ctx context.Context, req *schema.ChatRequest) (iter.Seq2[*schema.ChatChunk, error], error)
}

// Optional capability: a provider that implements this gets count_tokens via
// this path; otherwise the gateway falls back to its own estimator (§3.1).
type TokenCounter interface {
    CountTokens(ctx context.Context, req *schema.ChatRequest) (int64, error)
}
```

- `iter.Seq2` (Go 1.23+) makes goroutine/channel leaks impossible at the
  language level.
- The interface never exposes raw SSE (`io.Reader`) — token counting, audit
  logging, and filters all operate on canonical chunks.
- Adding a new provider = a `providers/<name>/` package + one blank-import
  line in `providers/register.go` + docs. **Zero core diff.**

### 4.2 The three v0.1 providers

1. `anthropic` — Anthropic API Direct.
2. `bedrock` — Amazon Bedrock (§4.3).
3. `openai_compatible` — vLLM / Ollama / llm-d and other OpenAI-compatible servers.

*(A fourth ingress-side addition shipped later: `bedrockapi`, a Bedrock
InvokeModel-shaped passthrough ingress for Claude Code's native
`CLAUDE_CODE_USE_BEDROCK=1` mode — ADR-024. It is an ingress, not a fourth
outbound provider.)*

### 4.3 Bedrock strategy — "Claude native, everything else via Converse"

| Model family | Default path | Rationale |
|---|---|---|
| Claude | **InvokeModel** (native Anthropic Messages shape) | ① Claude Code itself only uses Invoke on Bedrock (documented officially) ② Converse has broken thinking-block order in the wild (LiteLLM #21128 — an Anthropic-protocol violation) ③ preserving `anthropic_beta` is safe on the native path |
| Claude (alternative) | **Bedrock Mantle** (`bedrock-mantle.{region}.api.aws/anthropic/v1/messages`, standard Anthropic Messages shape) | The option to evaluate first since it eliminates conversion entirely. Needs region-availability/feature-parity verification (§10) |
| Non-Claude (Kimi, GLM, Nova, etc.) | **Converse** | One schema covers N models; model-specific parameters go through `additionalModelRequestFields` |

Per-model override in config: `api: invoke_model | converse | mantle`.

`anthropic-version` / `anthropic-beta` conversion table (header "passthrough"
only holds for Anthropic Direct):

| Path | Handling |
|---|---|
| Anthropic Direct | Header passthrough |
| Bedrock InvokeModel | Converted into the body's `anthropic_version` / `anthropic_beta` fields |
| Bedrock Converse | Attempted via `additionalModelRequestFields`; an unsupported beta gets an **explicit 4xx rejection** (silent downgrade is forbidden) |
| Bedrock Mantle | Header passthrough (parity unverified — §10 #2) |

Auth: IRSA / Pod Identity / static credentials / profile. The client's IAM
identity is never propagated to Bedrock (§5.2).

### 4.4 Prompt-cache pass-through — design constraint (v0.1 mandatory)

**The gateway does not modify the prompt prefix.**

- `cache_control` (Anthropic) / `cachePoint` (Bedrock Converse) blocks pass
  through unmodified.
- Forbidden: reordering messages, modifying/injecting the system prompt,
  inserting any metadata that affects the request body. (Adding HTTP headers
  is fine — the cache key is based on the body prefix.)
- Rationale: Claude Code traffic runs at roughly a 96% cache-hit rate. A
  broken cache can spike user cost up to 10× (a cache read is 10% of base
  input price; a 5-minute write is 1.25×).
- This constraint also applies to the filter chain: v0.2+ request-mutating
  plugins (e.g. PII masking) may only break the cache as an explicit opt-in,
  with the cost impact called out in the docs.

### 4.5 Routing / fallback / circuit breaker

- Model mapping is **static config plus an explicit priority fallback
  chain**. No automatic discovery or smart routing — debuggability comes
  first.
- Passive health check: N consecutive failures (default 5) → circuit open →
  exponential-backoff half-open.
- Fallback triggers: specified in config (`rate_limited`, `server_error`,
  `timeout`).
- **Streaming fallback limit**: fallback only works pre-TTFT (before the
  first chunk is sent, at the connection level). An upstream error after the
  first chunk cannot transparently fail over to another provider (HTTP 200
  plus partial body already sent), so the stream is terminated with a
  standard ingress-format error event and partial usage is settled (recorded
  in the audit log as `outcome.partial: true`).
- On fallback: recorded in the response header (`x-inferplane-fallback`),
  the audit log, and metrics.

---

## 5. Governance Layer

### 5.1 RBAC — three layers: Identity / Principal / Policy

| Layer | Definition | Owner |
|---|---|---|
| **Identity** (who) | A human's identity. Not created directly — delegated via OIDC (Dex/Keycloak/Okta). v0.2 | external IdP |
| **Principal** (the gateway's internal subject) | `user` / `team` / a virtual key (service account). Only the mapping rule from an OIDC `groups` claim to a team belongs to the gateway | inferplane |
| **Policy** (what is allowed) | A team × model × action matrix. v0.1 is a model allow-list. OPA integration is on the roadmap | inferplane |

v0.1 implementation scope:

- Issue/revoke virtual API keys + CLI (`inferplane keys create --team x`).
- Key → team binding; team → model allow-list + quota/budget.
- Keys are stored only as a hash (shown once at creation).

### 5.2 Separation of upstream auth from client auth

- A client authenticates **only with a gateway virtual key.** The real
  provider credential is never exposed to the client.
- Bedrock calls are made with the gateway's own credential
  (IRSA/Pod Identity). The client's IAM identity's SigV4 is never propagated.
- Accountability is established via the audit log's
  `principal → virtual key → upstream call` chain.
- Operational note — **noisy neighbor**: every team calls Bedrock with the
  gateway's single AWS credential, so they share an account-level model
  quota. One team's burst is defended first by the gateway's own rate limit;
  the operational docs include a guide for raising AWS Service Quotas.
  Per-team upstream credential override (a Role ARN) is on the v0.2 roadmap.

### 5.3 Rate limit / quota / budget — three separate concepts

These look similar but differ in time axis and purpose, so they are
**designed as separate concepts**.

| Concept | Time axis | Purpose | Enforcement |
|---|---|---|---|
| **rate limit** | seconds/minutes (TPM/RPM) | protection | pre-block, local counter |
| **quota** | days/months (tokens/day, etc.) | policy | optimistic pre-check + post-debit (two-phase) |
| **budget** | cost ($) | finance | aggregated via tokens × a per-model rate table |

Quota and budget both symmetrically declare `on_exceeded: block | warn`
(default `block`) — the on-exceeded behavior is never left implicit. If both
trip at once, **block wins over warn** (block if either says block).

Rate-limit enforcement semantics: RPM is a true pre-block. TPM cannot know
the output token count at request time, so it **pre-blocks on the prior
window's actuals plus the current request's input estimate, then updates the
window counter from actuals after the response completes.** The counter is
instance-local — with N replicas, the effective limit can be up to N× the
configured value; this is documented rather than hidden. Summed enforcement
across replicas (distributed rate limiting via a shared store) is on the
v0.2 roadmap.

*(Update: for **budget**, the mechanism that actually shipped to bound
multi-instance overspend is different from a shared rate-limit store — it is
the **lease pattern** of ADR-034. Each data-plane instance holds only its own
usage view, so rule propagation alone cannot enforce a global budget; the
control plane instead grants each instance a bounded lease — "this much
budget for this interval" — that it enforces locally with zero network round
trips, then reports consumption and renews asynchronously. This bounds
worst-case budget overshoot to roughly `lease grant × connected instances`,
which is a materially different (and tighter) guarantee than the "N× on N
replicas" framing above. Rate-limit counters themselves remain
instance-local as described, since ADR-013's shared-store HA design has not
been implemented.)*

Store abstraction:

```go
// internal/quota
type LimiterStore interface {
    // Optimistic pre-check. A few percent of overshoot is acceptable in a distributed setting.
    Check(ctx context.Context, key string, estimated int64) (Decision, error)
    // Post-debit against actual usage after the response completes (may be async).
    Debit(ctx context.Context, key string, actual int64) error
}
```

- Default implementation: in-memory (single replica).
- HA: Redis/Valkey, opt-in. Pre-check via a local cache with periodic sync;
  post-debit is async. **Exact global consistency is not guaranteed, and a
  few percent of overshoot is documented as possible** — an inherent limit,
  since a token quota is only known after the response completes.
- **Store-failure policy** (`quota_store.failure_mode`): default
  `fail_open` — during a store outage, keep enforcing locally from cache
  (degraded local enforcement) and alert via
  `inferplane_quota_store_errors_total`. `fail_closed` is opt-in (for
  organizations that accept a store outage as a full stop). An async
  `Debit` retries under an idempotency key (the request ULID) to avoid
  double-debiting.
- Rejected alternative: exact global quota via a DB transaction or a
  distributed lock — the hot-path synchronous round trip would destroy
  latency.

Budget implementation constraints:

- **Cost is handled end-to-end as an integer number of micro-dollars (int64,
  µUSD).** Float accumulation drifts, and in a product where budget is a
  core feature that drift becomes a trust problem at month-end
  reconciliation. The internal aggregation types, the audit log
  (`amount_usd_micros`), and the budget store are all µUSD; human-facing
  config (e.g. `usd_per_month`) is converted to µUSD at load time.
- **Settlement happens once per request, with a fixed rounding rule**: rates
  are stored as an integer µUSD/MTok, and cost is computed once, at the
  final token totals when the request completes — not per streaming chunk
  (`tokens × price / 10^6`, round-half-even). Truncating per chunk
  systematically under-bills low-price models. Target error bound: under 1
  µUSD per request.
- **Enforcement closes through a µUSD `BudgetStore`** — a separate store
  interface using the same two-phase pattern as quota (optimistic check +
  post-debit). Aggregation alone cannot implement `on_exceeded: block`.
- **Rate resolution order**: the bundled table, then config
  `pricing.overrides` (keyed by `(provider, model)`). A self-hosted model
  (vLLM/Ollama/llm-d) defining its own rate (GPU-amortization chargeback,
  etc.) is a first-class use case.
- **When a rate is missing** (`pricing.on_missing`): default `allow` —
  aggregate cost as 0, flag `pricing_missing: true` in the audit record, and
  surface it via the `inferplane_pricing_miss_total` metric. `block` is
  opt-in (for tightly controlled organizations).
- **The rate table's key is the `(provider, model)` pair.** The same Claude
  model costs differently on Anthropic Direct versus Bedrock, and Bedrock
  differs by region (the provider instance encodes the region, so a
  provider-scoped key resolves this naturally). The rate table is owned by
  the gateway — a bundled YAML plus user overrides, tracked with a
  `pricing_version` field.
- **Cache-write rates are TTL-tiered**: a 5-minute write is 1.25× the input
  rate, a 1-hour write is 2×. Omitting `cache_write_1h` under-bills a team
  using 1-hour caches with Claude Code. TTL is determined from the upstream
  usage's per-TTL `cache_creation` detail when present, else from the
  request's `cache_control.ttl`.
- Usage's `cache_read_input_tokens` **must be aggregated separately**.
  Billing it at the base input rate over-bills by 10×.
- On a streaming abort: even if the client disconnects first, the upstream
  stream is **drained in the background for a grace period**
  (`server.drain_grace`, default 10s) to receive the final usage chunk before
  settling — this closes off a bypass where repeated early termination
  under-reports quota/budget. Only when draining fails does the gateway fall
  back to an estimate from output chunks, flagged `estimated: true` in the
  audit log. **Trade-off**: draining gives up cancellation propagation — paid
  upstream generation continues for up to the grace period after the client
  disconnects. `drain_grace: 0` cancels immediately and settles as
  `estimated` (an opt-in for cost-conscious organizations).

### 5.4 Audit log

v0.1 scope: **append-only JSONL + structured records + a minimal hash
chain.** Each record's `prev_hash` is the SHA-256 of the previous record
(an independent chain per instance — the chain starts at the instance's boot
record, identified by the record's `instance` field). `inferplane audit
verify` checks chain integrity. External anchoring (S3 Object Lock) is v0.2
— the guarantee before anchoring is documented explicitly as
"tamper-evident," not tamper-preventing.

Records are written in **two phases**: `request_started` right after the
authorization decision (including principal, model, and the authorization
outcome — denials recorded too), and `request_completed` on completion
(including settled usage and cost). A crash still leaves a trace of any
request that passed auth.

Implementation note: hash-chain writing is **serialized through a single
writer goroutine** — the only safe structure when concurrent requests'
`request_started`/`request_completed` records could otherwise interleave and
race on `prev_hash`. The chain defines write order, and that order is
defined by the writer's queue (request handlers only enqueue; the writer
owns fsync and chain hashing).

Record schema (v0.1):

```json
{
  "schema_version": 1,
  "event": "request_completed",        // request_started | request_completed
  "id": "01J...",                      // ULID, sortable by time
  "ts": "2026-06-10T12:34:56.789Z",
  "instance": "inferplane-7d4f-abc12", // identifies the replica
  "principal": {
    "key_id": "ik_5f2...",             // a hash prefix of the key, never the plaintext
    "team": "platform-eng",
    "user": null                       // filled in once OIDC lands (v0.2)
  },
  "request": {
    "ingress": "anthropic",            // anthropic | openai
    "model_requested": "claude-sonnet-4-6",
    "model_resolved": "anthropic.claude-sonnet-4-6-v1:0",
    "provider": "bedrock-us",
    "provider_api": "invoke_model",
    "stream": true
  },
  "outcome": {
    "status": 200,
    "fallback_used": false,
    "fallback_chain": [],
    "partial": false,                  // true if the stream was cut short
    "error": null                      // null, or one of the closed taxonomy strings below
                                        // (internal/audit.DenyReason): model_not_allowed |
                                        // team_rate_limited | team_token_rate_limited |
                                        // team_quota_exceeded | key_rate_limited |
                                        // key_token_rate_limited | team_budget_exceeded |
                                        // key_budget_exceeded | region_blocked — never free text
  },
  "usage": {
    "input_tokens": 1200,
    "output_tokens": 850,
    "cache_read_input_tokens": 45000,
    "cache_creation_input_tokens": 1024,     // total
    "cache_creation_5m_input_tokens": 1024,  // per TTL — priced differently (1.25× vs 2×)
    "cache_creation_1h_input_tokens": 0,
    "estimated": false
  },
  "cost": {
    "amount_usd_micros": 31000,          // integer µUSD — never float (no accumulated drift)
    "pricing_missing": false,            // true if the model had no registered rate (cost aggregated as 0)
    "pricing_version": "2026-06-01"
  },
  "latency": { "ttft_ms": 420, "total_ms": 9800 },
  "trace_id": null,                    // reserved. v0.2: OTel trace correlation (W3C trace-id)
  "prev_hash": "sha256:9f2c..."        // hash of the previous record — per-instance chain (v0.1)
}
```

- Prompt/response bodies are not recorded by default (metadata only). Body
  logging is an explicit opt-in v0.2+ plugin (`prompt-log`).
- Sinks: `stdout` (the K8s-idiomatic choice — collected by Fluent Bit/Loki) /
  `file` / `s3` / `webhook`. Format options: raw JSONL or a CloudEvents
  envelope.

**Sink failure policy** — a product whose differentiator is "tamper-evident
audit" cannot quietly tolerate audit loss, but an unconditional fail-closed
turns an S3 outage into a full LLM outage. This is delegated to config, with
a default:

```yaml
audit:
  failure_mode: buffer_then_block   # fail_open | fail_closed | buffer_then_block (default)
  buffer: { path: /var/lib/inferplane/audit-wal, max_records: 100000, max_age: 5m }
```

- `buffer_then_block` (default): on a required sink's failure, buffer into a
  **local disk WAL** (an append-only file) and keep serving requests. The
  WAL replays on restart, so a crash/OOM/pod eviction never loses a record —
  an in-memory buffer is rejected because loss on crash is equivalent to
  fail-open. Once the buffer fills or `max_age` is exceeded, switch to
  blocking new requests.
- Per-sink `required` flag (default `true`): the failure policy applies only
  to required sinks. An observability sink such as `stdout` can be marked
  `required: false` for best-effort delivery.
- A transition to blocking must be observable:
  `inferplane_audit_write_failures_total{sink}` and
  `inferplane_audit_buffer_utilization_ratio` (§6.2) are wired to alerts.

- v0.2: periodic external **anchoring** of the chain head (e.g. S3 Object
  Lock) — raises the v0.1 hash chain's guarantee to "detectable even after a
  node compromise."
- Honest limit, stated in the docs: as long as writes land on the same disk,
  tamper-*prevention* does not exist. The hash chain plus an external anchor
  is the software-level ceiling.

### 5.5 Admin API auth and bootstrapping

Solves both the auth of the admin API that issues keys, and the "first key"
chicken-and-egg problem.

- **Admin API auth**: the admin listener's (`:9090`) admin API is protected
  by a separate **admin token**, injected via config's
  `server.admin_auth.token_ref` (an env/file/secret ref) — a credential
  system fully disjoint from data-plane virtual keys. The token is stored
  hashed and compared in constant time; `token_ref` may be specified
  multiple times (old and new both valid during rotation). `/metrics` and
  health checks are reachable without the admin token — the scrape path and
  the management path have separate auth.
- **Bootstrapping (the first key)**: the CLI supports a **local mode that
  writes directly to the key store (SQLite)**, bypassing the admin API.
  `inferplane keys create --team x`:
  (1) local mode (`--store <path>`): writes directly to the store file —
  works before the server starts or while it is stopped, for bootstrapping.
  (2) remote mode (default): goes through the admin API + admin token.
  Local-mode constraint: **restricted to pre-boot bootstrapping**, and it
  forces the key-issuance record into the same-path audit WAL (otherwise
  local mode would bypass audit, contradicting the "every governance event
  is recorded" success criterion). When the key store is Postgres (HA),
  local mode is disabled and everything goes through the admin API (to
  avoid split-brain).
- Admin API calls are themselves audit events (key issuance/revocation are
  governance events).
- v0.2: once OIDC is wired up, promote the admin API to IdP-group-based
  authorization; the admin token remains as a break-glass credential.

*(Update: OIDC admin authorization shipped as ADR-004 — the admin API now
accepts either the static admin token or an OIDC ID token on the same Bearer
header, resource-server-only, with the static token kept exactly as the
break-glass path this section anticipated.)*

---

## 6. Observability

### 6.1 Principles

- v0.1: **Prometheus metrics only.** OTel tracing was slated for v0.2 (it has
  since shipped as an opt-in, no-op-by-default seam — ADR-011).
- Metric/attribute naming follows **OTel GenAI semantic conventions from the
  first commit** (`gen_ai.request.model`, `gen_ai.usage.input_tokens`, etc.)
  — so that adding tracing later keeps the same attribute vocabulary.

### 6.2 v0.1 metrics list

| Metric (Prometheus) | Type | Labels | GenAI convention mapping |
|---|---|---|---|
| `gen_ai_client_token_usage_total` | counter | `type`(input\|output\|cache_read\|cache_write_5m\|cache_write_1h), `model`, `provider`, `team` | `gen_ai.usage.{input,output}_tokens` |
| `gen_ai_server_request_duration_seconds` | histogram | `model`, `provider`, `ingress`, `status` | `gen_ai.server.request.duration` |
| `gen_ai_server_time_to_first_token_seconds` | histogram | `model`, `provider` | `gen_ai.server.time_to_first_token` |
| `inferplane_requests_total` | counter | `ingress`, `model`, `provider`, `team`, `status` | — |
| `inferplane_fallback_total` | counter | `model`, `from_provider`, `to_provider`, `reason` | — |
| `inferplane_circuit_state` | gauge | `provider` (0=closed,1=half,2=open) | — |
| `inferplane_quota_utilization_ratio` | gauge | `team`, `window` | — |
| `inferplane_budget_spend_usd_total` | counter | `team`, `model`, `cost_type` | — |
| `inferplane_audit_write_failures_total` | counter | `sink` | — |
| `inferplane_audit_buffer_utilization_ratio` | gauge | — (global) | — |
| `inferplane_pricing_miss_total` | counter | `provider`, `model` | — |
| `inferplane_quota_store_errors_total` | counter | `op`(check\|debit) | — |

- Cardinality guard: `team`/`model` labels are only ever values declared in
  config (never raw request input used directly as a label).
- `inferplane_budget_spend_usd_total` is an observational approximation only
  (Prometheus is float). **The settlement source of truth is the integer
  µUSD aggregate in the budget store / audit log** (§5.3) — metrics are
  never used for settlement.
- `inferplane_audit_buffer_utilization_ratio` approaching 1.0 signals an
  imminent transition to blocking (§5.4's sink failure policy) — an alert
  rule ships with the dashboard.
- A Grafana dashboard JSON ships in the repo (`deploy/grafana/`).

### 6.3 v0.2 trace span shape (preview)

Spans split by `ingress conversion → governance pipeline → routing/fallback →
upstream call`, with GenAI-convention attributes attached.

*(Update: this shipped as ADR-011's opt-in tracing seam — one span per
generative request, W3C trace-context propagation, GenAI-semconv attributes,
and a `trace_id` written into the audit chain; a no-op tracer by default.)*

---

## 7. Full Config Schema Example

```yaml
server:
  listen: :8080
  drain_grace: 10s             # upstream drain time on a mid-stream abort — 0 = cancel immediately (§5.3)
  # tls: { cert_ref: { file: /etc/tls/cert.pem }, key_ref: { file: /etc/tls/key.pem } }
  #   ↑ self-terminated TLS (§2.3) — recommended when running as a non-K8s single binary
  admin_listen: :9090          # admin API + /metrics + health checks (/metrics is unauthenticated — §5.5)
  admin_auth:
    token_ref: { env: INFERPLANE_ADMIN_TOKEN }   # protects the admin API (§5.5; multiple entries = rotation)

providers:
  anthropic-direct:
    type: anthropic
    api_key_ref: { env: ANTHROPIC_API_KEY }    # env: | file: | secret: only
  bedrock-us:
    type: bedrock
    region: us-west-2
    auth: { mode: irsa }                       # irsa | pod_identity | static | profile
  local-vllm:
    type: openai_compatible
    base_url: http://vllm.gpu.svc:8000/v1
    api_key_ref: { secret: { name: vllm-key, key: token } }   # optional

models:
  claude-sonnet-4-6:
    targets:                                   # priority = array order
      - provider: anthropic-direct
        model: claude-sonnet-4-6
      - provider: bedrock-us
        model: anthropic.claude-sonnet-4-6-v1:0
        api: invoke_model                      # invoke_model | converse | mantle
    fallback:
      triggers: [rate_limited, server_error, timeout]  # named `triggers`, not `on`, to dodge the YAML 1.1 boolean trap
      circuit_break_after: 5
  kimi-k2:
    targets:
      - provider: bedrock-us
        model: moonshot.kimi-k2-v1:0
        api: converse
        model_fields: { top_k: 40 }            # additionalModelRequestFields
  qwen-coder:
    targets:
      - { provider: local-vllm, model: Qwen/Qwen2.5-Coder-32B }

teams:
  platform-eng:
    allowed_models: ["claude-sonnet-4-6", "qwen-coder"]
    rate_limit:  { requests_per_minute: 300, tokens_per_minute: 2000000 }
    quota:       { tokens_per_day: 50000000, on_exceeded: block }  # block | warn (default block)
    budget:      { usd_per_month: 5000, on_exceeded: block }       # aggregated internally as integer µUSD (§5.3)
  data-science:
    allowed_models: ["*"]
    quota: { tokens_per_day: 200000000, on_exceeded: block }

pricing:
  source: bundled                              # the bundled rate table
  on_missing: allow                            # allow (default: cost 0 + pricing_missing flag) | block
  overrides:                                   # keyed by (provider, model) — §5.3
    anthropic-direct:
      claude-sonnet-4-6:
        input_per_mtok: 3.00
        output_per_mtok: 15.00
        cache_read_per_mtok: 0.30
        cache_write_5m_per_mtok: 3.75          # 1.25×
        cache_write_1h_per_mtok: 6.00          # 2× — omitting this under-bills 1h caches
    bedrock-us:                                # the provider encodes the region → regional rates resolve naturally
      "anthropic.claude-sonnet-4-6-v1:0":
        input_per_mtok: 3.00
        output_per_mtok: 15.00
        cache_read_per_mtok: 0.30
        cache_write_5m_per_mtok: 3.75
        cache_write_1h_per_mtok: 6.00
    local-vllm:                                # self-hosted: define your own rate (e.g. GPU-amortization chargeback)
      "Qwen/Qwen2.5-Coder-32B":
        input_per_mtok: 0.20
        output_per_mtok: 0.60

plugins: []                                    # v0.1: built-in governance only. v0.2: pii-mask, etc.

audit:
  failure_mode: buffer_then_block              # fail_open | fail_closed | buffer_then_block
  buffer: { path: /var/lib/inferplane/audit-wal, max_records: 100000, max_age: 5m }  # disk-backed WAL (§5.4)
  sinks:
    - { type: stdout, format: jsonl, required: false }   # best-effort, for observability
    - { type: s3, bucket: llm-audit, prefix: gw/, format: jsonl }   # required defaults to true
  # hash chain: on by default in v0.1 — not a toggle (§5.4). v0.2: anchor: { type: s3_object_lock, interval: 5m }

quota_store:                                   # omit for in-memory (single replica)
  type: redis
  addr: redis.infra.svc:6379
  failure_mode: fail_open                      # fail_open (default, keeps enforcing locally) | fail_closed (§5.3)

key_store:                                     # virtual key / team metadata
  type: sqlite                                 # sqlite = single replica only | postgres = required for multi-replica HA (v0.2)
  path: /var/lib/inferplane/keys.db
```

*(Update: two config keys were added after this spec was written and are not
shown above — `policies` (the local GovernancePolicy file channel, ADR-033)
and `control_plane` (the ADR-034 heartbeat to `inferplaned`). They are
mutually exclusive: a data-plane instance has exactly one policy source.)*

Constraints:

- **Inline secrets are forbidden.** A value like `api_key: sk-...` is
  rejected at the config-parsing stage. Only `env:` / `file:` /
  `secret:` (K8s) refs are allowed.
- Helm: `values.yaml` carries only non-secret settings. Auth always goes
  through an `existingSecret` reference. Bedrock supports the IRSA path as
  first-class.

---

## 8. Project Structure

*(Historical note: the layout below is the original single-binary sketch.
The actual structure that shipped splits into two binaries under one Go
module — `cmd/mayu` (data plane) and `cmd/inferplaned` (control plane) — per
ADR-031, with `internal/policy` shared by both so a schema mismatch between
them is a compile error. See [CLAUDE.md](../../CLAUDE.md)'s Project
Structure section and [docs/architecture.md](../architecture.md) for the
current, accurate layout; only `pkg/schema` and `pkg/ulid` promise import
stability today — the `pkg/plugin` package sketched below did not ship as a
separate public package.)*

```
cmd/mayu/main.go           # single binary (serve / keys / audit subcommands)
api/                             # (reserved) CRD types — promoted to v1alpha1 once the config schema stabilizes
pkg/                             # ★ only these two packages are public API
  schema/                        #   canonical types (ChatRequest/Chunk/Usage...)
  plugin/                        #   the Filter interface
internal/
  server/
    anthropicapi/                # /v1/messages, count_tokens, /v1/models ingress
    openaiapi/                   # /v1/chat/completions, /v1/models ingress
    adminapi/                    # key-management API (admin listener)
  pipeline/                      # governance filter-chain executor
  router/                        # model resolution, fallback, circuit breaker
  auth/                          # virtual key, principal resolution
  quota/                         # LimiterStore + in-memory/redis implementations
  budget/                        # rate table, cost aggregation
  pricing/                       # bundled rate data + override merging
  audit/                         # record builder + sinks (stdout/file/s3/webhook)
  config/                        # loading, validation, secret-ref resolution
providers/                       # ★ outside core — provider PRs touch only here
  registry.go                    #   Register(name, factory)
  anthropic/
  bedrock/                       #   invoke_model / converse / mantle paths
  openaicompat/
  testing/mockprovider/          #   a deterministic mock (test-only)
plugins/                         # built-in plugins (v0.2: piimask/, promptlog/)
charts/inferplane/               # Helm chart
deploy/grafana/                  # dashboard JSON
docs/
  specs/                         # this document
  providers/                     # per-provider docs
hack/                            # dev scripts
```

Principles:

- Only `pkg/schema` and `pkg/plugin` promise import stability; everything
  else is `internal/`. (Putting everything under `pkg/` accumulates external
  dependents → SemVer debt.)
- What a provider PR touches: `providers/<name>/` + one line in
  `providers/register.go` + `docs/providers/<name>.md` + a config example.
  CI checks that "a provider PR has zero core diff."

Test strategy (three layers):

1. **Golden-file conversion tests**: request/response pairs per protocol
   conversion, under `testdata/`. The lossless invariant (§2.2-1) is
   verified via golden files — especially thinking-block order and
   `cache_control` position.
2. **httptest fake upstreams**: provider integration tests. CI must pass
   with no real API keys.
3. **mockprovider E2E**: routing/fallback/quota/audit-log scenarios.

---

## 9. Roadmap

*(Update: nearly everything below has shipped, and the project has since
undergone the ADR-031 control-plane/data-plane split, which this original
roadmap does not mention at all. Checkboxes are marked against what actually
exists in the codebase today; see the linked ADRs for what shipped, when,
and why.)*

### v0.1 — "Claude Code attaches in 5 minutes"

Table stakes (their absence loses trust; they are not the differentiator):

- [x] Dual ingress (§3 scope: messages + count_tokens + chat/completions + models); a third, Bedrock InvokeModel passthrough, was added later (ADR-024)
- [x] Three providers: anthropic / bedrock / openai_compatible
- [x] Virtual key issuance/revocation + CLI (`inferplane keys create --team x`) — admin-token auth + local bootstrap (§5.5)
- [x] Team-based token quota (two-phase enforcement)
- [x] Provider failover (explicit priority + circuit breaker)
- [x] Prometheus metrics (GenAI-convention naming) + Grafana dashboard JSON
- [x] Single binary + Helm chart + optional self-TLS listener
- [x] Prompt-cache pass-through guarantee (with golden tests)

Differentiating features (the actual wedge):

- [x] Audit log: append-only JSONL + two-phase records (started/completed) + a minimal hash chain + the `audit verify` CLI + a disk-backed WAL buffer + a sink-failure policy (default `buffer_then_block`, `trace_id` reserved)
- [x] RBAC: team × model allow-list
- [x] rate limit / quota / budget kept as three separate concepts + a `BudgetStore` + a `(provider, model)` rate table + TTL-tiered cache-write pricing + integer-µUSD aggregation + `pricing.on_missing` (supporting self-hosted custom rates)

Governance files (from the first commit): DCO, a vendor-neutral
GOVERNANCE.md, MAINTAINERS.md, SECURITY.md, CODE_OF_CONDUCT.md, a public
roadmap.

### v0.2 — completing governance

Priority ordered by enterprise-adoption impact (2026-06-12, ADR-003 — strike
dead center of the competitors' paywall first):

1. [x] **Free OIDC SSO** (Dex/Keycloak/Okta) — connects the Identity layer, maps `groups` → team (ADR-004)
2. [x] Console governance views — per-team quota/budget gauges (reusing the existing `quota_utilization`/`budget_spend` metrics) + a one-click audit-verify button (a tamper-evidence demo) (ADR-002/003)
3. [x] Chargeback report — the `inferplane report` subcommand: generates a monthly CSV from the audit log's team/model/µUSD data (finance-team lock-in) (ADR-007)
4. [x] PII masking plugin (explicit opt-in for cache destruction + a cost warning) (ADR-009)
5. [x] External audit-log anchoring (S3 Object Lock) — raises the v0.1 hash chain's guarantee (ADR-012)

Lower priority (order not significant):

- [x] OTel tracing (GenAI conventions) (ADR-011)
- [x] Key-issuance self-service page (minimal UI — log in → issue my own key; built on the ADR-002 console) (ADR-010)
- [ ] Redis/Valkey quota-store HA validation, Postgres key store (the required path for multi-replica HA) — designed in ADR-013, not yet implemented
- [ ] Distributed rate limiting (Redis/Valkey — summed enforcement across replicas) — not yet implemented; budget's equivalent problem was instead solved by the ADR-034 lease pattern, which does not require a shared rate-limit store
- [ ] Per-team upstream credential override (a Role ARN — to spread Bedrock noisy-neighbor load)
- [ ] OpenSSF Best Practices badge

### The control-plane split (not in the original roadmap)

Not anticipated by this document at all: the repository was restructured
into two binaries — `cmd/mayu` (the node-local data plane; everything above
that shipped) and `cmd/inferplaned` (a control plane distributing
`GovernancePolicy` documents and issuing budget leases, never on the
inference path) — because a central hop taxes every streamed chunk and
because a central outage should not stop every developer at once (ADR-031).
Follow-on work: policy units/subjects/cadence (ADR-032), three policy
delivery channels — a local file channel (ADR-033), the control-plane push
protocol (ADR-034), and a Kubernetes ConfigMap/CRD channel (ADR-035) — usage
telemetry pushed up from the data plane (ADR-036), console SSO on
`inferplaned` (ADR-037), and a Postgres-backed policy store for the control
plane (ADR-038).

### Phase 3 — applying to CNCF Sandbox (roughly 8–14 months after first release)

- [ ] CRD v1alpha1 (`ModelRoute`, `TeamQuota`, `Provider`) — promoting the
      same schema once the config schema stabilizes (a `GovernancePolicy`
      CRD shipped earlier than this phase envisioned — ADR-035 — but the
      broader CRD set here has not)
- [ ] Security self-assessment
- [ ] OPA integration (an optional externalized Policy layer)
- [ ] Wasm plugins: **evaluation only** (wazero-based; weigh ABI-freeze cost against demand)
- [ ] A Sandbox application naming the complementary relationship with
      kgateway/Higress/llm-d, tracking review comments on cncf/sandbox#486
- [ ] Deliberately out of scope: an MCP gateway (an area where Higress/Envoy
      AI Gateway are already strong — not a differentiator here)

Community track (after public release):

- CNCF Slack channel, a TAG Workloads Foundation talk, a KubeCon CFP
- ADOPTERS.md — a strategy for winning external users (assumes no captive
  internal adopter), running good-first-issue, a goal of one maintainer from
  another organization

### The core Sandbox-application message

> "kgateway/Higress route inference traffic; inferplane governs LLM
> consumption. llm-d/vLLM are inferplane's backends."

---

## 10. Open Questions (as of the original 2026-06-10 draft)

| # | Question | Decision deadline | Resolution |
|---|---|---|---|
| 1 | **count_tokens estimation strategy for non-Anthropic targets**: a conservative heuristic (chars/4, etc.) vs. bundling a local tokenizer (binary-size impact) — fallback accuracy for a provider without `TokenCounter` | v0.1 first spike | Resolved: a conservative chars/4-style estimator, no bundled tokenizer |
| 2 | **Bedrock path verification spike**: Mantle region availability / beta parity / IRSA auth, and whether the Bedrock CountTokens API accepts InvokeModel-shaped input | during v0.1 | InvokeModel chosen as the default Claude path (§4.3); Mantle parity remains unverified |
| 3 | **Bundled rate-table refresh cadence**: ship with releases vs. separate data releases (the missing-rate behavior itself was resolved via `pricing.on_missing`) | before v0.1 implementation | Resolved: bundled with releases, overridable via config |
| 4 | **Streaming-abort cost-estimation accuracy**: the acceptable error band when a grace-period drain fails, and how an `estimated` record feeds budget | during v0.1 | Resolved: drain-then-estimate per §5.3; `estimated: true` is audited, not silently folded into a hard budget decision |
| 5 | **Trademark/collision check for the project name "inferplane"**: required before a CNCF submission | before public release | Open |
| 6 | **OpenAI-ingress → Claude-provider tool-calling mapping detail**: parallel tool calls, per-`tool_choice` conversion rules | during v0.1 | Resolved as part of the OpenAI ⇄ canonical conversion (`internal/openai`) |
| 7 | **Company legal/policy sign-off**: when public release and external promotion become possible | external dependency | Open |

(Two questions from the original numbering were already resolved by the time
of this draft: the former #4, multi-replica chain handling → resolved as a
per-instance independent chain; the former #9, audit buffer durability →
resolved as a disk-backed WAL. See §5.4 and Appendix A.)

---

## Appendix A. Explicit "Regret-Prevention" Decision Log

| Decision | Rejected alternative | Reason |
|---|---|---|
| net/http + ServeMux | Fiber | fasthttp is incompatible with SSE/h2/the middleware ecosystem; unfamiliar to CNCF contributors |
| A typed filter chain | raw http.Handler middleware | Every plugin re-parsing the body; streaming-buffer hell |
| An `iter.Seq2` chunk iterator | io.Reader (raw SSE) | SSE dialect would leak into core; filtering/aggregation becomes impossible |
| Claude = InvokeModel | Unify on Converse | A real case of broken thinking-block order; loss of `anthropic_beta`; mismatch with Claude Code's own practice |
| Non-Claude = Converse | N per-model InvokeModel converters | Would reproduce LiteLLM's conversion hell |
| Static routing + explicit fallback | Automatic discovery / smart routing | Undebuggable routing is the ops team's enemy |
| Allow quota overshoot | Exact enforcement via a distributed lock | A synchronous hot-path round trip would destroy latency |
| Compiled-in plugins | Exposing a Wasm/gRPC ABI in v0.x | Freezing an ABI while the schema is still moving blocks evolution |
| Mandatory secret refs | Allow plaintext in config | Plaintext keys leaking via a ConfigMap is a real incident class, and a reputational security-audit risk |
| Only two public `pkg/` packages | Put everything under `pkg/` | Accumulating external dependents → SemVer debt (k8s's `staging/` as the cautionary tale) |
| File config first | CRD first | API migration cost is high while the schema is still moving |
| Independent-and-compatible with Gateway API | Implement `GatewayClass` | Conformance upkeep is a full-time job, and a head-on competitive posture against kgateway |
| Keep UI out of core (self-service only in v0.2) | A full UI in core | A frontend-maintenance tax on a small-maintainer project |
| Cost = integer µUSD (int64) | Float accumulation | Accumulated floating-point drift causing month-end reconciliation mismatches — a trust failure for a budget product |
| Rate key = (provider, model) | The model name alone as the key | The same model is priced differently by provider/region — a real source of Bedrock billing error |
| Audit defaults to `buffer_then_block` | Unconditional fail-open / fail-closed | Silent loss undermines the differentiator; a hard fail turns an S3 outage into a full LLM outage |
| Admin token + local bootstrap | An unauthenticated admin API / manual DB surgery for the first key | Either an unprotected key-issuance path, or an unsolved chicken-and-egg problem |
| A minimal hash chain in v0.1 + the term "tamper-evident" | Softening the wording, or keeping "tamper-prevention" | Claiming prevention would contradict v0.1's actual guarantee — a 4-model panel consensus flagged this as CRITICAL |
| Audit buffer = a disk-backed WAL | An in-memory buffer | Loss on crash is equivalent to fail-open — flagged by 6 of 6 review models |
| Two-phase audit (started/completed) | A single record after completion | A crashed or denied request would otherwise vanish from the audit trail |
| Settlement = once per request + round-half-even | Cumulative per-chunk truncation | Systematically under-bills low-price models |
| `pricing.on_missing` defaults to `allow` + flagged | Unconditional block | A self-hosted model's (vLLM) own rate/chargeback is a first-class use case — `block` is opt-in |
| Quota store defaults to `fail_open` (keep enforcing locally) | `fail_closed` across the board | Prevents a store outage from becoming a full LLM outage — `fail_closed` is opt-in |
| Fallback limited to pre-TTFT + drain-then-settle | Transparent mid-stream fallover | Impossible given HTTP/SSE semantics, and closes off an early-termination settlement bypass |
