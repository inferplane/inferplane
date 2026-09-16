# Enterprise product strategy

Status: canonical product direction · Last reviewed: 2026-09-16 against main `6b5cf9c` · Release posture: **alpha**

Owns: target market, enterprise contracts, release gates.
Does not own: implementation status ([roadmap.md](roadmap.md)), current system
description ([architecture.md](architecture.md)), implementation decisions
([decisions/](decisions/)), request flow (`CLAUDE.md` → Request Flow).
Where this document and an ADR disagree, the ADR governs until superseded.

Inferplane remains alpha and must not be described as enterprise
production-ready until every P0 below is closed and the acceptance suite passes.

Execution priorities and migration gates are in the
[current hardening program](superpowers/plans/2026-09-16-enterprise-hardening-program.md).
It distinguishes shipped mechanisms from remaining product contracts; do not
restart the old ledger/rate-share roadmap from a pre-ADR-045 checkout.

## 1. Scope

The governance plane for enterprise coding-agent traffic (Claude Code, Codex,
OpenCode) — not a general-purpose LLM proxy. First production topology: a fleet
of developer-local `mayu` data planes controlled by `inferplaned`, with the
control plane off the inference path.

**The first production qualification target is a developer-local `mayu` fleet.**
The shared Postgres gateway profile is implemented (ADR-046); its availability
depends on an HA database endpoint and still requires deployment/load qualification.
Keeping its production qualification separate does not mean the profile is absent.

**Non-goals for the first release:** generic API gateway, provider/modality
count, semantic response caching, custom authorization languages and MCP traffic
routing. Do not infer deployed shared-gateway HA from implementation tests.

## 2. Enterprise contracts

| Contract | Requirement | Status |
|---|---|---|
| Durable identity | `UserID = (OIDC issuer, subject)`. Key rotation, re-login, restart, and a second device must not split policy, budget, quota, or audit attribution. Email/owner strings/key IDs are not identities. | ❌ P0 |
| Duty separation | Fixed roles (`platform-admin`, `policy-admin`, `provider-admin`, `budget-admin`, `auditor`, `team-admin`) with org/team scope. Every control-plane endpoint authorizes after authenticating. Every policy/provider/pricing/budget/role mutation records actor, capability, scope, before/after hash, generation. | ❌ P0 |
| Two-pool user budget | Premium pool + total hard cap in one explicit window. Premium exhausted → first compatible model in an admin-approved fallback set; total exhausted → deny before egress. Token quotas must state fallback-or-block explicitly, never inherit monetary behavior. | ❌ P0 |
| Pre-egress PII policy | Typed detector result; the policy engine (not the plugin) picks `external-unmodified` \| `external-masked` \| `internal-only` \| `blocked` and attaches it as an **egress ceiling**. Later stages may only narrow it. Detector/masker failure is fail-closed. `external-unmodified` requires a completed detector chain reporting nothing protected. | 🔶 partial (ADR-043/044 inspection, InternalOnly/Block/Mask and reinspection implemented; detector and deployment qualification remain) |
| Fleet enforcement accuracy | Enforcement key ≥ `(org, UserID, pool, windowID)` in a durable ledger. Grants reserve spend authority centrally before delivery; only proven unused authority can be returned. Expiry alone never refunds. Rate/quota must not multiply by data-plane count. | 🔶 mechanisms implemented in ADR-045/046; typed person identity, user-pool contract and operational qualification remain P0 |
| Guardrail / residency | A configured guardrail and region lock apply on **every** egress path, with no opt-out reachable from routing config. | 🔶 refusal protection implemented; full application/qualification remains P0 |
| Cost explainability | Every served request settles observed usage against an immutable pricing version; every request mutation the gateway performs is recorded. Cache reads, 5m/1h writes, hit ratio, write-without-reuse, and masking/model-switch cache loss are reported. | 🔶 partial |

A hard cap governs admission against a versioned pricing table; it is not a
promise that a later invoice matches the internal ledger. Reconciliation
reports the difference. A target whose conservative upper bound cannot be
computed must be blocked, not admitted.

## 3. Current state

### Sound and worth preserving

