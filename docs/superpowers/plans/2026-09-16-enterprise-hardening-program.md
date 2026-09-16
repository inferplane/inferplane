# Enterprise hardening: execution plan from the shipped baseline

> **For agentic workers:** Use `superpowers:subagent-driven-development` or
> `superpowers:executing-plans` for each bounded implementation below. Checkboxes
> represent uncompleted deliverables, not evidence that a feature is absent.

**Reviewed:** 2026-09-16 against `main` at
`6b5cf9c0518b896d2355fa805e9826eea3eeccc5`.

**Implementation update:** Work package A's two reproduced defects have fixes in
this branch: provider redirect refusal (including client relay protection) and
CI database-test activation/required-result validation. Sections 1 and 7 retain
the original baseline evidence. The identity, management and operating-contract
milestones remain open; a source change does not attest a deployed fleet.

**Goal:** Close the remaining identity, management-trust and operational evidence
gaps without rebuilding the monetary authority, shared admission or coding-client
support already merged.

**Architecture:** Preserve both accepted profiles: ADR-045 node-local monetary
escrow and ADR-046 synchronous Postgres shared admission. Introduce verified
issuer/subject attribution through the existing key and accounting paths; retain
all existing liabilities during migration. Keep provider compatibility, privacy
and accounting regressions continuously tested.

**Tech Stack:** Go 1.25, pure-Go SQLite, pgx/Postgres, Go HTTP handlers, existing
OIDC verifier, hash-chained audit and current GitHub Actions test jobs.

**Spec:** [Enterprise contracts](../../enterprise-strategy.md), with implementation
semantics governed by ADR-042 through ADR-047. This is a coordinating program,
not a replacement protocol ADR or an instruction to execute all phases at once.
The [identity plan](2026-08-28-phase-0-identity-management-trust.md) remains input
to the identity tasks, subject to the migration corrections below.

## 1. Correct the baseline before allocating work

The supplied critique describes the older `feat/gpt-6-astra` checkout at
`401dd20`. It must not be used as the implementation inventory for current main.
An existing file named `identity.go` is also not sufficient evidence of durable
human identity: `internal/authority/pgstore/identity.go` binds an authority
database namespace and policy generation, not an OIDC person.

| Claim in the critique | Evidence in reviewed main | Remaining work |
|---|---|---|
| No Codex code/tests | `internal/responses/`, `internal/server/responsesapi/`, `internal/responses/testdata/codex-0.154-stateless.json`, `cmd/mayu/codex_test.go`, ADR-044/047 | Versioned real-client/model qualification and explicit unsupported capabilities |
| No durable windowed monetary ledger | `internal/authority/{local,pgstore}`, `internal/policy/authority.go`, ADR-045, PR #78 | Operational reconciliation/retention, restore exercises, person-identity migration |
| Keys only SQLite; all rate/quota multiply by N | `internal/keystore/postgres*.go`, `internal/authority/pgstore/shared*.go`, ADR-046, PR #79 | Production DB failover/throughput qualification; local profiles retain their stated limits |
| PII is only a masking plugin | `internal/sensitivity/`, `internal/router/request_routing.go`, ADR-043/044 | Finite detector evaluation, model-boundary attestation and masking/tool compatibility evidence |
| Policy writes have no separate credential or mutation record | `internal/controlplane/auth.go:authnWrite`, `policies.go:recordMutation`, PR #71 | Fine-grained capabilities and durable, complete mutation evidence |
| No race/vet/DB/vulnerability CI | `.github/workflows/ci.yml` provisions Postgres 17 and runs these jobs | Keystore DB tests require a second unset DSN variable and currently skip; release signing and deployed acceptance remain separate |
| No durable `(issuer, subject)` identity or six-role authorization | `KeyOptions.Owner`, OIDC claims containing only `Subject`, no `internal/identity` or `internal/authz` | Still a primary P0 |

Code existence, a passing fixture, deployment, and production qualification are
four different evidence levels. Record them separately. No live deployment or
enterprise-readiness claim follows from this document.

## 2. Global constraints and corrections to the proposed 90-day plan

