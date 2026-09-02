# Competitive comparison: inferplane vs LiteLLM vs Portkey

Status: analysis (2026-09-01) · Scope: enterprise coding-agent traffic
(Claude Code, Codex, OpenCode) — the market defined in
[enterprise-strategy.md](enterprise-strategy.md) §1.

This document answers one question per dimension: **where does inferplane
beat LiteLLM and Portkey today, and what exactly closes each gap where it
doesn't.** Claims about inferplane cite code and ADRs; claims about
competitors cite [Customer_needs.md](../Customer_needs.md) (field evidence,
2026-06) and public sources (listed at the end, retrieved 2026-09-01).
Honesty discipline matches [roadmap.md](roadmap.md): a row is a win only
with evidence, and known losses are stated as losses.

Companion documents: [roadmap.md](roadmap.md) owns execution status,
[enterprise-strategy.md](enterprise-strategy.md) owns the release gates this
document maps its gaps onto. Where this document and either of those
disagree, they govern.

## The competitors, as of 2026-09

- **LiteLLM** (Berri AI) — the incumbent: a central Python/FastAPI proxy,
  100+ providers, virtual keys/teams/budgets in OSS; SSO, JWT auth, RBAC,
  audit logs and SCIM are paid Enterprise (~$250/mo basic, ~$30k/yr
  premium). Global (multi-worker/multi-replica) rate limiting requires
  Redis; without it, limits are counted per worker and multiply by worker
  count. Field record (2026-06, Korean/Japanese enterprise accounts):
  a prompt-caching bug producing invisible cost spikes, a Bedrock
  Guardrails bypass, two support engineers worldwide, 10-day silence on a
  private-offer request, and one customer at "rip it out this week"
  C-level escalation.
- **Portkey** — the polish leader: an AI gateway with strong observability,
  guardrails, semantic caching, SOC2/ISO 27001/GDPR/HIPAA certification at
  Enterprise tier. Acquired by Palo Alto Networks (April 2026) to become
  the gateway of Prisma AIRS; the unified gateway was open-sourced March
  2026. It is a **central hop**: ~10–20ms P50 added latency self-reported,
  20–40ms on managed edge deployment. Field note (2026-06): "limited
  management features" was the recorded objection at a large Korean
  account.

**The structural difference.** Both competitors put a shared proxy on the
inference path. inferplane splits the roles: `inferplaned` distributes
policy/leases off the request path; `mayu` enforces node-locally
(ADR-031). Every dimension below is downstream of that one decision — it
is why inferplane can be faster than Portkey (no network hop to tax
streaming) and why beating LiteLLM's enforcement accuracy requires lease
work rather than a Redis dependency.

## Scoreboard

| Dimension | vs LiteLLM | vs Portkey | Decisive evidence |
|---|---|---|---|
| Security | **ahead** (audit integrity, credential custody) | mixed (they hold certifications; we hold the architecture) | §1 |
| Cost control | **ahead** (policy-driven tier substitution; leases without Redis) | ahead (budget primitives comparable, substitution is unique) | §2 |
| Cost accuracy | **ahead** (integer µUSD, cache-tier settlement, fail-closed billing) | ahead | §3 |
| Admin convenience | mixed (governance-as-code vs their mature UI) | **behind** (their dashboard is the product) | §4 |
| Performance | **ahead — measured** (9.4× lower overhead p50, 17× on first-token) | **ahead — measured** (2.5× lower overhead p50; plus their network hop) | §5 |
| Model compatibility | behind on breadth, **ahead** on coding-agent depth | behind on breadth, ahead on depth | §6 |
| Authentication | **ahead on price** (their paid tier is our OSS); identity durability now matched (Phase 0b-1/0b-2) — duty separation remains | same shape | §7 |

"Ahead structurally" means the architecture guarantees the property but no
reproducible benchmark or acceptance test yet proves it — those gaps are
called out inline and collected in §8.

---

## 1. Security

**inferplane today.**
- Tamper-evident, hash-chained audit of every request with offline
  verification (`mayu audit verify`, `internal/audit`) and optional S3
  Object Lock anchoring (ADR-012). Neither competitor offers
  cryptographic audit-chain verification; LiteLLM's audit logs are a paid
  feature and are mutable DB rows.
