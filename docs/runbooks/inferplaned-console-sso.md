# Runbook: inferplaned console SSO (ADR-037)

Lets an operator sign into inferplaned's usage console (`/ui/`) with their
company IdP instead of pasting the shared `INFERPLANED_TOKEN`. The static
token keeps working unchanged for the machine path (mayu's usage pusher and
policy heartbeat) — this is additive, not a replacement.

## Enable it (operator)

inferplaned has no config file — every setting is an env var on the
container:

```bash
INFERPLANED_OIDC_ISSUER=https://cognito-idp.ap-northeast-2.amazonaws.com/ap-northeast-2_XXXXXXXXX
INFERPLANED_OIDC_CLIENT_ID=<cognito app client id>
INFERPLANED_OIDC_GROUPS_CLAIM=cognito:groups
INFERPLANED_OIDC_ALLOWED_GROUPS=platform-admins,finance-readonly
INFERPLANED_OIDC_LOGIN_ORIGINS=https://inferplane.example.com
```

| Var | Required | Notes |
|---|---|---|
| `INFERPLANED_OIDC_ISSUER` | when any OIDC var is set | Absolute https URL, no path suffix beyond the pool ID, no query/fragment/userinfo. |
| `INFERPLANED_OIDC_CLIENT_ID` | when any OIDC var is set | The public (no-secret) app client's ID — this is the expected token `aud`. |
| `INFERPLANED_OIDC_GROUPS_CLAIM` | no (default `groups`) | **Set to `cognito:groups` for Cognito** — see the footgun below. |
| `INFERPLANED_OIDC_ALLOWED_GROUPS` | **yes**, once OIDC is configured | Comma-separated. Any member of any listed group gets full console access — there is no per-team scoping yet (ADR-037 D4/D5). |
| `INFERPLANED_OIDC_LOGIN_ORIGINS` | no | Comma-separated absolute https **origins** (no path) the console SPA runs from. Empty = OIDC verifies API callers but the browser "Sign in with SSO" button and its CSP widening stay off — byte-identical to a pre-SSO deploy. |

Restart the container to pick up new env vars — there is no hot-reload for
these (unlike GovernancePolicy files).

### Register a Cognito app client

Same shape as mayu's console SSO client (ADR-026) — a **separate** app client
from the one mayu's `/admin/ui/` uses, if both consoles exist:

- **Client type:** Public client, **no secret**
  (`GenerateSecret=false`). This is a browser PKCE client; a secret embedded
  in JS is not a secret.
- **Allowed OAuth flows:** Authorization code grant, with PKCE.
- **Allowed OAuth scopes:** `openid` only.
- **Callback URL:** the console's own root, exactly —
  `https://<your-console-host>/ui/`. Cognito requires an exact match, no
  wildcard.
- **CORS on the token endpoint:** required, or the browser-side code
  exchange fails outright with an opaque CORS error.
- **User pool sign-up:** `AllowAdminCreateUserOnly: true`
  (`selfSignUpEnabled: false`) — company policy is admin-created/invited
  accounts only, never self-registration. This is unrelated to the console
  client above but is the standard companion setting on any pool this client
  points at.

### The groups-claim footgun (read this before you deploy)

**Cognito always emits the `cognito:groups` claim in the ID token regardless
of requested scope.** If you leave `INFERPLANED_OIDC_GROUPS_CLAIM` at its
default (`groups`), every login will *succeed* (the ID token verifies fine)
but every subsequent API call will 403 with `"identity maps to no team"` —
because the middleware is looking for a claim named `groups` that Cognito
never sends. Set:

```bash
INFERPLANED_OIDC_GROUPS_CLAIM=cognito:groups
```

This is the single most likely first-deploy failure. If you see 200s on
`GET /ui/auth/config` and a clean IdP redirect, but every data call 403s
right after login, check this env var first.

### Boot-time guardrails

- `INFERPLANED_OIDC_ALLOWED_GROUPS` unset while any other OIDC var is set
  fails the process at boot (not a runtime 403 surprise later) — an
  SSO deploy where nobody can ever be authorized is treated as a
  configuration error.
- A **JWT-shaped** `INFERPLANED_TOKEN` (three dot-separated base64url
  segments) also fails boot — it would be routed to the OIDC verifier by the
  same total rule that keeps mayu's usage-pusher token safe, and could never
  authenticate as a static token either way. Use a plain random string for
  `INFERPLANED_TOKEN`, same as always.
- An **SSO-only deployment** (OIDC configured, `INFERPLANED_TOKEN` left
  unset) is a legitimate non-loopback posture — OIDC alone satisfies the
  authentication requirement. Rotating away from a shared static token
  entirely is supported, not just tolerated.

## Use it (operator, browser)

1. Open `https://<console-host>/ui/`.
2. If SSO is configured with `LOGIN_ORIGINS` including this origin, a "Sign
   in with SSO" button appears below the manual token field.
3. Click it → redirected to Cognito's hosted UI → sign in → redirected back
   to `/ui/` → the console unlocks automatically.
4. The three PKCE working values (`ip_sso_verifier`/`ip_sso_state`/
   `ip_sso_nonce`) live in `sessionStorage` only for the duration of the
   round trip and are cleared immediately after, success or failure. The
   ID token itself is held in page memory only — never written to disk,
   `localStorage`, or a cookie.
5. Remove yourself from every group in `INFERPLANED_OIDC_ALLOWED_GROUPS` and
   re-login to confirm the deny path: you should see 403, not a silently
   granted default team.

The manual token field keeps working unconditionally — SSO is an additional
path, not a replacement, and there is no way to disable the static token from
the SSO side.

## Verify the machine path is unaffected

```bash
curl -sf -H "Authorization: Bearer $INFERPLANED_TOKEN" \
  https://<control-plane>/v1alpha1/usage?group_by=team
```

should still work identically whether or not OIDC is configured — mayu's
usage pusher and policy-sync heartbeat both use the static token and never
send a JWT-shaped bearer, so they never touch the OIDC verifier at all.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| Every login succeeds but every subsequent request 403s ("identity maps to no team") | `INFERPLANED_OIDC_GROUPS_CLAIM` is still `groups` against a Cognito pool. Set it to `cognito:groups`. |
| inferplaned refuses to boot: "INFERPLANED_OIDC_ALLOWED_GROUPS is required" | Set at least one allowed group before enabling any other OIDC var. |
| inferplaned refuses to boot: "INFERPLANED_TOKEN must not be JWT-shaped" | You put an actual ID token or a JWT-looking string in `INFERPLANED_TOKEN`. Use a plain random secret instead — the OIDC token is never the static token. |
| "Sign in with SSO" button never appears | `INFERPLANED_OIDC_LOGIN_ORIGINS` doesn't include the origin you're browsing from, or is unset entirely. Check `GET /ui/auth/config` returns `sso:true`. |
| `GET /ui/auth/config` 404s | OIDC is not configured at all (this is by design — no permanent `{sso:false}` route). Set the required env vars and restart. |
| Browser console shows a CORS error on the token exchange | The Cognito app client's token endpoint doesn't have CORS enabled for this origin — check the app client settings, not inferplaned. |
| Redirect loop or Cognito rejects the callback | The registered callback URL doesn't exactly match `https://<console-host>/ui/` (trailing slash matters; no wildcard host/port). |