- Keep `CGO_ENABLED=0` static builds for both binaries. Keep leaf/package
  boundaries, integer microUSD arithmetic, DCO and secret-reference rules.
- The durable human ID is the verified `(OIDC issuer, subject)` within an
  organization. Never infer a human from email, key ID or a freely edited owner.
- Keys retain hashes and revocation history. Identity migration must preserve
  account history, open grants, pending permits, uncertainty and replay IDs.
- ADR-045 commits grants centrally **before** replying; write-behind persistence
  is not an acceptable replacement. Expiry or a lost node is not evidence of
  unused credit and never triggers an automatic refund.
- Node-local inference can continue only within valid existing local authority.
  Exhausted/expired hard authority or failed policy-readiness gates refuse.
  Shared admission requires Postgres and refuses on DB loss; CP-only loss does
  not itself require refusal after initial sync if database binding/generation
  and policy-readiness gates still pass. Neither profile promises unlimited
  partition availability.
- Do not add the old rate-share proposal beside ADR-046 as another authority.
  In particular, keeping expired shares while reallocating them can double
  capacity. A future disconnected global-rate profile needs a separately reviewed
  fencing/expiry contract; it is outside this program.
- Distinguish rate-bucket guarantees from fixed-window counts. A refillable
  bucket permits its burst plus refill over an interval; “300 rpm means at most
  300 in every arbitrary 60 seconds” requires a different algorithm.
- Preserve existing calendar semantics: ADR-045/046 database-owned UTC day/month
  windows; ADR-042 configured calendars in local mode. Do not silently replace
  all windows with monthly UTC or relabel historical bookings.
- Hard caps govern conservative admission against the captured pricing/model
  bounds, not arbitrary external invoices. A policy cut cannot retroactively
  revoke offline grants already issued under its prior limit.
- Zero microUSD can be legitimate after round-half-even, zero actual usage or
  explicit free pricing. Require explained settlement, not `cost > 0`.
- A stream that already sent HTTP headers cannot subsequently become HTTP 502.
  Preserve the protocol's terminal failure, observed usage and uncertain
  liability when a stream is interrupted.
- Privacy, strict budget targets, region restrictions and RBAC constrain every
  attempt. Legacy ADR-041 optional substitution still has its separate behavior.
- Signed model declarations and pricing accuracy are not implied by an operator
  setting a name/label. Compatibility tests do not establish task quality.
- A local machine with upstream credentials remains a trusted enforcement
  component. Central accounting does not make a compromised laptop bypass-proof.
- Keep enterprise readiness **alpha** until all P0 contract gates pass.

## 3. Order, scope and scheduling

Use completion gates, not dates, to activate changes. The schedule below is a
capacity estimate for one core owner plus an independent reviewer, with about
30% of time reserved for demonstrated compatibility/accounting regressions.
It is not a delivery commitment. Re-estimate weekly from completed gates.

| Window | Deliverable | Dependency / exit |
|---|---|---|
| Days 1–7 | Correct status documents and reproduce the existing safety suite | Recorded SHA, PostgreSQL tests actually executed, no duplicate ledger/rate work |
| Weeks 2–4 | Typed identity and a liability-preserving migration | Same issuer/sub across keys/devices; different issuers isolated; no fresh balance at cutover |
| Weeks 4–6 | Scoped fixed roles and durable mutation evidence | Every management route classified; negative cross-role/org/team tests |
| Weeks 6–8 | Diagnose retained liability and qualify recovery | Read-only diagnostics first; DB/node restart and stale-restore exercises |
| Weeks 8–10 | Explicit premium/total user budget contract | Identity + accounting + role gates; compatible fallback or refusal |
| Weeks 10–12 plus remaining buffer | Client/privacy/load qualification and limited pilot | Declared latency/quality/cost/failover gates; no universal PII or enterprise claim |

PII and coding-client regression work runs throughout. No new shared topology
store, Redis dependency, semantic cache, embeddings lane or self-update mechanism
is part of this program. Do not remove already shipped shared-profile support
merely to narrow the first pilot to managed developer-local gateways.

## 4. Independently reviewable work packages

### A. Baseline and cross-path acceptance

