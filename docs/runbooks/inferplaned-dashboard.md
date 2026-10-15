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
| Overview | Cost/user/token KPIs, UTC daily reported spend and model share, top team/user spenders, then distribution observations; advanced coverage notes are expandable |
| Usage & costs | Filter and sort teams, select a team for model detail, inspect separate cache tiers, export finest-granularity JSON |
| Users & costs | Reporting accounts/average, user spend and token classes, team filtering, model detail, personal budget/token actions with exact financial references |
| Budgets | Search team/user budget rules, filter daily/monthly windows, create a budget in a new or existing policy, edit limits in USD, review and publish |
| Data planes | Current generation, supported API versions, recent node observations, filter generation differences or rejections, last heartbeat, rejection details with links to fetched policies |
| Policies | Inspect all rules, guided access/rate/privacy/substitution creation, exact change summary and draft return, existing budget/rate/model-access edits, full JSON editing, explicit publish and name-confirmed deletion |

The default budget view is a comparative table of configured subject, scope,
window, USD limit and behavior. Row actions open the existing reviewed editor.
Advanced per-rule cards remain under the expandable details section. No
utilization or remaining admission balance is fabricated from reporting totals.
Primary spend KPIs use readable cent formatting for amounts ≥$1; exact microUSD
values remain in tooltips, detailed tables and exports. Positive sub-dollar
spend retains six places. Token abbreviations also retain exact tooltip counts.
Chart data comes from the separately authenticated bounded
`/v1alpha1/usage/charts` endpoint; a missing endpoint or store failure shows
unavailable, preserving the other views.

Use **Search workspace** or **Ctrl/⌘ K** to find a page, team, policy or recent
node in fetched data. Arrow keys select a result; Enter opens it; Esc closes
search. Teams open their model breakdown, policies open a filtered list, and
nodes open their recorded rejections. Search does not perform additional data
reads until the selected action needs one. Disconnect clears the results.

Overview's attention actions open the relevant inventory filter. Recorded
rejections offer **Inspect policy** when that policy exists in the fetched set.
Publish success offers **Inspect distribution**. Existing unpublished quick-edit
drafts are retained; policy filtering refuses to replace them until publish or
an explicitly confirmed refresh.

Dates are **inclusive UTC calendar dates** in the UI. The browser advances To
by one day to send the API's exclusive `until`. The initial range is month to
date. A range is applied only after Apply range or Refresh; model detail and
export use that applied range, not unsubmitted date inputs. Inventory and
policies are current observations, not filtered by the usage reporting period.
**Today**, **7 days** and **This month** apply inclusive UTC presets immediately.

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

### Guided governance rules

Open **Policies → Guided rule**. Choose model access, team throughput, sensitive
input protection or budget-threshold model substitution. Select an existing
policy to append a rule while retaining its subject and every other rule, or
create a named policy with a team and/or declared user account reference.
Rule names must be unique. Default/local supports team-only throughput. User
rate scopes require shared Postgres and explicit profile acknowledgment.
**Token limit** guides daily/monthly total quotas, also shared-only and always
FailClosed. The checkbox does not enable or attest the profile; data-plane
validation remains binding.

Privacy guides use `FailClosed` and block uninspectable input. Choose Block for
detected input, or InternalOnly with explicit internal model names. This covers
finite local detection signals, not universal detection of secrets or personal
data. Each internal target still needs configured routing, pricing and an
internal boundary on the data plane.

Substitution needs a numeric budget in the same policy. Set its reference,
threshold, requested model and substitute. Legacy substitution supports 1–99%;
**Require the substitute on every attempt** supports 1–100%. Requiring the
substitute can refuse requests when targets conflict or are unusable. Legacy
substitution serves the original in those cases. Verify the target's actual
price, access and routing; the control plane has no model catalog authority.
Independent hard caps remain binding. A second optional threshold maps the same
requested name to a different target; targets are not recursively chained.
Use the JSON editor for more than two tiers or multiple requested-model maps,
context routing, verified masking or advanced fields.

**Review rule** shows exact field differences and the complete proposed JSON.
**Edit draft** returns without losing the inputs; **Publish policy** explicitly
sends the PUT. The same summary applies to budget, quick-field and JSON changes.
Deletion retains its exact-name confirmation. Server validation, authentication
and generation observation remain authoritative.

### Setting a budget

Open **Budgets & limits → New budget**. Internal names are generated and may be
changed. Choose a team and/or user, calendar day/month window and USD limit.
For verified users, use their declared financial account reference;
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

**Users & costs → Set budget** prefills an exact declared financial reference.
An unmapped managed reporting ID requires `account_ref` from the authorized key
view; the digest is not guessed as an account. A personal action cannot silently
become a team-only policy or another declared user's policy. All-team reporting
combines spend; default/local personal counters remain per team and gateway,
while shared user-only limits can cover teams together.

The average excludes empty attribution and includes service identities. Only
declared correlations group old and managed reporting aliases; model detail
queries disjoint IDs. Exact account bytes, including significant whitespace,
are retained. The authenticated policy list optionally supplies
`identity_bindings` with reported/account refs and kind, omitting raw identity
tuples with no-store caching. This is declaration metadata, not a user registry
or live activation proof.

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
remote fonts. Theme choice is page-local. The policy-list projection adds optional
declared account correlations. No auth, readiness, policy-schema,
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
node internal/controlplane/ui/testdata/workspace.cjs
node internal/controlplane/ui/testdata/user_limits.cjs
python3 internal/controlplane/ui/testdata/browser_qa.py --screenshots /tmp/inferplane-cp-dashboard-qa
```

The browser check requires already installed Playwright and Chromium. It checks
login, token wiping, exact spend, keyboard search, attention-to-policy navigation,
guided substitution, review/back/publish, filtering, export, CRUD, rejection text
safety, CSP, degraded history, themes, mobile views/guides and disconnect.
Go tests also strictly decode and validate documents emitted by the shipped
guided-rule handlers. They require no browser or npm installation.

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
verifies that bootstrap and refresh produce ten 200 reads, including user
groupings and charts, without concurrent
signature checks. Node regressions also cover wrong state/nonce, real 401
refusal, no auth retry, PKCE cleanup and disconnecting queued requests. These
are hermetic tests, not evidence of a completed live Cognito human login.
