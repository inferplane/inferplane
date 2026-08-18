# Customer Issue Intake Analysis — LiteLLM Operational Reality and inferplane's Coverage

> **Source**: `[Claude Code] issue intake log.xlsx` (2026-06 through 2026-07, from a
> corporate Claude Code on Bedrock channel, **59 actually-filed tickets**). This is a
> real production environment built from a LiteLLM gateway + an AWS SSO profile +
> a separate token service (`get-gateway-token.sh`). It complements
> [`Customer_needs.md`](../Customer_needs.md) (based on Slack conversations and web
> research); this document is grounded in **actually-filed tickets**, so its
> evidence is more concrete.

This document classifies all 59 tickets by category and, for each category, judges
whether inferplane resolves it (✅ resolves directly / 🔶 partially resolves / ❌
unrelated to the gateway). The bar for "resolved" is a feature actually implemented
in the code and ADRs — unsupported claims are excluded.

## Summary

| Verdict | Count | Share |
|------|------|------|
| ✅ Resolves directly | ~35 | 59% |
| 🔶 Partially resolves (the gateway gives visibility/mitigation but can't remove the upstream root cause) | ~10 | 17% |
| ❌ Unrelated to the gateway (Anthropic plan policy, client bugs, out of scope) | ~14 | 24% |

## Analysis by Category

### 1. Auth-chain breakdown — ✅ resolves directly (~15 tickets, the largest category)

**Ticket evidence** (rows 3, 6, 7, 11, 14, 33, 40, 47, 49, 53, 54, 55, etc.):
- `401 Malformed API Key passed in. Ensure Key has Bearer prefix`
- `InvalidClientTokenId` / `NoCredentials: Unable to locate credentials`
- `Invalid proxy server token passed... Unable to find token in cache or LiteLLM_VerificationTokenTable` (row 54) — LiteLLM's token cache and the underlying DB were out of sync
- Revoking/reissuing a token required "delete the token in the LiteLLM UI AND delete it from the cached DynamoDB entry" (row 47)
- SSO device-code auth failures requiring re-authentication every 8 hours (rows 39, 49)

The current setup is a four-hop chain: client → AWS SSO → IAM role → a separate
token service → the LiteLLM verification table. If any single hop's state drifts
(an expired SSO session, a token-cache/DB mismatch, a corrupted IAM profile), the
user gets a 401/403 they have no way to diagnose.

inferplane removes this chain entirely. A virtual key (`ik_...`) issued via
`inferplane keys create` is SHA-256-hashed and stored once in the key store; the
plaintext is shown only once at issuance (unrecoverable after that). The client
keeps using that key in its `Authorization` header indefinitely — there is no AWS
SSO, no IAM role, and no separate token service in the picture. Revoking a key is
one line: `inferplane keys revoke --id <key_id>` (`cmd/mayu/keys.go:25`) — no need
to manually clear both a UI and a DynamoDB table the way LiteLLM requires.

### 2. Auth failure in CI/headless environments — ✅ resolves directly (row 39)

The existing CI injected `CLAUDE_CODE_OAUTH_TOKEN` to authenticate, but switching
to browser-based AWS SSO (8-hour expiry) broke CI automation. A virtual key is a
static string that needs no browser interaction, so it can be injected as a CI
environment variable exactly as before.

### 3. Strict-schema 400 errors — ✅ resolves directly, structurally (rows 16, 17)

**Ticket**: `API Error: 400 {"detail":{"error":"diagnostics: Extra inputs are not permitted"}}`.
LiteLLM rejects a new beta field (`diagnostics`) in Claude Code's `anthropic-beta`
header as an unknown field it doesn't recognize. The ticket's own root-cause
diagnosis is accurate: *"as long as the gateway stays strict, client-side config
alone means an endless game of whack-a-mole"* (row 16) — every time Claude Code
ships a version with a new field, the gateway blocks it again.

inferplane makes this class of bug structurally impossible by design:
- **The canonical schema invariant** (`CLAUDE.md` → Canonical schema invariant) —
  only pipeline-interpreted fields are typed; everything else is preserved
  verbatim via `Extra map[string]json.RawMessage`.
- **The cache invariant** (§4.4) — when the ingress and upstream protocols match,
  the request body is forwarded as-is via `RawBody`, with no re-serialization.

In other words, even when a client adds a new field, the gateway never needs to
know about that field to reject it — rejection is structurally impossible.

### 4. Model-name/routing errors — ✅ resolves directly (rows 25, 41, 52)

**Tickets**: `Invalid model name passed in model=apac.anthropic.claude-sonnet-4-6`,
`'model' keyword not found and unable to extract model from endpoint. Expected format:
/model/{modelId}/{action}. Got: v1/messages` — these arise because LiteLLM's
passthrough routing (embedding the model ID in the URL path) diverges from the
standard Anthropic Messages API shape (`/v1/messages` plus a `model` field in the
body). Row 41 is a case where a client's automatic model discovery tries a `us.*`
model ID the operator never registered.

inferplane exposes the standard `/v1/messages` and `/v1/chat/completions` ingresses
as-is (`internal/server`), and model→provider routing resolves internally through
configured model aliases/fallback chains (`internal/router`). The client never needs
to know a URL path convention. ADR-014 (provider-registration-ux-litellm-parity)
brings this routing-registration experience to parity with LiteLLM while keeping the
same structural advantage.

**Implemented (ADR-021):** this gap has actually been closed. (1) Declaring a model
alias via config `models.<name>.aliases` normalizes a name like
`apac.anthropic.*` to its canonical form before RBAC/routing/audit. (2) An
unregistered-model 404 or disallowed-model 403 now returns the list of models that
key can actually use, replacing LiteLLM's dead-end `Invalid model name`-style
errors. (3) As a 2026-07-12 follow-up, the same alias feature was extended to the
`provider_store` path (models registered via the admin UI) — the constraint that
only config-file models could have aliases is gone, so console-registered models can
have aliases too.

### 5. Cost opacity / limit management — ✅ resolves directly (rows 2, 4, 27, 37, 42, 45)

**Tickets**: no way to check usage, leaving an existing subscription pattern
uncertain (row 2); a $100/month cutoff allowing only two increase requests per month
(row 4); "I hit my limit on day 5 without doing anything" (row 37); a request, after
increase automation shipped, for developers to get their own on/off control (row 45).

inferplane's governance is two-phase: `Governor.PreCheck` checks rate/quota/budget
**before** billing, and `Governor.Settle` debits actual tokens/µUSD **after**
(`internal/governance`). `on_exceeded: block|warn` lets each team choose whether an
exceeded limit blocks immediately or just warns and passes through.
`inferplane report --by team,model` (ADR-007) pulls per-team/per-model settled cost
from the audit log to CSV, and cost is integer µUSD with no float accumulation
drift. ADR-017 (budget-alert webhooks) sends advance warning to Slack/SNS at
thresholds like 80%/100% — removing the "used it without realizing, then got cut
off" situation at the source.

**Implemented (ADR-021):** added `GET /v1/usage` (data plane, virtual-key auth) so a
developer can check this themselves with no dashboard or admin needed. It returns
the caller's own per-key and team budget/quota as integer µUSD (unlimited dimensions
are null), and the response never includes `key_id` — a direct fix for the "hit my
limit without doing anything" (row 37) anxiety.

