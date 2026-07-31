# ADR-033: Local GovernancePolicy file channel (standalone mode)

## Status

Accepted (2026-07-30)

## Context

ADR-031/032 fixed the `api/v1alpha1` GovernancePolicy schema and its three
delivery channels: K8s CRD, local file, and inferplaned push. This ADR lands
the **local file channel** — the workstation "edit the YAML, it's live"
loop — and the first real enforcement of policy documents in `mayu`.

## Decision

### 1. Config key `policies`, strict parsing, boot fail-fast

`config.json` gains `"policies": [paths...]` (files or directories, one level
deep, `*.yaml`/`*.yml`/`*.json`, multi-doc YAML supported). Parsing is
STRICT (`sigs.k8s.io/yaml` UnmarshalStrict): an unknown field is a
version-skew symptom — a document written for a newer schema fails loudly
instead of silently losing its newest field. Policy `metadata.name` must be
unique across everything loaded. An invalid document fails **boot**: a data
plane must not start while claiming to enforce a policy set it can't.

### 2. Save-and-it's-live: mtime poll every 2s, never-fatal reload

`policy.Store` snapshots the loaded set behind an atomic pointer (request-
path lookups never lock — the live.Holder posture, ADR-006). A watcher polls
file mtimes every 2s (`LocalWatchInterval`; edits, new files, and deletions
in watched directories are all detected) and atomically swaps on change. A
bad edit keeps the previous set serving and logs once per distinct error —
the same never-fatal posture as SIGHUP config reload.

### 3. Enforceability gate: reject what this build cannot enforce

The version-skew stance applies to enforcement, not just parsing: `mayu`
REFUSES to load rules it cannot enforce, because holding a policy while
silently not enforcing it is the worst governance failure mode. Today's
build enforces:

| Rule | Subject | Enforced via |
|---|---|---|
| `budget` (monthly window), `rate` | team | Governor team-lookup chain |
| `modelAccess` | team and user | Router policy gate |

Rejected at load (gates lift as enforcement lands): `routing` rules (no
cache-affinity engine yet) and user-subject `budget`/`rate` (governance
windows are team-keyed today).

### 4. Team-policy layering: file dimensions overlay a DB/config base

Base layer: a keystore team RECORD (runtime console edits) wins wholesale
over the static config map — ADR-016's rule, unchanged. A GovernancePolicy
FILE then overlays **only the dimensions its rules declare**: a budget rule
replaces the base budget (its `hardCap` maps to `on_exceeded: block`, else
`warn`), a rate rule replaces rpm/tpm, and everything undeclared —
tokens/day quota, the other rate dimension, the budget when only rate is
declared — falls through to the base. A rate-only policy must not silently
unlimit the team's config budget, and a modelAccess-only policy contributes
no team-limits entry at all (revised after review: the original
wholesale-precedence draft let exactly that happen). All layers produce
`TeamPolicy` through the same `PolicyFromLimits`, so the burst rule can
never diverge by source. Multiple policies matching one team merge
most-restrictive-first (smallest non-zero limit binds).

### 5. modelAccess narrows through one seam

`Router.SetPolicyGate` ANDs a policy check onto every `Router.Allows`
decision — the single funnel all ingress RBAC already goes through
(`/v1/messages`, `/v1/chat/completions`, Bedrock invoke, model listings,
cross-model fallback filtering). A policy can only **narrow** what a key's
own allow-list grants, never widen it. ALL matching modelAccess rules must
allow (team ∧ user), entries are alias-canonicalized like key lists
(ADR-021), and `"*"` allows all.

## Consequences

- Standalone mode now speaks the same policy documents inferplaned will
  distribute — no config migration when the control plane arrives.
- New direct dependency `sigs.k8s.io/yaml` (pure Go; CGO stays off).
- The 403 for a policy-denied model reuses the existing "model not allowed"
  wire shape, and `/v1/models` listings shrink to the policy view
  automatically (both already funnel through `Router.Allows`).
- Budget window is monthly (the existing budget store's window); a window
  field on `BudgetRule` is future schema work.
- Lease fields in local documents are validated but not exercised —
  standalone has no control plane, so local enforcement is exact; leases
  activate with the inferplaned channel.
