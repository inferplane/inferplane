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

`POST /v1/responses` accepts authenticated HTTP Responses requests. Native
openai_responses targets retain their wire; supported stateless text/tool
requests can use canonical adapters. Streaming emits the Responses event
lifecycle and settles observed usage, including interrupted streams.
Unsupported cross-protocol state is rejected before egress. The endpoint
shares body/readiness, RBAC, region, PII, strict budget and total admission gates.

New policy fields are `sensitiveData.onDetected: Mask`, context
`normalModel`, `maxNormalInputTokens`, `stability`, and budget-tier
`enforceTargets`. See [adaptive configuration and limits](adaptive-routing.md).