**Existing files:** `.github/workflows/ci.yml`,
`internal/server/shared_governance_test.go`,
`internal/server/responses_review_test.go`,
`providers/bedrock/{guardrail,mantle}_test.go`,
`internal/authority/pgstore/*_test.go`, `docs/enterprise-strategy.md`,
`docs/roadmap.md`.

**Consumes:** existing provider interfaces and authority protocols unchanged.
**Produces:** a versioned evidence inventory and focused regressions only where
the existing suite fails to demonstrate a contract.

- [ ] Run the full repository checks below with a disposable Postgres instance.
  Capture test JSON so a skipped database test cannot be mistaken for a pass.
- [ ] Close the reproduced CI variable mismatch:
  `internal/keystore/postgres_test.go` reads `KEYSTORE_TEST_POSTGRES_DSN`, while
  current CI sets only `INFERPLANE_TEST_PG_DSN`. The reviewed baseline skipped
  18 keystore DB tests. Before the existing race command, add:

  ```bash
  export KEYSTORE_TEST_POSTGRES_DSN="$INFERPLANE_TEST_PG_DSN"
  ```

  Keep both variables in the local disposable-DB instructions and require
  `TestPostgresResolveNeverTearsKeyAndTeamSnapshot` to execute, not skip.
  If the test helper is later standardized on one name, retain backwards
  compatibility for explicit local test invocations.
- [ ] Inventory Messages, Chat, Bedrock Invoke and Responses routes against
  Complete/Stream, guardrail/refusal, region lock, privacy, same-protocol bytes,
  cost/uncertainty and fallback reservation. Mark unsupported combinations
  explicitly instead of silently omitting them.
- [ ] Start with `TestEveryIngressUsesSharedAdmissionAndOriginalPolicyGeneration`,
  `TestAllIngressesUseAuthenticatedTeamSnapshot`,
  `TestResponsesDoesNotDropTerminalUsageAfterToolConversionFailure` and
  `TestFailedNativeUsageIsSettledBeforeRetryAdmission`; extend only uncovered
  cases in their existing fixtures.
- [ ] For each refusal, assert zero provider calls. For a partial billable
  stream, assert observed usage and retained reservation independently. For
  rounded-zero usage, assert the correct pricing snapshot and completed
  accounting rather than manufacturing a nonzero cost.
- [ ] Record fixture provenance as client version, public model ID, provider API,
  capability declaration, sanitized request/response digests and capture date.
  Live probes remain opt-in; offline CI must not require model credentials.

**First bounded fix:** a 307 two-server reproduction at the reviewed SHA showed
that Anthropic/Chat Completions follow an upstream redirect with body and synthetic
credential, while native Responses refuses. Execute the
[provider redirect plan](2026-09-16-provider-redirect-boundary.md) before
claiming uniform destination enforcement. This plan does not assert a live exploit
or silently apply its runtime fix.

Focused verification:

```bash
go test ./internal/server/... ./providers/... ./internal/responses/... \
  ./internal/authority/... ./internal/keystore/... -race
```

### B. Durable identity without resurrecting existing balances

**Reuse/rebase:** `2026-08-28-phase-0-identity-management-trust.md`.
**Additional affected files:** `internal/keystore/postgres{,_schema,_rows,_seed}.go`,
`internal/keystore/import.go`, `internal/policy/authority.go`,
`internal/authority/pgstore/{accounts,shared_constraints,shared_finish}.go`,
`internal/authority/local/`, and `internal/server/responsesapi/`.

**Consumes:** verified OIDC claims, current key snapshots, existing authority
namespace, historical accounts and original-window reservations.
**Produces:** the identity leaf and typed credential/policy attribution across
SQLite and Postgres, with an explicit migration command and cutover report.

- [ ] Update the old plan's file inventory before implementation. Its
  SQLite-only key migration is insufficient after ADR-046; it must cover shared
  seed fingerprints, snapshots, imports, conditional revocation and Responses.
- [ ] Keep the old plan's `identity.ID`/`NewHuman`/`NewService` design as the
  initial model. Persist the verified issuer from the verifier; never accept
  an issuer supplied by an inference request.
- [ ] Use a versioned, unambiguous encoded identity for new selectors. Additive
  storage first; do not overwrite `Owner` and assume accounting migrated.
