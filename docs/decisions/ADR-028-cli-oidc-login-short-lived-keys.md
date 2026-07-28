# ADR-028: CLI OIDC login — short-lived, auto-renewing virtual keys

**Date:** 2026-07-28
**Status:** Accepted
**Related:** ADR-004 (OIDC admin authz — resource-server-only), ADR-010
(self-service key issuance, `POST /admin/keys` + `GET /admin/whoami`), ADR-026
(console SSO login — SPA-as-OAuth-public-client), ADR-023 (declarative
virtual keys — the CI/service-account path this ADR does not change)

## Context

Onboarding today means a human copies a plaintext `ik_...` key out of the
console or `inferplane keys create` output and pastes it into
`ANTHROPIC_API_KEY`. That key never expires unless someone revokes it. The
static string itself is the risk: it survives in shell history, `.env` files,
CI logs, and laptops long after anyone remembers it exists.

Most of what's needed already exists. `POST /admin/keys` (ADR-010) already
accepts an OIDC bearer, re-checks `AdminIdentity.Entitled(team)`, accepts
`expires_at`/`owner`, and audits `admin_key_created`. `keystore.Resolve`
already enforces expiry fail-closed. What's missing is (1) a CLI that drives
this without a browser copy-paste, and (2) a port a developer can actually
reach — the Helm chart deliberately keeps the admin plane (`:9090`) off the
default ingress path (`persistence`/`ingress.admin.enabled` both default
false, and the values.yaml comment says explicitly: keep key-issuance actions
on an operator-restricted path).

Claude Code's `apiKeyHelper` (`~/.claude/settings.json`) makes automatic
renewal viable: it re-invokes the configured command on a timer
(`CLAUDE_CODE_API_KEY_HELPER_TTL_MS`, default 5 min) **and on every HTTP
401**, and sends the helper's stdout as both `Authorization: Bearer` and
`x-api-key` — exactly the two headers `KeyAuth` already reads. A short-lived
key that gets silently swapped out from under an in-progress session is
therefore invisible to the user, closing the loop the field notes in
`docs/customer-issue-analysis.md` complain about (SSO device-code failures,
8-hour re-auth churn) — provided the design doesn't reintroduce that same
class of failure by depending on something else that can also expire.

## Decision

Add `inferplane login` / `token` / `logout` to the existing binary, and a
new **data-plane** (not admin-plane) opt-in endpoint pair so a developer never
needs to reach `:9090`:

- **`GET /v1/auth/config`** — unauthenticated, secret-free: `{cli, issuer?,
  client_id?}`. Mirrors `GET /admin/auth/config` (ADR-026) exactly; omitted
  (404) unless `oidc.cli_login.enabled` is set.
- **`POST /v1/auth/key`** — OIDC-bearer-authenticated (see below), body is
  `{"team": "..."}` only. The **server** decides `expires_at` (from
  `oidc.cli_login.key_ttl`, default 8h, clamped to [15m, 24h] at config load —
  never client-supplied, or "short-lived" would be a false claim), `owner`
  (the verified `sub`), and `metadata: {"source":"cli"}`. Rate-limited per
  subject (10/min, burst 10) so one valid ID token can't grow the keys table
  without bound. Audits `cli_key_created` / `cli_denied`.
- **`DELETE /v1/auth/key`** — authenticates with the virtual key itself
  (`KeyAuth`, same as any other data-plane route) and revokes itself. Used by
  `logout`; needs no OIDC round-trip.

### A second, distinct OIDC client — never the console's

`oidc.cli_login.client_id` MUST differ from `oidc.client_id`. Two independent
reasons, either alone sufficient:

1. **It's structurally required.** `adminauth.Verifier` checks `aud ∋
   client_id` and, when `azp` is present, requires `azp == client_id`
   (OIDC Core §3.1.3.7, enforced at `internal/adminauth/oidc.go`). An ID token
   minted for the CLI's own client would be rejected by a `Verifier`
   configured with the console's `client_id`, and vice versa — so the two
   simply cannot share one verifier instance. `cliVerifier` in
   `cmd/inferplane/gateway.go` builds a second `adminauth.Verifier`, keyed to
   `cli_login.client_id`, reusing the SAME battle-tested verification code
   (alg pin, skew, lazy discovery, JWKS-refetch rate limit) — zero new
   crypto.
2. **Reuse would weaken the console.** ADR-026's console client is a
   secretless public client. Registering a CLI loopback redirect
   (`http://127.0.0.1:<port>/callback`) on that same client would let any
   local process complete a code flow with the console's audience. A distinct
   `client_id` bounds that blast radius to the CLI's own registration.

