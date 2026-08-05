# cmd Module

## Role
Binary entrypoints. Two binaries (ADR-031): `cmd/mayu` — the node-local data
plane (the former gateway; `mayu` is a component name, not a project name) —
and `cmd/inferplaned` — the control plane: `--policies` distributes watched
GovernancePolicy documents and issues budget leases over `/v1alpha1/sync`
(ADR-034; `INFERPLANED_TOKEN` gates it — loopback-only until set unless
console SSO covers it, ADR-037; with `INFERPLANED_POLICY_DSN` set the policy
set is Postgres-authoritative and `--policies` becomes the one-time seed,
ADR-038), plus `/healthz` + `/readyz`. Both binaries
import `internal/policy`, so schema version skew fails at compile time.

## Key Files
- `mayu/main.go` — CLI dispatch (`serve` / `keys` / `audit` / `report` / `pricing` / `login` / `token` / `logout`), wiring of keystore + audit + governor + metrics + router + providers, the TLS branch, and graceful shutdown.
- `mayu/pricing.go` (ADR-030) — `mayu pricing check --config <path>`: the CI guard against a newly added model silently billing 0 uUSD. Exits 1 listing every configured `(provider, upstream)` with no rate, 0 when all are priced, 2 on a config error. Reuses `live.UnpricedTargets` + `live.PricingTableFor` — the same functions boot validation calls — so the lint can never disagree with what the gateway bills; that shared path is also why it goes through the real config loader, which means CI must set `INFERPLANE_ADMIN_TOKEN` to any non-empty value.
- `mayu/login.go`, `mayu/credentials.go` (ADR-028) — `mayu login`/`token`/`logout`: loopback OAuth2 Authorization Code + PKCE against a SECOND OIDC client distinct from the console's (`gateway.go`'s `cliVerifier`), minting a short-lived virtual key via the new `POST /v1/auth/key`. No IdP refresh token is ever cached — `credentials.go`'s on-disk file (`~/.config/inferplane/credentials.json`, 0600, atomic temp+rename) holds only the current key + its expiry + TOFU-pinned issuer/client_id. `token`'s hot path is network-free (cached key, >5min left); past that it needs either `--id-token-command` (set at login, the only unattended renewal path) or an interactive re-login. `gateway.go`'s `cliVerifier`/`cliAuthConfigView`/`cliKeyTTL` build the server-side wiring from `config.OIDCConfig.CLILogin`.
- `inferplaned/oidcenv.go` (ADR-037) — parses/validates the five fixed `INFERPLANED_OIDC_*` env vars (inferplaned has no config file, the `INFERPLANED_TOKEN` precedent) into `oidcEnv`; rules ported from `internal/config`'s `validateOIDC` since there's no shared config loader to draw from. `.mapping()` projects `INFERPLANED_OIDC_ALLOWED_GROUPS` onto `adminauth.MappingConfig` (admin-equivalent whole-console access — no per-team model on the control plane to map into, v1 scope); `.connectSrc()` returns the CSP widening (issuer origin + `INFERPLANED_OIDC_LOGIN_ORIGINS`), nil when the browser flow is off.
- `inferplaned/main.go` — `buildMux` assembles every route (split out from `run()` so Task 8's fake-IdP suite can drive it via `httptest` without a real listener); `validateBoot` rejects a JWT-shaped `INFERPLANED_TOKEN` (would be mis-routed to the OIDC verifier) and widens the loopback waiver so an SSO-only deploy (empty static token, OIDC configured) is a legitimate non-loopback posture. `INFERPLANED_POLICY_DSN` (ADR-038) — read inside `buildMux`, env-only per the `INFERPLANED_TOKEN` precedent (inferplaned has no config file): when set, policy documents are Postgres-authoritative — `--policies` is required as the one-time seed source (boot fails with a clear message if it's empty), the mtime file watch is NOT started (`run()` guards on `cp.PolicyStoreAttached()`), and the boot attach (seed + first load) is a bounded 10s HARD dependency (`policyStoreBootTimeout` — unlike the lazy usage store, distributing possibly-stale file rules while claiming DB authority is worse than not booting). The store's `Close()` is CHAINED onto `closePG`, never overwriting the usage pool's closer.

## Rules
- `main` owns the metrics sink (`metrics.New()`) and threads it into the audit writer, router, governor, and ingress handlers.
- New providers are registered here by blank import (`_ "…/providers/<name>"`) — this is the only core file a provider PR may touch.
- Subcommand wiring stays thin; real logic lives in `internal/*`. Keep `main` readable as the system's assembly diagram.
- On any config/provider error, fail fast with a wrapped error and non-zero exit.
