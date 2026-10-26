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
| Audit sink / WAL unavailable | Preserve WAL bytes; required-sink backlog or WAL failures close new inference admission. Repair and reconcile before restart |
| Node lost | Preserve central grants as committed; expiry is not a refund |

ADR-045 can use valid existing local authority only within all gates. ADR-046 always needs its database for new shared admission. A control-plane HTTP outage and a database outage are different exercises.

## Restore authority safely

Restoring earlier policy/identity/authority rows can resurrect spend already used after the backup. Fence writers and establish every post-backup liability before exposing restored state. Include completed spending, open grants, pending shared permits and late reports in the reconciliation.

If outstanding authority cannot be established, keep affected windows frozen. An unreachable gateway or expired grant does not prove unused credit. Do not truncate counters, drop permit/grant rows, copy a running private journal, or reissue an account under a fresh reference to restore service.

No automated refund/reconciliation API or generic zero-downtime rollback procedure is provided. Follow the specific [durable authority](../durable-budgets.md), [shared store](../shared-governance.md), and [identity migration](../verified-identity.md) contracts.

## Recover audit admission

`buffer_then_block` closes new inference after 1024 retained WAL records, or
immediately after a WAL append/truncate failure. The latch lasts for the process.
Restarting with the same full backlog stays unready; malformed/truncated WAL
refuses startup. The gateway does not automatically replay WAL into audit sinks.

1. Stop the affected writer and preserve an exact copy of its WAL, per-instance
   sink segments and relevant delivery/checkpoint evidence before changing files.
2. Repair storage permissions/capacity and required-sink availability. Verify
   archived complete segments with `mayu audit verify`; preserve damaged tails
   separately for investigation rather than silently discarding them.
3. Reconcile which records each sink already accepted. Recover missing records
   through an isolated, sink-specific procedure that preserves exact line bytes,
   per-instance ordering and delivery checkpoints or idempotency. A blanket
   replay can duplicate records already delivered during partial fan-out.
4. After retained evidence is archived and missing delivery is accounted for,
   prepare the active WAL for a new segment and restart. Keep the archive until
   retention and external verification requirements are met. Confirm `/readyz`,
   buffer metrics and fresh sink delivery before restoring traffic.

There is no general-purpose audit WAL replay CLI. This procedure does not recover
records that never reached durable storage or prove resistance to host-side
history rewriting.

## Recover analytics evidence

Analytics events and rollups are rebuildable read models; audit JSONL is the
source. This recovery does not alter key/governance/identity, authority, grant,
permit or settlement tables. Never treat projection reset, an estimated zero or
missing cost as a refund or renewed admission balance (ADR-053).

1. Record the profile, upgraded writer versions, source segment inventory and
   retention gaps. Preserve exact original JSONL bytes, the derived DB and, in
   Mode B, checkpoints/lease metadata for diagnosis. Use a consistent backup;
   do not delete originals or checkpoints to make a completeness claim.
2. Upgrade **all analytics writers** before replay. Mixed old/new writers are
   not qualified for projection completeness: older code can still skip Cost=nil
   records or write conservative default projections. Plan schema-lock time and
   test rollback/restore in isolation; an old binary is not a validated rollback.
3. Follow the applicable source-recovery path below, then compare health/lag,
   retained-source counts and the outcome/cost invariants over an explicit UTC
   period. Keep any missing source interval documented even if legacy rows reach 0.

### SQLite Mode A

Opening the upgraded index adds missing columns idempotently. Existing rows keep
`has_cost=true`, `outcome_state=unknown`, `cost_state=legacy_unknown`,
`projection_version=0`; old HTTP 200 alone proves neither success nor complete cost.
SQLite's additive ALTER steps are individually applied, not the Mode-B single
backfill transaction; an interrupted open can resume missing-column migration.

Explicitly select the retained originals for replay. At gateway startup, Mode A
calls `Index.Replay` for each configured `audit.sinks` file path. With traffic
stopped, use the upgraded binary and the intended Mode-A index/configured original
file sinks, then inspect startup replay warnings and the resulting summary/logs.
Startup does not discover all rotated archives or recover stdout automatically;
only configured readable files are replayed. Preserve original segments and use
an isolated source replay when assessing additional archives. There is no Mode-A
HTTP rebuild or audit-replay CLI command; its rebuild route returns 405.

Replay upgrades a matching version-0 ID to version 1 from the original evidence;
version-1 IDs remain first-write-wins. Repeated replay does not double count.
Previously omitted Cost=nil completions can enter the index. An old Cost=nil
source becomes unavailable, while an old Cost-present source without usage evidence
remains legacy_unknown even at version 1. A truncated final JSONL line is skipped.
Without retained originals, conservative version-0 rows cannot gain evidence and
missing costless history cannot be reconstructed.

### PostgreSQL Mode B

Startup migration uses one connection, an advisory lock and one transaction to
add event/rollup columns and regenerate rollups from retained derived events
on the first upgrade. A durable migration marker commits in that transaction;
current-schema opens do not rewrite rollups. Failure rolls back columns, backfill
and marker together so the upgrade can retry; statement timeout is restored.
The first upgrade can take schema locks and scan retained events. It does **not** recover
Cost=nil originals that an older aggregator skipped before an advanced checkpoint.
Retain those source segments and the checkpoint backup.

After all writers are upgraded, an authorized full admin can call the existing
`POST /admin/analytics/rebuild`. A 204 means the reset succeeded, not that replay
has finished. The reset clears derived events/rollups/checkpoints and advances
the lease epoch to fence old workers; the aggregator then reacquires leadership
and replays retained segments from the start. Stale epochs cannot change events,
rollups or checkpoints. Check `/admin/analytics/health` and queried results until
replay catches up. PostgreSQL corrected-record upserts recompute both old and new
team/model/day groups; repeated replay remains idempotent.

If originals were expired, migration/rebuild cannot manufacture the absent
history. Zero `legacy_projection_records` is not proof of complete collection;
compare source retention, collection drops and lag. Keep the pre-rebuild backup
and originals until validation and retention requirements are satisfied.

These procedures describe implemented recovery behavior. Local synthetic tests
and a disposable PostgreSQL exercise are documented in ADR-053; no live database
rebuild, production rollout or paid-model qualification is claimed.

## Evidence needed before production

Record recovery time and permitted data loss from an actual restore/failover exercise. Include database loss separately from CP process loss, retained uncertainty after a crash, and identity rotation with historical balances. These are acceptance measurements, not guarantees supplied by this guide.
