# Choose a deployment profile

Select the accounting and availability contract before choosing replicas. All profiles are alpha; enabling a backend does not qualify a production deployment.

| | Local SQLite | Durable node-local money · ADR-045 | Shared Postgres · ADR-046 |
| --- | --- | --- | --- |
| Intended evaluation | One gateway | Managed developer-local gateways | Shared gateway fleet |
| Key/team records | Local SQLite | Local SQLite | Shared Postgres |
| Policy money | Local memory; optional legacy CP allowances | Global Postgres authority, private local reservations | Atomic Postgres reservations |
| Rates and token quotas | Local | Local | Shared |
| Database access for admission | Local storage only | Local private journal; grants obtained asynchronously | Synchronous shared Postgres |
| Multiple gateways | Independent local limits | Shared policy money, other limits remain local | Shared admission mechanism |
| Setup | [Quickstart](quickstart.md) | [Durable budgets](../durable-budgets.md) | [Shared governance](../shared-governance.md) |

Standalone/key-local monetary limits are not globalized by ADR-045. Legacy control-plane allowances are not durable escrow. ADR-046 must use the same authority database/schema on all participating gateways and control planes.

## Understand outages

| Event | Local/legacy | ADR-045 | ADR-046 |
| --- | --- | --- | --- |
| CP HTTP unavailable | Standalone has no CP dependency; attached gates still apply | Existing credit only, within policy/readiness and hard deadlines | Admission can continue with valid binding/policy and reachable DB |
| Authority/shared DB unavailable | No shared admission dependency | Existing local credit only; no replenishment | New key resolution/admission refuses |
| Gateway restart | In-memory counters reset | Journal recovery conservatively retains liabilities | Shared records survive; ambiguous permits remain reserved |
| Exhausted/expired authority | Local policy semantics | Refuse; no automatic refund | Refuse; no automatic refund |

`require_sync` gates initial policy delivery. `max_policy_age` can refuse stale policy. Durable authority and shared binding have their own mandatory initial checks; valid credit never bypasses a detected incompatible identity/policy response.

Counts remain local HTTP 200 even when generation is refused. Gateway, credential, storage and provider availability are separate dependencies. No profile guarantees unlimited disconnected operation or unconditional availability.

## Add identity deliberately

Every profile supports opt-in [verified identity](../verified-identity.md). Optional attribution preserves legacy accounting; required mode enforces registry bindings. Shared storage alone does not enable required identity.

Migration must preserve historical account references and unresolved liabilities, including revoked-only owners. Required mode is a persisted change, not a reversible feature toggle.

## Select an operating boundary

For a pilot, define the participating users, gateway profile, model allow-list, spend exposure, recovery owner, and exit criteria. Use [production readiness](../operations/production-readiness.md) to identify remaining qualification work.
