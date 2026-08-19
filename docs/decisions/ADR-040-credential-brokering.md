# ADR-040: Credential brokering — inferplaned vends short-lived Bedrock credentials to mayu

**Date:** 2026-08-18
**Status:** Accepted — design gate passed (3-AI panel: Kiro/Codex/Agy, 3
rounds, 10 findings fixed, final round unanimous PASS with zero
CRITICAL/MAJOR); implementation deferred to a follow-up plan
**Related:** ADR-031 §8 (the original sketch: "Credentials never rest in the
data plane. The control plane … brokers short-lived credentials (e.g. STS
AssumeRole), which also anchors per-user attribution at issuance time"),
ADR-034 (the shared-bearer-token channel auth this design inherits, and its
"mTLS/OIDC-brokered channel auth is future work alongside credential
brokering" deferral), ADR-036 (the separate-channel precedent this endpoint
copies), ADR-037 (the OIDC console branch this endpoint must exclude),
ADR-038 §7 (the same shared-identity limitation, accepted the same way)

## Context

Today every `mayu` instance calls Bedrock with its **own local AWS
identity**: `providers/bedrock/client.go` loads the default credential chain
(`config.LoadDefaultConfig` — env, shared config, IRSA, IMDS) once per
provider construction, and `inferplaned` has zero aws-sdk imports. The
control plane distributes policy and budget leases; it never touches
credentials. This has two costs:

1. **Bypass is only discouraged, never prevented.** The IAM permission that
   lets mayu call Bedrock (`bedrock:InvokeModel*`) sits on the developer
   machine / node itself — so the same permission lets a client skip
   `localhost:8081` entirely and call Bedrock directly, outside every
   quota, budget, and audit control inferplane exists to enforce. The
   governance model is "point your `ANTHROPIC_BASE_URL` at mayu, please."
2. **Per-node IAM toil.** Every node that runs mayu needs its own
   role/profile provisioned with Bedrock permissions, and rotating or
   revoking access means touching every node's identity.

Centralizing the credential and vending short-lived STS sessions inverts
both: the `bedrock:Invoke*` permission moves to a single broker role only
`inferplaned` can assume, nodes need **no** Bedrock permissions of their
own, and a client that bypasses mayu has no way to obtain credentials at
all. Enforcement moves from convention to IAM — **on hosts where mayu's
environment is not readable by its users** (K8s nodes, dedicated proxy
hosts). On a developer-owned machine running mayu locally, the developer
can read `broker_token_ref`'s secret out of mayu's environment and mint
credentials directly; there, brokering still removes the standing node
IAM grant but bypass prevention requires the trusted-host topology. The
claim is scoped accordingly throughout this ADR.

### Verified constraints this design is built on

- **1-hour hard cap.** inferplaned runs on ECS; its task role
  (`inferplane-demo-TaskRole-…`) is itself an assumed role, so
  broker-role `sts:AssumeRole` from it is **role chaining** — AWS caps
  chained sessions at 3600s regardless of the role's configured maximum.
  Renewal cadence must live inside that cap.
- **The refresh seam already exists in the SDK, not in our code.** The
  repo has zero credential-cache/injection code (no `CredentialsCache`,
  `StaticCredentialsProvider`, or `stscreds` anywhere); the bedrock client
  is built once per `BuildState` (boot, SIGHUP reload, UI-write validation
  — `cmd/mayu/gateway.go:279/:829/:890`) and never on the request path.
  `aws.CredentialsProvider` is consulted at signing time, so a rotating
  source needs no client re-construction.
- **`auth.mode` is an open slot.** `client.go` branches only on
  `"profile"`; `irsa`/`pod_identity`/`static`/`default` all fall through
  to the same `LoadDefaultConfig`, and `"static"` exists only in a
  comment. Adding `"broker"` collides with nothing.
- **Channel-separation precedent.** ADR-036's usage push is a separate
  endpoint + goroutine + bounded buffer + HTTP client that shares only the
  bearer token with the sync heartbeat. Same split applies here, for the
  same reason: different lifecycle (10s heartbeat vs ≤1h credentials),
  different blast radius.
- **The OIDC console branch must be excluded.** `internal/controlplane/
  auth.go`'s `authn` grants whole-console access to both the static token
  AND any verified OIDC identity (ADR-037). Mounting a credentials
  endpoint behind plain `authn` would let a browser SSO session mint AWS
  credentials — a machine-channel secret reachable from a web login.
- **Dataplane identity is self-reported.** `handleSync` checks only that
  `dataplane` is non-empty; any holder of the shared token can claim any
  dataplane id (and already can overwrite that id's ledger rows). The
  default id is per-boot (`host + ULID`, `gateway.go:1406-1412`).
- **`sts` is already in go.mod** (v1.43.3, indirect) — promoting it to a
  direct dependency downloads nothing new. inferplaned's env-only config
  surface (9 vars, no config file) has an established opt-in pattern
  (`INFERPLANED_POLICY_DSN`, ADR-038).

### Why only Bedrock (v1)

| Provider | Short-lived credentials | v1 |
|---|---|---|
| AWS Bedrock | STS AssumeRole — native | ✅ |
| 1P Anthropic (api.anthropic.com) | **No temporary-token mechanism exists** — only long-lived API keys. Centrally storing and *distributing* a static key is a different feature with none of the short-lived security benefit, and is out of scope here. | ❌ |
| GCP Vertex | SA impersonation / STS token exchange would work, but no GCP provider exists in the tree | ❌ (interface slot left open) |

## Decision

### 1. A dedicated endpoint, machine-channel only: `POST /v1alpha1/credentials`

Mounted on inferplaned **only when `INFERPLANED_BROKER_ROLE_ARN` is set**
(the ADR-038 opt-in pattern: unset ⇒ route absent ⇒ 404; default behavior
byte-identical). Request `{dataplane, provider: "bedrock"}` (1 MiB
`MaxBytesReader`, the sync precedent); response
`{accessKeyId, secretAccessKey, sessionToken, expiration}`.

Auth is a **dedicated broker token, not `INFERPLANED_TOKEN`**: a new
`INFERPLANED_BROKER_TOKEN` env var on inferplaned, required whenever the
role ARN is set, and **rejected at boot if it equals `INFERPLANED_TOKEN`**
(the ADR-028 distinct-client-id rule, applied to tokens). The mayu side
gets the matching wiring the first draft forgot: a new
`control_plane.broker_token_ref` secret ref (env/file, never inline — the
house rule), rejected at config load if it resolves to the same value as
`control_plane.token_ref`.

**Broker mode requires an https (or loopback) control-plane URL.** The
existing heartbeat channel deliberately tolerates plain `http://`
(`validateControlPlane` — policy documents and lease numbers), and the
live demo runs that way today. Credentials change the calculus: an STS
triplet on the wire in plaintext is a credential grant to the network
path. Config load rejects `auth.mode: "broker"` when `control_plane.url`
is non-loopback `http://`. The panel review was blunt and
right about why: the heartbeat token already sits in env on every node and
grants policy reads — if the same token minted portable AWS credentials,
compromising any one node's env would yield Bedrock access *without mayu*,
re-opening the exact bypass this feature exists to close. A distinct secret
means heartbeat-token compromise stops at policy, and the broker secret can
be scoped to fewer places. The route additionally rejects OIDC-authenticated
callers with 403 — a browser console session must never be able to mint AWS
credentials. This is a machine channel, like the usage push.

**Secret hygiene is a stated requirement, not an implication:** the mayu
fetcher and the inferplaned handler never log credential fields, never wrap
an upstream response body into a returned error (`%w` on an STS error is
fine; embedding the HTTP body is not), and the retry path stores no response
bytes — the same rule `internal/policystore` already applies to DSN-bearing
pgx errors. The rejection of heartbeat piggybacking above ("never audited
for secret hygiene") applies with equal force to the new channel and is
discharged by these requirements plus tests that grep the error/log paths.

Not on the heartbeat, deliberately: credentials have a different lifecycle
(≤1h vs 10s cadence), a different security class (signable secrets vs
policy documents), and piggybacking them on sync would put a secret in
every heartbeat response body. ADR-036 already established the
separate-channel shape.

### 2. inferplaned brokers via STS AssumeRole

- New env `INFERPLANED_BROKER_ROLE_ARN` names the broker role, which holds
  `bedrock:InvokeModel` / `bedrock:InvokeModelWithResponseStream` (and
  nothing else). IAM wiring is two-sided and the trust-policy half is easy
  to miss: inferplaned's task role gains `sts:AssumeRole` +
  `sts:TagSession` + `sts:SetSourceIdentity` on that one role ARN in its
  identity policy, **and the broker role's trust policy must allow those
  same three actions for the task-role principal** — `sts:TagSession` /
  `sts:SetSourceIdentity` denied in the trust policy fail the whole
  AssumeRole, not just the tag.
- **Recommended (runbook, not enforceable by inferplane): condition the
  broker role's Bedrock permissions on `aws:SourceIp` / `aws:SourceVpce`**
  so an exfiltrated credential triplet is dead on arrival outside the
  network — the single cheapest mitigation for the portable-credentials
  risk below.
- Per grant: `RoleSessionName` and `SourceIdentity` = the dataplane id
  **sanitized** to the STS constraint (64 chars, `[\w+=,.@-]` — the
  default id is `host-ULID` and can exceed it); session tag
  `dataplane=<id>`. `DurationSeconds` ≤ 3600 (the chaining cap).
- `SourceIdentity` survives further chaining and lands in CloudTrail —
  the issuance-time attribution anchor ADR-031 §8 named. **Caveat:** if
  the task-role session already carries a SourceIdentity (set by the
  orchestrator), AWS forbids changing it on the next hop — the broker
  probes this at boot and, when inherited, propagates it and falls back
  to session tags alone for the dataplane axis.
- Operators who want a stable audit trail across mayu restarts set
  `control_plane.dataplane` explicitly (runbook note); the per-boot
  default is harmless but noisy in CloudTrail.

### 3. mayu opts in with `auth.mode: "broker"` and a neutral CredentialSource

- `providers.Config` gains one field (the `HTTPClient` precedent — a
  documented, narrow, core→provider injection):

  ```go
  // CredentialSource supplies rotating upstream credentials to providers
  // that opt in (auth.mode "broker"). Provider-neutral on purpose: no AWS
  // types cross the core/provider boundary.
  type CredentialSource interface {
      Credentials(ctx context.Context) (id, secret, session string, expires time.Time, err error)
  }
  ```

- In `providers/bedrock`, `authMode == "broker"` wraps the source in
  `aws.CredentialsCache(aws.CredentialsProviderFunc(...))` and passes it
  via `config.WithCredentialsProvider`. The cache refreshes before expiry
  automatically; the once-per-BuildState client construction is untouched
  and rotation never requires a reload.
- The fetching implementation lives in `internal/proxy` (a sibling of the
  Syncer and UsagePusher, sharing the control-plane URL/token config);
  `cmd/mayu/gateway.go` threads the source into provider construction.
  `internal/proxy` stays disconnected from `internal/live`'s import graph
  — the wiring point is the gateway, same as everything else.
- **Fail-closed is a testable invariant, not an implication.** Two
  requirements the panel demanded be stated, because either omission
  silently fails open into the node's ungoverned local credentials:
  1. **Broker mode never falls back to the default credential chain.**
     `authMode == "broker"` constructs the client with the injected
     source or fails construction — no code path reaches
     `LoadDefaultConfig`'s chain. A brokered provider that cannot get
     credentials returns errors; it never quietly signs with node IAM.
     (BuildState failing on boot/reload is the correct behavior — same
     posture as any other invalid provider config.)
  2. **Unknown `auth.mode` values are rejected at config load.** Today
     every unrecognized mode falls through to the default chain, so a
     typo ("brokre") would silently deploy the bypassable posture this
     feature exists to remove. This ADR adds mode validation
     (`irsa|pod_identity|profile|static|default|broker`) — an
     independent pre-existing gap this feature makes dangerous.
     **Migration note:** configs that relied on the silent fall-through
     (any unlisted string quietly meaning "default") now fail boot —
     a breaking change for typo'd configs only, called out in the
     changelog.
  3. **`auth.mode: "broker"` without a `control_plane` block is a
     config-load error** — the fetcher has no URL or token otherwise;
     leaving this implied would let it degrade into rule 1's territory.
  4. **The initial credential fetch is eager, not lazy.** Wrapping the
     source in `aws.CredentialsCache` alone defers the first fetch to
     request-signing time, which would let a BuildState with a bad
     broker token or unreachable broker "succeed" at boot/SIGHUP and
     fail only on user traffic. Provider construction performs one
     blocking `Retrieve()`; failure fails BuildState. On reload that is
     exactly ADR-006's rollback semantics — the old topology stays live
     and the bad config is rejected loudly instead of half-applied.
     Accepted cost: every BuildState — including each UI-write
     validation (`gateway.go:890`) — mints a fresh STS session; the
     implementation may cache the fetcher across rebuilds to damp that
     churn, as long as the eager-validation property is preserved.
  With those two held: broker unreachable ⇒ cached credentials remain
  valid until expiry (≤1h) ⇒ after expiry, Bedrock signing fails and
  requests error — the same philosophy as hard-cap leases. An operator
  who cannot accept a ≤1h control-plane dependency keeps using the local
  IAM modes, which remain the default and are unchanged.

### 4. Accepted limitation (v1): one broker token, self-reported dataplane ids

Any holder of `INFERPLANED_BROKER_TOKEN` can request credentials and claim
**any** dataplane id. The panel rejected the first draft's framing of this
as "the same limitation ADR-038 §7 accepted" — it is worse, and the record
should say so plainly:

- ADR-038's token wrote policy documents inside the system; this one mints
  **portable AWS credentials usable without mayu** — a holder skips quota,
  budget, and inferplane's audit entirely. The distinct-token requirement
  (decision 1) keeps the widely-deployed heartbeat token out of that blast
  radius, and the `aws:SourceIp`/`aws:SourceVpce` conditions (decision 2)
  keep exfiltrated credentials from working off-network, but within the
  network a broker-token holder has ungoverned Bedrock access for ≤1h per
  mint.
- Because the dataplane id is caller-chosen, `SourceIdentity`/session-tag
  attribution is **frameable**: an attacker with the token can stamp an
  innocent dataplane's id on their sessions. CloudTrail attribution under
  v1 is therefore "which id was claimed," never "which machine called."

What v1 buys despite this: the *legitimate* fleet's nodes carry zero
Bedrock permissions, developers **on trusted-host deployments** (where
mayu's env is not theirs to read) cannot bypass mayu, and the secret that
matters is one revocable env var instead of N node roles. On
developer-owned machines the broker token is readable by the developer —
brokering there is an IAM-hygiene win, not a bypass control (see
Context).
Per-dataplane identity (bootstrap tokens, or authenticating the channel
with the node's own IAM identity) is the v2 follow-up that closes the
impersonation gap; it converges with roadmap item ②'s `dataplanes` table.

## Non-goals (v1)

- **1P Anthropic key brokering** — no temporary-token mechanism exists;
  distributing static keys centrally is a different feature.
- **GCP Vertex** — no provider in the tree; the neutral
  `CredentialSource` leaves the slot open.
- **Per-team credentials** (session policies narrowing model ARNs per
  team — attractive because it would double-enforce `modelAccess` at IAM
  level) — the bedrock client is one-per-provider, so per-team
  credentials mean per-team clients: a real structural change, recorded
  as the v2 candidate it is.
- **Forcing local-IAM removal** — stripping `bedrock:*` from
  developer/node roles is the operator's infrastructure change that makes
  bypass-prevention real; inferplane enables it, a runbook documents it,
  the product cannot do it for you.

## Alternatives considered

1. **Carry credentials on the sync heartbeat.** Rejected — puts a
   signable secret in every 10s response, couples a ≤1h artifact to a 10s
   cadence, and heartbeat handling code paths (retry, logging) were never
   audited for secret hygiene.
2. **`sts:GetFederationToken` instead of AssumeRole.** Rejected — not
   callable from an assumed role (inferplaned's task role is one), and it
   cannot use role session policies as cleanly.
3. **Pre-created per-team IAM roles.** Rejected for v1 — role-per-team
   multiplies IAM objects to manage and still needs per-team clients in
   mayu; the single-role + session-tags design gets attribution now and
   leaves session-policy narrowing as a compatible v2.
4. **mTLS / per-dataplane identity first.** Deferred, not rejected — it
   is the right v2, but requiring it first would block the bypass-closing
   benefit on a second large feature. ADR-038 set the precedent for
   shipping behind the shared token with the limitation recorded.

## Consequences

- Bedrock access can be revoked fleet-wide by disabling one role; node
  IAM shrinks to zero Bedrock permissions.
- New hard dependency **for opted-in providers only**: a mayu whose
  bedrock provider uses `auth.mode: "broker"` stops working ≤1h after the
  control plane becomes unreachable. All other auth modes are untouched;
  the config default remains local IAM.
- inferplaned takes its first aws-sdk dependency (`sts`, already in
  go.mod as indirect) and, when the env var is set, must itself run with
  an AWS identity allowed to assume the broker role.
- The shared-token limitation of ADR-034/038 now also covers credential
  issuance — recorded above, resolved by the v2 identity work.
- CloudTrail gains per-dataplane session attribution for every Bedrock
  call made through brokered credentials — an audit surface inferplane's
  own hash-chain cannot provide (it ends at mayu; CloudTrail covers the
  provider side). Under v1 that attribution is claimed-id, not proven-id
  (decision 4).
- **Brokered sessions carry unrestricted `bedrock:Invoke*` across all
  models** — per-team session policies are the v2 that would narrow them.
  Until then, a credential holder outside mayu is bound by the network
  conditions (decision 2) but not by any model allow-list; inside mayu,
  `modelAccess` policy enforcement is unchanged and unaffected.
- Two new env vars on inferplaned (`INFERPLANED_BROKER_ROLE_ARN`,
  `INFERPLANED_BROKER_TOKEN`) and one new config value on mayu
  (`auth.mode: "broker"`), plus auth-mode validation that tightens a
  pre-existing silently-ignored field.