- [ ] Produce an administrator-reviewed mapping from legacy subjects to verified
  identities. No automatic email matching, account splitting or silent
  human/service conversion. Ambiguous mappings block person-level cutover.
- [ ] Quiesce new issuance/admission for the migrating subject and stop old
  writers. Fence affected local journals and account for all outstanding grants
  before activation. Unreachable nodes leave their credit unavailable.
- [ ] Preserve original reservation/account/window references for terminal
  reports. Introduce an explicit mapping for future admission; never delete
  historical rows or create a zero-balance replacement under a new subject.
  Merge only through one transaction that carries every liability; reject
  one-to-many splits and unmatched retained authority.
- [ ] Test same user/two keys/two devices, same sub/different issuers, two teams,
  human versus service, late settlement, lost acknowledgment, retry, rollback,
  concurrent seed/import and unresolved migration.
- [ ] Reject old clients for policies requiring typed person identity.
  Additive JSON alone is not enforceable compatibility. Preserve explicitly
  classified legacy team operation until its cutover.

**Exit:** no per-person cap, revocation or audit attribution resets through key
rotation, identity migration, restart or an old report. Reuse existing money
accounts; do not create a second ledger.

### C. Management roles and durable mutation evidence

**Existing plan:** identity plan Tasks for `internal/authz`, management middleware,
role bindings and audit. **Current seams:** `internal/controlplane/auth.go`,
`policies.go`, `internal/server/server.go`, `internal/server/adminapi/`,
`internal/server/configapi/`, `internal/audit/`.

**Consumes:** B's verified typed identity and existing store/profile revisions.
**Produces:** a capability map for six fixed roles, scoped bindings and auditable
mutations without weakening machine-channel separation.

- [ ] Enumerate actual method/path registrations. Classify public health/static
  assets, local count endpoints, machine sync/usage/broker, inference and
  management separately. Do not require an admin role on intentional public
  endpoints or pass a management JWT to machine channels.
- [ ] Preserve the dedicated policy-write and broker tokens. Define their
  migration to scoped management/service principals explicitly.
- [ ] Add negative tests for every management operation: wrong role, wrong team,
  wrong organization, missing binding, store failure and stale authorization.
  A `team-admin` cannot mutate an organization-wide policy.
- [ ] Record actor, capability, scope, operation, before/after hashes, generation
  and result. Append audit fields; verify old/new exact-byte hash-chain fixtures.
- [ ] Couple durable mutation evidence to successful writes, using a transaction
  outbox where state is in Postgres. A successful policy change followed by a
  failed best-effort log call is not a completed audit guarantee.
- [ ] Keep UI hiding advisory; enforce the same capability on the server.

**Exit:** authorized mutations have durable evidence, rejected authenticated
operations have denial evidence, and unauthenticated 401s remain unaudited.

### D. Operate the authority already implemented

**Files:** `internal/authority/pgstore/{accounts,grants,shared_finish}.go`,
`internal/authority/local/`, `internal/controlplane/`, `cmd/mayu/`,
`docs/durable-budgets.md`, `docs/shared-governance.md`.
Do not create a parallel authority service.

**Consumes:** ADR-045/046 account and permit states, B/C for person-scoped views.
**Produces:** safe diagnostics, a recovery runbook and explicit reconciliation
design; no automatic refund or destructive cleanup.

- [ ] Add read-only diagnostics for authority namespace/generation, window,
  committed consumption, outstanding grants/permits, uncertainty, frozen state,
  last sync and deadline. Default support output omits person/key identifiers.
- [ ] Extend the existing restart/replay/late-report tests before adding any
  mutation endpoint. Make operator-visible discrepancies reproducible from
  authority and usage records, not inferred from an empty local cache.
- [ ] Specify evidence required to resolve uncertainty, capability checks,
  idempotency and a preview/audit flow. Do not allow a “reset budget” operation
  to erase liabilities. A refund API is a separate bounded implementation only
  after its authority transition is reviewed.