### 6. Timeouts / empty responses / unexplained failures — 🔶 partially resolves (rows 19, 20, 28, 43, 46)

**Tickets**: repeated `Unable to connect to API (ConnectionRefused)`,
`API returned an empty or malformed response (HTTP 200)`, and `The operation timed out`.
Even the operator's own answer stays at an unconfirmed guess: *"our best guess is
this is an internal availability shortage tied to the Fable 5 model launch"*
(row 46).

inferplane cannot prevent an availability outage in the upstream provider itself
(Anthropic/Bedrock). What it can do: `internal/router`'s circuit breaker
(consecutive failures → open → backoff → half-open) automatically fails over to an
alternate model/provider, but **only before the first token (TTFT)**, and GenAI
semantic-convention metrics (`internal/metrics`) plus opt-in OTel tracing (ADR-011)
let an operator trace exactly which stage a given request failed at. This is
classified as partial resolution because it replaces "guessing" with observed data
— no gateway can eliminate an upstream outage itself.

### 7. No access to closed networks (an internal "Braincloud") — 🔶 partially resolves (rows 24, 30, 56)

**Tickets**: the LiteLLM dashboard/gateway is only reachable from a corporate
private-IP range, so a separate network zone such as Braincloud (KAP) cannot reach
it. The operator's answer is the same every time: *"we'll take it up with the
security team."*

