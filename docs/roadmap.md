# Roadmap: closing the five operational gaps vs central-proxy gateways

Status updated 2026-09-16 against `698edf3`: item ② ships as ADR-045 node-local monetary escrow;
ADR-046 now implements shared keys and global rate/token quotas (item ①) in an
explicit Postgres shared gateway profile. Remaining fleet features are listed below.

[Enterprise product strategy](enterprise-strategy.md) is the canonical source for
target market, product contracts, priorities, and production-release gates. This
roadmap tracks execution status and retains the original five-gap work breakdown.

## Purpose alignment by profile (2026-09-16)

The five goals in `CLAUDE.md` are evaluated against an explicit profile, not a
single product-wide checkmark. Mechanism implementation, default activation,
failure behavior and enterprise qualification are separate evidence.

| Profile | Default / activation | Implemented authority | Availability boundary | Enterprise qualification |
|---|---|---|---|---|
| SQLite/local, including optional legacy CP | Standalone default; legacy CP is opt-in | Local keys/team records and rate/quota/money counters; legacy allowances do not provide durable global escrow. | No inference-time shared DB; attached CP policy/allowance readiness and expiry still apply. Local process/store failures remain possible. | Single-replica enforcement, not a fleet-global claim; alpha. |
| ADR-045 node-local monetary authority | Opt-in durable CP authority + private journal | Global GovernancePolicy money, including opaque user scopes; keys/rate/token quotas and standalone/key-local money remain local. | No inference-time CP/DB call; CP-only or authority DB loss prevents replenishment. Existing credit is usable only within all readiness, policy-age and hard deadlines. | Monetary mechanism implemented; recovery/load and person-identity qualification open; alpha. |
| ADR-046 shared gateway | Opt-in Postgres key/governance stores and common authority namespace | Shared key/team records; synchronous atomic RPM/TPM/token-quota/money admission. | CP-only loss is subject to valid binding and readiness/staleness gates. DB loss refuses new admission; gateway replicas do not remove this dependency. | Shared mechanism implemented; HA DB/deployment qualification open; alpha. |

| Purpose | Implemented mechanism / default | Remaining contract in every profile |
|---|---|---|
| #1 Coding-client entry point | Messages, Chat, Invoke and Responses; native/portable adapters and opt-in installed-client tests. | Versioned client/model tool qualification; no arbitrary opaque-state transfer or task-quality promise. |
| #2 User model choice | Allowed-model and configured opaque user-subject policy gates. | Verified `(issuer, subject)` identity, key-rotation/multi-device attribution and six-role org/team authorization are not implemented. Shared key records do not close this gap. |
| #3 Cost-driven routing | Optional legacy tiers; explicit strict targets and privacy rules; context defaults to Shadow, Enforce is opt-in. ADR-044 supports compatible tool/history workflows and local successful-target pins. | Premium/total person-pool contract, measured task/cost/latency results and fleet-wide session guarantees remain open. |
| #4 Budget control and visibility | Scope/durability depend on the profile above. Analytics and console report observed usage/cost; durable/shared paths retain uncertain liability. | Person attribution, invoice reconciliation and complete operational recovery evidence; never infer known actual cost from an HTTP 200. |
| #5 Bounded availability | CP HTTP does not carry inference. ADR-045 has finite local authority; ADR-046 deliberately uses synchronous DB admission. | No unconditional no-SPOF claim. Qualify process/node/DB/upstream failures for the chosen deployment. |

