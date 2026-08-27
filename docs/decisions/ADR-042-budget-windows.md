# ADR-042: Calendar budget windows through the policy and lease channel

**Date:** 2026-08-27
**Status:** Accepted (implemented)
**Related:** ADR-032 (policy units/subjects/cadence — EXTENDED: it fixed the
money unit, the subjects, and the lease cadences, but left the budget window
implicitly monthly), ADR-034 (control-plane distribution and leases —
EXTENDED: the grant/report/clamp cycle it defined is now per window), ADR-033
(local policy file channel — UNCHANGED: its enforceability gate still rejects
every user-subject budget/rate rule, and this ADR amends nothing about it),
ADR-039 (explicit `unlimited` rules — an unlimited rule stays window-agnostic)

## Context

ADR-032 fixed a GovernancePolicy budget rule's money unit, its subjects, and
its lease cadences — but never said *when the counter resets*. Every budget
rule implicitly capped the calendar month, anchored to UTC. Meanwhile the data
plane's own base layer (config and keystore, `budget_usd_micros_per_day`)
already enforced a calendar-day money cap, and `Governor` already ran a day
window beside the month — anchored to the operator's `budget_timezone`. So
the two channels disagreed twice over: the policy/CRD/lease channel could not
express "$50/day" at all, and the one window it could express reset at UTC
midnight on the 1st while the day window reset at the operator's midnight.

Closing the first gap without closing the second would have made it worse: a
policy-driven daily cap on Seoul midnight and a policy-driven monthly cap on
UTC midnight puts two different boundaries into one audit and billing
reconciliation.

The lease protocol (ADR-034) was also window-blind, and that is where the
sharpest trap in this change lived — see Decision 4.

## Decision

**Decision 1 — money caps get an operator-timezone CALENDAR anchor, not a
rolling window.** A budget window resets at the operator's midnight
(`CalendarDay`) or at the first instant of the month (`CalendarMonth`), never
at "24 hours after the first request." This deliberately diverges from
`tokens_per_day`, which STAYS a rolling 24h window
(`limiter.CheckQuota(..., 24*time.Hour)`), and the divergence is the point: a
token quota is a load-shaping control, where a rolling window is harmless and
even preferable (no thundering reset at midnight); a money cap is an
accounting control that has to line up with a billing period — "resets on the
1st" is what a monthly cap *means* to whoever reconciles the invoice. The
inconsistency between `tokens_per_day` and the money windows is intentional.
Do not "fix" it by making the money windows rolling or the token quota
calendar-anchored.

**Decision 2 — a per-rule `period` ENUM, not a second limit field on one
rule.** `api/v1alpha1.BudgetRule` gains `period: CalendarDay |
CalendarMonth`; empty means `CalendarMonth`, the window every budget rule
enforced before the field existed. The reason it is per rule is structural:
`hardCap`, `failurePolicy`, `lease` and `adminContact` are all per-rule
knobs, so a single rule with two limit fields could never express "day =
soft, month = hard"; and the control plane's lease ledger is keyed
`ruleKey{policy, rule}`, so one rule cannot hold two independent leases. A
day cap and a month cap are therefore two entries in `spec.rules`. The enum
is purely additive — an omitted `period` leaves every pre-existing document,
CRD object and Helm values file meaning exactly what it meant, and it must
not be combined with `unlimited: true` ("no cap" is a statement about the
budget dimension as a whole and has no window; an unlimited rule can never
unlimit the day while leaving the month capped).

**Decision 3 — both calendar windows share ONE operator timezone.**
`budget.CalendarMonthIn(loc)` mirrors `CalendarDayIn(loc)`, and
`Governor.SetBudgetTimezone` (config `budget_timezone`) now anchors BOTH
windows, so audit and billing reconciliation never straddle two anchors. The
default stays UTC — byte-identical to the previous behaviour for every
deployment that never set the knob. This is not a store-key migration:
`Window.Tag()` ignores `Loc`, so a month key built in Seoul and one built in
UTC are the same key — switching the operator timezone moves a counter's
boundary, never its identity.

**Decision 4 — per-window lease grant, consumption report, and clamp.**
`LeaseGrant` and `ConsumptionReport` carry the rule's `period` (appended
last, `omitempty` — an empty value on either side reads as `CalendarMonth`,
which is what every grant and report meant before windows existed). The
control plane's ledger row carries its rule's period and stamps it on each
grant; the data plane keys its `LeaseTable` by `(team, period)`, clamps each
window's local limit against its OWN lease, and `Syncer.SpentOf(team,
period)` reports each rule's spend in that rule's own window. The trap this
closed is the most instructive part of the change: a window-blind `SpentOf`
reported MONTHLY spend against a daily rule's ledger row, and since the
control plane computes `remaining = limit − reportedSpend`, a `$50/day` rule
would have been starved to a zero grant within hours of the month's spend
passing $50 — a correctly-written policy silently degrading to "blocked for
the rest of the month." A daily allowance and a monthly allowance are not
comparable quantities and are never merged into one minimum; the hard-cap
lease gate stays team-wide across windows (either window's expired or
exhausted hard lease blocks, and its 402 reason strings are byte-identical to
before — a month-only deployment's error body does not change).

The window work is deliberately independent of subject scope: `user`-subject
budget/rate rules remain rejected by the ADR-033 enforceability gate, exactly
as before this ADR.

## Consequences

### Positive
- A policy document (and therefore the CRD and the control-plane channel) can
  now express the daily money cap the config/keystore layer has had all
  along — one governance vocabulary across channels, including "day = soft
  warn, month = hard cap" as two rules.
- One timezone anchors every calendar money boundary in a deployment, so a
  spend report, the audit chain, and the provider invoice slice time the same
  way.
- Back-compat is total: omitted `period`, an old control plane's grants, and
  an old data plane's reports all read as `CalendarMonth`, and the UTC
  default is unchanged.

### Negative
- A daily rule exercises the control plane's window-rollover path ~30× more
  often than a monthly one (see the accepted limitation below).
- `governance.TeamPolicy` still carries ONE `adminContact` hint for both
  windows: the month rule's wins and the day rule's is the fallback, so a
  deployment with different contacts per window surfaces only one of them in
  the 402 body.

### Accepted limitation (roadmap ②, explicitly not claimed here)
The control plane still has no first-class notion of a window boundary: a
rollover is inferred by decrease-detection (a cumulative consumption report
that goes DOWN means the data plane's window rolled or its process
restarted), and `pruneAfter` — the point at which a silent data plane's
ledger rows are forgotten — is 24h, which is exactly one daily window. A
daily rule therefore inherits roadmap item ②'s window-boundary approximation
on a daily cycle instead of a monthly one: the global limit is approximate at
each window edge, and a dead proxy's day-window spend is forgotten on the
same timescale the window itself turns over. The fix — a
control-plane-computed `windowID` on each grant and a durable ledger keyed by
it — is roadmap item ②'s scope and is explicitly NOT claimed by this ADR.
