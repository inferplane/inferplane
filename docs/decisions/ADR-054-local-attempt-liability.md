# ADR-054: Local monetary liability per provider attempt

**Date:** 2026-10-26
**Status:** Accepted; implemented locally. Deployment qualification remains open.
**Related:** ADR-030 (pricing), ADR-034 (legacy leases), ADR-041 (tiers), ADR-045/046 (durable/shared authority), ADR-053 (cost evidence).

## Context

Local admission checked spend before dispatch and debited afterward. Overlapping
requests could each pass the same cap, multiplying the documented one-request
overshoot. Streams without final usage could complete without a monetary debit.
Inventing a token count or treating a conservative bound as observed spend would
corrupt chargeback evidence.

## Decision

Each billable provider attempt captures applicable team, key and user day/month
money scopes through the existing request-policy reservation seam. Under one
process mutex, blocking scopes check observed spend plus outstanding liability.
A request may start below its cap; its full verified price/context bound then
prevents overlapping attempts from multiplying that overshoot. A smaller soft
threshold cannot replace an independent hard limit. Soft scopes still meter
known cost for routing thresholds.

If finite price/context metadata is unavailable, the attempt claims the remaining
local cap. Proven complete priced usage debits captured scopes before releasing
the hold. Missing or incomplete observations retain the hold; independently
priceable observed categories are recorded without claiming unknown categories
are zero. Malformed usage or unpriceable completion freezes its blocking scopes
until their calendar windows end. A refused, never-dispatched request returns
its token-rate reservation. Refusing a later retry cannot refund an earlier
dispatch.

Liability is separate from spend and spend metrics. Local `/v1/usage` exposes
optional `reserved_usd_micros` and `accounting_uncertain`; available money accounts
for holds and is zero for frozen accounting. Per-attempt finalization remains
exactly once even when the downstream disconnects or a retry follows.

## Consequences and limits

This is process-local protection. Restart still loses local counters and
liability; it does not provide fleet escrow or change the legacy lease's global
durability limits. Use ADR-045/046 for those contracts. An interrupted request
can reduce usable local credit for the remainder of its window. Wide context
bounds can limit concurrency even when actual requests are small. Bounds depend
on validated configured prices, model metadata and upstream enforcement; they
do not attest an external invoice, optional provider surcharges or model quality.

Durable and shared authority keep their existing complete-usage/refund rules.
Unknown actual cost is never fabricated to make a report balance.

## Verification

Regression tests cover overlapping attempts, missing and partial usage, invalid
categories, independent key-day caps, no reservation on rejected scopes, known
cost across retries and undispatched TPM rollback. Integration checks cover all
four ingresses, with race and disposable PostgreSQL tests.