### No IdP refresh token is ever stored

The credential file (`~/.config/inferplane/credentials.json`, 0600, atomic
temp+rename, `INFERPLANE_HOME`-overridable) holds only the currently-minted
key, its expiry, and the TOFU-pinned issuer/client_id. It holds **no** IdP
refresh token. Two reasons:

1. A refresh token is a *stronger*, gateway-unrevocable credential than the
   `ik_` key it would protect — losing the file to disk theft would hand an
   attacker SSO, not a team-scoped, gateway-revocable, short-lived key.
2. Okta/Cognito/Auth0 default to refresh-token rotation with reuse detection.
   A helper-timer renewal racing a second Claude Code window or a manual
   `inferplane token` would eventually present a consumed token, kill the
   whole token family, and silently log the developer out — reproducing,
   nondeterministically, the exact 8-hour-re-auth pain this feature exists to
   remove.

The consequence: **`token` cannot silently renew across an interactive login
boundary.** Renewal only happens two ways:

- `token`'s hot path: the cached key still has `> renewBefore` (5 min) of
  life left in **laptop** clock time — `expires_at` is enforced against
  **server** time in `keystore.Resolve`, so the margin exists specifically to
  absorb a fast local clock. Zero network calls.
- `--id-token-command <cmd>` (set at `login` time, persisted): re-run the
  command, mint a fresh key, no browser. This is the CLI's *only* unattended
  renewal path — the kubectl exec-credential pattern ADR-004 already
  endorses, and it covers strictly more ground than an OAuth device-code
  fallback would (headless boxes, IdPs like Cognito that reject `http`
  loopback redirects entirely, and orgs that already have `aws`/`gcloud`/`az`
  CLIs authenticated). Device flow was considered and rejected: it's ~80 more
  lines of polling/backoff state, and the field notes already blame
  device-code failures for part of the current pain.
