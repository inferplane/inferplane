# Architecture

## System Overview

inferplane separates inference traffic from policy administration. **`mayu`**
authenticates and routes requests, enforces configured controls, and records
usage. **`inferplaned`** distributes policy and budget authority, aggregates usage,
and optionally brokers short-lived Bedrock credentials. Both are static Go
binaries; a standalone `mayu` needs no control plane.

<div class="ip-diagram" tabindex="0" role="region" aria-label="Control and data plane architecture; scroll horizontally on a small screen" markdown>

![Operators configure policy and budgets in inferplaned. Policy and finite authority reach mayu outside the inference path. Agents send requests through mayu to approved internal or external targets. Shared Postgres admission is an explicit additional request-time dependency; local and durable node-local profiles use local admission state.](assets/architecture.svg)

</div>

[Open the architecture diagram](assets/architecture.svg)

**Solid green arrows carry inference; dashed blue arrows carry policy, authority
or usage synchronization. The amber edge is synchronous shared-DB access, only
for ADR-046.** Prompts and response streams stay on the agent → mayu → provider
path. Usage telemetry returns separately to the control plane.

The control-plane HTTP service is not called for each inference admission.
Dependencies still vary by [deployment profile](getting-started/deployment-profiles.md):
local enforcement uses process-local counters; ADR-045 uses centrally committed
monetary grants and a private local journal; ADR-046 synchronously resolves keys
and reserves resources through Postgres. This separation is not an unconditional
availability guarantee.

## How configuration becomes automatic enforcement

Operators declare model/provider topology, processing boundaries, capabilities,
prices and referenced credentials on the data plane. `GovernancePolicy` supplies
matching subject rules for model access, rates, quotas, budgets and routing.
A gateway reads **either** watched local `policies` **or** an attached
`control_plane`; configuration rejects both together. Central policy distribution
does not automatically deploy provider topology or verify an internal destination.

With a control plane, policy and authority synchronization runs outside the
inference request. The gateway validates and installs the applicable state,
subject to first-sync, staleness, identity and profile-specific authority gates.
Changes propagate asynchronously, rather than atomically across every gateway.

