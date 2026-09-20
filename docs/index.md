---
title: Set policy once. Govern every request.
description: Separate policy management from inference traffic. Automate model access, budget cutover, PII routing, and audit with inferplane.
hide:
  - toc
  - navigation
---

<div class="ip-hero" markdown>

<p class="ip-eyebrow">Central policy. Local enforcement.</p>

# Set the policy. Govern every request. {#give-every-request-a-policy}

Define who can use which models, how much they can spend, and where sensitive data may go. inferplane applies those rules automatically as coding agents work: select an approved model, switch at a budget threshold, route detected PII to an internal destination, and record the decision.

A dedicated control plane manages policy and budget authority. Each `mayu` data plane enforces them on the inference path, close to your agents or inside your shared gateway fleet.

<div class="ip-actions" markdown>

[Make your first request](getting-started/quickstart.md){ .md-button .md-button--primary }
[Why inferplane](why-inferplane.md){ .md-button }

</div>
</div>

<div class="ip-facts">
<span>Separate control &amp; data planes</span><span>Self-hosted · Apache-2.0</span><span>Two static Go binaries</span><span>Alpha</span>
</div>

## Govern centrally. Enforce at each gateway. {#your-models-one-place-to-govern-access}

<div class="ip-diagram" tabindex="0" role="region" aria-label="Architecture diagram; scroll horizontally on a small screen" markdown>

![Operators configure inferplaned above the inference path. It distributes policy and budget authority to mayu. Agents send prompts to mayu, which applies access, privacy, routing and budget controls before calling approved providers. Only the shared profile calls Postgres for each admission.](assets/architecture.svg)

</div>

[Explore the architecture and failure boundaries](architecture.md) · [Open the diagram](assets/architecture.svg)

**`inferplaned` manages; `mayu` enforces.** Prompts and response streams travel between agents, data planes and providers. They do not transit the control-plane HTTP service, and request inspection needs no remote classifier. A standalone `mayu` can instead load local policies.

Choose the accounting boundary explicitly: local enforcement, durable global policy-money grants with local admission, or shared Postgres key/rate/token/money admission. The shared profile requires a database call for each admission; control-plane separation does not remove that dependency.

<div class="grid cards" markdown>

-   **Turn governance into request-time behavior**

    Configure virtual keys, model access and matching team/user policies. Each gateway applies the effective restrictions before billable egress, including on fallback.

    [See what runs automatically →](why-inferplane.md#configure-the-rules-the-gateway-applies-them)

-   **Make budgets change the route**

    Set a spending threshold and approved economy targets. Enable strict cutover to constrain subsequent attempts; keep a separate hard cap to stop spending.

    [Follow a budget cutover →](why-inferplane.md#one-configuration-several-automatic-decisions)

-   **Route PII inside your approved boundary**

    Inspect supported request content locally. Choose internal-only routing, verified masking or blocking. Fallback keeps the same privacy restrictions.

    [Configure privacy and routing →](adaptive-routing.md)

-   **Keep decisions accountable**

    Track requested, selected and attempted routes alongside usage and refusal reasons. Authority profiles reserve before each attempt and retain uncertain spend when usage is incomplete.

    [Understand durable accounting →](durable-budgets.md)

</div>

## Keep the client simple. Make the policy explicit.

A coding agent can keep requesting an approved alias such as `auto`. With context routing enabled, mayu evaluates request size and configured keywords, then chooses a compatible target within access, privacy and budget constraints. Context starts in **Shadow** for evaluation; PII rules already enforce. Model capabilities, internal boundaries and prices remain operator-reviewed inputs.

For example, the supplied adaptive policy routes detected PII to an approved internal `economy` model. At its illustrative $100 monthly switching threshold, strict budget routing selects `economy` for the configured model names. A separate $150 hard cap still blocks requests that cannot be admitted. These actions compose; one never bypasses another.

[Read the scenario and configuration path](why-inferplane.md#one-configuration-several-automatic-decisions). Finite PII detectors and rule-based context signals have explicit limits; automatic routing does not promise universal detection, best-model selection or measured savings.

## Start small. Select the right enforcement scope.

| Your next step | Start here |
| --- | --- |
| Evaluate one gateway and make a real request | [Local quickstart](getting-started/quickstart.md) |
| Govern money across managed developer machines | [Durable node-local budgets](durable-budgets.md) |
| Share keys, rates, token quotas, and money across gateways | [Shared Postgres governance](shared-governance.md) |
| Decide whether this meets a production requirement | [Readiness assessment](operations/production-readiness.md) |

!!! note "A documented product, with an honest release posture"
    inferplane is alpha. Working mechanisms and regression tests are not a production qualification. Fine-grained management roles, complete user-pool contracts, recovery/load evidence, and signed releases remain open. There is no published availability SLA or compliance certification in this repository.
