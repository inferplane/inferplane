# Session handoff — restructure + policy/lease work (2026-07-30 ~ 31)

Purpose: continue this work in a fresh Claude Code session (local CLI) with
zero loss of context. Read this file first, then the referenced ADRs.

## Where things stand

**Branch `claude/annyeong-l23ezp` = PR #50** (→ main, 12 commits, all
pushed). Everything below is IN this branch:

1. Monorepo restructure: `cmd/mayu` (data plane, the former gateway, moved
   via git mv) + `cmd/inferplaned` (control plane) — ADR-031.
2. `api/v1alpha1` GovernancePolicy schema: milliUSD wire money (1000 = $1,
   internal stays µUSD), team/user subjects, budget/modelAccess/rate/routing
   rule kinds, per-rule failurePolicy — ADR-032.
3. Local policy file channel with 2s live reload; enforceability gate
   (unenforceable rules are rejected, never silently held); file dims
   OVERLAY the DB/config base per dimension — ADR-033.
4. Control-plane distribution + budget-lease protocol: one heartbeat =
   policy pull + cumulative µUSD consumption report + lease renewal +
   version-skew rejections; lease ledger bounds global overshoot — ADR-034.
5. K8s channel: GovernancePolicy CRD manifest (`deploy/crd/`) + Helm
   policies ConfigMap mount — ADR-035.
6. Roadmap for the five gaps vs LiteLLM:
   `docs/superpowers/plans/2026-07-31-litellm-gap-roadmap.md`.

Reviews already done on this branch (fixes committed): modelAccess-only
policy shadowing base budgets (critical), (team,user) budget silently
unenforced, ledger not counting unreported grants, stranded-grant
starvation, window-rollover re-spend hole, milliUSD overflow guards.

## The single blocker on PR #50

`claude-review` (required check) fails with
`Not authorized to perform sts:AssumeRoleWithWebIdentity`: the repo was
renamed `inferplane/mayu` → `inferplane/inferplane`, but the AWS IAM role in
secret `AWS_ROLE_TO_ASSUME` still trusts `repo:inferplane/mayu:*`. Fix the
trust policy condition to `repo:inferplane/inferplane:*` and re-run the
failed job — OR merge with admin bypass (code is sound: full
`go test ./... -race` green locally; the failure is CI infra only).
There are NO merge conflicts with main.

## Next work (after #50 merges, on a NEW branch off main)

Sprint plan in the roadmap doc. S1 first:
- **Rate shares** (ADR-036 candidate): global rpm/tpm split among active
  data planes via the existing heartbeat; clamp at the governor team-lookup
  closure (same seam as the budget allowance clamp in
  `cmd/mayu/gateway.go`). Σ shares ≤ limit invariant.
- **Durable ledger + windowID** (ADR-037 candidate): control-plane-owned
  calendar-month window ids through `LeaseGrant`/`ConsumptionReport`;
  SQLite persistence for inferplaned's ledger; delete the
  decrease-detection heuristic it supersedes.
Group both into ONE sync-protocol revision.

## Conventions that bite (read before coding)

- Every commit: `git commit -s` (DCO, Signed-off-by: Junseok Oh
  <ojs0106@gmail.com>), gofmt/vet clean, `go test ./... -race` +
  `bash tests/run-all.sh` before push.
- Money: operator-facing = milliUSD; internal/accounting = µUSD int64;
  conversion ONLY in `internal/policy` (×1000). Never float.
- Governance tool prime directive: never accept-and-silently-ignore a rule —
  reject loudly + report upstream.
- `internal/CLAUDE.md`, `cmd/CLAUDE.md`, project `CLAUDE.md` must be updated
  when packages/endpoints change (auto-sync rules in root CLAUDE.md).
- New decisions get an ADR (next number: ADR-036).

## Open decisions deliberately left

- Server-cache TTL tracking (ADR-031 open Q3) — unjudged.
- user-subject budget/rate stays rejected until per-user governance windows.
- Control-plane HA / Postgres ledger backend — interface only in S1.
