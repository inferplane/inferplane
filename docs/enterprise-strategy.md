# Enterprise product strategy

Status: canonical product direction · Last reviewed: 2026-08-28 · Release posture: **alpha**

Owns: target market, enterprise contracts, release gates.
Does not own: implementation status ([roadmap.md](roadmap.md)), current system
description ([architecture.md](architecture.md)), implementation decisions
([decisions/](decisions/)), request flow (`CLAUDE.md` → Request Flow).
Where this document and an ADR disagree, the ADR governs until superseded.

Inferplane remains alpha and must not be described as enterprise
production-ready until every P0 below is closed and the acceptance suite passes.

## 1. Scope

The governance plane for enterprise coding-agent traffic (Claude Code, Codex,
OpenCode) — not a general-purpose LLM proxy. First production topology: a fleet
of developer-local `mayu` data planes controlled by `inferplaned`, with the
control plane off the inference path.

**First production claim covers developer-local `mayu` fleets only.** A shared,
horizontally scaled Kubernetes data plane is a later profile.

**Non-goals for the first release:** generic API gateway, provider/modality
count, semantic response caching, multi-replica shared-gateway HA, custom
authorization languages, MCP traffic routing.

## 2. Enterprise contracts

| Contract | Requirement | Status |
|---|---|---|
| Durable identity | `UserID = (OIDC issuer, subject)`. Key rotation, re-login, restart, and a second device must not split policy, budget, quota, or audit attribution. Email/owner strings/key IDs are not identities. | 🔶 phases 0b-1/0b-2 shipped 2026-09-02 per the [design spec](specs/2026-09-02-durable-identity-and-management-authz.md): canonical `iss#sub` minted at CLI login and admin issuance (keys.user_id, ALTER-migrated), enforcement + usage attribution key on it with a bounded Owner fallback, audit `principal.user_id`, bare-sub policy fallback; e2e proves two keys/devices = one ledger. Remaining: config-declared metadata paths adopt it fleet-wide, and the acceptance suite's remaining identity rows |
| Duty separation | Fixed roles (`platform-admin`, `policy-admin`, `provider-admin`, `budget-admin`, `auditor`, `team-admin`) with org/team scope. Every control-plane endpoint authorizes after authenticating. Every policy/provider/pricing/budget/role mutation records actor, capability, scope, before/after hash, generation. | 🔶 phases 0b-3/0b-4 shipped 2026-09-02 per the [design spec](specs/2026-09-02-durable-identity-and-management-authz.md): mayu-plane capability middleware per route class (opt-in `oidc.role_mappings`, audited 403s, unconfigured = byte-identical); control-plane policy writes gated on policy-admin (`INFERPLANED_OIDC_ROLE_MAPPINGS`) with `admin_mutation` records (actor, capability, scope, before/after sha256, generation → `INFERPLANED_MUTATION_LOG` or process log). Remaining: mutation records for mayu provider/team writes; acceptance-suite Administration rows |
| Two-pool user budget | Premium pool + total hard cap in one explicit window. Premium exhausted → first compatible model in an admin-approved fallback set; total exhausted → deny before egress. Token quotas must state fallback-or-block explicitly, never inherit monetary behavior. | 🔶 v1 shipped 2026-09-02 per the [design spec](specs/2026-09-02-two-pool-user-budget.md): `budget.premium {limitMilliUSD, models, fallback}` on user-subject rules (CalendarMonth v1); premium-model spend debits both pools; exhaustion → `router.ApplyUserPool` serves the FIRST compatible fallback and BLOCKS when none is (never the premium model — e2e-proven); total = the existing ADR-042 hard cap. `/v1/usage` gains `user_premium`. Remaining: atomic reserve/settle for concurrent near-cap requests, quota fallback-or-block wording, user-keyed leases (counters are per-plane, the ADR-042 caveat) |
| Pre-egress PII policy | Typed detector result; the policy engine (not the plugin) picks `external-unmodified` \| `external-masked` \| `internal-only` \| `blocked` and attaches it as an **egress ceiling**. Later stages may only narrow it. Detector/masker failure is fail-closed. `external-unmodified` requires a completed detector chain reporting nothing protected. | ✅ shipped 2026-09-02: GovernancePolicy `pii: {egress}` rules fold most-restrictive per subject and every ingress enforces the result fail-closed on the RESOLVED chain (so budget substitution/fallback cannot route around it) — `blocked` refuses; `internal-only` reaches only providers explicitly `classification: internal` (unlabeled = external, the D7 rule); `external-masked` refuses when the ADR-009 mask is not active for the team; `external-unmodified` egresses verbatim only after a completed detector pass (typed `filter.Detection`, the same Mask pass run detect-only) reports nothing protected — no detector, a detector error, or a hit refuses. Caveats: detector evidence in audit is the deny code only (no span counts/kinds yet), and the OpenAI ingress refuses `external-unmodified` outright in v1 (no maskBody there) |
| Fleet enforcement accuracy | Enforcement key ≥ `(org, UserID, pool, windowID)` in a durable ledger. A lease is spend authority already reserved centrally — non-overlapping, immediately reducing central balance, expiry returning only provably-uncommitted authority. Rate/quota must not multiply by data-plane count. | ❌ P0 |
| Guardrail / residency | A configured guardrail and region lock apply on **every** egress path, with no opt-out reachable from routing config. | 🔶 fenced 2026-09-02: `providers/bedrock/guardrail_fence_test.go` proves every egress in `egressAPIs` either APPLIES the guardrail upstream or REFUSES a guarded request pre-egress (Mantle — no guardrail parameter; the refusal is hereby the documented permanent posture: a guarded team cannot use Mantle-only models, and the audit chain never attests a guardrail that did not run); the bedrock INGRESS gained the region-lock + guardrail-override wire tests the other two ingresses already had. Remaining before ✅: accept-or-replace decision on the Mantle refusal by a maintainer, and guardrail evaluation off the InvokeModel/Converse APIs if replacement is chosen |
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

