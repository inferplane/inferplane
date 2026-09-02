# Design spec: durable identity + management authorization (strategy Phase 0b)

- Status: **Proposed** — the design-spec input strategy §4 requires before
  Phase 0b implementation begins. Not an ADR yet; the accepted decisions
  here should land as ADR-044 (identity) and ADR-045 (roles + mutation
  audit) when the gate passes.
- Date: 2026-09-02
- Owns: the two P0 contract rows "Durable identity" and "Duty separation"
  ([enterprise-strategy.md](../enterprise-strategy.md) §2), which block the
  enterprise-ready claim and are the largest remaining competitive gap on
  the authentication axis ([comparison.md](../comparison.md) §7).

---

## 1. Problem

**Identity is not durable.** A person's identity in inferplane today is
`keystore.Principal.Owner` — a free-form string. The CLI login path
(ADR-028, `internal/server/authapi/authapi.go`) sets it to the bare OIDC
`subject`; admin-issued keys carry whatever the admin typed; CLI-minted
(`mayu keys create`) keys carry nothing. Consequences, all observed in the
strategy's P0 list:

- Two IdPs (or an IdP migration) can collide two different people on one
  `sub` string, merging their budgets and audit trails.
- An admin typo (`jdoe` vs `j.doe`) silently splits one person's budget
  across two counters — per-user budget (ADR-042 Phase 3) keys its counter
  on `team + "/" + Owner`.
- Key rotation and a second device keep identity **only** for CLI-login
  keys, and only within one IdP; admin-issued keys re-minted with a
  different owner string reset every per-user window.
- Audit and usage telemetry attribute to the free-form string, so "what
  did this person spend" is answerable only as "what did keys with this
  exact owner string spend."

**Management authorization is coarse.** Any accepted OIDC identity (or the
static token) holds whole-console authority on the control plane; policy
`PUT`/`DELETE` sits behind that same single layer with no mutation audit
(ADR-038 accepted limitation). The mayu admin plane has full-admin vs
team-member from group mapping (ADR-004/016), but no duty separation —
provider writes, pricing, budget, and audit reads are one capability.

## 2. Contracts (restated acceptance bar, strategy §5)

- Two users in one team keep independent balances; one user on two devices
  is one ledger; a CLI key re-mint resets no budget, quota, or audit
  identity.
- A forbidden admin action is denied AND evidenced; every
  policy/provider/budget/role mutation records actor, capability, scope,
  before/after hash, and generation.

## 3. Design — durable identity (ADR-044 candidate)

### 3.1 The identity type

`UserID = (issuer, subject)`, canonical string form `issuer + "#" + subject`
(`https://idp.corp.example#a1b2c3`). `#` cannot appear in an https issuer
URL (it would start a fragment, which OIDC issuers must not carry) and is
opaque inside `sub`, so the FIRST `#` is an unambiguous split point.
Email, display names, and key owner strings are labels, never identities.

### 3.2 Where identity is minted

| Path | Today | After |
|---|---|---|
| CLI login (ADR-028, `POST /v1/auth/key`) | `Owner = sub` | `UserID = (verified issuer, sub)`; `Owner` stays as display label (unchanged wire) |
| Admin console key issuance (OIDC caller) | `Owner` free string, overridden to caller's own subject for non-admins | non-admin: `UserID = caller's (issuer, sub)`; full-admin MAY set another user's UserID explicitly (provisioning), audited |
| `mayu keys create` (CLI, local trust) | no owner | optional `--user-id 'iss#sub'`; absent = no user identity (a service key), never a fabricated one |
| Declarative `virtual_keys` (ADR-023) | no owner | optional `user_id` field, same semantics |

### 3.3 Storage and enforcement

- `keys` table gains a `user_id TEXT NOT NULL DEFAULT ''` column
  (ALTER-if-missing, the `budget_usd_micros_per_day` precedent);
  `Principal` gains `UserID string`.
- `governance.Subject.User` carries the canonical UserID when present, and
  falls back to `Owner` when empty — **bounded compat**: the fallback is
  for pre-migration keys only, logged once per key, and removed one minor
  release later. A user's budget window therefore survives rotation the
  moment both old and new keys carry the same UserID.
- Policy `subject.user` matching: a GovernancePolicy may name either the
  canonical `iss#sub` (exact) or a bare `sub` (matches any issuer —
  operator convenience, explicitly weaker, documented). `policy.Store`
  user lookups try exact first.
