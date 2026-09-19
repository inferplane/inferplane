# Upgrade and recover

Preserve identity, financial liabilities and audit evidence across changes. An old database snapshot is not a safe rollback when newer grants or spending exist.

## Plan an upgrade

1. Pin the running and candidate commits/images. Read the changed ADRs and configuration schema.
2. Record the selected profile, authority namespace, policy generation, participating binaries, CRD version and identity declaration.
3. Test the candidate in isolated staging with synthetic identities, disposable state and fake providers before a controlled live-model test.
4. Back up the relevant stores consistently and verify restoration into isolation. Include SQLite sidecars using an appropriate SQLite backup or stopped service, private node journals, audit segments, and all Postgres identity/policy/authority tables.
5. Establish how old writers and gateways will be stopped/fenced during incompatible changes. Account for issued authority; stopping a node does not prove its credit was unused.
6. Roll out compatible binaries/CRDs and verify readiness, key rotation, refusal cases, observed usage, retained uncertainty and audit verification.

Provider/model/pricing topology can use the documented SIGHUP reload where supported. Backend, identity declaration, authority mode, node ID, journal path and listener changes require restart. A schema/profile migration is not a hot reload.

## Outage response

| Failure | Safe operating response |
| --- | --- |
| CP unavailable | Restore policy/grant delivery; respect installed readiness/staleness and authority deadlines |
| Shared Postgres unavailable | Restore the qualified database path; new shared admission fails closed |
| Node journal unavailable | Stop affected admission; preserve journal and sidecars for investigation |
| Incomplete stream / unknown usage | Keep uncertain liability reserved; compare observed usage and provider evidence |
| Audit sink unavailable | Recover the sink before buffer exhaustion; retain WAL bytes |
| Node lost | Preserve central grants as committed; expiry is not a refund |

ADR-045 can use valid existing local authority only within all gates. ADR-046 always needs its database for new shared admission. A control-plane HTTP outage and a database outage are different exercises.

## Restore authority safely

Restoring earlier policy/identity/authority rows can resurrect spend already used after the backup. Fence writers and establish every post-backup liability before exposing restored state. Include completed spending, open grants, pending shared permits and late reports in the reconciliation.

If outstanding authority cannot be established, keep affected windows frozen. An unreachable gateway or expired grant does not prove unused credit. Do not truncate counters, drop permit/grant rows, copy a running private journal, or reissue an account under a fresh reference to restore service.

No automated refund/reconciliation API or generic zero-downtime rollback procedure is provided. Follow the specific [durable authority](../durable-budgets.md), [shared store](../shared-governance.md), and [identity migration](../verified-identity.md) contracts.

## Evidence needed before production

Record recovery time and permitted data loss from an actual restore/failover exercise. Include database loss separately from CP process loss, retained uncertainty after a crash, and identity rotation with historical balances. These are acceptance measurements, not guarantees supplied by this guide.
