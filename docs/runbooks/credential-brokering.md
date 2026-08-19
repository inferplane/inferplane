# Runbook: credential brokering (ADR-040)

Moves `bedrock:Invoke*` off developer/node IAM entirely: `inferplaned` holds
one STS broker role and vends ≤1-hour session credentials to each `mayu` over
`POST /v1alpha1/credentials`; mayu's bedrock provider opts in with
`auth.mode: "broker"`. On hosts where mayu's environment is not readable by
its users (K8s nodes, dedicated proxy hosts), bypassing mayu then yields no
Bedrock credentials at all. On a developer-owned machine the developer can
read the broker token out of mayu's env — there this is an IAM-hygiene win
(no standing node grant), not a bypass control. See ADR-040 for the full
threat model.

## 1. IAM wiring (do this first — it is two-sided)

Create the **broker role** (example name `inferplane-bedrock-broker`):

Identity policy — Bedrock invoke only, ideally network-conditioned:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": ["bedrock:InvokeModel", "bedrock:InvokeModelWithResponseStream"],
    "Resource": "*",
    "Condition": {
      "IpAddress": { "aws:SourceIp": ["<your corporate/VPC CIDRs>"] }
    }
  }]
}
```

> **Strongly recommended:** the `aws:SourceIp` (or `aws:SourceVpce` for VPC
> endpoints) condition makes an exfiltrated credential triplet dead on
> arrival outside your network — the cheapest mitigation for the
> portable-credentials risk ADR-040 accepts. Without it, anyone holding a
> minted triplet has Bedrock access from anywhere for up to 1 hour.

Trust policy — **must allow all three actions**, or every AssumeRole fails
(denying `sts:TagSession`/`sts:SetSourceIdentity` in the trust policy fails
the whole call, not just the tag):

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": { "AWS": "arn:aws:iam::<account>:role/<inferplaned task role>" },
    "Action": ["sts:AssumeRole", "sts:TagSession", "sts:SetSourceIdentity"]
  }]
}
```

Then add to **inferplaned's task role** the matching identity-policy grant:

```json
{
  "Effect": "Allow",
  "Action": ["sts:AssumeRole", "sts:TagSession", "sts:SetSourceIdentity"],
  "Resource": "arn:aws:iam::<account>:role/inferplane-bedrock-broker"
}
```

Session lifetime is capped at **1 hour regardless of the role's configured
maximum** — inferplaned's task role is itself an assumed role, so this is
STS role chaining. Do not raise the role's `MaxSessionDuration` expecting
longer broker sessions; it has no effect on chained sessions.

## 2. Enable it on inferplaned

Two env vars (inferplaned has no config file):

```bash
INFERPLANED_BROKER_ROLE_ARN=arn:aws:iam::<account>:role/inferplane-bedrock-broker
INFERPLANED_BROKER_TOKEN=<long random secret — NOT the value of INFERPLANED_TOKEN>
```

| Var | Required | Notes |
|---|---|---|
| `INFERPLANED_BROKER_ROLE_ARN` | opt-in switch | Unset ⇒ `POST /v1alpha1/credentials` does not exist (404); behavior is byte-identical to before ADR-040. |
| `INFERPLANED_BROKER_TOKEN` | when the ARN is set | Boot fails if missing, if JWT-shaped, or if **equal to `INFERPLANED_TOKEN`** — the heartbeat token sits in every node's env and grants policy reads; sharing it would turn any one node compromise into a Bedrock grant without mayu. Keep this secret in strictly fewer places than the heartbeat token. |

The endpoint authenticates with the broker token ONLY. A console SSO
session (OIDC bearer) is rejected with 403 — a browser must never be able
to mint AWS credentials.

## 3. Enable it on mayu

```json
{
  "control_plane": {
    "url": "https://inferplaned.example.com",
    "token_ref": {"env": "INFERPLANED_TOKEN"},
    "broker_token_ref": {"env": "INFERPLANE_BROKER_TOKEN"},
    "dataplane": "build-farm-node-07"
  },
  "providers": {
    "bedrock-global": {
      "type": "bedrock",
      "region": "ap-northeast-2",
      "auth": {"mode": "broker"}
    }
  }
}
```

Config load rejects, with a clear message, any of:
- `auth.mode` outside `default|irsa|pod_identity|profile|static|broker`
  (previously any string silently meant "default" — a typo now fails boot
  instead of silently deploying the bypassable local-IAM posture);
- `broker` without a `control_plane` block or without `broker_token_ref`;
- `broker_token_ref` resolving to the same value as `token_ref`;
- `broker` with a **non-loopback plain-http** `control_plane.url` —
  credentials must not transit plaintext (the heartbeat tolerates http;
  this channel does not).

**Set `control_plane.dataplane` explicitly.** The default id is
per-boot (`hostname-ULID`), so every mayu restart starts a new CloudTrail
session-name trail. A stable id gives you a stable audit line:
CloudTrail's AssumeRole events carry `RoleSessionName`, `SourceIdentity`,
and a `dataplane` session tag, all set to the sanitized id.

## 4. Make bypass prevention real (the part inferplane cannot do for you)

Remove `bedrock:InvokeModel*` from the node/developer roles that previously
carried it. Until you do, brokering only *adds* a path — the old direct
path still works and can still be used to skip mayu. After you do, the only
Bedrock credentials in the fleet are ≤1h broker sessions, revocable
fleet-wide by disabling one role.

## 5. Failure modes

| Symptom | Meaning / fix |
|---|---|
| mayu boot/SIGHUP fails: `credential source ... Retrieve` | The eager first fetch failed — broker unreachable, wrong broker token, or inferplaned's ARN/permissions wrong. This is fail-closed by design: a bad broker config never silently falls back to node IAM. On SIGHUP the OLD topology stays live (ADR-006 rollback). |
| Bedrock calls start failing ~1h after a control-plane outage | Cached session expired and no renewal is possible. Same posture as hard-cap leases: no control plane ⇒ (eventually) no credentials. Restore inferplaned; mayu recovers on the next signing. |
| inferplaned boot fails: broker token missing/equal/JWT-shaped | See §2 table. |
| 502 from `/v1alpha1/credentials` with a fixed body | STS AssumeRole failed server-side; the real error (which can name the role ARN) is in inferplaned's log, deliberately not in the response. Check the two-sided IAM wiring in §1 — the most common miss is `sts:TagSession`/`sts:SetSourceIdentity` absent from the broker role's **trust** policy. |
| CloudTrail shows sessions without `SourceIdentity` | inferplaned's own task-role session already carried an inherited `SourceIdentity` (AWS forbids changing it on the next hop). The broker retried without it; the `dataplane` session tag still attributes the session. |
| Sessions in CloudTrail under an id you don't recognize | v1 limitation (ADR-040 §4): the dataplane id is claimed by the caller, and any broker-token holder can claim any id. Attribution is "which id was claimed," not "which machine called" — per-dataplane identity is the v2 follow-up. Rotate `INFERPLANED_BROKER_TOKEN` if you suspect misuse. |

## What this does NOT do

- Broker 1P Anthropic API keys — no temporary-token mechanism exists for
  api.anthropic.com; that channel keeps using local `env:`/`file:` refs.
- Restrict which models a brokered session may invoke — v1 sessions carry
  the broker role's full `bedrock:Invoke*`. Model allow-lists are still
  enforced by mayu's policy layer for traffic that goes through mayu;
  per-team session policies at the IAM layer are the v2 candidate.
- Prove which machine called — see the last failure-mode row.
