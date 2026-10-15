# ADR-051: Cost-first dashboard and comparative budget table

**Date:** 2026-10-14
**Status:** Accepted
**Related:** ADR-049/050 (admin workspaces and personal limits), ADR-036 (telemetry).

## Context and reference

The user preferred the actual UI of
`aws-samples/sample-agentic-ai-acceleration-kr/projects/awsome-ai-gateway`.
The admin-ui tree `e4d6e15834ecfc4ae66a37888e1f2387dbc46d1a` was inspected,
including dashboard, budget summary/dialog, rankings, theme and navigation
components. The useful patterns are grouped KPIs, trend/distribution charts,
ranked spenders and a single comparative budget table.

Apply those information-design patterns with original dependency-free HTML,
CSS and JavaScript. No source component, external font, framework or unsupported
sample policy is copied into inferplane.

## Decision

Lead Overview with spend, average attributed-user cost, tokens and reporting
accounts. Follow with UTC daily spend and model distribution, team/user rankings,
then distribution observations. Put detailed accounting qualifications in an
expandable coverage section while retaining unavailable/degraded indications.
Replace marketing headings with concise page titles. Use a quieter teal/slate
palette, compact cards and a table for team/user configured budget limits;
advanced rule cards remain available in an expandable section.

Primary monetary KPIs use integer round-half-even cent formatting for values
at least $1, with exact microUSD in tooltips. Positive sub-dollar values retain
six places. Tables and exports retain exact financial values. Large token totals
use bounded integer-derived K/M/B formatting, with exact counts in tooltips.
Disconnect and unavailable replies clear both visible values and those tooltips.

Add authenticated `GET /v1alpha1/usage/charts?since=…&until=…` as a read-side
rollup over the existing retained telemetry windows. Both bounds are required;
the exclusive-end range must be positive and at most 366 days. The response
contains integer `total_micro_usd`, sorted `days: [{day, spent_micro_usd}]`,
the top eight `models: [{key, spent_micro_usd}]`, `other_micro_usd` and
`model_count`. UTC dates use WindowStart. No user/team/dataplane identifier is
added to this chart DTO. Success is no-store. A cursor/store failure is 503,
never a successful zero/partial chart or implicit memory fallback.

Charts use native SVG node APIs and static CSS classes under the existing CSP.
Only bounded pixel/share ratios cross to Number; monetary arithmetic stays
integer. Missing dates are not invented as measurements. Empty and unavailable
history remain distinct. A deployment without the new endpoint keeps the other
dashboard data and displays chart unavailability.

For an older CP, a 404 on the new route selects its existing authenticated JSON
export for the same dates. Client-side integer rollup is bounded to 8 MiB and
100,000 rows. Data stays in the signed-in page, not deployment-tool files.
Auth/store errors never trigger that path. This supports static releases
without restarting memory-only financial/telemetry services; it does not enable
policy writes or replace the existing profile contracts.

## Boundaries and verification

The new endpoint does not change settlement, financial admission, quotas,
policy delivery, identity or authorization. Reported spend is not admission
credit or an AWS invoice. Budget rows display configured rule limits; they do
not derive a fake utilization/remaining-credit percentage from unrelated
reporting windows. No user registry or enterprise role capability is implied.

Exact rollup tests cross the JavaScript safe-number boundary with independently
valid ingestion windows. Auth, invalid/unbounded ranges and store refusal are
tested. Existing policy, user-limit, cleanup and real JWT/JWKS checks remain;
bootstrap/refresh now make ten serialized protected reads including chart data.
Chromium checks chart rendering, the visible budget table, edit/review/publish,
CSP, dark/light and 390/768/1440px layouts. Repository build/race/vet/format and
harness checks remain mandatory.