Node-local data plane with the control plane off the request path (ADR-031) ·
virtual-key isolation and per-key/model allow-lists · team and user-subject
model access with post-routing RBAC re-check · region locking · budget-tier
substitution, fallback chains, circuit breakers (ADR-041) · integer µUSD
pricing, round-half-even · separate cache-read/5m/1h accounting, including
interrupted streams · OIDC login, short-lived virtual keys, STS credential
brokering (ADR-028/040) · hash-chained audit, optional encrypted body capture,
S3 anchoring (ADR-012/018) · optional Postgres usage analytics (ADR-036).

### Policy-aware routing and coding-client support (ADR-043/044/047)

Local finite inspection and an enforced destination restriction now constrain every
attempt. Independent context preferences start in Shadow; Enforce is opt-in for
completely inspectable single-user-turn requests without history/tools/media/
reasoning/structured output. The input threshold chooses simple versus complex;
a distinct compatible complex target can be selected above it. Privacy always enforces.
ADR-044 extends this with an optional normal class, compatible tool/history
sessions, bounded local successful-target affinity, strict budget targets,
policy-selected Mask and independent reinspection. InternalOnly and Mask compose;
unknown or unsafe content refuses rather than being labeled clean.
Boundary labels remain operator assertions and detector coverage is finite.
The legacy filter plugin is not the whole privacy implementation.

Responses ingress and native/stateless provider adapters are implemented.
ADR-047 adds Bedrock provider compatibility, model metadata and installed-client
acceptance tests against local fake endpoints. These establish a portable
protocol contract, not arbitrary model quality, opaque history transfer or every
backend's tool/effort support. See [adaptive routing](adaptive-routing.md).

Before promoting context to Enforce, compare task success, total settled cost
including cold-cache writes/retries, p95 latency, and privacy negative cases against
predeclared baseline gates. No measured savings or production readiness follows
from passing tests. Upgrade binaries/CRD before activation and use require_sync for
CP protection before first policy delivery. [Operator guide](policy-routing.md).

### P0 — blocks the enterprise-ready claim

**Selected destinations are not yet uniformly protected from HTTP redirects.**
A local 307 reproduction at `6b5cf9c` showed the Anthropic and Chat Completions
providers follow redirects with the request body and a synthetic gateway
credential. Native Responses already refuses redirects. Close this concrete
transport difference before claiming every egress remains on its approved
destination; see the [bounded fix plan](superpowers/plans/2026-09-16-provider-redirect-boundary.md).
This is a loopback reproduction, not evidence of a live compromise.

**Guardrails on the Mantle egress path: refused, not applied.**
Original bug: `guardrailFor` was called on the Converse and InvokeModel paths
only; the Mantle paths (`providers/bedrock/mantle.go` `Complete`/`Stream`)
never called it, while `internal/server/bedrockapi/invoke.go` writes
`pr.GuardrailID` into the tamper-evident record unconditionally — so a
`model_api: {"<model>": "mantle"}` entry silently disabled a mandated
guardrail **and the audit chain attested that it was applied.** Fixed:
`mantleGuardrailCheck` (`providers/bedrock/bedrock.go`) now refuses any
guarded request routed to Mantle with a 400 naming the conflict, before
egress — the bypass and the falsified attestation are gone. Remaining gap
(why the contract row stays ❌): Mantle has no guardrail parameter, so the
requirement "a configured guardrail *applies* on every egress path" is still
unmet — a guarded team simply cannot use Mantle-only models until guardrail
evaluation exists off the InvokeModel/Converse APIs (or the refusal is
accepted as the permanent posture and documented as such).

**A 200 response could bill zero on the Mantle path.**
Original bug: the Bedrock ingress settles only when `resp.Parsed != nil`, and
Mantle's `Complete` dropped `Parsed` on any unmarshal/conversion failure — a
malformed-but-200 upstream response was served with no debit, no cost, and
empty audit usage (the ADR-030 zero-cost class re-entering through a new
path; Converse and InvokeModel build Parsed from typed fields and cannot
reach this state). Fixed: that path is now fail-closed — an unparseable 2xx
returns a synthesized 502 (`providers/bedrock/mantle.go` `Complete`), and the
stream path errors when a 200 stream yields no parseable frame, so nothing is
served unbilled. Remaining gap: fail-closed conversion is a per-path
discipline, not a structural guarantee — a future egress that builds `Parsed`
from re-parsed JSON must repeat it (no test fences the invariant generically).