- [ ] Exercise CP loss, DB loss and stale backup restore in isolated staging.
  Test CP-only loss separately: initialized shared gateways can continue while
  DB binding/generation and policy-readiness checks pass. For DB loss, shared
  gateways refuse; node-local gateways can continue only with valid credit and
  valid policy-readiness state, including `max_policy_age`.
- [ ] Restore policy, identity and authority state consistently. Fence newer
  grants and reconstruct or conservatively encumber every post-backup liability
  before reopening admission. Fencing prevents future spending but cannot recover
  already-completed spending omitted by an old backup. If those liabilities
  cannot be established, freeze the affected windows. Include a completed
  post-backup spend, an outstanding grant, a late report and a pending shared
  permit in the restore exercise.
- [ ] Measure lock wait, database latency, retained-row growth, initial grant
  latency and restart recovery. Do not advertise process failover tests as a
  deployed multi-AZ database failover proof.

**Exit:** operators can explain unavailable balance and recover through documented
safe steps without SQL counter deletion.

### E. Premium/total pools as a user contract

**Files:** `api/v1alpha1/types.go`, `internal/policy/`,
`internal/authority/{local,pgstore}`, `internal/tier/`,
`internal/router/request_routing.go`, `internal/server/requestpolicy/`,
CRD and configuration/console consumers.

**Consumes:** B/C identity and permissions, current authority accounts and
ADR-044 strict targets. **Produces:** independently reviewed policy/schema
semantics for premium switching and total hard admission.

- [ ] Describe the existing soft meter + strict targets + independent hard-cap
  configuration before deciding which new schema is needed.
- [ ] Tie both pools to the same typed user and explicit window. Match policy
  generation and namespace; reserve every applicable hard scope atomically.
- [ ] Choose only administrator-approved targets that satisfy current RBAC,
  privacy, region, context, tools and protocol constraints. Never return to a
  premium target after pool exhaustion; no compatible target means refusal.
- [ ] Define whether fallback is one target or an ordered compatible set and
  test that semantics at every retry. Keep total-cap exhaustion distinct from
  premium exhaustion. A cheap/self-hosted model does not bypass a total hard cap.
- [ ] Test concurrent exhaustion, rollover, partial usage, two keys/devices,
  revocation, delayed reports and model/pricing changes.

**Exit:** premium exhaustion visibly switches as configured while total
exhaustion emits no new provider request. Explain self-hosted infrastructure
cost separately from an explicitly free per-token API rate.

### F. Pilot evidence, not a broader compatibility claim

**Files:** `cmd/mayu/{codex,codex_bedrock}_test.go`,
`internal/responses/testdata/`, `tests/launchers/`,
`internal/sensitivity/`, `internal/router/request_routing*_test.go`,
`docs/adaptive-routing.md`, `docs/policy-routing.md`, release workflow/runbooks.

- [ ] Record installed CLI version and native/portable model capability matrix.
  Run tool round-trip, replay, model switch, effort compatibility, context
  admission, cancellation, auth failure and usage accounting for each declared
  supported route. Separate protocol success from model task success.
- [ ] Verify original-input inspection, Block/InternalOnly/Mask composition and
  reinspection after masking across tools/history/media/opaque fields. Unknown
  coverage must not be labeled clean; destination labels need deployment proof.
- [ ] Compare a fixed manually selected model with Shadow recommendations and
  then opt-in Enforce. Predeclare task success, total cost including cold cache
  and retries, p95 latency and prohibited-egress gates for the pilot dataset.
- [ ] Test two shared gateways against a qualified HA DB endpoint; declare load,
  key population, request sizes, failure injection and expected refusal behavior.
  Measure rate against the documented burst/refill algorithm.
- [ ] Verify release provenance, dependency scan and signatures at deployment.
  Existing CI is a baseline, not proof that a release artifact was signed.

**Exit:** publish a bounded tested matrix and rollout decision. Leave unmet P0
rows open. Do not turn the roadmap's implementation checkmarks into an
enterprise-production-ready claim.

## 5. Required validation and review

Use an isolated disposable database; the DSN is test-only and is not a provider
credential. The normal offline suite remains runnable without it, but skipped
database integration tests are not evidence for authority correctness.
Set both `INFERPLANE_TEST_PG_DSN` and `KEYSTORE_TEST_POSTGRES_DSN` to the same
disposable instance; individual tests isolate themselves with random schemas.

