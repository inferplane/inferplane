---
title: Govern your coding-agent traffic
description: Route models, control spend, and trace usage with a self-hosted governance plane for coding agents.
hide:
  - toc
  - navigation
---

<div class="ip-hero" markdown>

<p class="ip-eyebrow">The governance plane for coding agents</p>

# Give every request a policy.

Connect coding agents to your models through inferplane. Control access, apply budget and privacy rules, and follow usage from request to audit record.

<div class="ip-actions" markdown>

[Make your first request](getting-started/quickstart.md){ .md-button .md-button--primary }
[Choose your deployment](getting-started/deployment-profiles.md){ .md-button }

</div>
</div>

<div class="ip-facts">
<span>Self-hosted</span><span>Apache-2.0</span><span>Two static Go binaries</span><span>Alpha · explicit operating limits</span>
</div>

## Your models. One place to govern access.

<div class="ip-flow" role="img" aria-label="Coding agents send requests to mayu, which applies access, routing, budget, and privacy checks before calling a configured provider.">
  <div><b>Coding agents</b><small>Claude Code · Codex · OpenCode</small></div>
  <span class="ip-arrow" aria-hidden="true">→</span>
  <div class="ip-gateway"><b>mayu</b><small>Access · routing · budgets · audit</small></div>
  <span class="ip-arrow" aria-hidden="true">→</span>
  <div><b>Your providers</b><small>Anthropic · Bedrock · compatible APIs</small></div>
</div>

`mayu` handles inference traffic. Optional `inferplaned` distributes policy and budget authority without carrying prompts or response streams. Choose local enforcement, durable node-local money budgets, or synchronous shared Postgres admission. Each has a different [availability contract](getting-started/deployment-profiles.md).

<div class="grid cards" markdown>

-   **Control who can use what**

    Issue virtual keys, constrain models, and opt into verified human and service identities without replacing historical accounts.

    [Understand identity →](verified-identity.md)

-   **Make budget decisions before egress**

    Reserve monetary authority, enforce limits, and configure approved lower-cost targets as spending changes.

    [Explore durable budgets →](durable-budgets.md)

-   **Route with privacy constraints**

    Compose model access, sensitive-data rules, context routing, and fallback restrictions across provider attempts.

    [Configure routing →](policy-routing.md)

-   **Explain usage and failures**

    Inspect request records, verify audit chains, and monitor latency, spending, and refusal signals.

    [Operate the gateway →](operations/observability.md)

</div>

## Start small. Select the right enforcement scope.

| Your next step | Start here |
| --- | --- |
| Evaluate one gateway and make a real request | [Local quickstart](getting-started/quickstart.md) |
| Govern money across managed developer machines | [Durable node-local budgets](durable-budgets.md) |
| Share keys, rates, token quotas, and money across gateways | [Shared Postgres governance](shared-governance.md) |
| Decide whether this meets a production requirement | [Readiness assessment](operations/production-readiness.md) |

!!! note "A documented product, with an honest release posture"
    inferplane is alpha. Working mechanisms and regression tests are not a production qualification. Fine-grained management roles, complete user-pool contracts, recovery/load evidence, and signed releases remain open. There is no published availability SLA or compliance certification in this repository.