**Per-user accounting exists; durable person identity is still absent.**
ADR-045 implements global GovernancePolicy money budgets, including user-only
and team-user subjects. ADR-046 adds global user rate/token quotas and shared
key/team storage. These scopes still use the configured opaque owner/subject:
`internal/keystore/keystore.go` and `internal/server/authapi/authapi.go` do not
persist a verified `(issuer, subject)` human identity. A shared counter for an
owner is not protection against equal subjects from different issuers.
Legacy/local modes retain their documented scope limits; do not generalize those
limits to the explicitly enabled durable/shared profiles. Identity migration must
preserve existing accounts, grants and pending permits rather than resetting them.

**The complete user-pool contract remains open.** Legacy ADR-041 substitution
keeps its optional behavior. ADR-044 strict `enforceTargets` rules instead
constrain every attempt, including threshold 100, and can refuse when a target
is unavailable. A soft switching meter can coexist with an independent hard cap.
This is useful implemented machinery, but not yet the complete typed-person
premium/total pool, approved fallback-set and management contract above.

**Management authorization is coarse.** `authnWrite` separates policy writes
from the heartbeat token; `recordMutation` emits actor/operation/content-hash
records. Provider/model writes have an admin check. What remains is the six-role
org/team capability model and durable mutation evidence covering before/after
state and generation. Current logging is not a transactionally coupled mutation
audit guarantee.

**Enforcement guarantees depend on the deployment profile.** Legacy ADR-034 and
standalone memory stores retain their limitations. ADR-045 commits monetary
authority in Postgres and journals each node's per-attempt reservations locally;
expiry never refunds central credit. ADR-046 synchronously resolves shared keys
and atomically reserves rate, token and monetary scopes against Postgres.
Helm permits multiple replicas only in that explicit shared profile.
Node-local valid-credit continuation and shared-mode refusal on DB failure are
different contracts. Neither means that a production HA database is deployed or
that a compromised developer machine cannot bypass upstream access.

**The broader PII/masking qualification remains partial.** ADR-043/044 implement
typed local inspection, InternalOnly/Block/Mask composition and reinspection
across the attempt chain. Detectors remain finite heuristics; opaque/unknown
content can refuse, and boundary labels are operator assertions. Prove detector
coverage and actual destination controls for a deployment before claiming
compliance. Tests do not establish universal PII detection.

### P1 — operational competitiveness

- **Undisclosed request mutation.** `providers/bedrock/converse.go` and
  `providers/bedrock/mantle.go` contain model-family parameter exceptions in
  different wire vocabularies. Existing strip/Converse/Mantle regression tests
  run in CI. The remaining gap is versioned live capability/probe provenance
  and per-request mutation evidence. A dropped `temperature: 0` can change
  sampling semantics; a once-per-model log does not explain an individual
  request's transformation. Pricing has `mayu pricing check`; capability
  qualification needs a similarly explicit process.
- **Cache behavior differs by path.** Anthropic passthrough and Bedrock Claude
  InvokeModel preserve `cache_control`; Bedrock Converse does not map it to
  `cachePoint`. The legacy `internal/cache.VolatileStore` interface remains
  unused; ADR-044's separate bounded local affinity is implemented and is not
  durable cross-node session pinning.
- **Cache efficiency is measured as tokens, not outcomes** — no hit ratio,
  write-without-reuse, prefix fragmentation, or model-switch loss attribution.
- **Request audit lacks durable identity** — records carry key ID and team;
  usage telemetry carries the key owner.
- **Mantle errors miss model-level fallback.** `isModelNotFound`
  (`internal/server/bedrockapi/invoke.go:277-282`) matches on
  `"ValidationException"`, which Mantle's OpenAI-shaped error bodies need not
  contain.