- Otherwise: `token` fails fast with "session expired; run: inferplane
  login" — an interactive re-login, exactly like today, just far less
  frequent (once per `key_ttl`, not once per static-key-never rotation... or
  rather: once is the *ceiling*, whereas today there's no rotation at all).

### Browser flow: loopback Authorization Code + PKCE (RFC 8252)

`inferplane login` is its own OAuth public client running Authorization
Code + PKCE (S256, via `golang.org/x/oauth2`'s built-in helpers — no hand-
rolled crypto) against a `127.0.0.1:<ephemeral-port>/callback` redirect.
`state` is checked with a constant-time compare before anything else,
`error` before `code`; a non-`/callback` path 404s without consuming the
one-shot listener (a stray favicon request can't kill the login); the
callback response is `Cache-Control: no-store` / `Referrer-Policy:
no-referrer` and never echoes the code into the page HTML. No `nonce` is
sent: the ID token is exchanged over TLS with a PKCE verifier only this
process holds, and the gateway — not the CLI — is the party that verifies
the token's signature; a nonce protects a client that verifies the token
itself, which the CLI deliberately does not do (that's the gateway's job,
same posture as ADR-026's SPA).

Every gateway/issuer URL is validated https-or-loopback (`validateEndpointURL`
— the loopback exception exists only so a plain `httptest.Server` can stand
in for both gateway and IdP in tests, and for local dev). A discovered
authorization/token endpoint must additionally be same-origin-or-subdomain of
the validated issuer (`sameOriginOrSubdomain`), bounding a compromised or
misconfigured discovery document's blast radius. On first login, issuer and
client_id are pinned into the credential file (TOFU); a silent change for an
already-known gateway is refused unless `--reset` is passed — protection
against a later-compromised gateway URL redirecting an existing user's SSO to
an attacker-controlled IdP.

### Attribution and governance, given rotating key_ids

- **CLI-key spend is governed at the TEAM level only.** `governance.go` keys
  per-key budget/TPM/RPM on `"budget:key:"+keyID` with a fixed-length window;
  since every login/renewal mints a new `key_id`, a per-key limit would reset
  every rotation. `POST /v1/auth/key` deliberately never sets
  `BudgetUSDMicros`/`TPM`/`RPM`.
- **Attribution survives rotation via the audit chain, not the keystore.**
  `cli_key_created` records `User: sub` alongside `key_id` — an operator
  answers "what did person X do" by chain lookup, not by treating `key_id` as
  a stable identity. This makes the audit chain's durability load-bearing in
  a new way: **`persistence.enabled: false` (the Helm default) combined with
  CLI login is an unsupported combination** — a pod restart wipes both the
  keystore and the audit WAL, and with ~1-3 key rows minted per developer per
  day (bounded by `renewBefore` + the mint rate limit), that's a much faster
  loss of the sub→key_id join than the single-static-key world ever had.
  Operators enabling `oidc.cli_login` should also enable persistent storage
  and a durable audit sink.
- **`owner` cannot be spoofed by a teammate.** Fixed as part of this ADR in
  `adminapi.KeysHandler.create` too (not just the new endpoint): a non-admin
  OIDC identity's `owner` is always overridden to its own `sub`, regardless
  of what the request body asks for. Closes a pre-existing hole in the
  console/API self-service path (ADR-010) where any team member could mint a
  key and attribute it to someone else.
- **Revoke-on-rotation was considered and rejected.** A second Claude Code
  process (or the same one, mid-request) may still hold the previous key up
  to its own `apiKeyHelper` TTL; revoking it on every rotation would break
  that process mid-session. The short `expires_at` is exactly the mechanism
  that retires a superseded key — `logout` is the only path that revokes.
- **No background pruning of expired rows.** `Resolve` is fail-closed on
  expiry regardless of row count (`internal/keystore/sqlite.go`), so
  correctness doesn't depend on cleanup. With the mint rate limit and
  server-decided TTL, growth is bounded (~1-3 rows/developer/day) — a
  `keys prune` subcommand is deferred until `GET /admin/keys` response size
  actually hurts, not built speculatively.

## Consequences

### Positive
- A developer's laptop never holds a credential more powerful or longer-lived
  than a team-scoped virtual key good for at most 24h (config-clamped).
- `apiKeyHelper`'s existing 401-triggers-refetch behavior means key rotation
  is invisible mid-session — no more disruptive than today's static key, and
  the compromise window shrinks from "forever" to `key_ttl`.
- Zero new endpoint on the admin plane; deployments that intentionally
  firewall `:9090` to operators only are unaffected and can enable this
  purely on the already-public data plane.
- CI/service-account provisioning is completely unchanged — `inferplane keys
  create`, `POST /admin/keys`, and declarative `virtual_keys` (ADR-023) all
  keep working exactly as before. This is a human-only, opt-in addition.

### Negative
- **Renewal past `key_ttl` without `--id-token-command` requires an
  interactive re-login.** This is a deliberate trade against storing a
  stronger, unrevocable IdP refresh token — but it means `key_ttl` sets a
  hard ceiling on how long a developer can go without touching a browser
  again, and operators should pick it with that in mind (8h default = once
  per workday).
- **`persistence.enabled: false` + CLI login silently degrades attribution**
  (not availability — `Resolve` still works) on every restart. Not enforced
  by code (a config validator can't see the persistence chart value from
  inside the binary); documented in the runbook instead.
- **A second OIDC app-client registration is now required per IdP** for any
  operator who wants both console SSO and CLI login. One more piece of IdP-
  side setup, covered in `docs/runbooks/cli-login.md`.
- **Cognito rejects `http://127.0.0.1:<any-port>/callback`** (no wildcard
  port support) — deployments on Cognito need either a fixed `--port` with
  that exact URI registered, or `--id-token-command` using the AWS CLI's own
  SSO login instead of the browser PKCE path.

## References
- `internal/server/authapi/` — the two new handlers
- `cmd/inferplane/login.go`, `cmd/inferplane/credentials.go` — the CLI
- `internal/config/config.go` — `CLILoginConfig`, `validateOIDC`
- `docs/runbooks/cli-login.md` — IdP registration + troubleshooting
- `docs/customer-issue-analysis.md` — the SSO/token-chain field pain this
  narrows, and the explicit case for keeping static keys for CI
