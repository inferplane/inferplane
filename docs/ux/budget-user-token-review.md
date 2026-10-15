# Budget, user-cost and token-policy UX review

Reference: [AWS internal LLM Gateway sample](https://aws.amazon.com/ko/blogs/tech/llmgateway-on-amazon-bedrock/). The article and its budget/dashboard images were inspected on 2026-10-13.

The previous console had a budget form and team usage, but the requested tasks did not connect clearly. User-cost APIs were absent from the UI, as were daily budget inputs and daily/monthly token-total guides. The following flows fill those gaps.

| Task | Previous behavior | Current flow |
| --- | --- | --- |
| Team/key budgets | Monthly input with ambiguous period labels | Separate daily/monthly USD input, inspection and editing in mayu |
| Personal budgets | Required knowing account references and policy names | CP user row's **Set budget** → account selected → amount/period → review/publish |
| Cost per user | API only | **Users & costs** shows spend, token classes and models; overview shows reporting accounts and average cost |
| Daily team tokens | Fractional inputs were truncated with parseInt | mayu team form validates whole-token daily limits |
| Monthly/daily user tokens | Full JSON editing | User row's **Token limit** → period/total → shared-profile acknowledgement → review/publish |
| TPM/RPM | Users had to distinguish throughput from totals | Separate per-minute throughput and calendar-total labels/cards |
| Model substitution | One guided threshold | Two thresholds and a visual requested-model → target mapping |

## Budget settings

The key and team forms at mayu `/admin/ui/` distinguish **Daily budget** and **Monthly budget**. USD input accepts up to six fractional digits and converts to integer microUSD. Empty/zero values add no limit in that scope; independent stricter limits still apply. Existing daily team budgets are loaded and preserved alongside monthly limits.

At CP `/ui/`, **Users & costs → Set budget** creates a personal budget. Generated names remove the need to invent technical policy identifiers first. Declared identity bindings prefill the exact `account_ref`. An unbound managed reporting ID may be an audit/reporting digest, so the UI does not guess its financial account. Select an explicit `account_ref` from an authorized key view. A different user's policy or a team-only policy cannot replace the personal target.

mayu key issuance also offers registered human and service accounts. This needs a verified-identity declaration and full-admin authority, with final validation by the existing server. Human issuer/subject are not constructed from browser input.

## User costs

The view shows spend, total tokens, input, output, cache reads and 5m/1h writes. Operators can filter teams, search/sort users and inspect a user's model detail. Output already contains reasoning tokens; each token class is counted once.

Average cost divides attributed reporting spend by the number of attributed accounts. Service identities may be included. Empty attribution is displayed separately and excluded from the average. Declared historical/current reporting aliases are grouped by financial account without changing accounting rows or authority. Monetary values remain integers.

The view is not a user pool, employee count, AWS invoice or remaining-credit ledger. It covers retained reports, which may arrive late. Query failures, incomplete fallback and missing attribution are distinguished. Because an empty API user filter selects all users, unattributed rows cannot open model queries or personal-limit actions.

## Token policies and profiles

- **TPM**: per-minute token throughput.
- **RPM**: per-minute request throughput.
- **Daily team tokens**: the default/local gateway team's daily token limit.
- **Token quota**: a shared Postgres daily/monthly total for a user or team, always `FailClosed`.

Per-user RPM/TPM and tokenQuota require shared Postgres. The form requires acknowledging this dependency; a checkbox cannot activate the profile or prove readiness. Data planes validate support and report refusals. Pending token reservations can refuse new requests before settled usage reaches the limit.

Default/local personal budget counters remain per team and gateway. Node-local money and shared profiles keep their separate contracts. Shared DB loss refuses new admission. Combined all-team reporting spend is not a local counter scope.

## Differences from the sample

| AWS sample feature | inferplane behavior |
| --- | --- |
| HARD_BLOCK | Hard-cap blocking; independent hard caps also apply |
| SOFT_WARNING with a 110% ceiling | Not directly supported; a soft threshold does not create a 110% blocking cap |
| Budget-driven 50% RPM THROTTLE | Automatic budget-linked RPM reduction is not implemented |
| CPM/CPH spend-velocity limits | Current rate forms use RPM/TPM; money limits are daily/monthly |
| Always refuse if no team budget is set | Existing default/local zero/empty semantics remain |
| Utilization and balance | Telemetry does not fabricate remaining admission credit |
| Downgrade chains | Threshold-specific requested-model → target mappings, not recursive chains |

## Reproduce

```bash
go test ./internal/controlplane ./internal/controlplane/ui ./internal/server/adminui -race
node internal/controlplane/ui/testdata/user_limits.cjs
node internal/server/adminui/testdata/routing_forms.cjs --money
python3 internal/controlplane/ui/testdata/browser_qa.py --screenshots /tmp/inferplane-budget-user-token-ux
python3 internal/server/adminui/testdata/browser_qa.py --screenshots /tmp/inferplane-daily-budget-ux
```

Browser tests use installed Playwright/Chromium and synthetic local APIs, with no real AWS, IdP, model or key. They check profile-specific policy acceptance and refusal, read permissions, exact account references, review-before-write, numeric bounds, stale replies, mobile/themes and disconnect. Human task times and error-rate comparisons require a separate evaluation.