- Credential custody: virtual keys SHA-256-hashed at rest, plaintext shown
  once; the client never sees the upstream key and the upstream never sees
  the client key (`internal/keystore`, CLAUDE.md security mandates).
  Config **rejects** inline API keys — `env:`/`file:`/`secret:` refs only.
- Credential brokering (ADR-040): with a control plane, nodes hold **no
  standing Bedrock IAM at all** — ≤1h STS sessions vended per request,
  fail-closed when the broker is unreachable. No equivalent exists in
  either competitor; both require the proxy to hold long-lived provider
  keys, and LiteLLM concentrates every provider key of the org in one
  central store.
- Bedrock Guardrails enforced on the data plane (ADR-019) — the exact
  control LiteLLM was observed bypassing in the field — and per-team
  region locking (ADR-020), which is the NCT/GDPR requirement
  (Customer_needs §2) that made a gateway mandatory for Korean
  manufacturers.
- PII masking filter, fail-closed on masker error (ADR-009,
  `plugins/piimask`) — and, as of 2026-09-02, a policy-selected **PII
  egress ceiling** (blocked / internal-only / external-masked /
  detector-verified external-unmodified) enforced
  fail-closed on the resolved chain, with provider residency
  classification. Neither competitor has a policy engine choosing the
  egress action per subject with a no-opt-out ceiling.
- Blast radius: a compromised or degraded central gateway is an org-wide
  incident; a compromised `mayu` is one node. The control plane can never
  push executable content by design (roadmap ③ security constraint).

**Honest gaps** (all already P0 in enterprise-strategy §3):
- Guardrail coverage regressed on the Mantle egress path — currently a
  fail-closed *refusal*, not application (strategy §3 first item).
- ~~PII detector evidence~~ **recorded** (2026-09-02): every masked
  request's audit records — and every `external-unmodified` refusal —
  carry the typed detection (`pii: {redactions, kinds}`; counts and
  filter-declared kind names only, never a matched value). Remaining: the
  OpenAI ingress cannot verify (refuses `external-unmodified` outright).
- No SOC2/ISO certification — inferplane is alpha; Portkey Enterprise has
  SOC2 Type 2/ISO 27001/GDPR/HIPAA, and post-acquisition carries Palo
  Alto's security brand. Certification is a business gate, not a code
  gap, but enterprise security reviews will ask.

**Verdict.** Ahead of LiteLLM on the mechanisms enterprises actually got
burned by (guardrail bypass, audit mutability, key concentration). Against
Portkey the architecture is stronger but the paperwork is weaker; the P0
guardrail/PII rows must close before claiming the dimension outright.

## 2. Cost control

**inferplane today.**
- Two-phase governance everywhere: PreCheck denies **before** any counter
  is charged, Settle debits actuals (`internal/governance`).
- Team budgets stay bounded across N data planes via control-plane leases
  (ADR-034) — worst case Σ outstanding grants, without a Redis
  dependency. Hard caps **stay hard through a control-plane outage**
  (lease expiry fails closed, per-rule `failurePolicy`) — LiteLLM's
  enforcement dies with its proxy, taking all traffic with it.
- Per-user budgets enforced (ADR-042 Phase 3); budget alert webhooks
  (ADR-017). **Two-pool user ladder** (Phase 1, 2026-09-02): each person
  gets a premium pool and a total hard cap in one window — premium
  exhausted, the first compatible admin-approved fallback serves (never
  the premium model); total exhausted, denied before egress. Neither
  competitor has a per-person spending ladder with policy-approved
  fallback.
- **Budget-tier model substitution (ADR-041) — the unique feature.**
  `routing.budgetTiers` swaps an already-routed premium model to a cheaper
  tier when the referenced team budget crosses a utilization threshold
  (`router.SubstituteTier`, `internal/tier`), latched per budget window,
  narrowing-only. Neither LiteLLM nor Portkey does policy-latched,
  budget-driven model substitution — their fallbacks trigger on
  *availability*, not *spend*. This is Core Purpose #3 and the direct
  answer to "let people use Opus until the team is at 80%, then Sonnet."

