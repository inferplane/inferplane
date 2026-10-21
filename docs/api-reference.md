# API Reference

inferplane exposes two HTTP planes: the **data plane** (`:8080`, client traffic) and
the **admin plane** (`:9090`, operations). Generation and model discovery authenticate
with a virtual key (`ik_...`). Count and opt-in login bootstrap endpoints have the
specific exceptions documented below.

## Data Plane (`:8080`)

### Authentication
Send the virtual key the way the client protocol expects:
- Anthropic ingress: `x-api-key: ik_...` (Claude Code sends this when `ANTHROPIC_API_KEY=ik_...`).
- OpenAI ingress: `Authorization: Bearer ik_...`.

The gateway resolves the key to a `Principal` (team + allowed models) and never forwards
the client key upstream; it injects the upstream provider credential itself.

### Anthropic ingress

```
POST /v1/messages
POST /v1/messages/count_tokens
GET  /v1/models
```

| Endpoint | Notes |
|----------|-------|
| `POST /v1/messages` | Messages API; streaming via SSE when `"stream": true`. Body forwarded verbatim to an Anthropic-protocol upstream. |
| `POST /v1/messages/count_tokens` | Token counting. **Always returns 200** (a non-200 crashes Claude Code). |
| `GET /v1/models` | Lists models the principal may use (negotiated by `anthropic-version`). |

### OpenAI ingress

```
POST /v1/chat/completions
POST /v1/responses
GET  /v1/models
```

| Endpoint | Notes |
|----------|-------|
| `POST /v1/chat/completions` | Chat Completions; streaming via SSE when `"stream": true`. Converted via the canonical schema when the upstream protocol differs. |
| `POST /v1/responses` | Native Responses or supported stateless text/tool bridging; see Responses and adaptive routing below. |
| `GET /v1/models` | Lists models the principal may use. |

### Bedrock-shaped ingress

```text
POST /model/{modelId}/invoke
POST /model/{modelId}/invoke-with-response-stream
POST /model/{modelId}/count-tokens
```

These gateway routes use virtual-key authentication (`x-api-key` or Bearer),
not client AWS SigV4 identity. URL-encode the configured model identifier.
Generation uses the common admission/routing controls; streaming uses the
Bedrock response shape. Count requests retain the local HTTP-200 contract
when authentication, readiness, routing or storage prevents generation.
Provider credentials and AWS signing are applied separately on egress.

### Usage

`GET /v1/usage` returns the authenticated principal's usage view. Shared mode
includes `enforcement_mode: shared` and subject-scoped `shared_limits`; `used`
includes reservations/uncertainty, and `reserved` identifies the retained
portion. This is separate from control-plane telemetry ingestion.

### CLI login (opt-in, ADR-028)

Only mounted when `oidc.cli_login.enabled` is set — 404 otherwise. Lets
`mayu login` mint a short-lived virtual key instead of a human copying
one out of the console. See [docs/runbooks/cli-login.md](runbooks/cli-login.md).

```
GET    /v1/auth/config   # unauthenticated; {cli, issuer?, client_id?}
POST   /v1/auth/key      # Authorization: Bearer <IdP ID token>; {"team"?: "..."} -> {key, key_id, team, expires_at}
DELETE /v1/auth/key      # x-api-key: <the minted key>; self-revoke, used by `mayu logout`
```

`expires_at`/`owner` are always server-decided — a client cannot request a
longer-lived key.

## Admin Plane (`:9090`)

### Unauthenticated
```
GET /healthz      # liveness
GET /readyz       # readiness
GET /metrics      # Prometheus exposition (no secret/key_id labels)
```

### Token-authenticated (`/admin/keys`)
Authenticate with the admin token (`Authorization: Bearer <INFERPLANE_ADMIN_TOKEN>`).

```
POST   /admin/keys        # issue a virtual key (plaintext returned once)
GET    /admin/keys        # list key metadata (never plaintext)
DELETE /admin/keys/{id}   # revoke a key
```

### Analytics outcome and cost evidence (ADR-053)