On each generation request, mayu combines the installed policy with current key
access, request inspection and budget state. Configured PII actions, budget
cutover and context preferences become routing decisions without the client
choosing each transition. Context is Shadow unless explicitly enabled; privacy
and strict budget targets still enforce. See
[the configuration-to-decision walkthrough](why-inferplane.md#one-configuration-several-automatic-decisions).

The architectural benefit is a separate management lifecycle plus local request
inspection and enforcement. It does not eliminate storage or credential
dependencies: ADR-045 can spend only existing valid grants during CP/DB loss;
ADR-046 requires reachable Postgres on each admission. The
[profile outage table](getting-started/deployment-profiles.md#understand-outages)
defines when generation continues or refuses.

## Components

### Ingress Layer (`internal/server`)

Messages, Chat Completions, Responses, and Bedrock-shaped generation routes share
identity, body-size, readiness, access and governance controls. Model discovery
and usage views are authenticated. Count endpoints preserve their HTTP-200
contract, including local estimates when generation would be refused.
See the [HTTP reference](api-reference.md) for routes and protocol limits.

The separate admin listener serves health, readiness, metrics, authenticated
management APIs and a data-free console shell. OIDC and static-token paths have
different identity/authorization contracts; the complete six-role org/team model
remains open.

### Governance Layer (`internal/governance`, `internal/authority`)

| Profile | Admission state | Failure boundary |
| --- | --- | --- |
| Local SQLite / optional legacy CP | Process-local rate, quota and money; legacy CP allowances | Local counters reset on restart; attached readiness/lease gates still apply |
| Durable node-local money · ADR-045 | Postgres policy-money authority, finite grants, private node journal | Existing credit only within readiness/staleness and hard deadlines; no replenishment during CP/DB loss |
| Shared Postgres · ADR-046 | Shared key/team snapshots and atomic rate/token/money reservations | New key resolution/admission requires reachable Postgres and valid policy binding |

Governance checks precede billable egress. Authority profiles reserve a
conservative bound for **each provider attempt**. Settlement records known usage
and retains uncertainty where a refund cannot be proved. Expiry, a lost node, or
an interrupted stream does not create fresh credit. Monetary arithmetic uses
integer microUSD and round-half-even; pricing is an operator-reviewed accounting
input, not a guarantee about an external invoice.

### Provider Layer (`providers/*`)

| Package | Responsibility |
| --- | --- |
| `anthropic` | Messages protocol and server-side credential injection |
| `bedrock` | Configured InvokeModel, Converse or Mantle paths; AWS signing and API-specific limits |
| `bedrockresponses` | Native Bedrock Responses transport with AWS credential/signing integration |
| `openaicompat` | Chat Completions-compatible upstreams |
| `openairesponses` | Native Responses upstreams |

The provider interface is the extension boundary. Protocol-compatible paths
preserve raw request bodies under the existing forwarding contract; deliberate
model rewrites, enabled masking and cross-protocol translation have separate
semantics. Declared capabilities do not prove universal tool/reasoning support.
Unsupported guardrail/API combinations refuse rather than silently bypassing the
control. See [provider reference](reference/agent-llm.md).

### Routing Layer (`internal/router`, `internal/sensitivity`, `internal/tier`)

Routing composes public model resolution, allowed targets, budget tiers,
sensitive-data decisions, context selection, and provider fallback. Every attempt
must satisfy access, privacy, region, capacity and active strict-target constraints.
Legacy optional tier substitution retains the original model when an alternative
cannot be used; opt-in strict targets can refuse. Context Shadow still enforces
privacy. Session stability is bounded and local, not a shared session authority.

Fallback is limited by the ingress/provider's pre-commit streaming boundary.
Once output has been committed, a failed stream cannot be replayed transparently.
See [policy routing](policy-routing.md) and [adaptive routing](adaptive-routing.md).

### Persistence Layer (`internal/keystore`, `internal/authority`, `internal/audit`)

SQLite is the default hashed-key/team store. The shared profile implements a
Postgres key/team backend and shared resource admission. Optional verified
human/service identity uses a persistent registry while retaining registered
financial account references; activation is explicit, not a side effect of
choosing Postgres.

Postgres can also store policy documents and usage/analytics. The mutable provider
topology store remains SQLite-only and is rejected in shared governance mode.
Private ADR-045 journals and per-instance audit WALs must never be shared between
running gateways. See [data reference](reference/data.md) and
[verified identity](verified-identity.md).

### Observability Layer (`internal/metrics`)

Prometheus serves bounded `gen_ai_*` and `inferplane_*` metrics on the admin port.
Opt-in traces use OTLP. Audit records use an exact-byte hash chain with per-instance
segments and optional external anchors. Body capture is a separate opt-in
encrypted store; it is not part of the audit chain.

### Control-Plane Telemetry (`internal/telemetry`, `internal/controlplane`, ADR-036)

Usage windows travel to `POST /v1alpha1/usage` separately from policy/authority
synchronization. They support analytics and the usage console; they are not OTLP
and are not a substitute for the authority ledger. Analytics delivery loss and
retained monetary uncertainty are different operating conditions.

### Security Layer (cross-cutting)

Virtual-key hashes, upstream credential isolation, referenced secrets, bounded
metrics and fail-closed control paths protect the configured gateway boundary.
Policy administration and credential brokering have dedicated credentials.
A compromised host can still obtain locally accessible credentials or sessions;
provider boundary labels and identity fingerprints are not host attestation.
See [security boundaries](operations/security.md).

## Request decisions inside mayu {#mayu-component-diagram}

<div class="ip-diagram" tabindex="0" role="region" aria-label="Request enforcement diagram; scroll horizontally on a small screen" markdown>

![The authenticated request is inspected locally. Privacy, access and strict budget targets constrain model selection. Each attempt passes readiness and budget admission before provider egress. Eligible retries pass through the same restrictions; observed usage and uncertain spend are recorded.](assets/request-flow.svg)

</div>

[Open the decision-flow diagram](assets/request-flow.svg)

This diagram describes combined decisions, not a promise that every ingress
handler executes identical functions in this exact order. Access, privacy,
capability, region and active strict-budget restrictions apply to every target.
Context and cache preferences operate only within that eligible set.

For `InternalOnly`, the model must belong to the allowed internal-model
intersection and the provider must carry a compatible internal boundary.
`Mask` requires complete transformation and independent reinspection before a
usable chain is returned; it never cancels a separate `InternalOnly` obligation.
`Block` wins. If no compatible target remains, the request refuses.

With `enforceTargets: true`, an active budget tier constrains attempts to its
configured target. It is separate from hard-cap admission and cannot buy more
credit. Legacy optional substitution instead retains the original model when the
alternative is unusable. Every authority-backed attempt reserves its own bound,
including an allowed retry. A partial stream or unproven cost does not create a
refund.

## Data Flow Summary

1. Authenticate the virtual key and establish its current principal/identity.
2. Parse and bound the request; retain the protocol representation needed for
   supported forwarding or conversion.
3. Resolve routing and enforce access, privacy, region and capability constraints.
4. Apply readiness/governance checks and reserve any required authority before
   each billable provider attempt.
5. Dispatch and stream under the selected protocol; preserve terminal failures.
6. Settle observed usage, retain unresolved liability, and emit audit/telemetry.

This is a conceptual sequence; ingress-specific protocol adaptation remains in
the corresponding handlers. A later fallback cannot widen an earlier restriction.

## Infrastructure

### Deployment

Each Dockerfile builds a static binary into a non-root distroless image. The Helm
chart renders config and references an existing Secret. Local mode allows one
gateway replica. Explicit shared mode supports multiple gateways; persistent
shared mode supplies separate audit PVCs, anti-affinity and a disruption budget.
Postgres HA, TLS, network controls, resource sizing and recovery qualification are
operator responsibilities. See [container and Helm deployment](operations/deployment.md).

### Modules / Resources

| Component | Path | Role |
| --- | --- | --- |
| Data plane | `cmd/mayu` | Thin assembly and operator CLI |
| Control plane | `cmd/inferplaned` | Policy, authority, telemetry and optional broker assembly |
| Policy schema | `internal/policy`, `api/v1alpha1`, `deploy/crd` | Shared rules and versioned contracts |
| Helm chart | `charts/inferplane` | Profile-aware gateway manifests |
| Monitoring | `deploy/grafana/inferplane.json` | Starter dashboard |

### Deployed Endpoints

The gateway defaults to data port 8080 and admin port 9090; production exposure is
explicit. `inferplaned` defaults to port 7601. See [API reference](api-reference.md)
and the [infrastructure reference](reference/infrastructure.md).

SIGHUP reloads supported provider/model/pricing topology atomically. Listen,
backend, identity declaration, node/journal and authority-mode changes require
restart. A failed reload retains the previous valid topology.

## Key Design Decisions

- Profile-specific authority defines accounting and outage behavior.
- Canonical conversion supports compatible cross-protocol requests; raw forwarding
  preserves supported same-protocol semantics.
- Admission precedes billable egress; each retry carries its own obligation.
- Uncertain usage remains unavailable until supported recovery can establish it.
- Audit fields evolve additively so old exact-byte records still verify.
- Working mechanisms remain alpha pending [production qualification](operations/production-readiness.md).

## Operations

Start with [monitoring](operations/observability.md), [upgrade and recovery](operations/recovery.md),
and [security](operations/security.md). Architectural history lives in
[the decision records](decisions/); package details live in the
[implementation reference](reference/INDEX.md).
