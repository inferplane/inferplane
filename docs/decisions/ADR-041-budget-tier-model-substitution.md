# ADR-041: Budget-tier model substitution (routing.budgetTiers)

**Date:** 2026-08-26
**Status:** Accepted (implemented — work items 1–5; item 6, providerstore/UI
pricing fields, and item 7's full two-plane e2e are follow-ups)
**Related:** ADR-008 (providerstore — the UI write path that registers a vLLM
endpoint and its models), ADR-017 (budget alert webhooks — the utilization
thresholds this ADR reuses as a routing trigger), ADR-029 (model-level
fallback — the availability-triggered substitution this ADR adds a
cost-triggered sibling to), ADR-030 (cost settlement correctness — why a
substitution target must be priced, and why "explicit zero price" is not
representable today), ADR-033 (local policy file channel — where `routing`
rules are currently rejected wholesale), ADR-034 (budget leases — the ledger
that makes team utilization a control-plane-computable quantity), ADR-036
(usage telemetry — the input for the future rule-proposal pipeline), ADR-038
(control-plane policy store — where proposed-not-yet-approved documents will
live), ADR-039 (`unlimited: true` — the "explicit, auditable declaration"
idiom this ADR follows for nominal pricing)

## Context

Core Purpose #3 — cost-driven model substitution — is the only one of the
five goals with **no** enforceable mechanism today. `internal/policy`
rejects every `routing` rule outright
(`store.go` `checkEnforceable`: "routing rules are not yet enforceable"),
and the roadmap parks the whole rule kind behind the cache-affinity engine
(`internal/cache.VolatileStore`, unimplemented). The only substitution that
exists is ADR-029's `model_fallbacks`, which fires when a requested model
has **no route at all** — availability-triggered, never cost-triggered.

The concrete scenario this ADR enables, requested by operators evaluating
inferplane against cost-optimizing local gateways:

> A team's users run Claude Opus. When the team has consumed 80% of its
> monthly budget, an admin-set policy substitutes designated traffic to a
> cheaper model — typically a self-hosted model on the org's own GPUs
> (vLLM/Ollama behind the `openai_compatible` provider) priced at a nominal
> internal chargeback rate — so work continues without breaching the budget.
> At the window rollover the substitution lifts automatically.

Three constraints discovered during design shape everything below:

1. **Utilization is a global quantity.** "80% consumed" must be judged
   against the fleet-wide sum, not any one data plane's local counter —
   per-plane evaluation would activate at different times on different
   nodes, the exact per-plane-drift failure ADR-034 exists to prevent. The
   ADR-034 lease ledger already aggregates per-team `spent` vs `limit`
   centrally, so the control plane can compute this today.

2. **Prompt caching forbids switching a live conversation's model.**
   Claude Code traffic is cache-dominated (ADR-039 context): resubmitting a
   150k-token prefix to a *different* model is a full cache miss, so
   substituting the main-loop model mid-conversation can cost *more* than
   it saves, besides degrading an in-flight agent session. Whole-session
   switching is therefore rejected. The substitution map is keyed by
   **requested model name**, which lets an admin target only the models
   that carry short-lived conversations — subagent/background models
   (Claude Code sends the subagent model's name in each request's `model`
   field) — while the main-loop model is simply absent from the map and is
   never touched. This is also the only deterministic lever available:
   mayu has no session identity, only the per-request `model` field.

3. **"Free" GPU models are not representable, and should not be.**
   `internal/pricing/fromconfig.go` documents that an explicit zero rate is
   indistinguishable from "unset" (zero means derive), and ADR-030's guard
   treats a missing rate as the billing-zero bug class. Rather than adding
   an explicit-free escape hatch to the pricing schema, self-hosted GPU
   models get a **nominal, operator-entered chargeback rate** (GPU capacity
   is not actually free — power, depreciation, opportunity cost), which
   keeps goal #4's "always answer how much we've spent" true for GPU
   traffic instead of making it invisible in every report. This follows
   ADR-039's idiom in spirit: an explicit small number is an auditable
   decision; a silent zero is a hole.

## Decision

### D1 — Split the `routing` rule kind: `budgetTiers` becomes enforceable

The wire `routing` rule gains a `budgetTiers` shape, mutually exclusive
with the existing (still-unenforceable) affinity shape. Exactly-one-kind-
per-rule is preserved — `routing` remains one kind; within it, exactly one
of `onAffinityConflict` or `budgetTiers` must be set. `checkEnforceable`
narrows from "reject all routing rules" to "reject affinity routing rules";
`budgetTiers` rules are accepted by data planes that implement this ADR.
Older data planes reject the document via the existing `UnsupportedError` →
sync rejection-report path — visible to the operator, never silently
ignored (the ADR-033 rule).

```yaml
apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
subject: { team: ml-platform }
rules:
  - name: team-monthly-budget
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 5000000, hardCap: true }   # $5,000/mo
  - name: downgrade-subagents-at-80
    failurePolicy: FailOpen
    routing:
      budgetTiers:
        budgetRef: team-monthly-budget      # names the budget rule judged against
        tiers:
          - thresholdPercent: 80
            substitute:
              claude-haiku-4-5: glm-4.7-gpu   # subagent traffic → self-hosted vLLM
              # the main-loop model is NOT in the map → never substituted
```

Validation at conversion (`FromV1Alpha1`): `budgetRef` must name a budget
rule with a numeric limit in the same document (a tier against `unlimited`
is meaningless); `thresholdPercent` in [1, 99], strictly increasing across
tiers; `substitute` non-empty with non-empty keys and values; a model may
not appear as both a key and a value in one rule (no chains). Validation at
apply time on each data plane: every substitution **target** must be routed
and priced (`live.State.Route` + `pricing.HasRate`) — a plane whose
topology lacks the target rejects the rule and reports it through the
existing rejection channel, closing the ADR-030 zero-billing hole before it
opens on the policy path.

### D2 — The control plane judges; the data plane applies

`inferplaned` computes utilization per `(policy, budgetRef)` from the lease
ledger it already maintains (Σ reported spend + Σ outstanding grants,
conservative — consistent with ADR-034's bounded-overshoot posture) and
carries the **active tier index** in the sync heartbeat (`SyncResponse`
gains an additive, `omitempty` `activeTiers` field). mayu keeps a tier
table beside the `LeaseTable` and applies the active tier's map at ingress.

Activation is **latched per budget window**: spend within a window is
monotonic, so a tier that activates stays active until the window rolls
over, then lifts. There is no flapping by construction, and the one-time
prompt-cache invalidation for mapped models happens at most once per
window. Window identity should ride the roadmap-item-② `windowID` design
when that lands; until then the latch resets when the ledger's window
resets.

Failure semantics (`FailOpen`, required on the rule): on control-plane
unreachability mayu keeps the **last received** tier state — never
activates a higher tier on its own, never deactivates early. Stale can
only mean under-substituting, which the 100% hard-cap budget rule (whose
lease gate already fails closed per ADR-034) backstops. Standalone mayu
(no control plane) evaluates tiers against its local budget store with the
same per-instance-approximation caveat standalone budgets already carry.

### D3 — Enforcement seam and the RBAC re-check

Substitution runs in the ingress handler as a step before
`router.ResolveModel`: canonicalize → tier-substitute (if a tier is active
and the canonical name is a map key) → `ResolveModel` → `ResolveChain` →
**`FilterModelAllowed` / `FilterRegions` re-check as always**. ADR-029's
"a configured model is never second-guessed" contract is untouched —
`ResolveModel` itself is not modified; the tier step is a distinct,
policy-driven layer above it.

If the substituted model does not pass the principal's `modelAccess`, the
substitution is **skipped and the original model served** — substitution
must never turn into a denial (the budget hard cap is the denial
mechanism), and must never widen access (the target passes the same RBAC
the user's own request would). This is the policy-can-only-narrow
invariant applied to routing.

### D4 — Substitution is never silent

Every substituted request carries, as one set: the audit record logs both
requested and served model (the analytics index and `GET /admin/logs`
follow), the response carries an `x-inferplane-substituted-model` header
naming what was served, and tier activation/deactivation fires the
ADR-017 webhook (a new event type beside the existing utilization
thresholds — same emitter, same dedupe discipline). A cost-governance
product that swaps models covertly forfeits the trust that justifies it.

### D5 — Self-hosted GPU models: nominal price, entered where the endpoint is

No pricing-schema change. The operator registers the vLLM/Ollama endpoint
as an `openai_compatible` provider and enters a **manual nominal per-MTok
rate** for each of its models in the same flow — via config, or via the
ADR-008 providerstore UI write path, which this ADR extends to carry the
pricing fields next to the endpoint and model list (one screen: endpoint,
models, price). The ADR-030 boot/`pricing check` guard applies unchanged:
a GPU model without its nominal rate is a validation failure, not a free
model. Recommended magnitude is whatever internal chargeback the platform
team actually attributes ($0.01–$0.10/MTok as a starting point); the
number being explicit and auditable matters more than its value.

### D6 — Multiple GPU pods: integrate an inference gateway, don't become one

When the self-hosted model runs as several vLLM pods, load distribution
across them is **out of scope for mayu** — "not competing on data-plane
inference performance" is a stated non-goal. mayu targets one logical
`openai_compatible` endpoint; behind it the operator points at a plain
Kubernetes Service for basic balancing, or at a Gateway API Inference
Extension implementation (Envoy AI Gateway, kgateway) when inference-aware
scheduling (KV-cache/LoRA-aware) is wanted. inferplane's provider-level
priority fallback and circuit breaker apply to that endpoint as to any
other. The division of labor is deliberate: inferplane governs *who may
spend what on which model*; the inference gateway optimizes *which pod
serves the token*.

## Implementation notes

Work items 1–5 shipped as designed, with four deviations/narrowings worth
recording:

- **`budgetTiers` is the second half of the existing `routing` rule kind**
  (`api/v1alpha1.RoutingRule.BudgetTiers`), not a new top-level rule kind —
  `checkEnforceable` narrows to reject only the cache-affinity half
  (`Routing.Affinity`); a `budgetTiers` rule is enforceable independent of
  the unimplemented `internal/cache.VolatileStore`.
- **The enforcement seam is `Router.SubstituteTier`, called at ingress right
  after `principal.From` and before the `Allows` check — a distinct call
  from `ResolveModel`.** `ResolveModel`'s documented contract ("a configured
  model is never second-guessed") stays intact; `SubstituteTier` is the
  ADR's cost-triggered sibling for an ALREADY-routed model, and it needs a
  `Principal` in scope (for the RBAC-skip rule below) that `ResolveModel`
  never had. It composes with D5 model-fallback substitution: an unrouted
  model may first take its `model_fallbacks` target, and that result may
  itself be tier-substituted (tested,
  `TestMessagesModelFallbackThenBudgetTierChain`).
- **Substitution can only narrow, never widen, never deny (D3), enforced as:
  fires only when the ORIGINAL is already `Allows`-permitted; if the TARGET
  fails RBAC or isn't routed on this data plane, the ORIGINAL is served —
  substitution never turns into a denial and never rescues an
  already-denied request.**
- **Window identity is an interim calendar-month-UTC key
  (`internal/tier.WindowKey`), not the control-plane-computed `windowID`**
  roadmap item ② will eventually introduce. `internal/tier.Latch` makes
  activation monotone within that key today; once item ② lands, the latch
  should be re-keyed on the real `windowID` instead.
- **The ADR-017 tier-activation alert event is emitted per data plane**, not
  once per global activation — the same per-instance posture ADR-017's
  existing budget-alert emission already has.
- **Bedrock's native ingress** (`internal/server/bedrockapi`, which
  hand-rolls model resolution rather than calling `router.ResolveModel`)
  gets its own `SubstituteTier` call site so it isn't a cost-leak path
  around the same policy the Anthropic/OpenAI ingresses enforce.

Deferred to follow-up work: item 6 (providerstore/UI pricing fields
alongside the `openai_compatible` endpoint write path, ADR-008 extension)
and item 7's full two-data-plane + tool-calling-fidelity e2e (a
control-plane-level "activates on the global sum, not any one plane's local
view" test is covered today,
`TestActiveTierFiresOnGlobalUtilizationNotPerPlane`).

## Explicitly not in this ADR

- **AI-generated rules.** The follow-on (separate ADR) is a proposal
  pipeline: an LLM reads ADR-036 telemetry aggregates and drafts
  GovernancePolicy documents — including budget-tier rules — stored as
  pending in the ADR-038 policystore until an admin approves them in the
  console. The enforcement path gains nothing: an approved proposal is a
  plain policy document, indistinguishable from a hand-written one. No
  model ever sits in the request path making routing decisions — that
  would make enforcement non-reproducible and unauditable.
- **Cache-affinity routing** (`onAffinityConflict`) stays rejected until
  `internal/cache.VolatileStore` exists.
- **Request-content-based tiering** (classify prompt complexity → pick a
  model). Deterministic, name-keyed substitution only.

## Consequences

- Core Purpose #3 gets its first enforceable mechanism, decoupled from the
  cache-affinity engine it was parked behind.
- One new wire surface (`routing.budgetTiers`, `SyncResponse.activeTiers`),
  both additive/`omitempty` — old planes and old control planes interop,
  with rejection reporting where they can't enforce.
- The prompt-cache trade-off is pushed to policy authorship: the docs must
  state plainly that mapping a long-conversation model will cost cache
  misses, and that the intended targets are subagent/background model
  names.
- Cross-protocol fidelity becomes a tested obligation: Anthropic Messages
  ingress → `openai_compatible` (vLLM) translation must preserve tool
  calling well enough for subagent traffic — an e2e case (subagent-shaped
  request with tool use, substituted to a vLLM-hosted model) gates
  acceptance.

## Work items

1. Wire schema: `api/v1alpha1` `routing.budgetTiers` (+ conversion,
   validation, tests).
2. Policy: internal `Routing` form split; `checkEnforceable` narrowed;
   apply-time target routed+priced check → rejection report.
3. Control plane: per-`budgetRef` utilization from the ledger; latched
   tier state; `activeTiers` in `SyncResponse`.
4. mayu: tier table beside `LeaseTable`; ingress substitution step; RBAC
   skip semantics; standalone-mode local evaluation.
5. Observability: audit requested+served, response header, ADR-017 tier
   event, `/metrics` counter (`substituted_total`, config-bounded labels).
6. providerstore/UI: pricing fields on the provider/model write path
   (ADR-008 extension).
7. e2e: two data planes + control plane — tier activates at the *global*
   80%, not per-plane; window rollover lifts it; RBAC-skip case;
   tool-calling fidelity through translation.
