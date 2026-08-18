# ADR-038: Control-plane policy store — Postgres-backed GovernancePolicy writes

**Date:** 2026-08-05
**Status:** Accepted — implemented.
**Related:** ADR-031 (control/data plane split — inferplaned distributes
policy, this ADR makes the distributed set editable), ADR-033 (local policy
file channel — the file loader whose documents become the seed), ADR-034
(sync heartbeat and lease ledger — the ledger the write path must rebuild
without divergence), ADR-035 (Kubernetes policy channel), ADR-036
(control-plane usage telemetry — the Postgres/ECS/statelessness reasoning and
the `DurableAggregator` commit ordering this ADR reuses), ADR-037
(inferplaned console SSO — the `authn` middleware and admin-equivalence
posture the write endpoints inherit), ADR-008 (UI-write posture and the
`internal/providerstore` file=seed/DB=authoritative precedent).

## Context

GovernancePolicy documents on inferplaned were file-only: `--policies` points
at a directory baked into the Docker image, watched for mtime changes. On ECS
that means changing one budget number needs an image rebuild, an ECR push and
a task redeployment — and any edit made on the running task is discarded the
moment ECS replaces it. ADR-036 already established the deployment reality
(stateless container, tasks replaced at will) and shipped the `/ui/` console;
ADR-008 already established, on mayu's side, how a file-configured resource
becomes UI-writable without losing the file channel (file = one-time seed,
DB = authoritative, marker-gated). This ADR applies that pattern to the
control plane's policy set: a new `internal/policystore` package, write
endpoints on `internal/controlplane`, and a Policies tab in the existing
console, all opt-in behind one env var.

## Decision

1. **Postgres, not SQLite.** ADR-036's precedent, for the same reason:
   inferplaned runs on ECS where tasks are replaced at will, so the container
   must stay stateless — a local SQLite file is not a durable store there.
   Pure-Go `jackc/pgx/v5` (already a direct dependency) keeps
   `CGO_ENABLED=0`. The store follows `internal/telemetry/postgres.go`'s
   shape: a lazy pool that never dials at construction (an unreachable
   database must not fail `NewPostgres`), schema migration once per process
   under Postgres advisory lock `847004` (after telemetry's 847003) held on
   one acquired connection, portable `TEXT`/`TIMESTAMPTZ` DDL (`policies` +
   `policy_meta`) so pg_dump/RDS snapshots are the backup story, and the
   DSN never wrapped into an error — a pgx parse error embeds the connection
   string, which may carry a password, so both constructor failures are
   hand-written strings.
2. **File = seed, DB = authoritative** (the `internal/providerstore`
   precedent), marker-gated: `Seed` imports the file-loaded documents and
   sets a durable `seeded` marker in one transaction, and is a no-op once the
   marker exists — gated on the marker, never a row count, so an operator who
   deletes every policy does not get the image's files back on the next boot.
   The mtime watch is disabled under a DSN in two places: `cmd/inferplaned`
   does not start `Watch`, and `changed()` refuses to reload from files once
   a store is attached regardless of caller — without the second guard, a
   future caller starting the watch would silently overwrite every UI edit
   with the seed files' contents on the next mtime tick.
3. **Whole-document PUT, no PATCH.** The GovernancePolicy document — not the
   individual rule — is the CRUD unit: budget/rate/modelAccess share one
   document, and a partial-merge API would need per-rule identity and
   conflict rules the schema does not define. The console clones the fetched
   document and mutates the clone, so rules the form does not render
   (`routing`, future fields) survive the round trip unchanged.
4. **One validation path.** `policy.ParseWireDocs` — the same strict
   unmarshal the file channel uses (an unknown field is version skew and is
   rejected) plus `FromV1Alpha1` validation — validates the write path, so a
   document accepted through the UI is exactly a document the file channel
   would accept. `applyWire`, extracted from `Reload()`, is the one install
   path both `Reload()` (files) and `ReloadFromStore()` (DB) go through, so a
   DB-sourced document set can never get different ledger carry-forward
   semantics from a file-sourced one.
5. **Commit-then-memory ordering** (ADR-036's `DurableAggregator`
   precedent): `ApplyWrite` commits to Postgres first and swaps the
   in-memory set second. A failed `Put` leaves the enforced set exactly as
   it was, and there is no window where memory enforces a rule the store
   does not hold.
6. **Boot posture differs from the usage store, deliberately.** ADR-036's
   usage store connects lazily because losing telemetry beats blocking boot.
   The policy store is *authoritative*: booting on possibly-stale file
   content while claiming database authority would distribute the wrong
   rules. The boot attach (seed + first load) is therefore a hard boot
   dependency, bounded at 10 seconds — an unreachable database fails the
   boot instead of hanging it.
7. **No new authorization axis (accepted limitation, not an oversight).**
   inferplaned has no admin-vs-viewer split at all — a verified OIDC
   identity and the static token are admin-equivalent (ADR-037, D4/D5) — so
   policy writes cross exactly the same `authn` threshold reads already
   cross: `GET`/`PUT`/`DELETE /v1alpha1/policies` sit behind the same
   middleware as sync, dataplanes and export. A read/write split is future
   work and would belong with a control-plane principal model, not bolted
   onto one endpoint. Meanwhile the feature stays functional without a
   store: `GET` works file-only with `"writable": false` (how the console
   renders read-only), and `PUT`/`DELETE` return 405.
8. **Deferred:** RDS provisioning and ECS secret injection (a cost/infra
   decision, separate from this code change); policy history / audit of who
   changed what (inferplaned has no audit chain); and `GET` of a single
   policy (the list is small).

## Consequences

- Operators get a Policies tab on `/ui/` and a `PUT`/`DELETE
  /v1alpha1/policies/{name}` API: a budget change takes effect on the
  running control plane immediately and survives an ECS task replacement —
  no image rebuild, no redeployment.
- Turning it on still requires operator work this ADR deliberately does not
  ship: provision an RDS (or any Postgres) instance, inject
  `INFERPLANED_POLICY_DSN` as an ECS secret, and rebuild/redeploy the image
  once to pick up the wiring. `--policies` stays required as the one-time
  seed source — a DSN alone has nothing to seed from, and boot fails with a
  clear message if it is set with an empty `--policies`.
- With the DSN unset, behaviour is byte-identical to today: file-only
  policies, mtime watch running, `PUT`/`DELETE` answering 405.
- Accepted limitation (recorded in decision 7): every authenticated identity
  can write policy, because inferplaned has no finer-grained principal model
  to hang a read/write split on.
- The env-only configuration follows the `INFERPLANED_TOKEN` /
  `INFERPLANED_USAGE_DSN` precedent — inferplaned has no config file.
- **`inferplaned` is task-replaceable, not horizontally scalable: run exactly
  one task.** Postgres-backing the policy/telemetry stores makes those stores
  safe across task replacement, but the budget-lease ledger
  (`internal/controlplane`) is still per-process in-memory. Two concurrent
  `inferplaned` tasks would each grant full budget allowances against the
  same rule — N× overshoot, not the bounded overshoot ADR-034 designs for a
  single instance. A durable ledger (roadmap item ②) is required before
  running more than one task. Even at exactly one task, an ECS-triggered
  replacement (deploy, failed health check, spot interruption) resets the
  ledger to empty — the replacement task starts with no memory of grants the
  old task had already issued, and relies on the same cumulative-report
  self-healing ADR-034 uses as its fallback rather than exact ledger state.