```bash
export KEYSTORE_TEST_POSTGRES_DSN="$INFERPLANE_TEST_PG_DSN"
CGO_ENABLED=0 go build -trimpath -o bin/mayu ./cmd/mayu
CGO_ENABLED=0 go build -trimpath -o bin/inferplaned ./cmd/inferplaned
go test ./... -race -json > /tmp/inferplane-test-results.json
go vet ./...
gofmt -l .
bash tests/run-all.sh
```

With `INFERPLANE_TEST_PG_DSN` set to the disposable instance, require actual
execution of at least:

```text
TestConcurrentReplicasReserveAtMostLimit
TestExpiryNeverRefundsOrExtendsReplay
TestTwoJournalsTwoReplicasAndJournalRestart
TestSharedPolicyMoneyCompetesWithLocalGrants
TestSharedPolicyRateUserScopesAndStableIdentity
TestSharedOriginalCalendarWindowsFinishAndCancel
TestPostgresResolveNeverTearsKeyAndTeamSnapshot
```

The test named `StableIdentity` above validates the existing configured opaque
subject, not an issuer/sub migration. Add B's separate tests before closing that
contract.

After the `go test -json` command, enforce the database gate with:

```bash
python3 - <<'PY'
import json
from pathlib import Path

required = {
    "TestConcurrentReplicasReserveAtMostLimit",
    "TestExpiryNeverRefundsOrExtendsReplay",
    "TestTwoJournalsTwoReplicasAndJournalRestart",
    "TestSharedPolicyMoneyCompetesWithLocalGrants",
    "TestSharedPolicyRateUserScopesAndStableIdentity",
    "TestSharedOriginalCalendarWindowsFinishAndCancel",
    "TestPostgresResolveNeverTearsKeyAndTeamSnapshot",
}
outcomes = {}
for line in Path("/tmp/inferplane-test-results.json").read_text().splitlines():
    event = json.loads(line)
    if event.get("Test") in required and event["Action"] in {"pass", "fail", "skip"}:
        outcomes[event["Test"]] = event["Action"]
bad = sorted(name for name in required if outcomes.get(name) != "pass")
if bad:
    raise SystemExit("Required DB tests missing, failed or skipped: " + ", ".join(bad))
PY
```

For each PR: keep DCO sign-off, run relevant tests and required checks, obtain AI
review of the latest HEAD, fix verified Critical/Major issues, then merge only
after reviewing the same HEAD and intended base. Never disable a required check.
No runtime implementation or deployment is authorized by a checked documentation
box alone; follow the user's actual implementation scope.

## 6. Progress reporting

Every two weeks record: reviewed commit, completed acceptance scenario, actual
test execution, deployed profile/version, outstanding limitation and next owner.
If compatibility incidents exceed the maintenance allocation, report the
trade-off and revise the forecast explicitly. Do not silently substitute model
patches for a person-identity or management-trust milestone.

The first next code increment is **A's reproduced redirect-boundary fix**.
The first core milestone is **B's verified identity leaf and additive credential
fields**, once its migration design covers both storage profiles. Neither calls
for another rate-share protocol or another durable ledger.

## 7. Verification performed while preparing this plan

On 2026-09-16, both static binaries, vet, format checks, the shell harness and
the full race suite passed against the reviewed runtime. A disposable local
Postgres instance exercised the authority tests. The initially skipped
keystore database tests were rerun successfully with their separate DSN variable.
Combined results contain 3,594 passing test/subtest outcomes; the three optional
installed-Codex tests were not enabled. No live provider qualification was run.

The redirect regression code in the bounded plan was executed separately against
the unchanged runtime: 40 Anthropic and 30 Chat Completions cases failed the
destination assertion; 20 native Responses cases passed. Every case reached its
configured source, so rejection during request parsing cannot produce a false
pass. Those expected red tests are evidence for the planned fix, not part of a
claim that the fix has shipped.

Initial checks hit the host's full `/tmp`; reruns used a task-specific temporary
directory. Both disposable database instances were removed after verification.
Only documentation changed in this worktree.
