# ADR-037: inferplaned console SSO — OIDC/Cognito login for the usage console

**Date:** 2026-08-05
**Status:** Accepted — implemented (T1–T9 on `feat/inferplaned-console-sso`).
**Related:** ADR-026 (mayu admin console SSO — this ADR ports that pattern
verbatim, `internal/adminauth` is reused unmodified), ADR-004 (OIDC
resource-server posture, `IsOIDCBearerShape` routing), ADR-036 (control-plane
usage telemetry — the `/ui/` console and its single shared bearer token this
ADR extends), ADR-034 (inferplaned's stateless-container, no-config-file
constraints, unchanged here), plan `optimized-strolling-wigderson.md`.

## Context

ADR-036 shipped a read-only usage console (`/ui/`) on inferplaned with one
privilege level: a single shared `INFERPLANED_TOKEN` pasted into the page.
There is no way to know *who* looked at spend data, and rotating access means
rotating the one secret for everyone. mayu already solved exactly this for its
own admin console (ADR-026): the gateway stays a pure OIDC *resource server*,
the browser SPA is the OAuth2 *public client* (Authorization Code + PKCE,
Cognito-backed, no client secret, no server-side session). This ADR ports
that pattern to inferplaned.

Two facts specific to inferplaned shape every decision below, and both are
unchanged from ADR-034/036:

1. **inferplaned has no config file** — every new setting is a fixed env var,
   like `INFERPLANED_TOKEN`.
2. **inferplaned must stay stateless** — ECS replaces tasks at will; there is
   no server-side session store, so the resource-server posture isn't a
   stylistic choice, it's the only one that fits.

Research before implementation confirmed three things worth recording: (a)
`internal/adminauth` is a leaf package (stdlib + `go-oidc`, already a
dependency) with no import-cycle risk for `internal/controlplane` to depend
on it; (b) `internal/controlplane`'s `Server.auth` and `UsageServer.auth` were
byte-identical duplicated bearer checks with zero OIDC awareness, and there
was no `Principal`/RBAC concept anywhere on the control plane; (c)
`internal/controlplane/ui/ui_test.go` banned `sessionStorage` **outright** —
stricter than mayu's console, which allows exactly the 3 PKCE keys. That last
point was the one real point of friction, resolved explicitly in D2 below.

## Decision

**Five fixed env vars, one shared middleware, ADR-026's exact browser flow
ported byte-for-byte.**

### D1 — Env vars, not config

`INFERPLANED_OIDC_ISSUER`, `INFERPLANED_OIDC_CLIENT_ID`,
`INFERPLANED_OIDC_GROUPS_CLAIM` (default `groups`; Cognito needs
`cognito:groups`), `INFERPLANED_OIDC_ALLOWED_GROUPS` (comma-separated, at
least one required once OIDC is configured — the alternative is every login
403ing with no diagnosis), `INFERPLANED_OIDC_LOGIN_ORIGINS`
(comma-separated, gates the browser flow + CSP widening — empty ⇒ SSO off,
byte-identical to today). **This is not the console's own origin** (`'self'`
already covers that) — it's every OTHER origin the browser flow fetches
from. For Cognito that means the **hosted-UI domain**
(`https://<domain>.auth.<region>.amazoncognito.com`), which is a different
host from the issuer (`cognito-idp.<region>.amazonaws.com/<pool-id>`, added
automatically): the issuer serves discovery, the hosted-UI domain serves the
authorize redirect and the token endpoint the SPA `fetch()`s. Getting this
backwards silently blocks the token exchange with no server-visible error —
confirmed live during T7/T8 rollout (see the runbook's troubleshooting
table). Validation rules (`cmd/inferplaned/oidcenv.go`) are
ported from `internal/config`'s `validateOIDC` since inferplaned has no
config loader to share them with: absolute https issuer/origins, no
query/fragment/userinfo, login origins carry no path, no duplicates.

### D2 — Relax the `sessionStorage` ban to the 3-key allowlist, not a workaround

