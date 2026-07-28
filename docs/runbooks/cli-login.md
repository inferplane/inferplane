# Runbook: `inferplane login` (CLI OIDC login, ADR-028)

Lets a developer authenticate against the company IdP and get a short-lived,
auto-renewing gateway virtual key — no hand-copied `ik_...` key. CI/service
accounts are unaffected: keep using `inferplane keys create` or declarative
`virtual_keys` (ADR-023).

## Enable it (operator)

```json
{
  "server": {
    "admin_auth": {
      "oidc": {
        "issuer": "https://idp.example.com",
        "client_id": "inferplane-console",
        "cli_login": {
          "enabled": true,
          "client_id": "inferplane-cli",
          "key_ttl": "8h"
        }
      }
    }
  }
}
```

`cli_login.client_id` **must differ** from the console's `client_id` — see
ADR-028. `key_ttl` is clamped to [15m, 24h] at config load; default 8h.

### Register a second IdP app client

The CLI is its own OAuth **public client** (no secret), separate from the
console's. Register:

- **Redirect URI:** `http://127.0.0.1/callback` (some IdPs require *some*
  redirect registered even though the CLI uses an ephemeral port and sends
  the actual port at request time — register the bare loopback URI without a
  port, or the exact one your IdP requires; see the Cognito note below).
- **Grant type:** Authorization Code, PKCE required, no client secret.
- **Scope:** `openid` only. No `offline_access` — the CLI never requests or
  stores a refresh token (ADR-028).
- **Groups claim**, if RBAC depends on it (same footgun as ADR-026's console
  SSO): Cognito emits `cognito:groups` regardless of scope; Okta/Keycloak may
  gate `groups` behind a scope your IdP config must grant to this client too.

### Deployment implication

`GET /v1/auth/config` and `POST /v1/auth/key` are **data-plane** endpoints
(the same port Claude Code already talks to) — unlike the console SSO
endpoints, no admin-plane (`:9090`) reachability is required for developers.

### Persistence requirement

**Do not enable `oidc.cli_login` with `persistence.enabled: false`.** A pod
restart wipes both the keystore and the audit WAL; with keys rotating every
`key_ttl`, that's a fast, permanent loss of who-minted-what. Enable a
persistent volume and a durable audit sink (`file` or `s3anchor`, ADR-012)
before turning this on in anything but a scratch/dev environment.

### Cognito-specific note

Cognito rejects `http://127.0.0.1:<any-port>/callback` — it requires an exact
redirect match, no wildcard port. Either:

- Register the CLI's fixed port and always pass `inferplane login --port
  <that port>`, or
- Skip the browser flow entirely: `inferplane login --id-token-command
  "aws sso login ... && aws sts get-caller-identity ..."`-style wrapper that
  prints a valid ID token to stdout (see below).

## Use it (developer)

```bash
inferplane login --gateway https://gateway.example.com
```

- If entitled to exactly one team, that team is used automatically.
- If entitled to more than one, pass `--team <name>`.
- A browser opens (or the URL is printed to stderr if `--no-browser` or SSH
  without X11) — sign in, and the CLI mints a key and prints:

```
logged in as team alpha; key ik_1a2b3c4d5e6f expires 2026-07-28T20:00:00Z

Claude Code — add to ~/.claude/settings.json (use an ABSOLUTE path to the binary):
  { "apiKeyHelper": "/usr/local/bin/inferplane token",
    "env": { "ANTHROPIC_BASE_URL": "https://gateway.example.com", "CLAUDE_CODE_API_KEY_HELPER_TTL_MS": "3600000" } }
OpenCode / scripts:  eval "$(inferplane token --export)"
```

Paste the `apiKeyHelper` block into `~/.claude/settings.json`. Use an
**absolute path** to the `inferplane` binary — a bare command name is a PATH-
hijack risk once it's something Claude Code re-invokes on a timer.

**The helper TTL must stay well under the key TTL.** The snippet above sets
`CLAUDE_CODE_API_KEY_HELPER_TTL_MS=3600000` (1h) against an 8h key — an 8×
margin. If you set a shorter `key_ttl` on the server, shrink the helper TTL to
match, or Claude Code will cache a key past its `expires_at` and eat 401s
until the next scheduled helper call (it does retry immediately on a 401, so
this is self-healing, just noisier than necessary).

### `inferplane token`

Prints the cached key (bare, for `apiKeyHelper`) with zero network calls when
it still has more than 5 minutes of life left. On a real terminal it prints
only `key_id` + expiry by default — pass `--raw` to print the secret itself,
or `--export` for `export ANTHROPIC_BASE_URL=... ANTHROPIC_AUTH_TOKEN=...`
(OpenCode / scripts).

Past expiry: if `login` was run with `--id-token-command`, `token` re-runs it
and mints a fresh key with no browser interaction. Otherwise it fails with
`session expired; run: inferplane login` — there is no IdP refresh token
cached (ADR-028), so an interactive re-login is required at most once per
`key_ttl`.

### `inferplane logout`

Revokes the cached key (best-effort — succeeds even offline) and always
deletes the local credential file. Safe to run when already logged out.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `gateway has no CLI-discoverable login config` | `oidc.cli_login.enabled` is false/absent. Pass `--issuer`/`--client-id` explicitly, or ask an operator to enable it. |
| `entitled to multiple teams; specify --team` | Your IdP groups map to more than one team. `inferplane login --team <name>`. |
| `your identity maps to no gateway team` | No `group_mappings`/`admin_groups` entry matches your groups. Ask an admin to add one — the gateway never grants a default team. |
| `gateway ... OIDC identity changed (issuer/client_id); pass --reset` | TOFU pin mismatch — the gateway's issuer/client_id differs from what you logged in with last time at this URL. If this is expected (gateway reconfigured on purpose), rerun with `--reset`. If NOT expected, stop — this could mean the gateway URL now points somewhere else. |
| `timed out waiting for the browser callback` | Nothing completed the IdP flow within 3 minutes — check the browser actually opened, or copy the printed URL manually. |
| Browser doesn't open automatically | Expected over SSH/WSL/headless — the authorization URL is always printed to stderr; open it manually, or pass `--no-browser` to skip the attempt outright. |
| `401` mid-session in Claude Code | Normal and self-healing — `apiKeyHelper` is re-invoked on every 401, so a rotated/expired key recovers on the next request without user action. |
| Key rows accumulating in `GET /admin/keys` | Expected at ~1-3 rows/developer/day (bounded by the server's mint rate limit and `key_ttl`). Not a correctness issue (`Resolve` is fail-closed on expiry regardless of row count) — a `keys prune` subcommand is a deferred follow-up, not built speculatively. |
