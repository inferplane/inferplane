# Security

### 1. Overview
Cross-cutting security: virtual-key authentication, team RBAC, key/secret isolation,
inline-secret rejection, optional self-TLS, and metrics that never leak secrets. These
are non-negotiable invariants (see CLAUDE.md → Security mandates).

### 2. Components
| Component | Path | Purpose |
|---|---|---|
| Data-plane auth | `internal/server/auth.go` | `KeyAuth` resolves `ik_...` → Principal |
| Admin auth | `internal/server/adminauth.go` | `AdminAuth`: static break-glass tokens + OIDC ID tokens on one Bearer header (ADR-004); total shape-predicate routing, 401 vs 403, denial audit |
| OIDC verify | `internal/adminauth/` | shared `IsOIDCBearerShape`, groups→team `Resolve`, go-oidc verifier (alg pin, aud/azp, ±60s skew, JWKS negative cache) |
| Console SSO (SPA=public client) | `internal/server/adminui/static/app.js` | ADR-026: OAuth2 Authorization Code + PKCE runs in the browser; gateway stays a resource server. `sessionStorage` holds ONLY `ip_sso_verifier`/`ip_sso_state`/`ip_sso_nonce` (cleared on every callback exit); id_token is memory-only. Callback follows RFC 6749 §4.1.2.1 (state-before-error, textContent-only error, replaceState-before-exchange, nonce check); discovered endpoints must be https. Opt-in via `oidc.login_origins` (empty ⇒ CSP byte-identical, SSO hidden) |
| Console CSP connect-src | `internal/server/adminui/adminui.go` + `cmd/mayu/gateway.go` (`ssoConnectSrc`) | ADR-026: when `login_origins` set, `connect-src 'self' <issuer-origin> <login_origins…>` (issuer path stripped so it can't break the source expression); script/style stay `'self'`. Empty ⇒ `default-src 'self'; frame-ancestors 'none'` unchanged |
| CLI login (loopback PKCE) | `cmd/mayu/login.go`, `internal/server/authapi/` | ADR-028: `mayu login` runs Authorization Code + PKCE against a DISTINCT OIDC `client_id`/`Verifier` from the console's (never share — the console's is a secretless public client); every gateway/issuer URL validated https-or-loopback, discovered endpoints same-origin-or-subdomain of the issuer; issuer/client_id TOFU-pinned into the credential file, silent change refused without `--reset`. No IdP refresh token is ever cached (`credentials.go`) — a stored refresh token would be a stronger, gateway-unrevocable credential than the `ik_` key it protects. `POST /v1/auth/key` decides `expires_at`/`owner` server-side only, rate-limited per subject |
| Config view | `internal/server/configapi/` | secret-free topology projection (ADR-005): view type cannot hold a secret; auth string from ref name / IAM mode only, never the resolved key |
| RBAC | `internal/keystore/keystore.go` | `Principal.Allows()` (team + allowed models) |
| Cross-model fallback RBAC re-check | `internal/router/router.go` (`FilterModelAllowed`) | ADR-029/D5: `ResolveChain` appends a model_fallbacks target's provider chain AFTER the ingress allow-list check already ran against the originally requested model — every ingress handler re-checks those appended targets' model with `FilterModelAllowed` before ever sending a request there, or a key allowed only model A would silently reach fallback model B. A key allowed only the unconfigured requested name is denied outright (fail-closed), never silently downgraded to a configured fallback |
| Key hashing | `internal/keystore/sqlite.go` | SHA-256 at rest; plaintext shown once (or, for a declaratively-bootstrapped key, ADR-023, referenced via `virtual_keys[].key_ref` — never inline, same §7 posture as a provider's `api_key_ref`) |
| TLS validation | `internal/server/tls.go` | rejects half-specified cert/key pairs |
| Secret refs | `internal/config/config.go` | `env:`/`file:`/`secret:` only; inline `api_key` rejected |
| Metrics safety | `internal/metrics/metrics.go` | no `key_id`/secret labels; `_rejected` sentinel bounds cardinality |

### 3. Key Decisions
- The gateway never forwards the client key upstream and never exposes its upstream keys to clients (§5.2).
- `/metrics` is unauthenticated but carries no secret or `key_id` and bounds label cardinality.
- Pre-resolution 403/404 paths use a sentinel model label so attacker-supplied model strings can't explode Prometheus series.
- ADR-029 model-level fallback fails closed on RBAC: a key allowed only the requested (unconfigured) model name is 403'd, never silently served by its fallback model; a cross-model fallback target appended after the allow-list check is re-checked via `FilterModelAllowed`.

### 4. Code Pointers
- `internal/server/auth.go` — virtual-key auth, empty-key bypass guard
- `internal/config/config.go` — secret-ref resolution + inline-secret rejection
- `internal/server/anthropicapi/messages.go` / `openaiapi/chat.go` — `_rejected` label on 403/404

### 5. Cross-references
- Related modules: `internal/keystore`, `internal/audit`, `internal/metrics`
- Related ADRs: docs/decisions/ADR-004-oidc-admin-authz.md, docs/decisions/ADR-026-console-sso-login.md, docs/decisions/ADR-028-cli-oidc-login-short-lived-keys.md, docs/decisions/ADR-029-model-level-fallback.md
- Related runbooks: docs/runbooks/ ; docs/runbooks/cli-login.md ; policy in `SECURITY.md`