`require_sync` gates first CP delivery; `max_policy_age` can reject stale policy.
Durable authority requires initial sync, shared mode requires initial binding,
and hard grant/window expiry or exhaustion cannot be bypassed by a freshness
setting. CP-only loss and DB loss are different events. Count APIs retain their
local HTTP-200 contract during generation refusal. See the
[profile tables](../README.md#deployment-profiles) and profile runbooks.

Sprint plan (each phase = separate PR(s), reviewed before the next):

| Sprint | Items | Why together |
|---|---|---|
| S1 | ①/② implemented as ADR-045/046 profiles | Local monetary escrow and synchronous shared resource admission have explicit, separate failure contracts |
| S2 (~1 wk) | ④ `mayu doctor` + ③ phase 1 (version visibility) | Pure observability, no protocol risk, unblocks real-world debugging |
| S3 (1–2 wk) | ③ phase 2 (signed self-update) + ⑤ embeddings lane | Release pipeline work + first non-chat modality |

---

## Policy-aware routing v1 (ADR-043, extended by ADR-044)

Implemented: original-byte local inspection; FailClosed sensitiveData destination
restrictions on every attempt; Shadow-default context recommendations. Legacy
ADR-043 Enforce eligibility is completely inspectable single-user-turn requests
without history/tools/media/reasoning/structured output; ADR-044 extended rules
support compatible tool/history sessions. Both have persisted topology metadata,
ingress/count integration, bounded audit/headers/counter evidence, and startup and
policy-apply target validation. In legacy rules, the input threshold chooses
simple versus complex, not eligibility;
a distinct compatible complex target can be selected above it. This extends
cost-driven routing without replacing ADR-041 budgets.

Rollout evaluation remains open: task success, total cost including cold-cache
writes/retries, p95 latency, and privacy-policy negative cases must pass declared
baseline-relative gates before Enforce. Finite detectors and translator capabilities
remain limits; this is not universal PII detection or measured savings. Durable
cross-node session pinning, learned/remote classifiers and deployed HA qualification
remain separate work; ADR-046's shared admission is already implemented.
ADR-044 adds bounded local pins, Responses and complete Mask
policy handling; see [adaptive routing](adaptive-routing.md). Upgrade binaries/CRD before activating rules; require_sync is
needed for CP privacy before first request. See [guide](policy-routing.md).

## ① Shared keys and global rate/token quotas — implemented (ADR-046)

The implementation uses transactional Postgres admission for the explicit shared
gateway profile. It replaces the earlier proposed rate-share/Redis design for
that deployment mode. Keys and team permissions share one authority; each provider
attempt atomically reserves all matching team/key/user resources. Complete known
usage releases unused amounts, while uncertain outcomes remain reserved.

Policy money competes in the same ADR-045 accounts as outstanding node-local
grants. A persistent namespace and policy-generation checks prevent mixed authority
sources. Rate refill is exact and database-clock based; token quotas use UTC
calendar day/month windows. Shared usage identifies reservations separately.

See [operator and migration guide](shared-governance.md). Default node-local rate
buckets remain local; disconnected global rate/key authority is not claimed.
Mutable shared provider topology, retention/reconciliation APIs and production
throughput benchmarking remain outside this implementation.

---

## ② Durable ledger + control-plane-owned budget windows — implemented (ADR-045)

The accepted implementation replaces the earlier proposed SQLite/write-behind
ledger with transactional Postgres escrow. Policies and authority share a
database/schema. Issuers read fresh policy under lock, reserve each grant before
replying, and preserve liabilities across retries, expired leases and restart.
Database time owns UTC day/month windows, including late reports and tier latches.

Each node uses a private SQLite journal to reserve a conservative bound before
every provider attempt. Complete observed usage releases the unused local portion;
partial or unknown results retain uncertainty. Restart burns old open grants and
fences earlier journal owners. Inference does not call Postgres or the control
plane. Real integration tests run two control planes and two gateways against one
database, including restart, concurrency, rollover and replay cases.

Enable both ends explicitly; see [configuration and failure behavior](durable-budgets.md).
Legacy clients are rejected by a durable authority server instead of receiving
weaker grants. This implements global GovernancePolicy money budgets, including
user scopes; it does not globalize key-local budgets, rates, quotas or key storage.

---

## ③ mayu version channel + signed self-update (ADR candidate — unassigned; ADR-038 has since shipped as the control-plane policy store)

**Gap.** mayu on developer laptops is an endpoint-agent fleet with no update
mechanism. Version skew is *detected* (heartbeat + `/v1alpha1/dataplanes`)
but the tail of stale planes can only shrink by hand today.

**Phase 1 — visibility & advice (S2, ~1 day).**
- Embed version via `-ldflags -X` at build; add `version` to `SyncRequest`;
  dataplane view shows the version distribution (the operator's "can I ship
  this rule yet" check gets a second axis besides apiVersions).
- Control plane config `minimumVersion`: sync responses include
  `updateAdvice {minVersion, url}`; mayu logs a loud warning and exposes it
  on `mayu version --check`. Advice only — nothing auto-applies.

**Phase 2 — signed manual update (S3).**
- Release pipeline: goreleaser + minisign/cosign signatures on artifacts;
  public key embedded in the binary at build.
- `mayu update [--channel stable]`: fetch → verify signature → atomic swap
  (write sibling, rename, keep previous as `.old`) → user restarts. No
  root: installs to the user-writable location it runs from. K8s is
  excluded — the image pipeline owns node upgrades there.

**Phase 3 — auto-update channel (later).** Idle-window self-update with
health self-check + rollback to `.old` on boot failure.

**The security constraint that shapes all of it.** The control plane must
never be able to push executable content — only a *version pin*, which the
data plane independently verifies against the embedded release public key.
Otherwise a control-plane compromise is RCE on every laptop. This is
non-negotiable and goes in the ADR's security section.

---

## ④ `mayu doctor` (S2, ~1–2 days)

**Gap.** Distributed debugging: "it fails only on my machine" requires
inspecting that node's state, and today that means grepping logs.

**Design.** One command, human output + `--json` for support tickets:

- config: parse/validation result, which policy source (files vs control
  plane), secret refs resolvable (never the values);
- control plane: reachability, auth OK, latency, applied generation vs
  server generation, pending rejections;
- governance: applied policies per team, lease table (allowance/spent/expiry,
  from `Governor.UsageOf` + `LeaseTable`), rate shares once ① lands;
- providers: connection probes (reuse `configapi/probe.go`'s SSRF-guarded
  prober), pricing coverage (reuse `live.UnpricedTargets`);
- environment: version, supported apiVersions, clock skew vs control plane
  (lease expiry math depends on it), listen-port conflicts.

Also `GET /admin/debug/governance` (admin-auth, secret-free DTO — same
redaction posture as `/admin/config`) so an operator can pull the same
snapshot remotely from a machine they can't shell into.

**Risk.** Leakage — every field goes through the existing secret-free view
discipline; `key_id`/owner stay out of the JSON by default.

---

## ⑤ Provider coverage: embeddings first (ADR candidate — unassigned; next available slot is ADR-040 as of 2026-08-14)

**Gap.** Three provider types, chat-only. The canonical schema is a
Messages-superset — embeddings structurally don't fit it, and forcing them
through it would violate the lossless-round-trip invariant.

**Design — a governed passthrough lane, not a canonical one.**
- New ingress `POST /v1/embeddings` (OpenAI wire shape — the de-facto
  standard clients speak).
- Providers opt in via an *optional* interface (`providers.Embedder`,
  discovered by type assertion) so existing provider packages and the §8
  zero-core-diff rule survive: `openai_compatible` forwards verbatim
  (§4.4 applies trivially), `bedrock` adds Titan/Cohere embed model mapping;
  `anthropic` simply doesn't implement it → clean 404 per model.
- Governance is identical: KeyAuth → canonicalize → modelAccess gate →
  PreCheck → forward → Settle with usage tokens × per-mtok input rate
  (embeddings have no output tokens; pricing table already keys on
  (provider, upstream)).
- Explicitly NOT in this phase: images, audio, rerank — each gets its own
  lane decision later; and no new chat providers until the lane pattern is
  proven (Gemini/Vertex next, via their OpenAI-compat endpoints first).

**Risk.** Scope creep is the failure mode — the lane pattern (optional
interface + governed passthrough) is the deliverable; Titan/Cohere mapping
details are swappable.

---

## Explicitly deferred (so the list stays five)

- **Credential brokering (ADR-040, Accepted — design gate passed)** — inferplaned vends
  short-lived STS Bedrock credentials so `bedrock:Invoke*` leaves
  developer/node IAM entirely (bypassing mayu then yields no credentials).
  Accepted 2026-08-18 after a 3-round 3-AI design gate (10 findings fixed).
  Requires a dedicated `INFERPLANED_BROKER_TOKEN` (never the heartbeat
  token) and auth-mode validation in mayu's config loader.
- **Mutable shared provider topology** remains deferred. ADR-046 shared gateways
  use a common file/ConfigMap rollout and reject the SQLite provider-store option.
  Shared key/rate/quota and key-local money enforcement are implemented.
- SSE push stream for policy distribution (poll-at-lease-cadence already
  beats the 60s/15s requirement).
- CRD-watch controller in inferplaned (ADR-035 follow-up).
- Disconnected global rate/key leases; shared-profile user rates/token quotas ship in ADR-046.
- Cache-affinity routing engine (the `routing` rule's *affinity* half stays
  rejected until then; the *budgetTiers* half shipped as ADR-041 — it never
  depended on the affinity engine).
