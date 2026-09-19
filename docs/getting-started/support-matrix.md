# Feature support

This is an implementation inventory, not an SLA. “Implemented” means repository code and tests exist; production evidence must come from the chosen deployment.

| Capability | Status | Scope / remaining qualification |
| --- | --- | --- |
| Messages, Chat Completions, Responses and Bedrock ingress | Implemented | Provider and capability combinations vary |
| Virtual keys, model allow-lists, team restrictions | Implemented | Protect issuance/admin paths and store access |
| Verified human/service identity | Opt-in | Required activation needs trusted historical bindings and rollout validation |
| Durable global policy money | Opt-in · ADR-045/046 | Conservative reservations; unknown usage retains liability |
| Global RPM/TPM and token quotas | Opt-in · ADR-046 | Synchronous Postgres dependency |
| Sensitive-data policy and adaptive routing | Opt-in | Finite detectors, operator-declared boundaries, local session pins |
| Hash-chained audit and optional anchoring | Implemented | Preserve every instance segment; WORM protection depends on storage/IAM |
| Metrics, traces, usage analytics | Implemented | Separate channels; configure collectors and retention |
| Six management roles with org/team scope | Open | Current credential/admin separation is coarser |
| Complete per-user premium + total pool contract | Open | Existing tiers and hard caps are building blocks |
| Automated uncertain-credit reconciliation | Open | No “reset counters” recovery shortcut |
| Signed release and deployed HA/load qualification | Open | CI results alone do not qualify a fleet |
| Embeddings, image/audio generation, rerank, MCP gateway | Outside current product scope | Chat/coding traffic is the supported lane |

## Compatibility policy

The project is alpha and the API is `v1alpha1`. Config/schema compatibility can change. Pin a reviewed commit or internally built image, upgrade participating binaries and CRDs together when a feature requires it, and run staged acceptance before rollout.

For decisions, use the [deployment profiles](deployment-profiles.md) and [readiness assessment](../operations/production-readiness.md). The [roadmap](../roadmap.md) separates shipped mechanisms from remaining contracts.
