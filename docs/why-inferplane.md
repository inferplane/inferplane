---
title: Why inferplane
description: A dedicated governance control plane and data planes that automatically enforce access, budget and privacy policy for coding agents.
---

# Why inferplane

**Set the operating policy centrally; let each gateway apply it as requests arrive.**
inferplane combines a dedicated governance control plane with data planes that
enforce model access, budget decisions, privacy boundaries and accounting. Coding
agents use one gateway endpoint and approved model names while the configured
rules determine which provider attempts are allowed.

The value is in how these controls work together: a cheaper route must still be
private enough, an internal route must still fit the budget, and a fallback must
still satisfy both.

## From API access to governed execution

API normalization, routing and observability are useful gateway capabilities.
Other gateways also provide budgets, guardrails or separate control and data
planes; these are not exclusive claims. For example, the
[Envoy AI Gateway architecture](https://aigateway.envoyproxy.io/docs/concepts/architecture/)
describes separate planes, and
[LiteLLM's proxy architecture](https://docs.litellm.ai/docs/proxy/architecture)
describes authentication, budgets and rate limits.

Evaluate inferplane on the following concrete operating contracts, rather than a
feature-count comparison:

| Operating need | inferplane's design | What it means for your team |
| --- | --- | --- |
| Manage policy without relaying every prompt through the management service | `inferplaned` distributes policy/authority; `mayu` carries inference and enforces decisions | Deploy the gateway near agents or in a shared fleet while keeping control-plane HTTP out of each inference admission |
| Make one rule apply across clients | Matching `GovernancePolicy` rules constrain model access, rates, quotas, money and routing | Supported ingresses share governance; users need not manually select a privacy route for each request |
| Change behavior before a total spending stop | Budget tiers select configured alternatives; optional strict targets constrain every attempt | Define an economy cutover separately from the hard cap |
| Keep sensitive requests inside approved destinations | Local inspection, `InternalOnly`, verified `Mask`, `Block`, and restricted fallback | Privacy obligations survive context selection, budget switching and retries |
| Choose where shared accounting lives | Durable node-local money grants or synchronous shared Postgres admission | Select global money with local request admission, or shared keys/rates/tokens/money with an explicit DB dependency |
| Explain what actually happened | Routing evidence, per-attempt authority accounting, audit chains and metrics | Separate recommendations, actual provider attempts and uncertain spend |

These contracts are implemented mechanisms in an alpha product. Complete
organization/team administration roles, full user-pool contracts and production
qualification remain open. See the [support matrix](getting-started/support-matrix.md)
and [readiness assessment](operations/production-readiness.md).

## Separate management from inference

**The control plane sets the rules. The data plane applies them.**
`inferplaned` distributes `GovernancePolicy` documents and budget authority,
receives usage telemetry, and can broker short-lived Bedrock credentials.
`mayu` authenticates requests, inspects supported content, resolves approved
routes, checks admission, calls providers and records usage.

Prompts and response streams do not transit the control-plane HTTP service.
Local inspection and bounded session preferences need no remote classifier or
affinity service. This gives operators separate management and inference
deployment boundaries; it does not guarantee unlimited operation during an outage.

| Choose a profile | Request-time authority | Useful boundary |
| --- | --- | --- |
| Local `mayu` | Local stores/counters, optionally local policy files | Evaluate one gateway without a control plane |
| Durable node-local money · ADR-045 | Private journal backed by finite centrally committed money grants | Govern policy money across managed machines without a shared DB call per inference request; keys/rates/token quotas stay local |
| Shared Postgres · ADR-046 | Synchronous key lookup and atomic rate/token/money reservations | Apply shared admission across gateways; reachable Postgres is required |

Existing node-local credit is usable only within readiness, policy-age and
authority deadlines; control-plane or authority-DB loss prevents replenishment.
Shared mode refuses new admission on DB loss. The
[architecture diagram](architecture.md#system-overview) shows each dependency;
[deployment profiles](getting-started/deployment-profiles.md) specify the outage contracts.

## Configure the rules. The gateway applies them.

Automation starts with explicit operator choices. It does not discover your data
boundary, invent a budget, or automatically approve a model.

| You configure | mayu automatically applies | Boundary to retain |
| --- | --- | --- |
| Virtual keys, allowed models, team/user policy selectors; optional verified identity | Authenticates and evaluates applicable restrictions | A model substitution cannot widen access; full management-role and user-pool models remain open |
| Provider targets, prices, context windows and capabilities | Resolves public model names and filters incompatible routes | Declared metadata must match the actual deployment |
| PII actions and explicit internal model names | Inspects original supported content; routes internally, masks and reinspects, or blocks | Finite detectors; unknown/opaque input follows `onUninspectable`; an `internal` label is an operator assertion |
| Budget reference, thresholds and substitutes | Changes the effective route as the configured usage threshold is reached | Enable `enforceTargets` for strict cutover; legacy substitution can retain the original model |
| An independent hard cap and accounting profile | Checks before billable egress; authority profiles reserve every attempt and settle observed usage | Cheap/internal routes still consume the cap; incomplete usage can retain the reservation |
| Context targets, thresholds, keywords and optional stability | Recommends in Shadow; selects compatible routes in Enforce and revalidates local successful-target preferences | Rule-based signals do not prove task difficulty or model quality; context cannot loosen privacy or strict targets |
| Audit sinks and monitoring | Records decisions, attempts, usage and refusals | Analytics are not the budget authority; body capture is separate and opt-in |

Policy distribution and application are asynchronous. A new policy is not an
instantaneous fleet-wide transaction. Configure synchronization/staleness gates
and understand issued authority before changing operating limits.

## One configuration, several automatic decisions

The repository includes an
[adaptive gateway configuration](../examples/config.adaptive-routing.json) and
[governance policy](../examples/adaptive-routing/governance.yaml).
The example uses an `engineering` team, an `auto` model alias, internal `economy`
targets, a **$100 monthly switching threshold**, and a separate **$150 monthly
hard cap**. Amounts, endpoints, prices and model IDs are illustrative.

### Establish the operating policy

1. Replace provider endpoints/model IDs, verify internal processing boundaries,
   and declare real capabilities, context windows and accounting prices. Supply
   the example's referenced secrets through your secret manager.
2. Load the governance policy with **one** of
   [internal routing](../examples/adaptive-routing/privacy-internal.yaml) or
   [masking](../examples/adaptive-routing/privacy-mask.yaml). The supplied gateway
   config selects internal routing. Loading both requires both masking and an
   internal destination.
3. Connect the client to mayu and issue an allowed virtual key. Keep using `auto`
   for requests compatible with the configured target protocols and capabilities.
4. Evaluate context recommendations in **Shadow**, then explicitly set
   `routing.context.mode: Enforce` if workload results justify automatic selection.
   Privacy and strict budget routing do not wait for this context toggle.
5. For fleet accounting, follow the chosen
   [durable](durable-budgets.md) or [shared](shared-governance.md) profile.
   Replace the local `policies` source with `control_plane`; the two sources are
   mutually exclusive. Configure the corresponding authority/state stores too.

See [adaptive routing](adaptive-routing.md#example-configuration) for the
commands and complete compatibility requirements.

### Observe the request decisions

<div class="ip-diagram" tabindex="0" role="region" aria-label="Automatic governance flow; scroll horizontally on a small screen" markdown>

![A request enters mayu under the active policy. Access and local inspection constrain candidates; budget and context routing select within those constraints. Admission precedes each provider attempt. Failed eligible attempts return through the same checks; usage is settled and audited. No safe or funded route means refusal.](assets/request-flow.svg)

</div>

[Open the decision-flow diagram](assets/request-flow.svg). This is a conceptual
decision flow; constraints compose, rather than letting a later stage override an
earlier restriction. Ingress adaptation and readiness gates remain protocol-specific.

| Example request or event | Automatic result under the example policy |
| --- | --- |
| Ordinary inspectable `auto` request below the switching threshold | Shadow records a context recommendation while retaining the baseline route; Enforce may select a compatible weak/normal/strong target |
| A supported email/phone/card or other covered PII signal | `InternalOnly` restricts the request to approved internal `economy` targets, subject to access, capabilities and admission |
| Detected PII when using the alternative masking policy | Mayu completes masking and independent reinspection before any usable route; unsafe transformation refuses |
| Opaque or uninspectable input | The example's `onUninspectable: Block` refuses generation |
| The $100 switching threshold becomes active | `enforceTargets: true` constrains mapped model names to `economy`, including subsequent eligible retries; a conflicting privacy rule or unavailable target can refuse |
| The remaining $150 hard-cap allowance cannot admit the next attempt | Refusal before billable egress, even for the economy/internal route |
| An approved provider fails before the supported output-commit boundary | An eligible fallback must pass the same privacy/access/strict-target checks and new attempt admission; no unrestricted external or premium escape |
| A stream ends with incomplete usage | Authority accounting retains unproven spend; observed usage and uncertainty remain distinct |

A cutover is evaluated from the profile's accounting state; it is not an exact
external-invoice threshold or a promise to interrupt already-running requests.
Reservations can refuse a request before the displayed settled spend reaches the
hard cap. Model/provider switches can also lose cache warmth. Evaluate total
settled cost, retries, latency and task success before claiming savings.

## What the automation does not decide for you

PII inspection covers finite email, supported phone, card, SSN, IPv4 and Korean
resident-ID shapes. It does not establish universal PII discovery, regional legal
compliance or provider trust. Internal targets must really meet the processing
boundary you declare. If a safe route cannot be established, restrictive privacy
rules refuse rather than letting context or cost preferences relax the boundary.

Network controls and provider credentials determine whether users can bypass
your gateway. A node controlled by an adversary is not made trustworthy by
installing mayu or enabling credential brokering. Operators also own HA database
deployment, recovery qualification and model quality evaluation.

Start with the [local quickstart](getting-started/quickstart.md), select an
[accounting profile](getting-started/deployment-profiles.md), and use the
[security boundaries](operations/security.md) and
[production readiness](operations/production-readiness.md) pages to define your pilot.