- Audit `PrincipalRef` gains `user_id` (appended at end, omitempty — the
  ModelSubstitutedFrom rule); usage telemetry entries carry it beside the
  display owner.

### 3.4 What deliberately does NOT change

- Virtual keys stay SHA-256-hashed bearer secrets; identity binds at mint
  time, never per request (no per-request IdP calls on the data path —
  Core Purpose #5).
- Teams remain the enforcement scope for rate/lease; UserID scopes only
  user-subject rules and attribution.

## 4. Design — duty separation + mutation audit (ADR-045 candidate)

### 4.1 Fixed roles

`platform-admin` (everything, incl. roles), `policy-admin`,
`provider-admin`, `budget-admin`, `auditor` (read audit/logs/debug only),
`team-admin` (scoped to named teams: keys + team record). No custom
authorization language (strategy non-goal). The static admin token is
`platform-admin` break-glass, unchanged.

### 4.2 Role source

OIDC groups → roles via an extended `adminauth.MappingConfig`
(`role_mappings: {"grp-llm-platform": ["platform-admin"], ...}`), the same
groups claim already verified for team mapping — no new token shape, no
session store. inferplaned reuses the identical mapping (its env-var
config gains `INFERPLANED_OIDC_ROLE_MAPPINGS`).

### 4.3 Enforcement point

One capability-check middleware wrapping each management route CLASS
(keys, teams, providers+pricing, policies, audit-read, debug-read), on
both planes. Every deny is an audited 403 with the capability named
(`adminDenialEmitter` precedent). Absent any role mapping config, every
authenticated identity keeps today's authority — roles are opt-in, so
existing deployments are byte-identical until configured.

### 4.4 Mutation audit

Every management WRITE (policy put/delete, provider/model write, pricing
sync, team upsert/delete, key issue/revoke, role-mapping change) appends
an `admin_mutation` audit record: actor UserID, capability, scope,
`sha256(before)`/`sha256(after)` of the canonical JSON of the touched
object (hashes, not bodies — no secret can leak through refs-only objects,
but hashing keeps records small and diffable against exports), and the
resulting generation where one exists. Rides the existing hash chain; on
inferplaned (no audit writer today) a minimal append-only JSONL writer
reuses `internal/audit`'s record shape.

## 5. Phasing (each = one PR, reviewed)

1. **0b-1 identity capture:** UserID type + keys column + Principal +
   CLI-login/admin mint paths + audit/telemetry attribution. Acceptance:
   re-mint keeps the user budget window (extends the ADR-042 test suite).
2. **0b-2 enforcement switch:** `Subject.User` = UserID with bounded Owner
   fallback; policy exact-match; `/v1/usage` user fields keyed by UserID.
3. **0b-3 roles + capability middleware** (both planes), opt-in via
   mapping config; negative authorization tests per role × route class.
4. **0b-4 mutation audit** + console surfacing.

## 6. Risks / open questions for the gate

- **Counter migration:** a user's existing spend is keyed by Owner; after
  0b-2 new spend keys by UserID. Options: (a) accept a one-time window
  split at rollout (simple, bounded to one window), (b) dual-read old key
  during the first window (complex). Proposal: (a), documented in the
  release notes — budget windows reset monthly anyway.
- **Issuer normalization:** trailing slash / case. Proposal: store the
  issuer EXACTLY as verified by go-oidc (which already requires exact
  match to the discovery document) — no normalization of our own.
- **inferplaned mutation audit durability** shares the ADR-038 open
  question (its policy write path currently has no audit at all); 0b-4
  may land control-plane-side first since that's where the P0 names it.
