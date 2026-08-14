# ADR-001: Minimal embedded admin key console

**Date:** 2026-06-11
**Status:** Accepted
**Deciders:** maintainers, with a 2-round multi-model consensus gate (codex
gpt-5.5, gemini 3.1-pro) on the implementation plan
**Related:** design spec §9 (roadmap), Appendix A ("UI separation" decision), §5.5 (admin
auth), plan `docs/superpowers/plans/2026-06-11-e2e-gateway-setup-and-admin-ui.md`

## Context

The design spec deliberately keeps UI out of the v0.1 core. Appendix A records the
regret-prevention decision "keep UI out of core (self-service only in v0.2)" — a
full UI in core was rejected as "a frontend-maintenance tax on a small-maintainer
project." The v0.2 roadmap carries "key-issuance self-service page (minimal UI —
log in → issue my key)".

Meanwhile v0.1 operators have only two key-management surfaces: the
token-gated `/admin/keys` JSON API and the local `inferplane keys` CLI.
Operators evaluating the gateway (the "attach it in 5 minutes" pitch) asked
where the admin page is — there is none, and that gap is felt in the first
five minutes.

## Decision

Pull forward **only the minimal key console** from the v0.2 line, with a hard
ceiling on frontend surface:

- One Go package `internal/server/adminui` embedding **three static files**
  (`index.html`, `app.js`, `style.css`) via `go:embed` — no build step, no
  framework, no npm, no toolchain. The assets ship inside the single static
  binary.
- Served on the **admin plane** at `GET /admin/ui/`. The static assets are
  **data-free** (no key material, no team data, nothing secret) and therefore
  served unauthenticated — the same posture as `/metrics` on the same plane
  (§5.5). All data operations go through the existing token-gated
  `/admin/keys` JSON API; the operator pastes the admin Bearer token into the
  page, which holds it **in a JS variable only** (never localStorage, never
  cookies, never the URL).
- The page covers exactly the existing API surface: list keys, create a key
  (plaintext `ik_...` rendered once with a "will not be shown again" warning),
  revoke a key. Nothing else.
- Responses carry a strict CSP (`default-src 'self'`) and
  `X-Content-Type-Options: nosniff`.

## Alternatives considered

1. **Full SPA (React/Vue + npm toolchain).** Rejected — exactly the
   maintenance tax Appendix A predicted: lockfiles, supply-chain surface, a second
   build system in a CNCF-aspiring single-binary Go project.
2. **Server-rendered templates with session/cookie auth.** Rejected — invents
   a second authentication path on the admin plane (sessions, CSRF) where one
   already exists (Bearer token, SHA-256 constant-time). More security surface
   for no capability gain.
3. **Postpone entirely to v0.2 (status quo).** Rejected for the key console
   only — the v0.1 evaluation experience demonstrably stalls at "where do I
   issue a key?". The OIDC-login self-service page (per-user keys) **remains
   v0.2**; this console is the operator view of the API that already ships.

## Consequences

- Frontend tax is capped at three dependency-free static files; any future
  growth of the UI requires a new ADR.
- The unauthenticated surface of the admin plane grows by static assets only;
  the authenticated JSON API is unchanged. The admin plane remains
  cluster-internal by deployment convention (§5.5) — operators exposing
  `:9090` publicly were already exposing `/metrics` and must keep using
  network policy, not obscurity.
- The v0.2 self-service page (OIDC login → my keys) builds on, and does not
  replace, this console.
- Tests enforce the posture: assets contain no secrets and no
  `localStorage`/`document.cookie` usage; `/admin/keys` stays 401 without a
  token after the UI is wired.