**Honest gaps.**
- ~~Rate is per-replica in-memory~~ **closed for team policy rate rules**
  (ADR-043, 2026-09-01): control-plane rate shares bound the fleet
  aggregate at the configured rpm/tpm without a Redis dependency —
  verified by a two-gateway e2e — and, as of 2026-09-02, the split FOLLOWS
  DEMAND: half the limit divides proportionally to each plane's reported
  EWMA flow, half stays the idle floor. Still per-replica: token quotas,
  per-key rates, and standalone mode.
- Per-user budget and standalone-mode budget have no lease (N× exposure,
  ADR-042 accepted limitation). The user ladder itself shipped (premium
  pool → approved fallback → total cap, 2026-09-02), and budget windows
  now reserve a request's cost upper bound atomically from pre-check to
  settle — concurrent near-cap requests cannot bypass a block-posture cap
  on one plane; the cross-plane half is the lease/user-key gap above.

**Verdict.** Ahead on architecture, on substitution, and now on global
rate accuracy (roadmap ① shipped) and single-plane budget atomicity.
The remaining cost-control caveats are user-keyed leases and
standalone-mode leases.

## 3. Cost accuracy

**inferplane today.**
- Integer micro-USD end to end, round-half-even via `math/big`
  (`internal/pricing`) — never float. LiteLLM tracks spend in floats.
- Cache-tier-correct settlement: cache reads, 5m and 1h writes accounted
  separately, including on interrupted streams (strategy §3 "sound");
  token true-up reconciles PreCheck estimates against actuals (ADR-039);
  ADR-030 closed the zero-cost settlement class, and the Mantle
  reintroduction of it was fixed fail-closed (an unparseable 2xx becomes
  a 502 rather than a free response).
- `mayu pricing check` is a CI gate: a configured model without a pricing
  rate fails the build. Chargeback reporting from the audit chain
  (`mayu report --by team,model`, ADR-007).
- The field contrast: LiteLLM's known caching bug produced *invisible*
  cost spikes — the customer couldn't even diagnose the cause
  (Customer_needs §1). inferplane's cache invariant (verbatim `RawBody`
  forwarding when protocols match) prevents the corruption class, and
  per-tier accounting makes cache economics visible.

**Honest gaps.** Invoice reconciliation is partial (strategy contract row
🔶); cache *efficiency* is measured as tokens, not outcomes (hit ratio,
write-without-reuse — strategy Phase 3). The unbilled-2xx class is now
structurally fenced at every ingress on the non-streaming path (Phase 0a,
2026-09-02); the streaming equivalent remains per-provider discipline.

**Verdict.** Ahead of both. This is the dimension where the codebase is
strongest relative to the field evidence; Phase 3 turns it from accurate
to *explainable*.

## 4. Admin convenience

**inferplane today.**
- Governance-as-code: one CRD-style `GovernancePolicy` document works
  identically via local file, control-plane push, Helm ConfigMap, and a
  real CRD manifest (ADR-033/034/035); file changes apply in ~2s with no
  restart (ADR-006). Reviewable, diffable, GitOps-native — this is the
  Istio-operator experience, and neither competitor has a
  policy-as-versioned-document model.
- Web console (keys, dashboard, logs, policies tab), i18n'd (ADR-027),
  self-service key page (ADR-010); `GET /admin/logs` backed by the
  analytics index; OIDC SSO on both consoles (ADR-026/037).
- Vendor responsiveness is part of this dimension in practice: LiteLLM's
  two-engineer support bench and unanswered sales threads are what
  actually triggered the 2026-06 escalations — an OSS project a customer
  can patch beats a paid tier nobody answers.

**Honest gaps.**
- Portkey's dashboard is genuinely better: it is the product. inferplane's
  console is functional, not polished, and the ADR-038 policy write path
  is experimental with no per-rule authorization or change audit yet.
- No `mayu doctor` (roadmap ④) — fleet debugging is grep-on-node today;
  version skew is visible but not actionable (roadmap ③).
- Admin roles are coarse (whole-console authority — strategy P0
  "management authorization").