Because inferplane has zero external SaaS dependency and is a single static,
Kubernetes-native binary, deploying a separate instance per network zone is an
architecturally natural fit (it's a deployable binary, not a centralized SaaS).
But that's a deployment-flexibility property — whether a specific IP range is
actually allowed is the organization's own firewall/security-policy decision, which
no gateway software can make on its behalf. Classified as partial resolution.

### 8. Onboarding/setup complexity — ✅ resolves directly (rows 1, 5, 12, 31, 48, 59)

**Tickets**: a changed script-distribution URL (row 5), AWS CLI install/version
issues (row 48), repeated re-setup requests (rows 55, 59), a request for an MCP
integration guide (row 31).

Today's onboarding is a multi-step flow — install the AWS CLI → configure an SSO
profile → log in → run a gateway-token script — where every step is its own failure
point. inferplane's client-side config is just a base URL and a virtual key, full
stop; no AWS CLI, SSO profile, or region setting is needed on the client at all.

### 9. Delayed activation of new models — 🔶 partially resolves (rows 44, 46)

**Tickets**: a request to activate a new model (`claude-fable-5`), with the
operator's answer being *"we'll roll out an update at some point"* — a structure
that requires waiting for a redeploy.

inferplane's `SIGHUP`-based hot reload (ADR-006) atomically swaps config with no
restart, and when `provider_store` is enabled, a model can be added/removed via the
`PUT/DELETE /admin/providers|models` API (ADR-008, ADR-014) and take effect
immediately from the console. The model still has to actually be enabled on the
Anthropic/Bedrock side for that region/account (row 44 is an "overseas usage
restricted" case), so the gateway cannot bypass the upstream provider's own model
release policy.

### 10. Subscription-only features — ❌ unrelated to the gateway (rows 9, 13, 32, 34, 35, 36, 38)

**Tickets**: the Google Drive connector, Claude for Office (Excel/PowerPoint/Word),
the Chrome extension — all gated by Anthropic's official policy to *"Pro, Max,
Team, and Enterprise plans only."* This happens regardless of whether traffic goes
through Bedrock or a gateway, and no LLM gateway can resolve it. Honestly marked
out of scope.

### 11. Other client-side / out-of-scope issues — ❌ unrelated to the gateway (rows 26, 50, 57)

An Auto-mode version issue (a client-side bug, fixed in a later version), a UI bug
where tool-call text is exposed raw with no selection option (row 57), and an
embedding-model request (row 50 — the requester themselves corrected that "this
isn't a good fit for a gateway meant for Claude Code traffic"). Unrelated to
inferplane's v1 scope (LLM consumption governance).

### 12. Custom script/SDK auth failures — ✅ resolves directly (rows 3, 8, 15, 22)

**Tickets**: *"The Claude Code app itself works fine, but the same token doesn't
connect from my Python code"* (row 8); confusion over `bedrock:ListInferenceProfiles`/
`bedrock:InvokeModel` IAM permissions (rows 3, 15, 22). The root cause is a
structure where the client has to know that AWS IAM policy exists at all, and the
specifics of its scope.

Because inferplane exposes the standard Anthropic/OpenAI API shape as-is, Claude
Code and an arbitrary Python script behave identically with the same virtual key.
The client never needs to know Bedrock sits behind the gateway, or anything about
IAM permissions (§5.2, client/upstream key isolation — the client never sees the
upstream provider's key, and the gateway never forwards the client's key upstream).

## Summary of inferplane's Strengths (grounded in actually-filed tickets)

1. **Auth was in practice the single biggest source of outages** — roughly a
   quarter of the 59 tickets were an SSO/token-chain problem. A virtual key removes
   this four-hop chain entirely.
2. **"Extra inputs are not permitted" is structurally impossible by design** —
   verbatim body forwarding plus `Extra` preservation is the structural fix for
   LiteLLM's strict-schema problem. It ends the recurring whack-a-mole every time a
   client ships a new version.
3. **Governance runs both pre-check and settle** — team-scoped `warn`/`block`
   policy plus real-time reports and threshold alerts (ADR-017), instead of getting
   cut off without warning.
4. **Key lifecycle is one command** — `inferplane keys revoke` versus LiteLLM's
   "delete in the UI and delete in DynamoDB."
5. **`count_tokens` never returns a non-200** — an explicit guarantee
   (`docs/reference/api.md`) against the exact failure mode that crashes Claude
   Code; LiteLLM carries no such guarantee.
6. **Replaces the operator's "guess" with observed data** — the circuit breaker
   plus GenAI metrics plus OTel tracing let a timeout/empty-response failure
   actually be traced to its cause.
7. **A deployment model friendly to closed networks** — zero external SaaS
   dependency, a single static binary, deployable per network zone. It can't
   override an organization's firewall policy, but it removes the fundamental
   constraint of a centralized architecture.
8. **Bypass-resistant Bedrock Guardrails (ADR-019) and per-team region locking
   (ADR-020)** — these already cover the "guardrail bypass" problem and the "NCT
   compliance (Korea-only region lock)" requirement `Customer_needs.md` calls out.

## What It Doesn't Solve (an honest accounting of the gaps)

- Anthropic's subscription-only features (the Desktop connector, Claude for
  Office, the Chrome extension) are a plan-level policy no gateway can bypass.
- An upstream provider's (Anthropic/Bedrock) own availability outage can be
  mitigated by the circuit breaker (automatic failover) but not eliminated at the
  source.
- Whether an internal firewall/network zone is allowed through is the
  organization's own security-policy decision; the gateway only offers deployment
  flexibility.
- Embedding models and client-side UI bugs are outside the v1 scope (LLM
  consumption governance).

## Related Documents

- [`Customer_needs.md`](../Customer_needs.md) — LiteLLM pain-point analysis based on Slack conversations and web research (the broader context for this document)
- [ADR-014](decisions/ADR-014-provider-registration-ux-litellm-parity.md) — parity with LiteLLM's model-registration UX
- [ADR-017](decisions/ADR-017-budget-alert-webhooks.md) — budget-threshold webhook alerts
- [ADR-019](decisions/ADR-019-bedrock-guardrails-data-plane.md) — bypass-resistant Bedrock Guardrails
- [ADR-020](decisions/ADR-020-per-team-region-locking.md) — per-team region locking (NCT compliance)
- [ADR-007](decisions/ADR-007-chargeback-report.md) — audit-log-based chargeback report
- [ADR-006](decisions/ADR-006-config-hot-reload.md) — SIGHUP-based config hot reload
