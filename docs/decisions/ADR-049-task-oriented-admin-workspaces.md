# ADR-049: Task-oriented admin workspaces with explicit evidence and change review

**Date:** 2026-10-13
**Status:** Accepted
**Related:** ADR-001/002 (embedded console), ADR-014 (provider registration),
ADR-027 (gateway languages), ADR-037/038 (control-plane console and policy store),
ADR-041/044 (budget substitution), ADR-048 (verified identity).

## Context

The admin experience needs to help operators finish tasks: connect a coding
assistant, find a key or model, understand consumption, investigate a policy
rejection and make a deliberate governance change. Attractive tables alone do
not close these workflows. The existing control-plane dashboard required JSON
knowledge for new non-budget rules; attention items had no action. The gateway
used a different visual system and placeholder-only forms.

The comparison baseline is LiteLLM's documented admin tasks: create keys,
track spend and add models without manually managing configuration or CRUD
endpoints. The [official admin UI quick start](https://docs.litellm.ai/docs/proxy/ui)
was inspected on 2026-10-13. This is a task baseline, not evidence of a measured
usability win or complete LiteLLM feature parity.

## Decision

Keep the dependency-free HTML/CSS/JS embedded in each binary. Use consistent
typography, spacing, color tokens, light/dark themes and responsive tables while
keeping the two workspaces truthful about their separate capabilities:

- **mayu `/admin/ui/`:** model/provider registration, team governance, scoped
  virtual-key issuance, coding-assistant setup, request logs and analytics.
  Setup actions connect provider/model → key → client. Form labels remain
  visible after typing. Search finds pages and fetched records, with local
  filters for keys, teams, providers and models. Existing en/ko/zh/ja language
  behavior remains page-local. Provider-store forms require the client-side
  full-admin indication as well as store support; the server remains authoritative.
- **inferplaned `/ui/`:** reported usage, configured budget rules, distributed
  policies and recent heartbeat observations. Attention items open the relevant
  filtered inventory; recorded rejections link to a fetched matching policy.
  Workspace search provides keyboard navigation, quick actions and record
  lookup. Reporting presets cover today, seven days and month to date.
- **Guided policy creation:** create model-access, team throughput, sensitive
  input or budget substitution rules without JSON. Append to a selected policy
  while preserving its subject and all existing rules, or create a new policy.
  Substitution references a numeric budget in the same document. Advanced
  multi-tier, masking and context configuration remains in the JSON editor.
- **Review:** show exact field differences and the complete proposed document.
  Strict substitution and sensitive-input rules include their admission impact.
  Return to the draft without losing inputs, explicitly publish, then inspect
  distribution. Review is not a server revision lock.

Do not infer financial authority from telemetry. The gateway's overview reads
settled analytics with an explicit inclusive UTC range of today and the previous
29 days. Its old lookup for the nonexistent
`inferplane_budget_spend_usd_micros` metric is removed. An unavailable analytics
response displays unavailable, not zero. The trend uses only returned days in
that range, with integer cost ratios mapped to bounded pixel coordinates; it
does not fabricate missing days. Prometheus USD counters remain approximate
observability, separate from settlement.

JSON parsing preserves unsafe plain integer literals as BigInt before any
financial formatting or document round trip. Gateway key/team USD fields accept
up to six decimal places and convert decimal text to integer microUSD; the
existing $1 billion input ceiling remains. Policy budget fields retain their
three-place milliUSD contract. Neither workflow rounds an invalid amount into
a different cap.

## Boundaries and consequences

No API, policy schema, authorization rule, store profile, financial authority,
upstream transport or inference-path behavior changes. The public assets remain
data-free. Protected reads follow authentication; no token, key plaintext,
subject, policy or search state is persisted. Search excludes shown-once key
plaintext and client setup snippets. Strict CSP stays in force, without inline
styles/handlers or external assets. Disconnect clears search and drafts.

The control plane does not become a fleet key registry or provider manager.
Generation comparisons and recorded rejections do not prove readiness, durable
credit, database health or HA. Default/local, node-local monetary authority and
shared Postgres keep their existing availability contracts. UI guidance cannot
attest a substitute's cost, quality, access or privacy boundary.

Guides create a bounded initial rule set. Numeric targets and policy semantics
are checked through the existing server contract; data-plane topology and
readiness still require runtime validation. The throughput guide refuses user
subjects because the local policy channel does not implement user rate shares.
Privacy guides block uninspectable input and use FailClosed. They make no
universal sensitive-data detection claim.

## Verification

Node checks execute shipped handlers at DOM/fetch boundaries. Go checks strictly
decode and validate the policy documents those same handlers emit, including
privacy and strict substitution. Monetary checks cover unsafe int64 responses,
microUSD inputs, pixel ratios and rejected excess precision. Existing provider
metadata and alias round-trip tests remain.

Optional Chromium checks use synthetic loopback APIs only. They cover both
workspaces, keyboard search, review/back/publish, rejection-to-policy navigation,
key issuance, provider/model preservation, read-only sessions, plaintext
exclusion, exact costs, degraded/unavailable data, CSP, gateway Korean,
light/dark themes and 390/768/1440px widths. These are functional and visual
checks; they are not a human usability study, live SSO certification or fleet
qualification.

See the [dashboard runbook](../runbooks/inferplaned-dashboard.md) and
[admin UX acceptance guide](../ux/admin-workspaces.md).