**Verdict.** Ahead of LiteLLM OSS tier, behind Portkey's dashboard.
The winning move is not out-polishing a funded SaaS UI — it is doubling
down on governance-as-code + fleet operability (S2: doctor + version
visibility), which a Palo Alto-owned SaaS structurally won't do for
self-hosted fleets.

## 5. Performance

**inferplane today.**
- **Zero network hop.** `mayu` sits on localhost/the node; a central
  gateway taxes every SSE chunk of every response, landing directly on
  time-to-first-token and inter-chunk latency. Portkey's own figure is
  10–20ms P50 added (20–40ms managed edge); a localhost hop is
  microseconds of proxying with no queueing shared across the org.
  Coding agents are the worst case for central hops: long streams, high
  chunk counts, latency-sensitive interactive use.
- **No shared choke point**: no org-wide queue to saturate, no noisy-
  neighbor team, no gateway-wide outage mode (Core Purpose #5). LiteLLM
  under load is a known operational pain (Python/FastAPI central proxy).
- Go static binary, CGO off, verbatim body forwarding on matched
  protocols (zero re-serialization on the hot path for Claude Code →
  Anthropic/Bedrock-Claude traffic).
- Prompt-cache preservation *is* performance for this workload: a broken
  cache (LiteLLM's field bug) means full-price, full-latency prefills on
  every turn.

**Measured — head-to-head (2026-09-02).**
[`benchmarks/gwcompare`](../benchmarks/gwcompare/README.md) runs the real
binaries of all three gateways — mayu on its full governed hot path (key
auth, RBAC, PreCheck with budget reservation, settle, hash-chained audit),
LiteLLM proxy 1.99.0, Portkey OSS gateway 1.15.2 — against the SAME instant
mock upstream on loopback, so the numbers are gateway overhead alone:

| p50, c=1 | direct | **mayu** | Portkey | LiteLLM |
|---|---|---|---|---|
| non-streaming latency | 0.14ms | **0.94ms** | 2.14ms | 7.67ms |
| streaming first-content | 0.14ms | **0.81ms** | (500 — see below) | 11.49ms |

At c=8 the spread widens: mayu 4.02ms p50 vs Portkey 12.36ms vs LiteLLM
47.62ms (non-streaming). Overhead over direct: mayu +0.8ms, Portkey 2.5×
that, LiteLLM 9.4× that — and on first-token time LiteLLM adds 17× mayu's
overhead. Portkey OSS on plain Node 22 returned an internal error on every
STREAMING request in this environment (`TypeError: immutable`), so its
streaming row is honestly unmeasurable here; its non-streaming rows stand.
This is the on-box comparison — a hosted Portkey/LiteLLM additionally pays
its network hop (Portkey self-reports 10–20ms P50), which mayu's node-local
position never pays. The older
[`benchmarks/streaming`](../benchmarks/README.md) harness (mayu vs a
simulated central hop) remains for the network-position argument.

**Honest gaps.**
- Loopback + mock upstream measures the proxy hop, not a production
  deployment: no real model, no TLS, one 4-vCPU container. mayu CPU/RSS
  under sustained concurrency remains to be published.
- Single-replica `mayu` per node is by design, but the shared-K8s-gateway
  profile (later, per strategy §1) will need the ADR-013 successor.

**Verdict.** Ahead of both — now measured, not just structural: lowest
overhead of the three at every percentile tested, on top of the structural
no-network-hop advantage.

## 6. Model compatibility

**inferplane today.**
- Three ingress protocols — Anthropic Messages, OpenAI Chat Completions,
  Bedrock InvokeModel — with lossless same-protocol round-trip and typed
  cross-protocol translation (`pkg/schema`, `internal/openai`), streaming
  included (`*string` frame fields so empty deltas survive).
- Coding-agent-specific fidelity no breadth-first gateway matches:
  verbatim `RawBody` forwarding preserves `cache_control` and prompt-cache
  hits (the cache invariant); `count_tokens` never returns non-200
  (a non-200 crashes Claude Code); model-level fallback covers hardcoded
  client model IDs (ADR-029); Bedrock routes Claude via InvokeModel and
  other families via Converse with schema translation.
- Providers: Anthropic 1P, Amazon Bedrock, OpenAI-compatible (vLLM,
  Ollama, GLM endpoints, …) — exactly the triad enterprise coding-agent
  fleets run (Customer_needs §2: mixed 1P + Bedrock is the observed
  pattern), plus self-hosted.

**Honest gaps.**
- Breadth: LiteLLM claims 100+ providers, Portkey similar. inferplane has
  three families; embeddings shipped (roadmap ⑤, 2026-09-02:
  `POST /v1/embeddings`, a governed passthrough over the optional
  `providers.Embedder` capability — `openai_compatible` serves it, RBAC/
  budget/PII/audit apply in full, and a 2xx without usage is refused). No
  images/audio/rerank — deliberate non-goals (README) — and no Bedrock
  Titan/Cohere embed mapping yet. Still a real loss for any buyer scoring
  provider-count checkboxes.
- **Real-client verification (2026-09-02,
  [docs/verification/coding-agents.md](verification/coding-agents.md)):**
  ALL THREE target clients — Claude Code 2.1.258, OpenCode 1.18.26, and
  Codex 0.152.1 — completed REAL turns through a running mayu with a
  virtual key: served, settled in µUSD, hash-chain-audited, zero
  client-side patches. Codex initially exposed a genuine gap (current
  Codex removed `wire_api = "chat"` and requires the OpenAI Responses
  API), closed the same day: `POST /v1/responses`
  (`internal/server/responsesapi`) adapts the Responses wire onto the
  chat pipeline, built and tested against Codex's own captured request.
  Neither LiteLLM nor Portkey advertises coding-agent client verification
  at this level; against a real provider (vs this mock upstream) is the
  remaining verification step.

**Verdict.** Behind on breadth, ahead on depth-where-it-counts. The
winning frame is "the gateway that doesn't corrupt your coding agent's
traffic" — LiteLLM's caching bug is the market's own evidence that
breadth-first translation layers break Anthropic-native semantics. Codex
fixture verification converts the target market claim from aspiration to
fact.

## 7. Authentication

**inferplane today — all of it OSS/free.**
- End-user: virtual keys (SHA-256 at rest, once-shown), team RBAC,
  per-key model allow-lists, post-routing RBAC re-check so fallback
  targets can't escape the allow-list (CLAUDE.md invariant).
- Human SSO: OIDC on the mayu console (ADR-004/026), on the inferplaned
  console (ADR-037), and — the differentiator — `mayu login`: developers
  OIDC-authenticate from the CLI and receive **short-lived virtual keys**
  (ADR-028), so laptops hold no long-lived credential.
- Workload: STS credential brokering (ADR-040) removes standing node IAM.
- Pricing contrast: SSO, JWT auth, RBAC and audit logs are exactly the
  features LiteLLM paywalls at Enterprise ($250/mo–$30k/yr). inferplane
  ships them under Apache-2.0.

**Honest gaps** (strategy P0 "durable identity" + "duty separation"):
- ~~Identity is not durable~~ **closed** (Phase 0b-1/0b-2, 2026-09-02):
  `UserID = (issuer, subject)` is first-class — minted at CLI login and
  admin issuance, enforced and attributed through governance and audit —
  so key rotation and a second device keep one budget ledger and one
  audit identity (e2e-proven).
- No fixed admin role set (`platform-admin`, `auditor`, …), no mutation
  audit on management writes.
- No SAML/SCIM (OIDC only) — some enterprise IdP checklists ask.

**Verdict.** Ahead on price and on credential lifetime (short-lived keys +
STS brokering have no competitor equivalent); behind on identity
durability and duty separation until strategy Phase 0b ships.

---

## 8. What it takes to win — consolidated

Every gap above is already tracked; this table is the priority lens from
the competitive angle. No new workstream is invented here.

| # | Gap (dimension it blocks) | Existing tracker | Competitive stake |
|---|---|---|---|
| 1 | ~~Streaming benchmark harness~~ **shipped** (`benchmarks/streaming`); remaining: side-by-side vs real Portkey/LiteLLM installs (Performance) | this document | Converts "faster than Portkey" from architecture argument to measured number — the harness now shows mayu +0.8ms vs +17ms for a best-case central hop |
| 2 | Durable identity **shipped** (0b-1/0b-2) and mayu-plane duty-separation roles **shipped** (0b-3: six fixed roles, capability middleware, audited 403s); and control-plane policy-write gating + mutation audit **shipped** (0b-4); remaining: mutation records for mayu provider/team writes | strategy Phase 0b (P0) | Identity + RBAC now match both competitors' enterprise tiers on the data-plane console — LiteLLM charges ~$30k/yr for this |
| 3 | ~~Global rate accuracy — rate shares~~ **shipped** (ADR-043: equal-split v1 + the demand-following EWMA split; two-gateway e2e proves 429 at the global limit); remaining: token quotas, per-key rates (Cost control) | roadmap ① / S1 | Was the one row where LiteLLM-with-Redis beat us — now matched without the Redis dependency, and the split follows demand |
| 4 | Durable ledger + window IDs (Cost control, Cost accuracy) — **both halves shipped**: `INFERPLANED_LEDGER_PATH` SQLite ledger (restart resumes grants exactly, dead-plane spend survives) + control-plane-owned windowID epochs (UTC calendar ids on every grant, wholesale ledger reset on epoch change, stale-epoch reports refused, mayu baselines timezone skew) | roadmap ② / S1, strategy Phase 1 | "Bounded overspend" is now a durable, fleet-wide-single-window guarantee — rollover is deterministic, not a per-plane heuristic |
| 5 | ~~Codex/OpenCode wire fixtures~~ **shipped** (`internal/server/openaiapi/agent_wire_test.go`); remaining: recorded captures from real client sessions (Model compatibility) | Core Purpose #1 (🔶) | Both agents' documented wire shapes — agentic tools, tool-call round trips, `include_usage` — now pass on both provider wires; a real-client capture closes Purpose #1 |
| 6 | Guardrail-on-every-egress + PII egress ceiling (Security) | strategy Phase 0a/2 (P0) | LiteLLM's guardrail bypass is our best sales evidence — only if we provably don't have one |
| 7 | ~~`mayu doctor` + fleet version ops (Admin)~~ **shipped in full**: version visibility (roadmap ③ phase 1), the `mayu doctor` CLI, and `GET /admin/debug/governance` (roadmap ④ complete); remaining: signed self-update (③ phase 2) | roadmap ③④ / S2 | The self-hosted-fleet operability Portkey's SaaS won't build |
| 8 | Benchmark-backed cache-efficiency reporting (Cost accuracy) | strategy Phase 3 | Turns the anti-LiteLLM caching story into a dashboard, not an anecdote |

Sequencing already decided elsewhere stands: strategy §4 orders 0a → 0b →
1 → 2 → 3 → 4 by trust boundary; roadmap sprints S1–S3 order the
distributed-enforcement work. Item 1's harness landed with this document;
the rest follow their existing trackers.

## Sources

External claims retrieved 2026-09-01:

- Portkey latency overhead, enterprise features, certifications:
  [truefoundry.com/blog/portkey-pricing-guide](https://www.truefoundry.com/blog/portkey-pricing-guide),
  [portkey.ai/buyers-guide/ai-gateway-solutions](https://portkey.ai/buyers-guide/ai-gateway-solutions)
- Portkey acquisition by Palo Alto Networks (2026-04) and gateway
  open-sourcing (2026-03):
  [truefoundry.com/blog/best-ai-gateway](https://www.truefoundry.com/blog/best-ai-gateway)
- LiteLLM enterprise tiers/pricing, SSO/audit/RBAC paywall:
  [docs.litellm.ai/docs/enterprise](https://docs.litellm.ai/docs/enterprise),
  [truefoundry.com/blog/litellm-pricing-guide](https://www.truefoundry.com/blog/litellm-pricing-guide)
- LiteLLM Redis requirement for multi-worker rate limits:
  [docs.litellm.ai/docs/proxy/redis_requirements](https://docs.litellm.ai/docs/proxy/redis_requirements)
- Field evidence (caching bug, guardrail bypass, support staffing,
  escalations): [Customer_needs.md](../Customer_needs.md) (internal,
  2026-06-26)
