# Control-plane operations dashboard

## Entry point and scope

Open `/ui/` on **inferplaned**, not mayu's `/admin/ui/`. The console is embedded
in the static Go binary; there is no npm build or external asset dependency.
It controls distributed GovernancePolicy documents and reads usage and heartbeat
observations. It does not administer virtual keys, configure upstream models,
carry inference, or enable OTel export.

Sign in with [console SSO](inferplaned-console-sso.md), or enter
`INFERPLANED_TOKEN` as the read credential. An empty credential is only for a
server configured for loopback development. A failed authentication does not
unlock the workspace. Disconnect clears credentials, fetched data and drafts,
and fences late responses. It does not end the IdP session.

## Workspace views

| View | Evidence and actions |
| --- | --- |
| Overview | Reported spend, teams with usage (not registered teams), input/output/cache tokens, recent nodes, top five teams by spend, generation differences and recorded rejections |
| Usage & costs | Filter and sort teams, select a team for model detail, inspect separate cache tiers, export finest-granularity JSON |
| Budgets | Search team/user budget rules, filter daily/monthly windows, create a budget in a new or existing policy, edit limits in USD, review and publish |
| Data planes | Current generation, supported API versions, recent node observations, generation comparisons, last heartbeat, recorded rejection details |
| Policies | Inspect all rules, filter by policy/subject, edit existing budget/rate/model-access fields, create or edit a whole JSON document, review before publishing, type the exact name before deleting |

Dates are **inclusive UTC calendar dates** in the UI. The browser advances To
by one day to send the API's exclusive `until`. The initial range is month to
date. A range is applied only after Apply range or Refresh; model detail and
export use that applied range, not unsubmitted date inputs. Inventory and
policies are current observations, not filtered by the usage reporting period.

The JSON export includes **all teams** in the selected period; search and sort
controls do not change its scope. It uses `/v1alpha1/usage/export` with explicit
bounds, not the displayed aggregate table. A store failure or incomplete JSON
stream is not converted into a successful download. Export currently buffers
in the browser; narrow the range for very large datasets.

## Reading spend and distribution honestly

- Monetary values stay integer microUSD through JSON parsing, aggregation,
  formatting and export, including int64 values above JavaScript's safe-number
  range. All six decimal places are displayed; no floating-point cost math.
- Total tokens add input, output, cache read, 5-minute writes and 1-hour writes
  exactly once. Output already includes reasoning. This is reported aggregate
  consumption, not per-request context occupancy or an AWS invoice.
- A degraded usage reply explicitly means incomplete durable history and
  recent memory fallback. Missing/unavailable APIs are shown as unavailable,
  not zero spend or a healthy empty fleet. Telemetry-only deployments still
  work when fleet/policy endpoints are not mounted.
- The data-plane API omits nodes absent for more than **10 minutes**. The
  dashboard cannot identify omitted nodes or claim they are offline. A matching
  generation is only a comparison of reported identifiers, not readiness,
  admission authority, database health or fleet HA. Rejections are a bounded
  recorded history and may no longer describe a current failure.
- Refresh is manual. Each snapshot shows its fetch time. API reads are separate
  requests, not a transactional fleet snapshot. There is no live budget ledger,
  lease revocation, node restart or node-wide financial authority control here.

## Policy write authority and review

### Setting a budget

Open **Budgets → New budget**. Choose **Create a new policy**, enter its name
and a team and/or user, then enter a budget name, calendar day/month window and
USD limit. For verified users, enter their declared financial account reference;
the console does not derive an identity from an email or audit digest. Selecting
an existing policy adds a rule without changing that policy's subject. Rule
names must be unique within the selected policy. To set both daily and monthly
caps, add two rules; both apply independently alongside other matching caps.

Amounts accept up to three decimal places, matching the policy's milliUSD
precision. Decimal text is converted with integer arithmetic: `$12.345` becomes
`limitMilliUSD: 12345`. Zero, negatives, exponent notation, excess decimal places
and values that would overflow internal microUSD are rejected before review.
New budgets default to a hard cap with `FailClosed`. Unchecking hard cap creates
a soft threshold; editing an existing soft budget retains its failure policy.

**Review budget** shows the complete resulting policy; **Publish policy** sends
the existing policy PUT and reloads the list. **Edit budget** preserves all other
rules, lease settings, unedited defaults and additional fields. Enabling hard cap
forces `FailClosed`. Unlimited declarations and advanced rule/lease changes use
the full document editor in **Policies**. Budgets displays configured limits,
not remaining admission credit or utilization inferred from reported spend.

