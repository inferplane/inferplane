# cmd Module

## Role
Binary entrypoints. Currently one binary: `cmd/inferplane`.

## Key Files
- `inferplane/main.go` — CLI dispatch (`serve` / `keys` / `audit` / `report` / `login` / `token` / `logout`), wiring of keystore + audit + governor + metrics + router + providers, the TLS branch, and graceful shutdown.
- `inferplane/login.go`, `inferplane/credentials.go` (ADR-028) — `inferplane login`/`token`/`logout`: loopback OAuth2 Authorization Code + PKCE against a SECOND OIDC client distinct from the console's (`gateway.go`'s `cliVerifier`), minting a short-lived virtual key via the new `POST /v1/auth/key`. No IdP refresh token is ever cached — `credentials.go`'s on-disk file (`~/.config/inferplane/credentials.json`, 0600, atomic temp+rename) holds only the current key + its expiry + TOFU-pinned issuer/client_id. `token`'s hot path is network-free (cached key, >5min left); past that it needs either `--id-token-command` (set at login, the only unattended renewal path) or an interactive re-login. `gateway.go`'s `cliVerifier`/`cliAuthConfigView`/`cliKeyTTL` build the server-side wiring from `config.OIDCConfig.CLILogin`.

## Rules
- `main` owns the metrics sink (`metrics.New()`) and threads it into the audit writer, router, governor, and ingress handlers.
- New providers are registered here by blank import (`_ "…/providers/<name>"`) — this is the only core file a provider PR may touch.
- Subcommand wiring stays thin; real logic lives in `internal/*`. Keep `main` readable as the system's assembly diagram.
- On any config/provider error, fail fast with a wrapped error and non-zero exit.