These existing routes require **full-admin** authorization. No token gives 401;
an authenticated team-mapped non-admin receives 403. The static console remains
data-free and public. When analytics is disabled, these routes are unavailable.

```text
GET  /admin/analytics/summary?since=2026-10-21&until=2026-10-21
GET  /admin/analytics/timeseries?since=2026-10-21&until=2026-10-21
GET  /admin/logs?limit=200&before=<event-id>
GET  /admin/analytics/health
POST /admin/analytics/rebuild
```

Summary and timeseries use inclusive UTC days, default to the last 30 days and
reject invalid/reversed ranges or windows longer than 366 days. Logs keep the
existing newest-first ID keyset order and maximum page size of 200. They now
include every observed `request_completed`, including records with no Cost.
Rebuild is supported in PostgreSQL Mode B: 204 acknowledges the derived-state
reset; the aggregator then replays retained source segments. Mode A returns 405.
See [recovery procedures](operations/recovery.md#recover-analytics-evidence).

`operations` is additive in summary totals, team/model rows and each day point.
Its unit is an **observed completion record**, not all traffic, unique user
requests, sessions or model-task quality. `request_started` refusals,
unauthenticated 401s, admin events and requests never sent by the client are
excluded. Retry/fallback record granularity can differ by ingress.

Outcome precedence is: Partial=true → `partial`; missing Outcome or status outside
200–599 → `unknown`; valid status with a nonempty Outcome.Error or status ≥300 →
`failed`; remaining 2xx → `succeeded`. Partial is counted once, separately from
failed. The observed failure proportion is `(failed + partial) / completed`;
show `—` for completed=0 and disclose unknown outcomes beside the ratio.

| Cost state (precedence order) | Meaning |
| --- | --- |
| `unavailable` | Cost=nil, including old costless source records; no amount is known |
| `unpriced` | Cost.PricingMissing; a zero numeric field is not a zero-price estimate |
| `legacy_unknown` | Cost exists but Usage or Usage.Evidence is missing |
| `uncertain` | Partial, Usage.Estimated, a false InputKnown/OutputKnown/InputTotalKnown/CacheWriteTTLKnown flag, AccountingUncertain, or a negative amount |
| `estimated` | Remaining records with usage/pricing evidence; zero is a valid estimate |

Cost state is independent of outcome: HTTP 400 may have an estimate. Even
`estimated` is a rate-based estimate, not the supplier's final invoice.
`estimated_cost_micros` sums estimated records;
`uncertain_recorded_cost_micros` sums uncertain + legacy_unknown recorded amounts.
All amounts remain integer microUSD. Existing `requests`, token and `cost_micros`
totals retain their Cost-present (`has_cost=true`) membership and arithmetic;
new operational counts do not redefine chargeback or budget settlement.

The eight-record acceptance fixture has the following totals excerpt. The same
`operations` object appears in its one team row, model row and day point.

```json
{
  "requests": 6,
  "cost_micros": 3350,
  "operations": {
    "completed": 8,
    "succeeded": 4,
    "failed": 2,
    "partial": 1,
    "unknown": 1,
    "estimated_requests": 3,
    "uncertain_requests": 1,
    "unpriced_requests": 1,
    "unavailable_requests": 2,
    "legacy_unknown_requests": 1,
    "estimated_cost_micros": 1300,
    "uncertain_recorded_cost_micros": 2050,
    "legacy_projection_records": 0
  }
}
```

`completed` equals both the sum of the four outcome counts and the sum of the five
cost-state counts. Costless-only teams/models/days can have legacy requests and
amounts of zero while their operations are nonzero. `legacy_projection_records`
counts version-0 rows awaiting source replay. Zero does not prove all historical
records were collected: retention, queue drops, ingestion lag and missing
completion records still limit coverage.

`GET /admin/logs` returns `{"events":[...]}`. Existing event fields keep their
names; `has_cost`, `outcome_state`, `cost_state`, `partial`, `error_code` and
`projection_version` are additive. Empty `error_code` is omitted. This event
excerpt represents an HTTP-200 partial response:

```json
{
  "status": 200,
  "cost_micros": 2000,
  "has_cost": true,
  "outcome_state": "partial",
  "cost_state": "uncertain",
  "partial": true,
  "error_code": "stream_incomplete",
  "projection_version": 1
}
```

For unavailable/unpriced records, do not render the numeric zero as a known dollar
amount. Missing `operations` or `cost_state` from an older API means evidence is
unavailable, rather than success or zero cost. Bounded stream reasons and
[coverage limits](operations/observability.md#read-outcome-and-cost-evidence)
apply; raw error text is not added to this API.

## Error Codes

| Code | Meaning |
|------|---------|
| 400 | Bad request (malformed body) |
| 401 | Missing/invalid virtual key (data plane) or admin token (admin plane) |
| 402 | Monetary authority exhausted in durable/shared admission |
| 403 | Access, privacy, region or policy refusal; some legacy governance denials |
| 404 | No eligible route after configured model resolution/fallback |
| 429 | Rate/token quota exhausted in the enforcing profile |
| 503 | Required policy/identity readiness, authority or shared store unavailable |
| 5xx | Upstream provider error (teed through) or gateway failure |

Errors are returned in the shape of the ingress protocol (Anthropic error object on the
Anthropic ingress; OpenAI error object on the OpenAI ingress).

## CLI

```
mayu serve  --config <path>
mayu keys   create --team <t> --models <csv> --store <path>
mayu keys   list   --store <path>
mayu keys   revoke --id <key_id> --store <path>
mayu keys   create --team <t> --models <csv> --config <path>  # select the configured backend
mayu keys   list --config <path>
mayu keys   revoke --id <key_id> --config <path>
mayu keys   import --config <shared-config> --sqlite <old-db>
mayu audit  verify --file <path>
mayu report --file <path> --by team,model
mayu pricing check --config <path>                                  # ADR-030, CI guard: exit 1 if any route has no rate
mayu login  --gateway <url> [--team <t>] [--id-token-command <cmd>]  # ADR-028
mayu token  [--export] [--raw]                                      # ADR-028, meant to run as apiKeyHelper
mayu logout                                                         # ADR-028
```

## Policy-aware request routing (ADR-043)

Anthropic Messages, OpenAI Chat Completions and native Bedrock generation use the
same privacy-constrained attempt chain; security refusals return an ingress-shaped
403. Anthropic and Bedrock token-count endpoints instead return local HTTP-200
estimates with zero upstream calls on routing refusal, unready/stale governance,
or oversized/unreadable bodies. Responses support was added separately in ADR-044.

`x-inferplane-routing-reason` is a bounded decision reason and
`x-inferplane-routed-model` identifies applied privacy/context selection. Context
Shadow still enforces privacy. Budget's earlier `x-inferplane-substituted-model`
may differ from final selection. The audit `request.routing` object records
requested (resolved pre-tier), selected, proposed and actual-attempt evidence
separately, including provider/boundary. See [schema, upgrade requirements and
limits](policy-routing.md) before enabling rules.

## Responses and adaptive routing (ADR-044)

Tool-free Codex compaction requests may set `parallel_tool_calls: false`. The stateless bridge accepts this neutral preference only when `tools` is absent, null or an empty array. With any offered tool, serialized execution remains unsupported and the request is rejected. This does not add support for remote conversation state or encrypted compaction items. See [measured client compatibility](performance/clients.md).

`POST /v1/responses` accepts authenticated HTTP Responses requests. Native
openai_responses targets retain their wire; supported stateless text/tool
requests can use canonical adapters. Streaming emits the Responses event
lifecycle and settles observed usage, including interrupted streams.
Unsupported cross-protocol state is rejected before egress. The endpoint
shares body/readiness, RBAC, region, PII, strict budget and total admission gates.

New policy fields are `sensitiveData.onDetected: Mask`, context
`normalModel`, `maxNormalInputTokens`, `stability`, and budget-tier
`enforceTargets`. See [adaptive configuration and limits](adaptive-routing.md).