When **New budget** is disabled, the access message distinguishes an unavailable
API, a file-authoritative store, and a missing write credential. **Policy access
& advanced settings** opens the credential controls. A write token cannot make
a file-authoritative deployment writable.

`writable` means a policy store is attached, **not that the reader may write**.
For static-token deployments, enter the separate
`INFERPLANED_POLICY_WRITE_TOKEN` in Policy authority. It is selected in memory,
not validated by a speculative mutation. SSO uses the console OIDC credential.
Only the existing server middleware decides write permission; the heartbeat
read token never acquires write authority. A refused publish keeps the review
open so the operator can retry or cancel.

Quick edits clone the complete fetched document. Each budget rule is edited
separately, so daily and monthly caps coexist; omitted defaults, period,
unknown/unrendered fields and other rules are retained. Existing FailClosed is
never weakened by a normal form save; enabling a hard cap forces FailClosed.
Explicit unlimited declarations require the full-document editor to change.
New numeric quick-edit values must be nonnegative safe integers. The document
editor retains int64 integer precision (plain JSON integer literals; no decimal or
exponent notation) and sends all semantics to server
validation, including hard-cap/failure-policy compatibility and identity binding.

Publishing is a two-step edit/review then explicit PUT. Delete requires typing
the exact policy name, then an explicit DELETE. There is **no server-side
revision lock**: coordinate concurrent editors; review is not compare-and-swap.
Publishing persists and hot-applies the document set on the CP, then nodes pull
on heartbeat. Verify generation coverage and rejections afterward. Reload after
a successful mutation discards other quick-edit drafts, as stated in review.
A file-authoritative deployment remains read-only; enable
`INFERPLANED_POLICY_DSN` or edit the `--policies` files and redeploy.

## Security and verification

The public shell contains no fleet, team, spend or credentials. Protected reads
start only on connection. Read and write tokens are separate closure variables;
no credential is persisted. Only the three short-lived PKCE session values from
ADR-037 survive the SSO redirect. Requests are same-origin, refuse redirects and
omit ambient cookies. Rendering uses DOM text, not API-derived HTML. Strict
`default-src 'self'` CSP is unchanged, including no inline scripts/styles or
remote fonts. Theme choice is page-local. No Go API, auth, readiness, policy,
lease, money-authority, count-token or routing contract is changed.

Source: `internal/controlplane/ui/static/{index.html,style.css,app.js}`.
The optional [CloudFront asset deployment](inferplaned-cloudfront.md) publishes
these data-free files separately without restarting the CP or changing its APIs.
The handler and standard-library-only Node behavioral checks live alongside
these assets. Optional real-browser QA uses only synthetic local APIs and does
not contact an installed service or AWS:

```bash
go test ./internal/controlplane/ui ./internal/controlplane -race
node internal/controlplane/ui/testdata/dashboard.cjs
node internal/controlplane/ui/testdata/budgets.cjs
python3 internal/controlplane/ui/testdata/browser_qa.py --screenshots /tmp/inferplane-cp-dashboard-qa
```

The browser check requires already installed Playwright and Chromium. It checks
login, token wiping, exact spend, navigation, filtering, export, policy CRUD and
review, rejection text safety, CSP, degraded history, themes, mobile width and
disconnect. Go tests do not require a browser or npm installation.

## SSO request scheduling

The console queues OIDC-authenticated API reads and writes, including bootstrap,
refresh, model detail and export. The currently deployed CP's JWKS miss limiter
refuses a second signature check while another is in flight. The previous
parallel three-query bootstrap could therefore turn a valid sign-in into a 401.
Queueing avoids the console's own fan-out without relaxing JWT validation,
changing Cognito/group settings, retrying denied requests, or restarting the CP.
Disconnect aborts the in-flight request and fences queued requests from the old
session. Static bearer requests remain independent. Server-side contention
between separate tabs/clients is still possible; this is a client mitigation,
not a redesign of the global verifier. Never share an ID token for diagnosis.

`TestSSOBootstrapWithColdRealVerifier` drives the shipped SSO callback and reads
through real Go signature/JWKS/group checks with a slow, local fake IdP. It
verifies that bootstrap and refresh produce six 200 reads without concurrent
signature checks. Node regressions also cover wrong state/nonce, real 401
refusal, no auth retry, PKCE cleanup and disconnecting queued requests. These
are hermetic tests, not evidence of a completed live Cognito human login.
