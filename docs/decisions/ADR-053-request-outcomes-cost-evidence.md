# ADR-053: Request outcomes and cost evidence

**Date:** 2026-10-21
**Status:** Accepted; implementation merged to main. Production rollout and live rebuild are not qualified.
**Related:** ADR-007 (chargeback), ADR-015 (analytics Mode B), ADR-018 (logs), ADR-030 (settlement), ADR-045/046 (authority), ADR-049 (workspaces).

## Context

Analytics previously selected `request_completed && Cost != nil`. Costless
failures could disappear from Logs and Usage, HTTP-200 interrupted streams could
look successful, and absent prices/usage could look like dollar zero. Existing
request/token/amount totals are compatibility and financial-reporting contracts;
redefining them as all traffic would obscure their meaning. The audit already
contains outcome, usage and pricing evidence, so no audit-schema or exact-byte
hash-chain change is needed.

## Decision

Use the shared pure classifier in `internal/analytics/evidence.go` to project
all `request_completed` records into SQLite and PostgreSQL. Preserve the old
Cost-present (`has_cost=true`) request/token/amount calculations. Add `operations`
to totals/team/model/day and evidence fields to events through existing full-admin
APIs; there is no new route or permission. Analytics remains a derived read model,
without changing admission, policy, budget authority, reserve/settle/refund,
quota, identity, privacy routing or integer microUSD/round-half-even arithmetic.

The denominator is **observed completion records**, not all traffic, unique user
requests, sessions or task quality. Start-only refusals, unauthenticated 401s,
admin events and client-local failures are excluded. Retry/fallback record units
can differ by ingress. Transport success does not attest model answer quality.

Outcome precedence: Partial=true → partial; no Outcome or status outside 200–599
→ unknown; a valid status with nonempty Outcome.Error or status≥300 → failed;
remaining 2xx → succeeded. Partial is not double counted as failed. Show
`(failed + partial) / completed`, its fraction and unknown count; completed=0
shows `—`, and unknown makes the observation incomplete.

Cost precedence: Cost=nil → unavailable; PricingMissing → unpriced; absent Usage
or Usage.Evidence → legacy_unknown; Partial, Usage.Estimated, false
InputKnown/OutputKnown/InputTotalKnown/CacheWriteTTLKnown, AccountingUncertain
or negative amount → uncertain; otherwise estimated. A valid estimated zero is
shown as an estimate; unavailable/unpriced numeric zero is not a known amount.
Cost and transport are independent, including an HTTP-400 estimate. Complete
usage/rate evidence is still an **estimate**, never the supplier's final invoice.
Estimated amounts and uncertain + legacy_unknown recorded amounts are separate.

