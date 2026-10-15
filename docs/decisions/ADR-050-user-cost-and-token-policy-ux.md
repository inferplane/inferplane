# ADR-050: User cost visibility and profile-aware budget/token policy guides

**Date:** 2026-10-13
**Status:** Accepted
**Related:** ADR-049 (admin workspaces), ADR-036 (usage telemetry),
ADR-041/042 (budget routing and windows), ADR-046 (shared governance),
ADR-048 (verified account references).

## Context

The [AWS LLM Gateway article](https://aws.amazon.com/ko/blogs/tech/llmgateway-on-amazon-bedrock/)
shows team/user budget tables, per-user cost indicators, token consumption and
budget-triggered model substitutions together. Inspecting our existing
implementation against those operator tasks exposed three gaps: the CP user
query existed but was absent from the console, daily key/team budget fields
were absent from the gateway forms, and a token-total policy required JSON.

The two implementations have different governance semantics. Reproducing a
screen must not invent enforcement or make reported telemetry look like
remaining financial admission credit.

## Decision

1. Add CP **Users & costs** with reported spend, total tokens and separate token
   classes, team filtering, sorting, search and a user-model breakdown. Overview
   shows attributed reporting-account count and average spend. The average
   excludes empty attribution, includes service identities and uses integer
   microUSD with round-half-even display. These are retained reported
   settlements, not an employee registry or an AWS invoice.
2. Add direct **Set budget** and **Token limit** actions to an attributed user.
   Generate internal policy/rule names; the operator chooses the amount or
   count and window, reviews the full document, then publishes. Existing policy
   subjects, rules, metadata and omitted defaults are retained during edits.
   Anonymous rows never issue an empty API user filter, because that filter
   means all users.
3. Expose optional declared identity correlations on the already-authorized
   policy list. `identity_bindings` contains only `reported_ref`, `account_ref`
   and `kind`; no raw issuer, subject or IdP groups. Send `Cache-Control:
   no-store`. No declaration means the field is omitted. This is declaration
   metadata, not a user registry or proof of live identity activation.
4. Use those explicit correlations to group a legacy and managed reporting
   alias for the same declared financial account. The model breakdown queries
   disjoint exact reporting IDs. Policy and key selectors use the **exact**
   financial account reference, retaining significant whitespace. No financial
   accounts, counters, windows, grants, permits or historical rows are merged,
   split, rewritten or refunded. An unbound managed reporting ID requires an
   explicit account reference instead of guessing an account from its digest.
5. Guide creation and editing of `tokenQuota` for CalendarDay/CalendarMonth,
   and user-scoped RPM/TPM for shared governance. Acknowledge the actual shared
   Postgres deployment before reviewing a shared-only rule. The checkbox does
   not enable or attest a profile; data-plane validation remains binding and
   the default/local profile refuses these rules. Quotas use FailClosed and
   positive int64 token counts, without parseInt truncation or float arithmetic.
6. Display configured throughput, quota and budget substitution rules beside
   budgets. Guide up to two substitution thresholds for a single requested
   model and visualize existing thresholds. Each threshold maps the original
   requested name to a target; this is not recursive target-to-target routing.
   Advanced maps stay in the whole-document editor. Names do not attest price.
7. Add explicit daily/monthly key/team USD fields to mayu and preserve both in
   edits. Keep the zero/blank "no additional cap" semantics visible. Reject
   fractional/exponent token or request counts rather than silently truncating
   them. Add full-admin registered-account and service-account selectors;
   their existing server permissions and identity declarations still decide
   issuance.

## Boundaries

No inference-path, pricing, monetary authority, rate/token admission, lease,
identity verification or authorization algorithm changes. The policy-list
projection is additive and retains its whole-console read authorization;
fine-grained organization/team capabilities remain open. Static assets carry
no user data; all data/credentials stay page-local and clear on disconnect.
OIDC reads/writes remain queued, including the added user grouping query.

All matching caps continue to apply. Default/local personal counters are per
team and gateway; node-local monetary authority and shared Postgres retain
their separate contracts. Shared mode requires its database for admission.
Combined all-team telemetry does not establish global remaining credit,
readiness, database health or fleet availability.

The AWS sample's automatic RPM THROTTLE, soft-warning 110% grace cap, CPM/CPH
and missing-team-budget denial are not silently emulated by our forms.
Our soft accounting threshold does not acquire an additional 110% hard stop.

## Verification

Shipped Node event handlers exercise averages, attribution exclusions, declared
alias correlation, exact financial selectors, quick budget/token actions,
create/edit preservation, profile acknowledgment, multiple thresholds, int64
limits, stale reads and disconnect. Tests start unsuccessful and explicitly
mark completion, so an unresolved promise cannot exit as a passing suite.

Generated documents pass the real strict wire decoder and semantic validator.
Shared-only documents are accepted by a synthetic shared policy store and
rejected by the default store; this is a contract check, not DB/HA qualification.
Authorization tests refuse unauthorized binding reads and assert the omission
of raw identity tuples. The real JWT/JWKS bootstrap/refresh fixture covers all
eight queued reads.

Chromium QA uses only synthetic local services to cover user costs → personal
budget/quota → review → publish, registered key issuance, daily budget/token
edits, themes and 390/768/1440px views. Repository build, race, vet, formatting,
harness and documentation checks remain required.

See [the AWS comparison and task audit](../ux/budget-user-token-review.md).