`internal/controlplane/ui/ui_test.go`'s outright ban is relaxed to exactly the
same allowlist `internal/server/adminui/adminui_test.go` enforces:
`ip_sso_verifier`, `ip_sso_state`, `ip_sso_nonce`. The rejected alternative
(signing the PKCE verifier into `state` instead of storing it) needs a new
signing-key secret on a binary that is deliberately stateless, and leaks the
verifier into IdP access logs — strictly worse than the storage it was meant
to avoid. The ban predates any flow that needed browser storage at all;
relaxing it with this reasoning recorded is not an erosion of the original
invariant, it's the invariant's next chapter. `localStorage` and
`document.cookie` stay banned outright, as does bracket/computed-key
`sessionStorage[...]` access and `sessionStorage.clear()` (both bypass the
literal-key regex the allowlist test uses).

### D3 — One shared auth middleware

`internal/controlplane/auth.go`'s `authn` replaces the two duplicated `auth`
methods on `Server` and `UsageServer`. Routing is the same *total* rule as
mayu's `AdminAuth`: a verifier is configured **and** the bearer is JWT-shaped
(`adminauth.IsOIDCBearerShape`) ⇒ OIDC path; everything else ⇒ static path.
This is what keeps `POST /v1alpha1/usage` (mayu's machine pusher, a plain
shared token, never JWT-shaped) completely unaffected while `GET
/v1alpha1/usage` and `GET /v1alpha1/dataplanes` gain human OIDC logins — one
middleware, not two, so a static token can never accidentally reach the
verifier (timing-oracle-free) and a JWT-shaped bearer never gets compared
against the static token (auth-bypass guard).

### D4/D5 — No request-context principal, no team-scoping in v1

There is no team-authorization concept on the control plane to preserve —
today's single shared token already sees every team's spend. SSO v1 is a
strict improvement (per-human identity, group-gated, short-lived IdP-issued
tokens) with **zero** authorization regression: a resolved OIDC identity and
the static token grant the exact same whole-console access the one shared
token grants today. `oidcEnv.mapping()` therefore projects
`INFERPLANED_OIDC_ALLOWED_GROUPS` onto `adminauth.MappingConfig.AdminGroups`
only — any member of an allowed group gets in, full stop. Per-team-scoped
reads are follow-up work once the control plane has a team concept at all.

### D6 — `GET /ui/auth/config`

Mounted on the root mux as an exact pattern (Go 1.22 `ServeMux` precedence
beats the `/ui/` prefix pattern regardless of registration order), mirroring
mayu's `GET /admin/auth/config`: secret-free `{sso, issuer?, client_id?}`,
not mounted at all when OIDC is unconfigured (404) — an always-present
`{sso:false}` route would be a permanent lie about a feature never wired up.

### D7 — No new static asset

The PKCE flow (`initSSO`/`startSSO`/`handleSSOCallback`/`clearSSOState`,
ported verbatim from `internal/server/adminui/static/app.js`) is added to the
console's existing `app.js`/`index.html`; no new embedded file, no new
dependency. `scope=openid` only, no `offline_access`, no client secret — the
SPA is the OAuth2 public client, inferplaned stays a pure resource server.
The id_token lands in the same `token` closure variable the manual-paste path
already used and is never persisted.

### What is deliberately NOT SSO-gated

`POST /v1alpha1/usage` (mayu's usage pusher), `POST /v1alpha1/sync` (mayu's
policy heartbeat), and `POST /v1alpha1/config/export` all stay reachable by
the static token exactly as before — D3's total routing rule is precisely
what guarantees a fleet of already-deployed mayu instances needs zero
reconfiguration when an operator turns SSO on.

## Consequences

- **Deployment (out of repo).** Same Cognito app-client shape ADR-026
  requires: public client, no secret (`GenerateSecret=false`), CORS enabled on
  the token endpoint, exact redirect URI (`https://<console>/ui/`, no
  wildcard ports), `AllowedOAuthFlows=[code]`. Per the org's Cognito policy,
  `selfSignUpEnabled` must be explicit `false` — accounts are
  admin-created/invited only, never self-registered.
- **Groups claim footgun, again.** Cognito always emits `cognito:groups`
  regardless of requested scope; `INFERPLANED_OIDC_GROUPS_CLAIM` must be set
  to `cognito:groups` or every login succeeds but every request 403s
  ("identity maps to no team"). `loadOIDCEnv` cannot catch this at boot (it's
  an IdP-side claim-naming fact, not something the five env vars alone
  reveal) — the runbook calls it out as the most likely first-deploy failure.