- **Alpha qualification posture** — `.github/workflows/ci.yml` runs static
  builds, race/vet/format, CRD, harness, Postgres integration and vulnerability
  checks. Its DSN enables authority and other integration suites, but keystore
  tests read the distinct `KEYSTORE_TEST_POSTGRES_DSN`, which CI currently omits;
  18 keystore integration tests skip under that environment. Close this variable
  mismatch and assert required DB tests execute. The shared Helm profile has separate persistent audit storage,
  anti-affinity and a disruption budget. Production sizing, network controls,
  DB failover/load evidence and signed release/deployment qualification remain
  operational work; default resource requests still need operator configuration.

## 4. Delivery sequence

Ordered by trust boundary. Each phase is a separate design spec and plan.
The table describes contract closure, not a claim that every mechanism is
unimplemented. ADR-044/045/046 shipped portions out of the original order.
Use the current hardening program for the remaining execution sequence.

| Phase | Work | Exit gate |
|---|---|---|
| **0a. Invariant guards** | Refuse or record honestly when a configured control is unreachable; account for observed usage and retained uncertainty on every billable attempt; close redirect-boundary and capability-provenance gaps | No egress path silently drops a mandated control or accounting obligation; interrupted streams retain uncertainty and nonbillable count endpoints remain local/200 |
| **0b. Identity & management trust** | First-class `(issuer, sub)` and typed service accounts; credentials reference identity; fixed roles with org/team scope; capability check on every management endpoint; mutation audit for policy/provider/pricing/budget/role | Key rotation and a second device retain the same policy and audit identity; cross-role negative authorization tests pass |
| **1. User budget state machine** | Durable window IDs and enforcement ledger; premium + total pools; atomic reserve/settle/release; approved fallback sets with compatibility checks; explicit quota fallback-or-block; non-overlapping fleet leases | Concurrent requests, restart, key rotation, and two data planes cannot resurrect spend or bypass the total cap |
| **2. Pre-egress PII policy** | Typed detector/transform contract; central action selection; destination classification; fail-closed detector and masker; PII-free OTel plus correlated audit | Detector timeout, transform failure, and every action prove unmasked protected data never reaches an external target |
| **3. Cache & cost efficiency** | Converse cache-point translation; hit/write/waste and fragmentation metrics; model-switch and masking loss attribution; live per-path write-then-read tests; Bedrock billing reconciliation | Operators explain cache-cost changes from telemetry without reading prompt bodies |
| **4. Fleet ops & release hardening** | Fleet-wide rate/quota accuracy; data-plane version visibility, rollout, diagnostics; production packaging and network controls; race/vulnerability/signing/release CI; SLOs and runbooks | The scenario suite and supply-chain gates pass from a clean checkout |

## 5. Acceptance suite

Unit tests of isolated functions are not sufficient evidence. Every scenario
must show agreement among the actual target model/provider, enforcement
ledger, usage ledger, OTel metadata, and audit record.

**Identity** — two users in one team keep independent balances · one user on
two devices is one ledger · CLI key re-mint resets no budget, quota, or audit
identity.

**Budget** — concurrent near-cap requests cannot bypass the cap · an upper
bound exceeding balance denies before egress · premium exhaustion selects an
approved compatible target · no compatible fallback blocks (never serves the
premium model) · total exhaustion issues no provider request · a fallback after
a failed attempt takes a separate reservation · uncertain usage after
cancellation stays unavailable until reconciled · control-plane interruption
and window rollover resurrect no spend.

**PII** — maskable data is irreversibly transformed and evidenced as metadata
only · internal-handling data reaches only approved internal targets · an
`internal-only` request under budget or provider fallback never goes external ·
detector or masker failure makes no unmasked external call.

**Egress controls** — every path applies a configured guardrail and region
lock, or refuses the request; no routing-config value is an opt-out · every
request mutation the gateway performs appears in the audit record.

**Cost** — a stable same-model prefix produces a cache write then a measured
read · masking and model switching attribute the expected cache loss · every
billable attempt records known observed cost and retains unproven liability
explicitly. An interrupted stream may already have HTTP 200; it must not invent
final usage or refund uncertain cost. Legitimate rounded-zero/free usage is
valid, and local nonbillable count responses are outside this billing assertion.

**Administration** — a forbidden admin action is denied and evidenced · every
policy/provider/budget mutation records actor, scope, diff hash, and generation.