`completed = succeeded + failed + partial + unknown` and also equals the sum of
the five cost-state counts. Costless-only groups exist with zero legacy requests
and amounts. The [API contract](../api-reference.md#analytics-outcome-and-cost-evidence-adr-053)
contains the exact additive field names and literal eight-record examples.

## Streaming reasons

Preserve typed provider codes `context_length_exceeded` and
`invalid_encrypted_content`. Gateway reasons are `request_canceled`,
`upstream_timeout`, `stream_incomplete`, `response_incompatible`, `usage_missing`,
`client_write_failed`, `upstream_error`. Classification uses typed provider errors
first, then `errors.Is` cancellation/deadline, explicit gateway sentinels and
fallback. Never classify raw Error() text or infer a provider code from a native
provider-owned frame. Nonempty unknown analytics codes become upstream_error;
empty codes remain empty and are omitted from JSON.

Before commit, HTTP status and retry rules remain unchanged. After commit,
HTTP 200 + partial remains and typed/bounded reason is retained in audit and the
gateway-generated SSE error if writable. There is no retry after committed output.
Client-write failure makes no additional SSE write. Native success bytes remain
unchanged. Existing exactly-once finish/settlement consumes observed usage;
missing usage is not fabricated as zero, cost or refund. No metric labels,
raw errors/prompts, key/session IDs or runtime dependencies are added.

## Migration and recovery

Add conservative event defaults: version 0, has_cost=true, outcome unknown,
cost legacy_unknown, partial=false, empty error. Past HTTP 200 does not supply
missing usage/outcome evidence. Source projection writes version 1. A source can
remain legacy_unknown at version 1 when it never contained usage evidence.

SQLite adds missing columns idempotently; ALTER steps are individual rather than
a single backfill transaction. Explicitly replay retained readable file-sink
originals through the existing startup `Index.Replay` path. It upgrades matching
version-0 IDs; version-1 remains first-write-wins and repeats never duplicate.
Startup does not discover rotated archives or stdout. Cost=nil originals can be
recovered, truncated final JSONL lines are skipped, and lost source cannot be
manufactured. The nonblocking 4096-entry queue, drops and close-drain contract remain.

Mode B adds event and all operational rollup columns and regenerates rollups in
one advisory-locked, single-connection schema transaction on first upgrade.
A durable migration marker commits atomically with the columns and backfill;
current-schema opens skip that work. Failure rolls back all three, allowing retry;
timeout restoration is retained. The first backfill scans retained
derived events and can take schema locks. Corrected-record upserts recompute both
old and new team/model/day keys. Existing lease/checkpoint semantics remain.

An old checkpoint can already be beyond costless source records never projected.
Migration alone cannot recover them. After **all analytics writers** are upgraded,
full admins explicitly request the existing Mode-B rebuild. It resets derived
events/rollups/checkpoints, advances epoch to fence old workers, and the aggregator
replays retained source history. A 204 acknowledges reset, not completed replay;
Mode A returns 405. Preserve originals, consistent derived backups and checkpoint
metadata, validate lag and source coverage after replay. No live rebuild is automatic.

Mixed-version writers are not qualified for complete projections. Neither a
rollback to old binaries nor a zero `legacy_projection_records` certifies history.
Retention gaps, queue loss, ingestion delay and start-only requests remain limits.
Without originals, version-0 evidence and skipped history remain unrecoverable.
Follow [recovery](../operations/recovery.md#recover-analytics-evidence); no financial
rows are reset and existing profile/authority/DB-loss contracts remain binding.

## Acceptance evidence

The shared A–H fixture yields completed=8, succeeded=4, failed=2, partial=1,
unknown=1; estimated=3, uncertain=1, unpriced=1, unavailable=2,
legacy_unknown=1; estimated cost=1300 µUSD, incomplete recorded cost=2050 µUSD.
The compatible legacy total remains requests=6 and cost=3350 µUSD. Its 3/8 observed
failure proportion is 37.5%, with one unknown outcome disclosed.

| Requirement | Behavioral acceptance tests |
| --- | --- |
| §4 classifier, exclusions, zero-vs-missing, bounded text | `TestEvidenceClassification`, `TestZeroCostRequiresEvidence`, `TestUnknownErrorCodeIsBounded` |
| SQLite A–H totals/groups/logs/keyset/replay and costless freshness | `TestObservableProjectionContract`, `TestCostlessGroupsRetainObservableTotals`, `TestSinkIncludesCostlessCompletion`, `TestCostlessCompletionUpdatesHealth` |
| SQLite legacy upgrade, repeated replay, truncated source | `TestReplayUpgradesLegacyProjectionOnce`, `TestReplayKeepsTruncatedTailOut` |
| Mode-B A–H, costless groups and safe errors | `TestPGObservableProjectionContract`, `TestPGCostlessGroupsAndBoundedErrors` |
| Transactional migration/repeat/rollback and corrected keys | `TestPGProjectionMigrationPreservesLegacyTotals`, `TestPGProjectionMigrationRollsBackColumnsAndBackfill`, `TestPGCorrectionRecomputesBothKeys` |
| Checkpoint-skipped history and stale epochs | `TestPGRebuildRecoversCostlessHistory`, `TestTickRacingRebuildIsFenced`, `TestGatewayAnalyticsModeBEndToEnd` |
| §6 committed typed errors, no retry, native bytes, once-only finish | `TestCommittedStreamPreservesTypedFailure`, `TestCommittedStreamNeverRetries`, `TestNativeStreamBytesRemainUnchanged`, `TestClientDisconnectFinishesOnce` |
| Missing usage, renderer, incomplete stream, error-text safety | `TestStreamMissingUsage`, `TestStreamRendererFailureRetainsLaterUsage`, `TestIncompleteStreamKeepsObservedUsage`, `TestStreamFailureDoesNotClassifyRawText` |
| §7 API serialization/authorization and actual rendered UI | `TestAnalyticsEvidenceSerialization`, `TestLogsEvidenceSerialization`, `TestAdminMux_AnalyticsEvidenceFullAdminBoundary`, `TestAnalyticsEvidenceBehavior` |
| §8/9 bilingual docs and integration | Translation/index checks, generated-report checks, docs unittest, strict MkDocs, both static builds, full race/vet/format/harness and explicit disposable-Postgres checks |

Tasks 1–4 ran the named regressions with passing local evidence. Task 2's actual
PostgreSQL 17 suite had 24/24 tests (22 database-backed, two interface/source checks)
and Mode-B end-to-end passed separately. Task 4 saved 54 synthetic browser
screenshots across English/Korean, themes and widths; this is not live model/IdP
coverage. Task 5 reran the actual 24-test PostgreSQL suite and separate Mode-B test with
zero skips. Both static builds, default full race, vet, empty gofmt, harness,
47 translation pairs/indexes, generated-report checks, 31 docs tests and strict
MkDocs passed. Exact commands/exits are in its SDD report. The default race
command reused Go cache for unchanged product packages.
Default Go checks with DB DSNs unset do not establish PostgreSQL execution.
Harness 70/70 top-level success includes 12 nested installed-Codex opt-in skips
and the expected conservative 16384-token fallback diagnostic. No paid model
experiment, live DB rebuild, deployment or production HA qualification is claimed.
The final independent branch review remains pending.
