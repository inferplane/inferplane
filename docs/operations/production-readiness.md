# Production readiness

**Current assessment: suitable for a scoped, supervised pilot; not yet an enterprise production-ready release.** Reviewed against the merged `main` baseline `b039b75` on 2026-09-19. The project explicitly retains alpha status.

Durable money authority, shared Postgres admission, Responses/Codex integration, sensitive-data routing, and verified identity mechanisms are implemented. Treating these as entirely absent would describe an older checkout. Their presence still does not establish a qualified production deployment.

## What is already established

| Area | Repository evidence | What it demonstrates |
| --- | --- | --- |
| Budget authority | [ADR-045](../decisions/ADR-045-durable-budget-authority.md), `internal/authority/local`, `internal/authority/pgstore` | Durable grants and conservative per-attempt reservation mechanism |
| Shared enforcement | [ADR-046](../decisions/ADR-046-shared-governance.md), shared store/gateway tests | Shared key/rate/token/money admission with a synchronous DB dependency |
| Verified identity | [ADR-048](../decisions/ADR-048-verified-identity-bindings.md), registry and migration tests | Opt-in bindings retaining registered financial references |
| Client compatibility | Responses fixtures and installed-client tests; [launcher](../codex-launcher.md) | Tested protocol/client paths, not all live models |
| Privacy/routing | [Routing guide](../policy-routing.md), sensitivity and router tests | Fail-closed composition within declared detector/provider capabilities |
| Continuous checks | [CI workflow](../../.github/workflows/ci.yml) | Static builds, race/vet/format, CRD validation, required DB tests, harness and vulnerability checks |

The DB result gate rejects missing/skipped required coverage. This assessment does not treat a local test run without Postgres as database verification, nor a green CI run as a live load/failover benchmark.

## Remaining release gates

| Gate | Current gap | Evidence required to close |
| --- | --- | --- |
| Management trust | Six org/team-scoped roles and complete durable mutation evidence remain open | Endpoint capability map, negative authorization tests, transactionally durable actor/before/after/generation records |
| User budget contract | Complete premium-pool + total-cap behavior remains open | Same verified person across devices, compatible approved fallback or refusal, no credit resurrection |
| Identity rollout | Mechanism is opt-in; historical binding and required activation are operational work | Reviewed mapping including revoked-only history, rotation/migration/restore exercises |
| Recovery | Unknown liability is retained; no automatic reconciliation workflow | Safe operator diagnostics, preserved liabilities and demonstrated stale-restore handling |
| HA and capacity | Profiles and shared storage exist; deployment qualification remains open | Measured throughput/latency, database failover, fault-domain and restart evidence |
| Provider/privacy qualification | Finite detectors, declared boundaries, API-specific controls | Versioned client/model tests, forbidden-egress cases, guardrail refusal/application and mutation evidence |
| Release supply chain | No published signed-release workflow/artifact channel in the reviewed baseline | Reproducible pinned artifacts, verification instructions and staged rollout/rollback evidence |

The canonical criteria are the [enterprise contracts](../enterprise-strategy.md). New release claims should close those criteria with evidence rather than re-label alpha.

## Pilot acceptance

Agree on the users, traffic classes, provider accounts, maximum spend exposure and accountable operator. Choose one [deployment profile](../getting-started/deployment-profiles.md); configure required identity if person-level controls are part of the evaluation.

Demonstrate allowed/forbidden model calls, key rotation, shared near-cap concurrency where applicable, interrupted streams, policy/identity mismatch, CP outage, DB outage, and backup restoration. Compare actual provider attempts with authority, usage and audit records. Measure task success and end-to-end cost, including retries and cold-cache writes.

Keep an exit decision: expand only after the specified gates pass, or stop admission and preserve evidence when a gate fails. There is no published SLA, managed service, or compliance certification established by this repository.