- **`INFERPLANED_OIDC_ALLOWED_GROUPS` is mandatory once any OIDC var is set.**
  Boot fails loudly rather than shipping a deploy where every login is
  authenticated-but-403 with no operator-visible cause.
- **A JWT-shaped `INFERPLANED_TOKEN` is now a boot-time error**, not a
  silently-broken static token — `authn`'s total routing rule would route it
  to the (possibly unconfigured) OIDC verifier and it could never
  authenticate either way.
- **SSO-only deployments are now legitimate.** An empty `INFERPLANED_TOKEN`
  with OIDC configured satisfies the non-loopback authentication requirement
  on its own; this was previously impossible (empty token ⇒ loopback-only,
  unconditionally).
- **Disconnect is a local lock only** (ADR-026 precedent, unchanged): clearing
  the console's in-memory token does not invalidate the IdP session; a
  re-click re-logs-in frictionlessly. RP-initiated logout stays out of scope.

## Operations workspace extension (2026-10-07)

The embedded console is redesigned into overview, usage/costs, recent data
planes and policy views; the original SSO and public-shell posture remain.
No API, server authorization, financial authority or inference path is added.
All assets still ship as the existing three embedded files without a frontend
build dependency. The browser validates connection before revealing data,
clears both credentials and fetched DOM data on disconnect, and fences stale
responses. Theme selection is memory-only.

Policy-store capability and write authority are intentionally separate. Static
read sessions select the dedicated policy-write token in a second memory-only
closure variable; OIDC sessions reuse their console credential. The server
remains the authorization authority. Complete-document cloning, separate
quick-edit fields for each budget rule, explicit review before PUT and a typed
name before DELETE replace immediate mutation. Unedited fields and existing
FailClosed survive quick edits. The full JSON editor supports other rule kinds
without partially reconstructing those documents. There is no revision/CAS
contract, simulated lease control, or inferred node readiness.

Cost parsing and export retain int64 integers above JavaScript's safe-number
range and display all six microUSD decimals. Fleet coverage means only reported
generation equality; the API's ten-minute node omission and historical rejection
ring remain visible limitations. Unmounted APIs and incomplete durable history
are never presented as healthy or zero. See the
[operations runbook](../runbooks/inferplaned-dashboard.md) for the operator flow,
verification commands and remaining limitations.

### Guided budget settings (2026-10-07)

The workspace also exposes a dedicated **Budgets** view. Guided forms create
new policies or append a budget to an existing document and edit individual
daily/monthly limits in USD. This addresses the earlier dependence on JSON
authoring to create a budget. Amounts use decimal-text-to-BigInt conversion at
the wire's milliUSD precision and reject values above the internal int64
microUSD ceiling. Existing subjects, other rules, lease settings and omitted
defaults survive edits. Each form enters the same explicit policy review/PUT
flow with the existing write-authority gate; no financial API or enforcement
semantics are added. Configured limits are shown without inventing remaining
credit from telemetry. File-authoritative deployments stay read-only.

### SSO bootstrap scheduling follow-up (2026-10-07)

The new three-query bootstrap exposed the deployed verifier's in-flight
signature-check refusal: the first JWT request could be valid while the other
two returned 401, causing the client to lock the session. The client now queues
OIDC API reads/writes across bootstrap, refresh, drill-down and export. This is
not a retry or a fallback to the static bearer; genuine 401/403 still refuse.
Disconnect aborts the active request and session-fences queued jobs. Server
signature/audience/issuer/group checks and Cognito configuration are unchanged.
Separate clients can still contend at the existing server limiter; the client
fix does not qualify or redesign that global mechanism. The static release is
updated independently of CP processes so memory-only usage history survives.

A cold-JWKS regression runs the shipped SSO callback against the real Go
verifier with a synthetic signed token and local IdP; bootstrap plus refresh
must yield six authorized reads with one JWKS fetch. Browser replay of the
previous deployed assets reproduces two in-flight 401s; the updated assets
serialize the same synthetic flow without denials. Neither test claims a
completed live human Cognito login or relaxes authorization.