### P0 — blocks the enterprise-ready claim

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
unmet in the "applies" sense. As of 2026-09-02 the refusal is the DOCUMENTED
permanent posture (guardrail_fence_test.go, the ADR-019 comment on
mantleGuardrailCheck): a guarded team cannot use Mantle-only models, and the
audit chain never attests a guardrail that did not run. The fence also makes
the posture structural — every egress in `egressAPIs` must either apply or
refuse, so a fourth egress cannot ship guard-unchecked. A maintainer may
still supersede the refusal with real off-API guardrail evaluation; until
then this row is fenced-refusal, not silent regression.

**A 200 response could bill zero on the Mantle path.**
Original bug: the Bedrock ingress settles only when `resp.Parsed != nil`, and
Mantle's `Complete` dropped `Parsed` on any unmarshal/conversion failure — a
malformed-but-200 upstream response was served with no debit, no cost, and
empty audit usage (the ADR-030 zero-cost class re-entering through a new
path; Converse and InvokeModel build Parsed from typed fields and cannot
reach this state). Fixed: that path is now fail-closed — an unparseable 2xx
returns a synthesized 502 (`providers/bedrock/mantle.go` `Complete`), and the
stream path errors when a 200 stream yields no parseable frame, so nothing is
served unbilled. The structural half (Phase 0a) is now also in place for the
non-streaming paths: every ingress refuses a 2xx whose `Parsed` is nil —
falling back to the next target when one exists, else a synthesized 502 —
with fence tests per ingress (`zero_bill_fence_test.go` ×3), so a future
egress that builds `Parsed` from re-parsed JSON fails in CI instead of
review. Remaining: an equivalent generic fence for a 2xx STREAM that yields
no settleable frame (today per-provider discipline, Mantle's fixed).

**Per-user governance: budget shipped (ADR-042 Phase 3), identity now durable
(Phase 0b-1/0b-2, 2026-09-02).** User-subject budget rules are enforced, and
the enforcement identity is the canonical `UserID = issuer + "#" + subject`
captured at CLI login (`authapi`) and admin issuance (`keys.user_id` column;
non-admin callers always get their own verified identity), with the free-form
Owner string demoted to a display label and a bounded fallback for
pre-migration keys. A per-person budget now survives key rotation and a
second device (`cmd/mayu/identity_e2e_test.go`). Remaining in this area:
user-subject RATE (needs rate shares per user, ADR-043 follow-up) and the
roles/mutation-audit half of Phase 0b below.

**Substitution is team pressure, not a user fallback contract.** ADR-041
activates a per-team substitution map from a referenced team budget;
`router.SubstituteTier` leaves the premium model unchanged when the target is
not allowed. Premium-pool exhaustion → user-specific fallback → total hard cap
does not exist.

**Management authorization: mayu plane gated (0b-3, 2026-09-02), control
plane still coarse.** The mayu admin plane now enforces opt-in duty
separation: `oidc.role_mappings` grants the fixed roles from the verified
groups claim, and each management route class sits behind
`RequireCapability` (an auditor can read audit/logs but not issue keys; a
team-admin the reverse — negative tests per class; unconfigured deployments
byte-identical). The control plane now gates policy writes on policy-admin
(`INFERPLANED_OIDC_ROLE_MAPPINGS`, 0b-4) and records every policy mutation
(actor, capability, scope, before/after sha256, generation) — closing the
ADR-038 accepted limitation. Remaining: mutation records for mayu-plane
provider/team writes beyond the existing admin_key audit events.

**Enforcement state is neither durable nor globally accurate.** Key store is
SQLite; rate/quota/budget counters are process-local; the lease ledger is
in-memory with approximate window rollover and prunes dead data-plane spend
(`internal/controlplane/controlplane.go:39`). Standalone and per-key budgets
get no lease. Helm pins `replicaCount: 1`.

**PII policy: all four ceilings shipped (2026-09-02).** The
policy-selected egress ceiling exists end to end: `pii: {egress}` rules
(blocked / internal-only / external-masked / external-unmodified), provider
`classification` labels (closed set, unlabeled = external),
`router.FilterInternal` at the same resolved-chain point as the region
lock, fail-closed refusal when a mandated mask is not active, and the typed
detector result (`filter.Detection`, derived from the same Mask pass
enforcement uses so detection and transformation can never disagree) gating
`external-unmodified` — verified-clean egresses verbatim; no detector, a
detector error, or a hit refuses (`pii_detector_unavailable` /
`pii_protected_detected`). Remaining from the Phase 2 contract:
detector-evidence in the audit record beyond the deny code (span
counts/kinds), and the OpenAI ingress verifies nothing in v1 (no maskBody
there — it refuses `external-unmodified` outright, same posture as its
masked-team rejection).

### P1 — operational competitiveness

- **Undisclosed request mutation.** `providers/bedrock/converse.go` and
  `providers/bedrock/mantle.go` are two separate model→param strip tables in
  different wire vocabularies, both keyed by `strings.Contains(upstream, …)`.
  The stored-artifact + CI-guard half is now in place (Phase 0a):
  `providers/bedrock/testdata/strip_tables.json` records both tables with
  their probe date, and `strip_tables_guard_test.go` fails any table edit
  that doesn't update the artifact in the same commit — every drift is a
  reviewable diff naming which models lose which params. The per-request
  disclosure is also in place (2026-09-02): a provider that strips reports
  it on `ProxyRequest.ParamsStripped`, and every ingress sets
  `x-inferplane-params-stripped` on the response and `params_stripped` on
  the audit record (`internal/audit` RequestRef, appended-at-end rule).
  Still open: a Prometheus metric for strips (needs the cardinality
  discussion — param names are table-bounded, but per-model×param series
  should be a deliberate choice).
- **Cache behavior differs by path.** Anthropic passthrough and Bedrock Claude
  InvokeModel preserve `cache_control`; Bedrock Converse does not map it to
  `cachePoint`. `internal/cache.VolatileStore` and cache-affinity are
  unimplemented.
- **Cache efficiency is measured as tokens, not outcomes** — no hit ratio,
  write-without-reuse, prefix fragmentation, or model-switch loss attribution.
- **Request audit lacks durable identity** — records carry key ID and team;
  usage telemetry carries the key owner.
- **Mantle errors miss model-level fallback.** `isModelNotFound`
  (`internal/server/bedrockapi/invoke.go:277-282`) matches on
  `"ValidationException"`, which Mantle's OpenAI-shaped error bodies need not
  contain.
- **Alpha deployment posture** — Helm defaults persistence off, no resource
  requests, no security context, PDB, NetworkPolicy, or autoscaling; no CI
  workflow running the full race/vet/vulnerability/release gates.

## 4. Delivery sequence

Ordered by trust boundary. Each phase is a separate design spec and plan.

| Phase | Work | Exit gate |
|---|---|---|
| **0a. Invariant guards** | Refuse to boot (or record honestly) when a configured guardrail or region lock is unreachable on a selected egress path; make a settled cost mandatory for every 2xx; CI guard for strip-table and guardrail-path coverage, in the `mayu pricing check` mold | No egress path can silently drop a mandated control or a billable settlement; a new path fails CI rather than review |
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
2xx carries a settled cost.

**Administration** — a forbidden admin action is denied and evidenced · every
policy/provider/budget mutation records actor, scope, diff hash, and generation.
