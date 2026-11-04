# Audit Console Completion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Finish the audit console (sub-project A) on `feat/audit-console`: every governance denial names the limit that refused it, end to end from the governor to the Denials table; Mode B and Mode A console projection fail safely; Responses audits every legacy-mask refusal; and the PII view is verified live with a shipped email-masking example policy.

**Architecture:** Provenance travels with each enforced limit. `policy.TeamLimits`/`UserLimits` name the rule whose value each merged limit holds, the gateway's team-policy composition keeps the source of whichever layer binds, and `governance.TeamPolicy`/`UserPolicy` carry a `LimitSource` per dimension. Every refusal (local `PreCheck`, the pricing guard, ADR-054 local liability, ADR-045 authority, ADR-046 shared admission) produces an `audit.GovernanceRef`; the ingress stores it in the ADR-057 evidence collector with `audit.SetGovernance`, and `audit.Finish` writes it onto the denial record only. The analytics projection copies it into three new `audit_events` columns (with migrations for SQLite and Postgres), and `audit.js` renders it. Separately, the Mode B ingest writes a tick's console rows in one savepoint and treats only data errors as skippable, and the Mode A live sink commits each row atomically.

**Tech Stack:** Go 1.26.10, `modernc.org/sqlite`, `pgx/v5` (Postgres 17 for Mode B and ADR-046 tests), dependency-free browser JS, Node (standard library) and Python Playwright for console checks.

**Spec:** No separate spec, by user instruction. Inputs: `/home/atomoh/.tmp/inferplane-remediation-20261029/inputs/program.md` (Plan 1 items 1–6) and `/home/atomoh/.tmp/inferplane-remediation-20261029/inputs/status-page.html` ("문서 1" rows: governance "정책 · rule" out of scope, PII live unverified, the three follow-up fixes and the Responses gap). The plan argues from `docs/superpowers/specs/2026-10-29-audit-console-design.md`, `docs/decisions/ADR-057-audit-console-evidence.md` (both amended in Task 10) and the residual rulings in `/home/atomoh/.tmp/inferplane-audit-console-20261029/rulings-and-deferred.md` (lines 71–73; deferred Task 6 minor at line 92).

## Design decisions

1. Decision: the record field is `governance{scope, limit, source, policy{name, generation, rule}}`, appended after `auto_mode` with `omitempty` (source and policy omit when empty) — it mirrors `routing.policies[]{name, generation, rule}` so the console renders both alike, and closed `scope`/`limit`/`source` sets let it name settings that are not rules — cost if wrong: a new limit kind needs a new closed value, its docs and a console label.
2. Decision: the ref rides the existing ADR-057 evidence collector (`audit.SetGovernance` before the denial write; `audit.Finish` copies it onto `request_started` records with an outcome only) instead of a new parameter on every ingress audit helper — one line per deny site, the same pattern as `SetAutoMode` — cost if wrong: a deny site that forgets the call records no source ("not recorded"), never a wrong one.
3. Decision: provenance is carried with the merged value at every layer (`policy.RuleRef` per merged limit; `governance.LimitSource` per `TeamPolicy`/`UserPolicy` dimension; `restrictiveRate`/`restrictiveBudget` gain a `fromOverlay` result) instead of being reconstructed at denial time — the deny site only sees the merged number, and recomputation would duplicate the merge rules — cost if wrong: a future merge rule that forgets its ref names a stale layer (pinned by the provenance tests in Tasks 2 and 5).
4. Decision: ties keep the first or base source, exactly as the merges keep the first or base value (`takesRate`/`takesBudget` are the merges' own comparisons) — the record then names the layer whose value is in force — cost if wrong: when a policy sets the same value as the config, the record names the config.
5. Decision: a budget store at capacity (`budget.BlockCapacity`) and a full ADR-054 liability ledger are named `budget_store`, not the configured limit — the limit did not refuse; the fail-safe did — cost if wrong: none for correctness; the console shows "budget store full".
6. Decision: the ADR-034 lease gate names `source: lease, limit: budget` (no window) while a lease allowance that clamps a budget names `lease` with its window — `LeaseTable.Blocked` returns only a reason, and changing its signature would touch `internal/proxy` for a label — cost if wrong: a lease-gate row cannot say day versus month.
7. Decision: pre-dispatch money refusals carry the ref in typed errors — `governance.BudgetDenial` wraps `ErrLocalBudgetExhausted`/`ErrAuthorityExhausted` (so `errors.Is` and `requestpolicy.BudgetStatus` are unchanged) and `SharedDenial` gains `Governance` — read by one `governance.DenialGovernance(err)` — cost if wrong: an authority that returns a bare sentinel records no source.
8. Decision: Messages, Chat and Invoke reservation refusals now write `outcome.error` `budget_authority_unavailable` (added to the closed `audit.DenyReason` set), as Responses already did (`responsesapi/handler.go:246`) — without a code the console filed them as "Error" and the Denials table never showed them — cost if wrong: a consumer that relied on `error: null` for those 402/429/503 records sees a code on new records.
9. Decision: ADR-046 shared refusals name the refusing definition (policy rule with its document generation, else `team_record` for the authenticated team snapshot or `key` for the key option); ADR-045 refusals name the exhausted grant's policy and rule without a generation, because `policy.AuthorityBudget` carries none — cost if wrong: durable-authority rows show `name·rule` without `#generation`.
10. Decision: the projection adds three closed-code columns `gov_scope`, `gov_limit`, `gov_source`; `policy_ref` holds the governance rule for a governance denial, and on a denial with a governance or RBAC deny code it never holds the first routing policy — the Denials table groups by these, and a privacy rule the request matched did not refuse it — cost if wrong: rows already in an index keep their old `policy_ref` until rebuilt.
11. Decision: migrations keep every row: SQLite adds each missing column on open (`PRAGMA table_info`, the existing `migrateEventsColumns` loop generalised to `migrateColumns`); Postgres adds them once under migration marker `version = 2` with `ALTER TABLE … ADD COLUMN IF NOT EXISTS`, so later boots take no ALTER lock; boot replay and Mode B ingest stay first-write-wins (`ON CONFLICT (id) DO NOTHING`) — cost if wrong: a row projected by an older binary from a newer record keeps empty columns until Mode B rebuild or a Mode A `analytics.db` re-creation.
12. Decision: an old record (written before `governance` existed) projects empty `gov_*` columns, and the console shows "not recorded (older record)" in Denials and in the trace denial section; routing denials keep showing their routing rule — the chain must not be rewritten and the console must not guess — cost if wrong: none; the label is explicit.
13. Decision: Mode B writes all console rows of a tick inside ONE savepoint (one subtransaction, far under Postgres's 64-entry per-backend cache) rather than a savepoint-free batch — a savepoint-free failure would abort the whole transaction and force the usage work to be redone; per-row savepoints are used only after the batch fails with a data error — cost if wrong: a tick with a bad row pays per-row savepoints for that tick only.
14. Decision: Mode B skips a console row only for SQLSTATE class 22 or 23; any other error aborts the tick (no checkpoint advance) and is returned as `project audit console rows: SQLSTATE <code>` without the Postgres message — transient errors must be retried, and messages can quote request bytes — cost if wrong: a persistent non-data error (for example `42501` after a grant change) stalls Mode B console ingest, visible as repeated tick errors and lag, instead of silently dropping rows.
15. Decision: for item 4 the code is changed to match the docs, which is the safer fix: the Mode A live sink commits each row with its categories in one transaction and logs a refusal by record ID and driver code (`LogAuditSkip`), returning the error the sink already discards; the docs then describe each path exactly — today a category failure leaves a row without categories and every live refusal is silent — cost if wrong: one BEGIN/COMMIT per record instead of 1+k autocommits (no more fsyncs than before).
16. Decision: the Responses legacy-mask redaction error and the unreadable-result branch share one helper (`legacyMask`) and one `mask_failed` denial; the unreadable branch is unreachable with the shipped redactor (it keeps protocol structure and reinspects), so it is pinned by a helper test with a fake redactor — cost if wrong: none.
17. Decision: the example lives at `examples/policies/pii-email-mask.yaml`, team `pii-demo`, `sensitiveData: {onDetected: Mask, onUninspectable: Block}`, no routing targets — `TestExamplePoliciesLoad` (`internal/policy/load_test.go:96`) covers it, and team `demo` is unaffected when the directory is loaded; the live check loads the file alone because `demo.yaml` names quick-start models the verify config does not route — cost if wrong: quick-start users loading the directory also get a privacy rule for team `pii-demo`.
18. Decision: `mayu config validate` is not changed: it runs `config.LoadRaw`, which checks only that each `policies` entry is non-empty (`internal/config/config.go:855-859`); the policy document is validated by `TestExamplePoliciesLoad` and by `mayu serve` at boot (Task 13) — parsing policies in `config validate` would be a behaviour change outside this plan — cost if wrong: an operator running `config validate` alone can miss a broken policy file.
19. Decision: reference docs for the governance field land with the projection (Task 7), when the field is populated by every path; Tasks 1–6 update the module notes in `internal/CLAUDE.md` — the field's meaning spans six tasks and a reference doc that is true at each intermediate commit would need six rewrites — cost if wrong: commits 1–6 have module notes but no reference docs for the field.
20. Decision: no new dependency.
21. Decision (rulings, no tasks): request class and subagent linking stay empty (Claude Code 2.1.312 sends no `x-claude-code-request-class` or other `x-claude-code-*` header except the session ID), auto mode block samples are not mapped (never observed), and the mockup's "fast" stage is not added (no observed basis) — these need client data, not code — cost if wrong: none.

## Global Constraints

- Worktree `/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree`, branch `feat/audit-console`. No push, no merge.
- Scratch and logs go to `S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run` (live check: `/home/atomoh/.tmp/inferplane-audit-console-20261029/verify2`). Never `/tmp`. No `rm -rf`.
- Shell state does not persist between commands: every command block below defines the variables it uses.
- Every `go build`/`go test`/`go vet` uses `-buildvcs=false` (VCS stamping fails in this linked worktree). Never pipe build or test output to `head`/`tail`; redirect to a log under `$S` and grep it.
- Never run `git stash`, `git checkout`, `git restore`, `git reset` or `git clean` (refs/stash is shared across worktrees). To show RED after the fact, copy a file aside with `cp` or use `go test -overlay`.
- Commits: `git commit -s` with the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Run `gofmt -w` on the Go files you changed before committing.
- Go 1.26.10, both binaries `CGO_ENABLED=0`, pure-Go dependencies only; no new dependency.
- Audit record fields are append-only and `omitempty`; a record without the new field keeps its exact bytes, and old audit files still pass `mayu audit verify`.
- `governance` holds names only: never a limit value, a counter, key material, a user identifier or a secret. Logs carry record IDs and store codes only, never request values or store messages.
- `/metrics` gains nothing; no `key_id` or identity label anywhere.
- Authenticated `count_tokens` handler results stay HTTP 200; this plan does not touch them.
- Cost is integer microUSD; no settlement path changes here (ADR-054 liability rules unchanged).
- Postgres tests: container `postgres:17-alpine` named `inferplane-audit-pg-fix` on `127.0.0.1:55432` with `--rm`; DSN `postgres://postgres:audit-test@127.0.0.1:55432/postgres?sslmode=disable` in `INFERPLANE_TEST_PG_DSN` (the repo's existing convention, e.g. `internal/analytics/pgstore/pgstore_test.go:20`). Stop it at the end of each task that starts it.
- Every changed doc under `docs/` with a `.ko.md` pair changes both, Korean in Korean (literal UTF-8), and the `.ko.md` `translation_source_sha256` is refreshed; `python3 scripts/check_docs_translations.py` passes.
- When a layer changes, update the matching module note in `internal/CLAUDE.md` in the same commit. `AGENTS.md` is generated: never edit it (root `CLAUDE.md` is not changed by this plan).
- Live checks use only the test gateway on 127.0.0.1 ports 18381/19391 with throwaway config and keys under `verify2`; never `mayu.service`, `/home/atomoh/inferplane-local` or the shared control plane. Admin tokens and keys are files that are never printed and are deleted at the end.
- Console code: textContent rendering only, no browser storage (ADR-001), i18n keys in en/ko/zh/ja.

## Review Focus

1. **The record names the wrong layer after composition.** A policy rule narrower than the config, a config narrower than the policy, a team record and a key option must each be named as the source of a 429. Pinned by `TestE2EGovernanceDenialNamesItsLimit` (Task 5), with the merge rules pinned by `TestTeamLimitsNameBindingRules`/`TestUserLimitsNameBindingRules` (Task 2) and the `fromOverlay` tables (Task 5).
2. **A lease-bound 402 is named after the policy rule the lease was granted for.** Once local spend passes the control-plane allowance, the denial must name `lease`. Pinned by `TestE2ELeaseBoundDenialNamesTheLease` (Task 5).
3. **A governance denial that also matched a privacy rule shows the privacy rule in "policy · rule".** Pinned by `TestProjectGovernanceDenial` (Task 7), including a legacy record with a governance code and routing policies.
4. **Upgrading a gateway with an existing analytics index.** A console table created before the new columns must open, keep its rows (shown as not recorded), and accept boot replay and new rows, twice in a row. Pinned by `TestAuditEventsMigrationKeepsRowsAndReplays` (Mode A) and `TestPgstoreAuditEventsMigrationKeepsRows` (Mode B), both in Task 7.
5. **A transient Mode B error silently drops console rows or leaks request text.** A deadlock-class error must abort the tick without committing usage rows or the checkpoint, name only the SQLSTATE, and succeed on the next tick. Pinned by `TestPgstoreConsoleTransientErrorAbortsTick` (Task 9).

## File Structure

| Task | Create | Modify |
|---|---|---|
| 1 | `internal/audit/governance.go`, `internal/audit/governance_test.go` | `internal/audit/record.go`, `internal/audit/evidence.go`, `internal/CLAUDE.md` |
| 2 | `internal/policy/rule_ref_test.go` | `internal/policy/store.go`, `internal/CLAUDE.md` |
| 3 | `internal/governance/denial.go`, `internal/governance/denial_test.go` | `internal/governance/governance.go`, `fromconfig.go`, `local_money.go`, `shared.go`, `internal/CLAUDE.md` |
| 4 | `internal/server/governance_denial_test.go` | `internal/audit/deny_reason.go`, `deny_reason_test.go`, `internal/server/anthropicapi/messages.go`, `openaiapi/chat.go`, `bedrockapi/invoke.go`, `responsesapi/handler.go`, `requestpolicy/pricing.go`, `requestpolicy/pricing_test.go`, `internal/CLAUDE.md` |
| 5 | `cmd/mayu/limit_sources.go`, `cmd/mayu/governance_denial_e2e_test.go` | `cmd/mayu/gateway.go`, `rate_overlay.go`, `budget_overlay.go`, `rate_overlay_test.go`, `budget_overlay_test.go` |
| 6 | `internal/authority/pgstore/shared_denial_test.go`, `internal/authority/local/denial_test.go` | `internal/authority/pgstore/shared_constraints.go`, `internal/authority/local/admission.go`, `internal/CLAUDE.md` |
| 7 | `internal/analytics/governance_projection_test.go`, `internal/analytics/pgstore/governance_projection_test.go` | `internal/analytics/auditquery.go`, `auditevents.go`, `index.go`, `pgstore/pgstore.go` (`ensureSchema`), `docs/reference/{data,api,security}{,.ko}.md`, `internal/CLAUDE.md` |
| 8 | `internal/analytics/ingest_audit_test.go` | `internal/analytics/auditevents_sqlite.go`, `docs/reference/data{,.ko}.md`, `internal/CLAUDE.md` |
| 9 | `internal/analytics/pgstore/console_batch_test.go` | `internal/analytics/pgstore/pgstore.go` (`ingestItems`), `pgstore/audit_isolation_test.go`, `docs/reference/data{,.ko}.md`, `internal/CLAUDE.md` |
| 10 | — | `internal/server/adminui/static/{audit,i18n}.js`, `testdata/{audit_console.cjs,audit_qa.py}`, `adminui/audit_console_test.go`, `docs/decisions/ADR-057-audit-console-evidence.md`, `docs/superpowers/specs/2026-10-29-audit-console-design.md` |
| 11 | `internal/server/responsesapi/mask.go`, `responsesapi/mask_test.go` | `internal/server/responsesapi/handler.go` |
| 12 | `examples/policies/pii-email-mask.yaml`, `internal/server/example_pii_mask_test.go` | — |
| 13 | `verify2/` scratch only | — |
| 14 | — | — |

Line numbers in each task are those of the file as it stands when that task starts (after the earlier tasks).

---

### Task 1: Audit record names the limit behind a governance denial

**Files:**
- Create: `internal/audit/governance.go`
- Modify: `internal/audit/record.go:146` (end of `Record`), `internal/audit/evidence.go:58` (collector field), `internal/audit/evidence.go:232-234` (end of `Finish`)
- Modify: `internal/CLAUDE.md:93` (`audit/` bullet)
- Test: `internal/audit/governance_test.go`

**Interfaces:**
- Produces:
  - constants `GovScopeTeam`, `GovScopeKey`, `GovScopeUser`, `GovScopeGateway`; `GovLimitRPM`, `GovLimitTPM`, `GovLimitTokensPerDay`, `GovLimitTokensPerMonth`, `GovLimitBudgetMonth`, `GovLimitBudgetDay`, `GovLimitBudget`, `GovLimitPricing`; `GovSourceConfig`, `GovSourceTeamRecord`, `GovSourcePolicy`, `GovSourceLease`, `GovSourceKey`, `GovSourceBudgetStore`
  - `type PolicyRuleRef struct{ Name string; Generation int64; Rule string }`
  - `type GovernanceRef struct{ Scope, Limit, Source string; Policy *PolicyRuleRef }`
  - `Record.Governance *GovernanceRef` (`json:"governance,omitempty"`)
  - `func SetGovernance(ctx context.Context, ref *GovernanceRef)`
- Consumes: `WithEvidence`, `evidenceFrom`, `Finish` (existing, `internal/audit/evidence.go`).

- [ ] **Step 1: Write the failing test**

Create `internal/audit/governance_test.go`:

```go
package audit

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Only the gateway denial record carries the limit that refused it.
func TestGovernanceOnlyOnDenialRecord(t *testing.T) {
	ctx := WithEvidence(context.Background(), time.Now(), nil)
	ref := &GovernanceRef{Scope: GovScopeTeam, Limit: GovLimitRPM, Source: GovSourcePolicy,
		Policy: &PolicyRuleRef{Name: "team-rate", Generation: 3, Rule: "rpm"}}
	SetGovernance(ctx, ref)
	ref.Policy.Rule = "mutated" // the collector keeps its own copy
	denied := Record{Event: "request_started", Outcome: &OutcomeRef{Status: 429}}
	Finish(ctx, &denied)
	done := Record{Event: "request_completed", Outcome: &OutcomeRef{Status: 200}}
	Finish(ctx, &done)
	admitted := Record{Event: "request_started"}
	Finish(ctx, &admitted)
	if denied.Governance == nil || denied.Governance.Policy == nil || denied.Governance.Policy.Rule != "rpm" {
		t.Fatalf("denial governance = %+v", denied.Governance)
	}
	if done.Governance != nil || admitted.Governance != nil {
		t.Fatalf("governance leaked: completed=%+v admitted=%+v", done.Governance, admitted.Governance)
	}
	SetGovernance(ctx, nil)
	again := Record{Event: "request_started", Outcome: &OutcomeRef{Status: 429}}
	Finish(ctx, &again)
	if again.Governance != nil {
		t.Fatalf("cleared governance written: %+v", again.Governance)
	}
}

// The field is appended last; a policy without a generation omits it.
func TestGovernanceWireShape(t *testing.T) {
	r := Record{SchemaVersion: 1, Event: "request_started", ID: "01J", TS: "2026-10-29T00:00:00Z", Instance: "i",
		Principal: PrincipalRef{KeyID: "ik", Team: "t"}, Request: RequestRef{Ingress: "anthropic", ModelRequested: "m"},
		Outcome:    &OutcomeRef{Status: 402, Error: DenyTeamBudgetExceeded.Ptr()},
		Governance: &GovernanceRef{Scope: GovScopeTeam, Limit: GovLimitBudgetDay, Source: GovSourcePolicy, Policy: &PolicyRuleRef{Name: "p", Rule: "r"}}}
	got, err := r.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"event":"request_started","id":"01J","ts":"2026-10-29T00:00:00Z","instance":"i","principal":{"key_id":"ik","team":"t"},"request":{"ingress":"anthropic","model_requested":"m","stream":false},"outcome":{"status":402,"fallback_used":false,"partial":false,"error":"team_budget_exceeded"},"trace_id":null,"prev_hash":"","governance":{"scope":"team","limit":"budget_day","source":"policy","policy":{"name":"p","rule":"r"}}}`
	if string(got) != want {
		t.Fatalf("wire shape:\n got %s\nwant %s", got, want)
	}
}

// A chain mixing records with and without the field still verifies.
func TestGovernanceMixedChainVerifies(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter("i", filepath.Join(t.TempDir(), "a.wal"), []Sink{NewWriterSink("b", &buf, true)})
	if err != nil {
		t.Fatal(err)
	}
	w.Append(Record{SchemaVersion: 1, Event: "request_started", ID: "01A", TS: "2026-10-29T00:00:00Z", Outcome: &OutcomeRef{Status: 429, Error: DenyKeyRateLimited.Ptr()}})
	ctx := WithEvidence(context.Background(), time.Now(), nil)
	SetGovernance(ctx, &GovernanceRef{Scope: GovScopeKey, Limit: GovLimitRPM, Source: GovSourceKey})
	rec := Record{SchemaVersion: 1, Event: "request_started", ID: "01B", TS: "2026-10-29T00:00:01Z", Outcome: &OutcomeRef{Status: 429, Error: DenyKeyRateLimited.Ptr()}}
	Finish(ctx, &rec)
	w.Append(rec)
	w.Close()
	if !bytes.Contains(buf.Bytes(), []byte(`"governance":{"scope":"key","limit":"rpm","source":"key"}`)) {
		t.Fatalf("chain lacks the new field: %s", buf.Bytes())
	}
	res, err := Verify(bytes.NewReader(buf.Bytes()))
	if err != nil || !res.OK || res.Records != 2 {
		t.Fatalf("verify = %+v, %v", res, err)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run; mkdir -p $S
cd $W && go test -buildvcs=false ./internal/audit/ -run TestGovernance > $S/t1-red.log 2>&1; echo "exit=$?"; grep -E 'FAIL|undefined' $S/t1-red.log
```
Expected: `exit=1`, `FAIL	github.com/inferplane/inferplane/internal/audit [build failed]` and `undefined: GovernanceRef`, `undefined: SetGovernance`, `denied.Governance undefined`.

- [ ] **Step 3: Implement**

Create `internal/audit/governance.go`:

```go
package audit

import "context"

// Governance denial evidence (ADR-057): which limit refused a request before
// dispatch. Every value is from a closed set; a GovernanceRef never carries a
// limit value, a counter, a key, a user identifier or a secret.
const (
	GovScopeTeam    = "team"
	GovScopeKey     = "key"
	GovScopeUser    = "user"
	GovScopeGateway = "gateway"

	GovLimitRPM            = "rpm"
	GovLimitTPM            = "tpm"
	GovLimitTokensPerDay   = "tokens_per_day"
	GovLimitTokensPerMonth = "tokens_per_month"
	GovLimitBudgetMonth    = "budget_month"
	GovLimitBudgetDay      = "budget_day"
	// GovLimitBudget is a budget whose window the refusing gate does not
	// report (the ADR-034 lease gate).
	GovLimitBudget  = "budget"
	GovLimitPricing = "pricing"

	GovSourceConfig      = "config"       // config file: teams.<team>, pricing.on_missing
	GovSourceTeamRecord  = "team_record"  // keystore team record (/admin/teams)
	GovSourcePolicy      = "policy"       // a GovernancePolicy rule, named in Policy
	GovSourceLease       = "lease"        // a control-plane budget lease (ADR-034)
	GovSourceKey         = "key"          // the virtual key's own limit
	GovSourceBudgetStore = "budget_store" // the budget store's capacity fail-safe
)

// PolicyRuleRef names one GovernancePolicy rule. Generation is omitted when
// the enforcing authority does not carry it (ADR-045 grants name only the
// policy and the rule).
type PolicyRuleRef struct {
	Name       string `json:"name"`
	Generation int64  `json:"generation,omitempty"`
	Rule       string `json:"rule"`
}

// GovernanceRef names the limit behind a governance denial: its scope, the
// kind of limit, and where that limit was set. Source is omitted when the
// governor was built without sources (a library caller, a test).
type GovernanceRef struct {
	Scope  string         `json:"scope"`
	Limit  string         `json:"limit"`
	Source string         `json:"source,omitempty"`
	Policy *PolicyRuleRef `json:"policy,omitempty"`
}

// SetGovernance stores the limit that is refusing this request. Finish writes
// it onto the gateway denial record (a request_started record with an
// outcome) and nowhere else; a nil ref clears it.
func SetGovernance(ctx context.Context, ref *GovernanceRef) {
	e := evidenceFrom(ctx)
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.gov = cloneGovernance(ref)
}

func cloneGovernance(ref *GovernanceRef) *GovernanceRef {
	if ref == nil {
		return nil
	}
	c := *ref
	if ref.Policy != nil {
		p := *ref.Policy
		c.Policy = &p
	}
	return &c
}
```

In `internal/audit/record.go`, replace line 146:

```go
	AutoMode *AutoModeRef `json:"auto_mode,omitempty"`
```

with:

```go
	AutoMode *AutoModeRef `json:"auto_mode,omitempty"`
	// Governance names the limit behind a governance denial (ADR-057). Only a
	// gateway denial (request_started with an outcome) carries it; appended
	// with omitempty like the fields above.
	Governance *GovernanceRef `json:"governance,omitempty"`
```

In `internal/audit/evidence.go`, replace line 58:

```go
	auto     *AutoModeRef
```

with:

```go
	auto     *AutoModeRef
	gov      *GovernanceRef
```

In `internal/audit/evidence.go`, replace lines 232–234:

```go
		rec.AutoMode = &a
	}
}
```

with:

```go
		rec.AutoMode = &a
	}
	if rec.Event == "request_started" && e.gov != nil {
		rec.Governance = cloneGovernance(e.gov)
	}
}
```

- [ ] **Step 4: Run the package and watch it pass**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run
cd $W && gofmt -l internal/audit; go test -buildvcs=false -race ./internal/audit/... > $S/t1.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---)' $S/t1.log
```
Expected: no `gofmt` output, `exit=0`, `ok` for `internal/audit` and `internal/audit/s3anchor`. `TestRecordWithoutEvidenceKeepsBytes` still passes, so records without the field keep their bytes.

- [ ] **Step 5: Module note**

In `internal/CLAUDE.md`, replace line 93:

```markdown
- `audit/` — single-writer hash-chain writer, WAL, `verify.go`. `audit/s3anchor/` is opt-in S3 Object Lock anchoring (ADR-012). `Record.BodyRef`/`RecordRef` (ADR-018) are appended at the struct end (omitempty) so mixed-version chains still verify byte-identically. `UsageRef.CacheCreation{5m,1h}InputTokens` follow that same append-at-the-end rule: `CacheCreationInputTokens` stays the flat TOTAL for existing consumers while the two tiers make the 1.25x/2x billing reproducible from the ledger alone. `RequestRef.ModelSubstitutedFrom` (ADR-041, appended at that struct's end, same rule) carries the client's ORIGINAL requested model when a budget tier substituted it — `ModelRequested` by then already holds the model actually served. `WithSubstitutedFrom`/`SubstitutedFrom` thread it via request context from the ingress substitution point down to the audit call. `evidence.go` (ADR-057) collects per-request `attempts[]`, `client` and `auto_mode` evidence: `WithEvidence` attaches the collector, `BeginAttempt` returns each attempt's end function, and `Finish` stamps it, plus `latency` and `fallback_used`/`fallback_chain`, onto terminal records only (`request_completed`, or a `request_started` that carries an outcome); the new fields are appended `omitempty`.
```

with:

```markdown
- `audit/` — single-writer hash-chain writer, WAL, `verify.go`. `audit/s3anchor/` is opt-in S3 Object Lock anchoring (ADR-012). `Record.BodyRef`/`RecordRef` (ADR-018) are appended at the struct end (omitempty) so mixed-version chains still verify byte-identically. `UsageRef.CacheCreation{5m,1h}InputTokens` follow that same append-at-the-end rule: `CacheCreationInputTokens` stays the flat TOTAL for existing consumers while the two tiers make the 1.25x/2x billing reproducible from the ledger alone. `RequestRef.ModelSubstitutedFrom` (ADR-041, appended at that struct's end, same rule) carries the client's ORIGINAL requested model when a budget tier substituted it — `ModelRequested` by then already holds the model actually served. `WithSubstitutedFrom`/`SubstitutedFrom` thread it via request context from the ingress substitution point down to the audit call. `evidence.go` (ADR-057) collects per-request `attempts[]`, `client` and `auto_mode` evidence: `WithEvidence` attaches the collector, `BeginAttempt` returns each attempt's end function, and `Finish` stamps it, plus `latency` and `fallback_used`/`fallback_chain`, onto terminal records only (`request_completed`, or a `request_started` that carries an outcome); the new fields are appended `omitempty`. `governance.go` (ADR-057) adds `governance{scope, limit, source, policy}`, the limit behind a governance denial: `SetGovernance` stores it in the same collector and `Finish` writes it only onto a gateway denial record (`request_started` with an outcome). It holds names from closed sets plus the policy and rule name, never a limit value, counter or identifier.
```

- [ ] **Step 6: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree
git -C $W add internal/audit/governance.go internal/audit/governance_test.go internal/audit/record.go internal/audit/evidence.go internal/CLAUDE.md
git -C $W commit -s -m "feat(audit): record the limit behind a governance denial (ADR-057)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Policy merges name the rule behind each limit

**Files:**
- Modify: `internal/policy/store.go:53` (end of `TeamLimits`, plus `RuleRef`), `:71` (end of `UserLimits`), `:534-537`, `:563`, `:570` (`mergeTeamLimits`), `:579-584` (`takesRate` before `minNonZero`), `:613`, `:622-623` (`narrow`), `:629`, `:640-643` (`takesBudget`, `mergeBudgetWindow`), `:649` (`userLimitsFromBudget`), `:681` (`mergeUserLimits` call)
- Modify: `internal/CLAUDE.md:114` (`policy/` bullet)
- Test: `internal/policy/rule_ref_test.go`

**Interfaces:**
- Produces:
  - `type RuleRef struct{ Policy string; Generation int64; Rule string }`
  - `TeamLimits.RPMRule, TPMRule, BudgetRule, BudgetDayRule RuleRef`; `UserLimits.BudgetRule, BudgetDayRule RuleRef`
  - unexported `takesRate(current, next int64) bool`, `takesBudget(limit int64, hard bool, otherLimit int64, otherHard bool) bool`; `userLimitsFromBudget(b *Budget, ref RuleRef) UserLimits`
- Consumes: `Policy.Name`, `Policy.Generation`, `Rule.Name` (existing).

- [ ] **Step 1: Write the failing test**

Create `internal/policy/rule_ref_test.go`:

```go
package policy

import (
	"testing"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
)

// Each merged team limit names the rule whose value it holds (ADR-057). A hard
// cap names its rule over a smaller soft meter; a tie keeps the first rule.
func TestTeamLimitsNameBindingRules(t *testing.T) {
	policies := []*Policy{
		{Name: "wide", Generation: 2, Subject: Subject{Team: "t"}, Rules: []Rule{
			{Name: "rate", Rate: &Rate{RPM: 100, TPM: 5000}},
			{Name: "soft-day", Budget: &Budget{LimitMicroUSD: 1_000, Period: v1alpha1.PeriodCalendarDay}},
		}},
		{Name: "tight", Generation: 7, Subject: Subject{Team: "t"}, Rules: []Rule{
			{Name: "rpm", Rate: &Rate{RPM: 10}},
			{Name: "tpm-tie", Rate: &Rate{TPM: 5000}},
			{Name: "hard-day", Budget: &Budget{LimitMicroUSD: 9_000, HardCap: true, Period: v1alpha1.PeriodCalendarDay}},
			{Name: "month", Budget: &Budget{LimitMicroUSD: 50_000, HardCap: true, Period: v1alpha1.PeriodCalendarMonth}},
		}},
	}
	tl := mergeTeamLimits(policies)["t"]
	want := map[string]RuleRef{
		"rpm":   {Policy: "tight", Generation: 7, Rule: "rpm"},
		"tpm":   {Policy: "wide", Generation: 2, Rule: "rate"},
		"day":   {Policy: "tight", Generation: 7, Rule: "hard-day"},
		"month": {Policy: "tight", Generation: 7, Rule: "month"},
	}
	for name, got := range map[string]RuleRef{"rpm": tl.RPMRule, "tpm": tl.TPMRule, "day": tl.BudgetDayRule, "month": tl.BudgetRule} {
		if got != want[name] {
			t.Errorf("%s rule = %+v, want %+v", name, got, want[name])
		}
	}
	if tl.RPM != 10 || tl.TPM != 5000 || tl.BudgetMicrosPerDay != 9_000 || !tl.BudgetDayHard || tl.BudgetMicrosPerMonth != 50_000 {
		t.Fatalf("merged values changed: %+v", tl)
	}
}

// User limits name their rule through both folds: rules inside one subject
// and the user-only view narrowed by the (team, user) view.
func TestUserLimitsNameBindingRules(t *testing.T) {
	dir := t.TempDir()
	writePolicy(t, dir, "p.yaml", `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: user-global, generation: 4 }
spec:
  subject: { user: sub-1 }
  rules:
  - name: month-cap
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 1000, hardCap: true }
  - name: day-cap
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 300, hardCap: true, period: CalendarDay }
---
apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: user-in-t, generation: 9 }
spec:
  subject: { team: t, user: sub-1 }
  rules:
  - name: month-tight
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 200, hardCap: true }
`)
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ul, ok := s.UserLimits("t", "sub-1")
	if !ok || ul.BudgetMicrosPerMonth != 200_000 || ul.BudgetMicrosPerDay != 300_000 {
		t.Fatalf("UserLimits = %+v, %v", ul, ok)
	}
	if ul.BudgetRule != (RuleRef{Policy: "user-in-t", Generation: 9, Rule: "month-tight"}) {
		t.Fatalf("month rule = %+v", ul.BudgetRule)
	}
	if ul.BudgetDayRule != (RuleRef{Policy: "user-global", Generation: 4, Rule: "day-cap"}) {
		t.Fatalf("day rule = %+v", ul.BudgetDayRule)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run; mkdir -p $S
cd $W && go test -buildvcs=false ./internal/policy/ -run NameBindingRules > $S/t2-red.log 2>&1; echo "exit=$?"; grep -E 'FAIL|undefined' $S/t2-red.log
```
Expected: `exit=1`, `[build failed]`, `undefined: RuleRef`, `tl.RPMRule undefined (type TeamLimits has no field or method RPMRule)`.

- [ ] **Step 3: Implement**

In `internal/policy/store.go`, replace line 53:

```go
	RoutingBudgetMicrosPerDay   int64
```

with:

```go
	RoutingBudgetMicrosPerDay   int64
	// RPMRule, TPMRule, BudgetRule and BudgetDayRule name the rule whose value
	// each merged admission limit above holds (ADR-057), so a governance denial
	// can name it. A tie keeps the first rule; zero means no rule sets it.
	RPMRule, TPMRule, BudgetRule, BudgetDayRule RuleRef
}

// RuleRef names one GovernancePolicy rule by policy name, generation and rule
// name. It carries no limit value.
type RuleRef struct {
	Policy     string
	Generation int64
	Rule       string
```

In `internal/policy/store.go`, replace line 71:

```go
	AdminContactDay      string
```

with:

```go
	AdminContactDay      string
	// BudgetRule and BudgetDayRule name the rule each merged window holds
	// (ADR-057), as TeamLimits does.
	BudgetRule, BudgetDayRule RuleRef
```

In `internal/policy/store.go`, replace lines 534–537:

```go
		for _, r := range p.Rules {
			if r.Rate != nil {
				contributed = true
				tl.RPM = minNonZero(tl.RPM, r.Rate.RPM)
```

with:

```go
		for _, r := range p.Rules {
			ref := RuleRef{Policy: p.Name, Generation: p.Generation, Rule: r.Name}
			if r.Rate != nil {
				contributed = true
				if takesRate(tl.RPM, r.Rate.RPM) {
					tl.RPMRule = ref
				}
				if takesRate(tl.TPM, r.Rate.TPM) {
					tl.TPMRule = ref
				}
				tl.RPM = minNonZero(tl.RPM, r.Rate.RPM)
```

In `internal/policy/store.go`, replace line 563:

```go
				case r.Budget.Period == v1alpha1.PeriodCalendarDay:
```

with:

```go
				case r.Budget.Period == v1alpha1.PeriodCalendarDay:
					if takesBudget(tl.BudgetMicrosPerDay, tl.BudgetDayHard, r.Budget.LimitMicroUSD, r.Budget.HardCap) {
						tl.BudgetDayRule = ref
					}
```

In `internal/policy/store.go`, replace line 570:

```go
					// meaning of "no period stated" everywhere in this schema.
```

with:

```go
					// meaning of "no period stated" everywhere in this schema.
					if takesBudget(tl.BudgetMicrosPerMonth, tl.BudgetHard, r.Budget.LimitMicroUSD, r.Budget.HardCap) {
						tl.BudgetRule = ref
					}
```

In `internal/policy/store.go`, replace lines 579–584:

```go
		}
	}
	return out
}

func minNonZero(a, b int64) int64 {
```

with:

```go
		}
	}
	return out
}

// takesRate reports whether minNonZero(current, next) takes next: a nonzero
// rate binds over none, and the smaller one over a larger one.
func takesRate(current, next int64) bool {
	return next != 0 && (current == 0 || next < current)
}

func minNonZero(a, b int64) int64 {
```

In `internal/policy/store.go`, replace line 613:

```go
func (u UserLimits) narrow(o UserLimits) UserLimits {
```

with:

```go
func (u UserLimits) narrow(o UserLimits) UserLimits {
	if takesBudget(u.BudgetMicrosPerMonth, u.BudgetHard, o.BudgetMicrosPerMonth, o.BudgetHard) {
		u.BudgetRule = o.BudgetRule
	}
	if takesBudget(u.BudgetMicrosPerDay, u.BudgetDayHard, o.BudgetMicrosPerDay, o.BudgetDayHard) {
		u.BudgetDayRule = o.BudgetDayRule
	}
```

In `internal/policy/store.go`, replace lines 622–623:

```go

// mergeBudgetWindow preserves the independent hard admission cap. Original
```

with:

```go

// takesBudget reports whether a window merge keeps the other limit: hard caps
// bind before soft meters, then the smaller limit wins; a tie keeps the
// current one. mergeBudgetWindow and the ADR-057 rule names share it.
func takesBudget(limit int64, hard bool, otherLimit int64, otherHard bool) bool {
	return otherLimit != 0 && (limit == 0 || (otherHard && !hard) || (otherHard == hard && otherLimit < limit))
}

// mergeBudgetWindow preserves the independent hard admission cap. Original
```

In `internal/policy/store.go`, replace line 629:

```go
	if limit == 0 || (otherHard && !hard) || (otherHard == hard && otherLimit < limit) {
```

with:

```go
	if takesBudget(limit, hard, otherLimit, otherHard) {
```

In `internal/policy/store.go`, replace lines 640–643:

```go
// rule is the caller's business, not this function's.
func userLimitsFromBudget(b *Budget) UserLimits {
	if b.Period == v1alpha1.PeriodCalendarDay {
		return UserLimits{BudgetMicrosPerDay: b.LimitMicroUSD, BudgetDayHard: b.HardCap, AdminContactDay: b.AdminContact}
```

with:

```go
// rule is the caller's business, not this function's. ref names the rule.
func userLimitsFromBudget(b *Budget, ref RuleRef) UserLimits {
	if b.Period == v1alpha1.PeriodCalendarDay {
		return UserLimits{BudgetMicrosPerDay: b.LimitMicroUSD, BudgetDayHard: b.HardCap, AdminContactDay: b.AdminContact, BudgetDayRule: ref}
```

In `internal/policy/store.go`, replace line 649:

```go
	return UserLimits{BudgetMicrosPerMonth: b.LimitMicroUSD, BudgetHard: b.HardCap, AdminContact: b.AdminContact}
```

with:

```go
	return UserLimits{BudgetMicrosPerMonth: b.LimitMicroUSD, BudgetHard: b.HardCap, AdminContact: b.AdminContact, BudgetRule: ref}
```

In `internal/policy/store.go`, replace line 681:

```go
			ul = ul.narrow(userLimitsFromBudget(r.Budget))
```

with:

```go
			ul = ul.narrow(userLimitsFromBudget(r.Budget, RuleRef{Policy: p.Name, Generation: p.Generation, Rule: r.Name}))
```

- [ ] **Step 4: Run the package and watch it pass**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run
cd $W && gofmt -l internal/policy; go test -buildvcs=false -race ./internal/policy/... > $S/t2.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---)' $S/t2.log
```
Expected: no `gofmt` output, `exit=0`, `ok  	github.com/inferplane/inferplane/internal/policy`. The existing merge tests (`budget_merge_test.go`, `userlimits_test.go`) still pass, so no merged value changed.

- [ ] **Step 5: Module note**

In `internal/CLAUDE.md`, replace line 114:

```markdown
- `policy/` — rule + budget-lease schema shared by BOTH binaries (ADR-031/032/033) — the single source of truth. Converts `api/v1alpha1` wire docs and explicitly rejects anything unsupported (`UnsupportedError`, reported upstream, never silently ignored). Subjects (`team`/`user`) are equal citizens; most-restrictive-wins on multi-match. Exactly one rule kind per rule; `failurePolicy` required, no default. A `rate`/`budget` rule may set `unlimited: true` instead of a numeric limit — an explicit, auditable "no cap" (ADR-039) that internally still produces the pre-existing zero-value sentinel every consumer treats as unlimited; `mergeTeamLimits` and the controlplane lease ledger both give it its own branch so it can never narrow, widen, or silently overwrite a real limit set by another rule. The local file channel (ADR-033) rejects the routing rule's cache-affinity half and user-subject rate rather than accepting and ignoring them; the routing rule's `budgetTiers` half (ADR-041) and user-subject BUDGET rules (ADR-042 Phase 3) ARE enforceable — `Routing` is exactly one of `Affinity`, `BudgetTiers`, or `Context`; context and independent `SensitiveData` rules are enforceable (ADR-043). `Store.SetRoutedAndPriced` installs the apply-time check that every budget-tier, privacy, and context TARGET is routed+priced on this data plane (nil skips it; the control plane's `LoadWirePaths` deliberately never installs one, same posture as its skip of `checkEnforceable`). `BudgetRule.period` (ADR-042) selects a budget rule's calendar window — `CalendarDay` | `CalendarMonth`, empty = CalendarMonth, so every pre-existing document keeps its meaning; a day cap plus a month cap are TWO rules in one policy (`hardCap`/`failurePolicy`/`lease`/`adminContact` are per-rule, and so is the lease ledger row), and `period` must not be combined with `unlimited`. `TeamLimits` folds the day window independently of the month (`BudgetMicrosPerDay`/`BudgetDayHard`/`AdminContactDay`) — most-restrictive-wins is computed per window, and `unlimited` is window-agnostic, touching neither. `UserLimits`/`mergeUserLimits` (ADR-042 Phase 3) do the same for user-subject BUDGET rules — budget only, no RPM/TPM; most-restrictive-wins per window via `narrow`, `unlimited` window-agnostic here too — and `Store.UserLimits(team, user)` folds a user-only subject together with the (team, user) one. `checkEnforceable` is now rate-only for the subject restriction: user-subject budget is enforceable, user-subject rate still is not (needs a rate-share model).
```

with:

```markdown
- `policy/` — rule + budget-lease schema shared by BOTH binaries (ADR-031/032/033) — the single source of truth. Converts `api/v1alpha1` wire docs and explicitly rejects anything unsupported (`UnsupportedError`, reported upstream, never silently ignored). Subjects (`team`/`user`) are equal citizens; most-restrictive-wins on multi-match. Exactly one rule kind per rule; `failurePolicy` required, no default. A `rate`/`budget` rule may set `unlimited: true` instead of a numeric limit — an explicit, auditable "no cap" (ADR-039) that internally still produces the pre-existing zero-value sentinel every consumer treats as unlimited; `mergeTeamLimits` and the controlplane lease ledger both give it its own branch so it can never narrow, widen, or silently overwrite a real limit set by another rule. The local file channel (ADR-033) rejects the routing rule's cache-affinity half and user-subject rate rather than accepting and ignoring them; the routing rule's `budgetTiers` half (ADR-041) and user-subject BUDGET rules (ADR-042 Phase 3) ARE enforceable — `Routing` is exactly one of `Affinity`, `BudgetTiers`, or `Context`; context and independent `SensitiveData` rules are enforceable (ADR-043). `Store.SetRoutedAndPriced` installs the apply-time check that every budget-tier, privacy, and context TARGET is routed+priced on this data plane (nil skips it; the control plane's `LoadWirePaths` deliberately never installs one, same posture as its skip of `checkEnforceable`). `BudgetRule.period` (ADR-042) selects a budget rule's calendar window — `CalendarDay` | `CalendarMonth`, empty = CalendarMonth, so every pre-existing document keeps its meaning; a day cap plus a month cap are TWO rules in one policy (`hardCap`/`failurePolicy`/`lease`/`adminContact` are per-rule, and so is the lease ledger row), and `period` must not be combined with `unlimited`. `TeamLimits` folds the day window independently of the month (`BudgetMicrosPerDay`/`BudgetDayHard`/`AdminContactDay`) — most-restrictive-wins is computed per window, and `unlimited` is window-agnostic, touching neither. `UserLimits`/`mergeUserLimits` (ADR-042 Phase 3) do the same for user-subject BUDGET rules — budget only, no RPM/TPM; most-restrictive-wins per window via `narrow`, `unlimited` window-agnostic here too — and `Store.UserLimits(team, user)` folds a user-only subject together with the (team, user) one. `checkEnforceable` is now rate-only for the subject restriction: user-subject budget is enforceable, user-subject rate still is not (needs a rate-share model). `TeamLimits`/`UserLimits` also name the rule whose value each merged limit holds (`RuleRef{Policy, Generation, Rule}`, ADR-057) so a governance denial can name it; `takesRate`/`takesBudget` are the comparisons the merges themselves use, and a tie keeps the first rule.
```

- [ ] **Step 6: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree
git -C $W add internal/policy/store.go internal/policy/rule_ref_test.go internal/CLAUDE.md
git -C $W commit -s -m "feat(policy): name the rule behind each merged limit (ADR-057)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: The governor names the refusing limit

**Files:**
- Create: `internal/governance/denial.go`
- Modify: `internal/governance/governance.go:57-59` (`TeamPolicy`), `:104-106` (`UserPolicy`), `:305-311` (`GovDecision`), `:362`, `:368-373`, `:379`, `:403`, `:414`, `:425-428`, `:439`, `:449`, `:477`, `:487` (`PreCheck` deny sites), `:840-842` (`PricingGuard`)
- Modify: `internal/governance/fromconfig.go:36` (`Limits`), `:60` (`PolicyFromLimits`), `:87` (`PoliciesFromConfig`)
- Modify: `internal/governance/local_money.go:8-9` (imports), `:18` (`localMoneyScope`), `:46-61` (`localMoneyScopes`), `:100-101`, `:116-118` (`ReserveLocalBudget` refusals)
- Modify: `internal/governance/shared.go:6` (imports), `:63-67` (`SharedDenial`)
- Modify: `internal/CLAUDE.md:91` (`governance/` bullet)
- Test: `internal/governance/denial_test.go`

**Interfaces:**
- Consumes: `audit.GovernanceRef`, `audit.PolicyRuleRef`, `audit.Gov*` constants (Task 1).
- Produces:
  - `type LimitSource struct{ Source string; Policy *audit.PolicyRuleRef }`
  - `type TeamSources struct{ Rate, TokenRate, Quota, Budget, BudgetDay LimitSource }`; `type UserSources struct{ Budget, BudgetDay LimitSource }`
  - `TeamPolicy.Sources TeamSources`; `UserPolicy.Sources UserSources`; `Limits.Sources TeamSources`
  - `func ConfigSources() TeamSources`; `func PricingDenial() *audit.GovernanceRef`
  - `GovDecision.Governance *audit.GovernanceRef`
  - `type BudgetDenial struct{ Err error; Governance *audit.GovernanceRef }` with `Error() string`, `Unwrap() error`
  - `SharedDenial.Governance *audit.GovernanceRef`
  - `func DenialGovernance(err error) *audit.GovernanceRef`

- [ ] **Step 1: Write the failing test**

Create `internal/governance/denial_test.go`:

```go
package governance

import (
	"errors"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/pricing"
)

func policySource(name, rule string) LimitSource {
	return LimitSource{Source: audit.GovSourcePolicy, Policy: &audit.PolicyRuleRef{Name: name, Generation: 5, Rule: rule}}
}

func requireGovernance(t *testing.T, got *audit.GovernanceRef, scope, limit, source, rule string) {
	t.Helper()
	if got == nil || got.Scope != scope || got.Limit != limit || got.Source != source {
		t.Fatalf("governance = %+v, want %s/%s/%s", got, scope, limit, source)
	}
	gotRule := ""
	if got.Policy != nil {
		gotRule = got.Policy.Rule
	}
	if gotRule != rule {
		t.Fatalf("policy rule = %q, want %q", gotRule, rule)
	}
}

// Every PreCheck denial names the limit that refused it (ADR-057).
func TestPreCheckNamesDenyingLimit(t *testing.T) {
	sources := TeamSources{Rate: policySource("team-rate", "rpm"), TokenRate: policySource("team-rate", "tpm"),
		Quota: LimitSource{Source: audit.GovSourceTeamRecord}, Budget: policySource("money", "month"),
		BudgetDay: LimitSource{Source: audit.GovSourceConfig}}
	blocking := blockingStore{resets: map[string]time.Time{}}
	for _, tc := range []struct {
		name                       string
		team                       TeamPolicy
		key                        KeyPolicy
		store                      budget.BudgetStore
		estimate                   int64
		scope, limit, source, rule string
	}{
		{"team rpm", TeamPolicy{RatePerMin: 1, RateBurst: 1}, KeyPolicy{}, nil, 0, audit.GovScopeTeam, audit.GovLimitRPM, audit.GovSourcePolicy, "rpm"},
		{"team tpm", TeamPolicy{TokensPerMinute: 1000}, KeyPolicy{}, nil, 800, audit.GovScopeTeam, audit.GovLimitTPM, audit.GovSourcePolicy, "tpm"},
		{"team quota", TeamPolicy{TokensPerDay: 1000}, KeyPolicy{}, nil, 2000, audit.GovScopeTeam, audit.GovLimitTokensPerDay, audit.GovSourceTeamRecord, ""},
		{"team month", TeamPolicy{BudgetMicrosPerMonth: 1, BudgetExceeded: "block"}, KeyPolicy{}, blocking, 0, audit.GovScopeTeam, audit.GovLimitBudgetMonth, audit.GovSourcePolicy, "month"},
		{"team day", TeamPolicy{BudgetMicrosPerDay: 1, BudgetDayExceeded: "block"}, KeyPolicy{}, blocking, 0, audit.GovScopeTeam, audit.GovLimitBudgetDay, audit.GovSourceConfig, ""},
		{"team store capacity", TeamPolicy{BudgetMicrosPerMonth: 1, BudgetExceeded: "warn"}, KeyPolicy{}, capacityBlockingStore{blocking}, 0, audit.GovScopeTeam, audit.GovLimitBudgetMonth, audit.GovSourceBudgetStore, ""},
		{"key rpm", TeamPolicy{}, KeyPolicy{RatePerMin: 1}, nil, 0, audit.GovScopeKey, audit.GovLimitRPM, audit.GovSourceKey, ""},
		{"key tpm", TeamPolicy{}, KeyPolicy{TokensPerMinute: 1000}, nil, 800, audit.GovScopeKey, audit.GovLimitTPM, audit.GovSourceKey, ""},
		{"key day", TeamPolicy{}, KeyPolicy{BudgetMicrosPerDay: 1}, blocking, 0, audit.GovScopeKey, audit.GovLimitBudgetDay, audit.GovSourceKey, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.store
			if store == nil {
				store = budget.NewMemory()
			}
			team := tc.team
			team.Sources = sources
			g := NewGovernor(map[string]TeamPolicy{"t": team}, limiter.NewMemory(), store, nil)
			s := Subject{Team: "t", KeyID: "k"}
			dec := g.PreCheck(s, tc.key, tc.estimate)
			if dec.Allowed {
				dec.Admission.Close()
				dec = g.PreCheck(s, tc.key, tc.estimate)
			}
			if dec.Allowed {
				t.Fatalf("second request admitted: %+v", dec)
			}
			requireGovernance(t, dec.Governance, tc.scope, tc.limit, tc.source, tc.rule)
		})
	}
}

func TestPreCheckNamesUserRuleLeaseAndPricing(t *testing.T) {
	g := NewGovernor(map[string]TeamPolicy{"t": {}}, limiter.NewMemory(), blockingStore{resets: map[string]time.Time{}}, nil)
	g.SetUserLookup(func(team, user string) (UserPolicy, bool) {
		return UserPolicy{BudgetMicrosPerDay: 1, BudgetExceeded: "block", Sources: UserSources{BudgetDay: policySource("person", "day")}}, true
	})
	dec := g.PreCheck(Subject{Team: "t", KeyID: "k", User: "alice"}, KeyPolicy{}, 0)
	requireGovernance(t, dec.Governance, audit.GovScopeUser, audit.GovLimitBudgetDay, audit.GovSourcePolicy, "day")

	g.SetLeaseGate(func(string) (bool, string) { return true, "budget lease expired" })
	dec = g.PreCheck(Subject{Team: "t", KeyID: "k"}, KeyPolicy{}, 0)
	requireGovernance(t, dec.Governance, audit.GovScopeTeam, audit.GovLimitBudget, audit.GovSourceLease, "")

	dec = PricingGuard(pricing.New(pricing.OnMissingBlock, nil), []PricedTarget{{Provider: "p", Upstream: "m"}})
	requireGovernance(t, dec.Governance, audit.GovScopeGateway, audit.GovLimitPricing, audit.GovSourceConfig, "")

	if dec := g.PreCheck(Subject{Team: "t"}, KeyPolicy{}, -1); dec.Allowed || dec.Governance != nil {
		t.Fatalf("an invalid estimate is not a configured limit: %+v", dec)
	}
}

// Config teams name the config file as the source of every limit.
func TestPoliciesFromConfigNameConfig(t *testing.T) {
	p := PoliciesFromConfig(map[string]ConfigTeam{"t": {RatePerMin: 1}})["t"]
	if p.Sources != ConfigSources() || p.Sources.Rate.Source != audit.GovSourceConfig {
		t.Fatalf("sources = %+v", p.Sources)
	}
}

// A local money refusal names its scope's limit and still matches its
// sentinel, so the 402 mapping is unchanged.
func TestReserveLocalBudgetNamesExhaustedScope(t *testing.T) {
	team := TeamPolicy{BudgetMicrosPerMonth: 100, Sources: TeamSources{Budget: policySource("money", "month")}}
	g := NewGovernor(map[string]TeamPolicy{"t": team}, limiter.NewMemory(), budget.NewMemory(), nil)
	s := Subject{Team: "t", KeyID: "k"}
	g.bud.Debit(budget.Key(budget.ScopeTeam, "t", g.monthWindow()), 100, g.monthWindow())
	_, err := g.ReserveLocalBudget(s, KeyPolicy{}, 10, true)
	if !errors.Is(err, ErrLocalBudgetExhausted) {
		t.Fatalf("err = %v", err)
	}
	requireGovernance(t, DenialGovernance(err), audit.GovScopeTeam, audit.GovLimitBudgetMonth, audit.GovSourcePolicy, "month")

	g = NewGovernor(nil, limiter.NewMemory(), budget.NewMemory(), nil)
	g.bud.Debit(budget.Key(budget.ScopeKey, "k", g.dayWindow()), 1, g.dayWindow())
	_, err = g.ReserveLocalBudget(s, KeyPolicy{BudgetMicrosPerDay: 1}, 10, true)
	requireGovernance(t, DenialGovernance(err), audit.GovScopeKey, audit.GovLimitBudgetDay, audit.GovSourceKey, "")
}

func TestDenialGovernanceReadsSharedAndPlainErrors(t *testing.T) {
	ref := &audit.GovernanceRef{Scope: audit.GovScopeKey, Limit: audit.GovLimitRPM, Source: audit.GovSourceKey}
	if got := DenialGovernance(&SharedDenial{Status: 429, Reason: "shared rate or token quota exhausted", Governance: ref}); got != ref {
		t.Fatalf("shared = %+v", got)
	}
	if got := DenialGovernance(ErrAuthorityUnavailable); got != nil {
		t.Fatalf("an unavailable authority names no limit: %+v", got)
	}
	wrapped := &BudgetDenial{Err: ErrAuthorityExhausted, Governance: ref}
	if !errors.Is(wrapped, ErrAuthorityExhausted) || DenialGovernance(wrapped) != ref {
		t.Fatal("budget denial lost its sentinel or its limit")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run; mkdir -p $S
cd $W && go test -buildvcs=false ./internal/governance/ > $S/t3-red.log 2>&1; echo "exit=$?"; grep -E 'FAIL|undefined' $S/t3-red.log
```
Expected: `exit=1`, `[build failed]`, `undefined: LimitSource`, `team.Sources undefined (type TeamPolicy has no field or method Sources)`, `dec.Governance undefined`.

- [ ] **Step 3: Implement**

Create `internal/governance/denial.go`:

```go
package governance

import (
	"errors"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/budget"
)

// LimitSource says where one enforced limit was set (ADR-057), so the denial
// it produces can name it in the audit record. Source is an audit.GovSource*
// value; Policy names the GovernancePolicy rule when Source is
// audit.GovSourcePolicy. The zero value names no source.
type LimitSource struct {
	Source string
	Policy *audit.PolicyRuleRef
}

// TeamSources names the source of each TeamPolicy limit.
type TeamSources struct {
	Rate, TokenRate, Quota, Budget, BudgetDay LimitSource
}

// UserSources names the source of each UserPolicy window.
type UserSources struct {
	Budget, BudgetDay LimitSource
}

// ConfigSources marks every team limit as set by the config file.
func ConfigSources() TeamSources {
	c := LimitSource{Source: audit.GovSourceConfig}
	return TeamSources{Rate: c, TokenRate: c, Quota: c, Budget: c, BudgetDay: c}
}

// keySource is every per-key limit's source: the virtual key's own options.
var keySource = LimitSource{Source: audit.GovSourceKey}

// limitRef names one limit by value; callers that keep it per request (the
// local money scopes) avoid an allocation until a denial needs it.
func limitRef(scope, limit string, src LimitSource) audit.GovernanceRef {
	return audit.GovernanceRef{Scope: scope, Limit: limit, Source: src.Source, Policy: src.Policy}
}

func denial(scope, limit string, src LimitSource) *audit.GovernanceRef {
	ref := limitRef(scope, limit, src)
	if src.Policy != nil {
		p := *src.Policy
		ref.Policy = &p
	}
	return &ref
}

// budgetDenial names a budget refusal. A store at capacity refuses whatever
// the limit says (mustDenyBudget), so the store is named instead.
func budgetDenial(scope, limit string, src LimitSource, dec budget.Decision) *audit.GovernanceRef {
	if dec == budget.BlockCapacity {
		src = LimitSource{Source: audit.GovSourceBudgetStore}
	}
	return denial(scope, limit, src)
}

// PricingDenial names the pricing.on_missing block setting (ADR-030), the
// source of every pricing_missing refusal.
func PricingDenial() *audit.GovernanceRef {
	return denial(audit.GovScopeGateway, audit.GovLimitPricing, LimitSource{Source: audit.GovSourceConfig})
}

// BudgetDenial is a pre-dispatch money refusal that names its binding limit
// (ADR-057). errors.Is still matches its sentinel (ErrLocalBudgetExhausted or
// ErrAuthorityExhausted), so status mapping is unchanged.
type BudgetDenial struct {
	Err        error
	Governance *audit.GovernanceRef
}

func (e *BudgetDenial) Error() string { return e.Err.Error() }
func (e *BudgetDenial) Unwrap() error { return e.Err }

// DenialGovernance returns the limit an admission error names, or nil when it
// names none (an unavailable store or authority, an invalid request).
func DenialGovernance(err error) *audit.GovernanceRef {
	var b *BudgetDenial
	if errors.As(err, &b) {
		return b.Governance
	}
	var s *SharedDenial
	if errors.As(err, &s) {
		return s.Governance
	}
	return nil
}
```

In `internal/governance/governance.go`, replace lines 57–59:

```go
	// response, when set (see policy.TeamLimits.AdminContact).
	AdminContact string
}
```

with:

```go
	// response, when set (see policy.TeamLimits.AdminContact).
	AdminContact string
	// Sources names where each limit above was set, so a denial can name it
	// in the audit record (ADR-057). The zero value names none.
	Sources TeamSources
}
```

In `internal/governance/governance.go`, replace lines 104–106:

```go
	// TeamPolicy.AdminContact.
	AdminContact string
}
```

with:

```go
	// TeamPolicy.AdminContact.
	AdminContact string
	// Sources names the rule behind each window (ADR-057).
	Sources UserSources
}
```

In `internal/governance/governance.go`, replace lines 305–311:

```go
// !Allowed: 429 (rate/quota), 402 (budget), 0 (allowed).
type GovDecision struct {
	Admission *Admission // owned by the caller only when Allowed
	Allowed   bool
	Status    int
	Reason    string
	Code      audit.DenyReason
```

with:

```go
// !Allowed: 429 (rate/quota), 402 (budget), 0 (allowed). Governance names the
// limit behind a denial for the audit record (ADR-057); nil when allowed or
// when the refusal is not a configured limit (an invalid estimate).
type GovDecision struct {
	Admission  *Admission // owned by the caller only when Allowed
	Allowed    bool
	Status     int
	Reason     string
	Code       audit.DenyReason
	Governance *audit.GovernanceRef
```

In `internal/governance/governance.go`, replace line 362:

```go
			return GovDecision{Status: 402, Reason: reason, Code: audit.DenyTeamBudgetExceeded}
```

with:

```go
			return GovDecision{Status: 402, Reason: reason, Code: audit.DenyTeamBudgetExceeded,
				Governance: denial(audit.GovScopeTeam, audit.GovLimitBudget, LimitSource{Source: audit.GovSourceLease})}
```

In `internal/governance/governance.go`, replace lines 368–373:

```go
			return GovDecision{Status: 429, Reason: "rate limit exceeded", Code: audit.DenyTeamRateLimited}
		}
		// token rate limit (TPM): charge the request estimate against a per-minute
		// token bucket whose burst is one minute's worth of tokens.
		if p.TokensPerMinute > 0 && !reserve("tpm:"+s.Team, p.TokensPerMinute) {
			return GovDecision{Status: 429, Reason: "token rate limit exceeded", Code: audit.DenyTeamTokenRateLimited}
```

with:

```go
			return GovDecision{Status: 429, Reason: "rate limit exceeded", Code: audit.DenyTeamRateLimited,
				Governance: denial(audit.GovScopeTeam, audit.GovLimitRPM, p.Sources.Rate)}
		}
		// token rate limit (TPM): charge the request estimate against a per-minute
		// token bucket whose burst is one minute's worth of tokens.
		if p.TokensPerMinute > 0 && !reserve("tpm:"+s.Team, p.TokensPerMinute) {
			return GovDecision{Status: 429, Reason: "token rate limit exceeded", Code: audit.DenyTeamTokenRateLimited,
				Governance: denial(audit.GovScopeTeam, audit.GovLimitTPM, p.Sources.TokenRate)}
```

In `internal/governance/governance.go`, replace line 379:

```go
					return GovDecision{Status: 429, Reason: "token quota exceeded", Code: audit.DenyTeamQuotaExceeded}
```

with:

```go
					return GovDecision{Status: 429, Reason: "token quota exceeded", Code: audit.DenyTeamQuotaExceeded,
						Governance: denial(audit.GovScopeTeam, audit.GovLimitTokensPerDay, p.Sources.Quota)}
```

In `internal/governance/governance.go`, replace line 403:

```go
				budgetDeny = GovDecision{Status: 402, Reason: budgetExceededMessage("budget", resetsAt, g.budgetLocOrUTC(), p.AdminContact), Code: audit.DenyTeamBudgetExceeded}
```

with:

```go
				budgetDeny = GovDecision{Status: 402, Reason: budgetExceededMessage("budget", resetsAt, g.budgetLocOrUTC(), p.AdminContact), Code: audit.DenyTeamBudgetExceeded,
					Governance: budgetDenial(audit.GovScopeTeam, audit.GovLimitBudgetMonth, p.Sources.Budget, dec)}
```

In `internal/governance/governance.go`, replace line 414:

```go
					budgetDeny = GovDecision{Status: 402, Reason: budgetExceededMessage("daily budget", resetsAt, g.budgetLocOrUTC(), p.AdminContact), Code: audit.DenyTeamBudgetExceeded}
```

with:

```go
					budgetDeny = GovDecision{Status: 402, Reason: budgetExceededMessage("daily budget", resetsAt, g.budgetLocOrUTC(), p.AdminContact), Code: audit.DenyTeamBudgetExceeded,
						Governance: budgetDenial(audit.GovScopeTeam, audit.GovLimitBudgetDay, p.Sources.BudgetDay, dec)}
```

In `internal/governance/governance.go`, replace lines 425–428:

```go
		return GovDecision{Status: 429, Reason: "key rate limit exceeded", Code: audit.DenyKeyRateLimited}
	}
	if kp.TokensPerMinute > 0 && !reserve("tpm:key:"+s.KeyID, kp.TokensPerMinute) {
		return GovDecision{Status: 429, Reason: "key token rate limit exceeded", Code: audit.DenyKeyTokenRateLimited}
```

with:

```go
		return GovDecision{Status: 429, Reason: "key rate limit exceeded", Code: audit.DenyKeyRateLimited,
			Governance: denial(audit.GovScopeKey, audit.GovLimitRPM, keySource)}
	}
	if kp.TokensPerMinute > 0 && !reserve("tpm:key:"+s.KeyID, kp.TokensPerMinute) {
		return GovDecision{Status: 429, Reason: "key token rate limit exceeded", Code: audit.DenyKeyTokenRateLimited,
			Governance: denial(audit.GovScopeKey, audit.GovLimitTPM, keySource)}
```

In `internal/governance/governance.go`, replace line 439:

```go
			keyBudgetDeny = GovDecision{Status: 402, Reason: budgetExceededMessage("key budget", resetsAt, g.budgetLocOrUTC(), ""), Code: audit.DenyKeyBudgetExceeded}
```

with:

```go
			keyBudgetDeny = GovDecision{Status: 402, Reason: budgetExceededMessage("key budget", resetsAt, g.budgetLocOrUTC(), ""), Code: audit.DenyKeyBudgetExceeded,
				Governance: budgetDenial(audit.GovScopeKey, audit.GovLimitBudgetMonth, keySource, dec)}
```

In `internal/governance/governance.go`, replace line 449:

```go
				keyBudgetDeny = GovDecision{Status: 402, Reason: budgetExceededMessage("key daily budget", resetsAt, g.budgetLocOrUTC(), ""), Code: audit.DenyKeyBudgetExceeded}
```

with:

```go
				keyBudgetDeny = GovDecision{Status: 402, Reason: budgetExceededMessage("key daily budget", resetsAt, g.budgetLocOrUTC(), ""), Code: audit.DenyKeyBudgetExceeded,
					Governance: budgetDenial(audit.GovScopeKey, audit.GovLimitBudgetDay, keySource, dec)}
```

In `internal/governance/governance.go`, replace line 477:

```go
					userDeny = GovDecision{Status: 402, Reason: budgetExceededMessage("user budget", resetsAt, g.budgetLocOrUTC(), up.AdminContact), Code: audit.DenyUserBudgetExceeded}
```

with:

```go
					userDeny = GovDecision{Status: 402, Reason: budgetExceededMessage("user budget", resetsAt, g.budgetLocOrUTC(), up.AdminContact), Code: audit.DenyUserBudgetExceeded,
						Governance: budgetDenial(audit.GovScopeUser, audit.GovLimitBudgetMonth, up.Sources.Budget, dec)}
```

In `internal/governance/governance.go`, replace line 487:

```go
						userDeny = GovDecision{Status: 402, Reason: budgetExceededMessage("user daily budget", resetsAt, g.budgetLocOrUTC(), up.AdminContact), Code: audit.DenyUserBudgetExceeded}
```

with:

```go
						userDeny = GovDecision{Status: 402, Reason: budgetExceededMessage("user daily budget", resetsAt, g.budgetLocOrUTC(), up.AdminContact), Code: audit.DenyUserBudgetExceeded,
							Governance: budgetDenial(audit.GovScopeUser, audit.GovLimitBudgetDay, up.Sources.BudgetDay, dec)}
```

In `internal/governance/governance.go`, replace lines 840–842:

```go
				Status: 402,
				Reason: "no pricing rate for " + t.Provider + "/" + t.Upstream,
				Code:   audit.DenyPricingMissing,
```

with:

```go
				Status:     402,
				Reason:     "no pricing rate for " + t.Provider + "/" + t.Upstream,
				Code:       audit.DenyPricingMissing,
				Governance: PricingDenial(),
```

In `internal/governance/fromconfig.go`, replace line 36:

```go
	BudgetDayExceeded    string // block|warn
```

with:

```go
	BudgetDayExceeded    string // block|warn
	// Sources names where each limit was set (ADR-057); copied unchanged.
	Sources TeamSources
```

In `internal/governance/fromconfig.go`, replace line 60:

```go
		BudgetDayExceeded:    l.BudgetDayExceeded,
```

with:

```go
		BudgetDayExceeded:    l.BudgetDayExceeded,
		Sources:              l.Sources,
```

In `internal/governance/fromconfig.go`, replace line 87:

```go
			BudgetDayExceeded:    c.BudgetExceeded,
```

with:

```go
			BudgetDayExceeded:    c.BudgetExceeded,
			Sources:              ConfigSources(),
```

In `internal/governance/local_money.go`, replace lines 8–9:

```go

	"github.com/inferplane/inferplane/internal/budget"
```

with:

```go

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/budget"
```

In `internal/governance/local_money.go`, replace line 18:

```go
	hard  bool
```

with:

```go
	hard  bool
	gov   audit.GovernanceRef // the limit this scope names when it refuses
}

// exhausted is this scope's refusal (ADR-057). A store at capacity refuses
// whatever the limit says, so the store is named instead.
func (s localMoneyScope) exhausted(atCapacity bool) error {
	ref := s.gov
	if atCapacity {
		ref = audit.GovernanceRef{Scope: ref.Scope, Limit: ref.Limit, Source: audit.GovSourceBudgetStore}
	} else if ref.Policy != nil {
		p := *ref.Policy
		ref.Policy = &p
	}
	return &BudgetDenial{Err: ErrLocalBudgetExhausted, Governance: &ref}
```

In `internal/governance/local_money.go`, replace lines 46–61:

```go
	add := func(scope, id string, limit int64, mode string, win budget.Window) {
		if limit > 0 {
			scopes = append(scopes, localMoneyScope{budget.Key(scope, id, win), win, limit, mode != "warn"})
		}
	}
	month, day := g.monthWindow(), g.dayWindow()
	if p, ok := g.policyOf(s.Team); ok {
		add(budget.ScopeTeam, s.Team, p.BudgetMicrosPerMonth, p.BudgetExceeded, month)
		add(budget.ScopeTeam, s.Team, p.BudgetMicrosPerDay, p.BudgetDayExceeded, day)
	}
	add(budget.ScopeKey, s.KeyID, kp.BudgetMicrosPerMonth, "", month)
	add(budget.ScopeKey, s.KeyID, kp.BudgetMicrosPerDay, "", day)
	if s.User != "" && g.userLookup != nil {
		if p, ok := g.userLookup(s.Team, s.User); ok {
			add(budget.ScopeUser, userBudgetID(s), p.BudgetMicrosPerMonth, p.BudgetExceeded, month)
			add(budget.ScopeUser, userBudgetID(s), p.BudgetMicrosPerDay, p.BudgetExceeded, day)
```

with:

```go
	add := func(scope, id string, limit int64, mode string, win budget.Window, gov audit.GovernanceRef) {
		if limit > 0 {
			scopes = append(scopes, localMoneyScope{budget.Key(scope, id, win), win, limit, mode != "warn", gov})
		}
	}
	month, day := g.monthWindow(), g.dayWindow()
	if p, ok := g.policyOf(s.Team); ok {
		add(budget.ScopeTeam, s.Team, p.BudgetMicrosPerMonth, p.BudgetExceeded, month, limitRef(audit.GovScopeTeam, audit.GovLimitBudgetMonth, p.Sources.Budget))
		add(budget.ScopeTeam, s.Team, p.BudgetMicrosPerDay, p.BudgetDayExceeded, day, limitRef(audit.GovScopeTeam, audit.GovLimitBudgetDay, p.Sources.BudgetDay))
	}
	add(budget.ScopeKey, s.KeyID, kp.BudgetMicrosPerMonth, "", month, limitRef(audit.GovScopeKey, audit.GovLimitBudgetMonth, keySource))
	add(budget.ScopeKey, s.KeyID, kp.BudgetMicrosPerDay, "", day, limitRef(audit.GovScopeKey, audit.GovLimitBudgetDay, keySource))
	if s.User != "" && g.userLookup != nil {
		if p, ok := g.userLookup(s.Team, s.User); ok {
			add(budget.ScopeUser, userBudgetID(s), p.BudgetMicrosPerMonth, p.BudgetExceeded, month, limitRef(audit.GovScopeUser, audit.GovLimitBudgetMonth, p.Sources.Budget))
			add(budget.ScopeUser, userBudgetID(s), p.BudgetMicrosPerDay, p.BudgetExceeded, day, limitRef(audit.GovScopeUser, audit.GovLimitBudgetDay, p.Sources.BudgetDay))
```

In `internal/governance/local_money.go`, replace lines 100–101:

```go
		if len(g.localMoney)+len(scopes) > 100_000 {
			return nil, ErrLocalBudgetExhausted
```

with:

```go
		if len(g.localMoney)+len(scopes) > 100_000 {
			// The gateway-wide liability ledger is full: no limit refused.
			return nil, &BudgetDenial{Err: ErrLocalBudgetExhausted,
				Governance: denial(audit.GovScopeGateway, audit.GovLimitBudget, LimitSource{Source: audit.GovSourceBudgetStore})}
```

In `internal/governance/local_money.go`, replace lines 116–118:

```go
		if state.frozen || spent >= scope.limit || state.held >= scope.limit-spent ||
			g.bud.Check(scope.key, state.held, scope.limit, scope.win) != budget.Allow {
			return nil, ErrLocalBudgetExhausted
```

with:

```go
		if state.frozen || spent >= scope.limit || state.held >= scope.limit-spent {
			return nil, scope.exhausted(false)
		}
		if dec := g.bud.Check(scope.key, state.held, scope.limit, scope.win); dec != budget.Allow {
			return nil, scope.exhausted(dec == budget.BlockCapacity)
```

In `internal/governance/shared.go`, replace line 6:

```go
	"time"
```

with:

```go
	"time"

	"github.com/inferplane/inferplane/internal/audit"
```

In `internal/governance/shared.go`, replace lines 63–67:

```go
// details, capabilities, hashes or key/user identifiers.
type SharedDenial struct {
	Status     int
	Reason     string
	RetryAfter int
```

with:

```go
// details, capabilities, hashes or key/user identifiers. Governance names the
// binding limit for the audit record only (ADR-057); it is never a response.
type SharedDenial struct {
	Status     int
	Reason     string
	RetryAfter int
	Governance *audit.GovernanceRef
```

- [ ] **Step 4: Run the packages and watch them pass**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run
cd $W && gofmt -l internal/governance; go build -buildvcs=false ./... > $S/t3-build.log 2>&1; echo "build=$?"; go test -buildvcs=false -race ./internal/governance/... ./internal/server/requestpolicy/... > $S/t3.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---)' $S/t3.log
```
Expected: no `gofmt` output, `build=0`, `exit=0`, `ok` for `internal/governance` and `internal/server/requestpolicy`. `TestLocalMoneyRejectIsAtomicAcrossTeamAndKey` still passes (the refusal still matches `ErrLocalBudgetExhausted`).

- [ ] **Step 5: Module note**

In `internal/CLAUDE.md`, replace line 91:

```markdown
- `governance/` — `Governor` (`PreCheck`/`Settle`). `SetLeaseGate` (ADR-034) checks a budget lease first in `PreCheck`, denying 402 before any counter is charged. `UsageOf` (ADR-021) is the read-only companion behind `GET /v1/usage` — no debit, no state change. `SetTeamLookup` (ADR-016) prefers a DB team record over the static config map. `SetBudgetNotify`/`SetKeyBudgetNotify` (ADR-017) fire post-debit alert hooks. Negative estimates deny with `invalid_token_estimate` before any limit is consumed. Daily token quota records validated observed quantities only; uncertain usage retains the TPM estimate as a minimum without fabricating an estimated daily-quota debit. `PreCheck` returns an `Admission` whose `Close` must be deferred; `Settle` consumes it once and trues up its captured TPM reservations after validating token classes and their sum (ADR-039), and debits the daily quota by total tokens including all three cache tiers — not `Input+Output` alone. `PreCheck`/`Settle` also enforce a calendar-DAY budget beside the monthly one, against a separate counter with its own store key, block wins on tie via whichever window resets soonest; `SetBudgetTimezone` is the `budget_timezone` seam (nil = UTC) and now anchors BOTH calendar windows — the month boundary honours the operator timezone too (ADR-042), no longer the day's midnight only. `PreCheck`/`Settle`/`UsageOf` take a `Subject{Team, KeyID, User}` value replacing the two leading string args (ADR-042 Phase 3) — each ingress builds it with its own `subjectOf`, same deliberate per-package duplication as `keyPolicyOf`, keeping this package a leaf. `SetUserLookup`/`UserPolicy` add per-USER budget: budget only (no RPM/TPM), ONE `on_exceeded` for both windows (block wins on tie, collapsed in the gateway closure). The user counter's id is `team + "/" + user` (`userBudgetID` — "/" is safe because `adminapi.validateTeamName` forbids it in team names, so the split point is unambiguous); a user-ONLY subject therefore counts separately per team, accepted in ADR-042. `GET /v1/usage` gains `user_budget`/`user_budget_day` (appended, omitempty).
```

with:

```markdown
- `governance/` — `Governor` (`PreCheck`/`Settle`). `SetLeaseGate` (ADR-034) checks a budget lease first in `PreCheck`, denying 402 before any counter is charged. `UsageOf` (ADR-021) is the read-only companion behind `GET /v1/usage` — no debit, no state change. `SetTeamLookup` (ADR-016) prefers a DB team record over the static config map. `SetBudgetNotify`/`SetKeyBudgetNotify` (ADR-017) fire post-debit alert hooks. Negative estimates deny with `invalid_token_estimate` before any limit is consumed. Daily token quota records validated observed quantities only; uncertain usage retains the TPM estimate as a minimum without fabricating an estimated daily-quota debit. `PreCheck` returns an `Admission` whose `Close` must be deferred; `Settle` consumes it once and trues up its captured TPM reservations after validating token classes and their sum (ADR-039), and debits the daily quota by total tokens including all three cache tiers — not `Input+Output` alone. `PreCheck`/`Settle` also enforce a calendar-DAY budget beside the monthly one, against a separate counter with its own store key, block wins on tie via whichever window resets soonest; `SetBudgetTimezone` is the `budget_timezone` seam (nil = UTC) and now anchors BOTH calendar windows — the month boundary honours the operator timezone too (ADR-042), no longer the day's midnight only. `PreCheck`/`Settle`/`UsageOf` take a `Subject{Team, KeyID, User}` value replacing the two leading string args (ADR-042 Phase 3) — each ingress builds it with its own `subjectOf`, same deliberate per-package duplication as `keyPolicyOf`, keeping this package a leaf. `SetUserLookup`/`UserPolicy` add per-USER budget: budget only (no RPM/TPM), ONE `on_exceeded` for both windows (block wins on tie, collapsed in the gateway closure). The user counter's id is `team + "/" + user` (`userBudgetID` — "/" is safe because `adminapi.validateTeamName` forbids it in team names, so the split point is unambiguous); a user-ONLY subject therefore counts separately per team, accepted in ADR-042. `GET /v1/usage` gains `user_budget`/`user_budget_day` (appended, omitempty). `denial.go` (ADR-057): `TeamPolicy.Sources`/`UserPolicy.Sources` (`LimitSource`) say where each limit was set, and every `PreCheck`/`PricingGuard` denial carries `GovDecision.Governance` (scope, limit, source, policy rule); a budget store at capacity is named `budget_store` instead of the limit, the lease gate `lease`. Pre-dispatch money refusals carry it through `BudgetDenial` (local liability and ADR-045; `errors.Is` still matches the sentinel) or `SharedDenial.Governance` (ADR-046), read by `DenialGovernance`.
```

- [ ] **Step 6: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree
git -C $W add internal/governance/denial.go internal/governance/denial_test.go internal/governance/governance.go internal/governance/fromconfig.go internal/governance/local_money.go internal/governance/shared.go internal/CLAUDE.md
git -C $W commit -s -m "feat(governance): name the refusing limit on every denial (ADR-057)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Every ingress writes the refusing limit onto its denial record

**Files:**
- Modify: `internal/audit/deny_reason.go:32` (new code), `:46` (`KnownDenyReason`); `internal/audit/deny_reason_test.go:13-24`, `:31-32`
- Modify: `internal/server/anthropicapi/messages.go:323` (pricing denial), `:332` (`PreCheck` denial), `:392` (reservation refusal)
- Modify: `internal/server/openaiapi/chat.go:295`, `:304`, `:347`
- Modify: `internal/server/bedrockapi/invoke.go:332`, `:341`, `:379`
- Modify: `internal/server/responsesapi/handler.go:183`, `:191-192`, `:210`, `:246`
- Modify: `internal/server/requestpolicy/pricing.go:31`, `:41`; `internal/server/requestpolicy/pricing_test.go` (whole file: one test appended)
- Modify: `internal/CLAUDE.md:88` (`server/` bullet)
- Test: `internal/server/governance_denial_test.go`

**Interfaces:**
- Consumes: `audit.SetGovernance` (Task 1); `GovDecision.Governance`, `governance.DenialGovernance`, `governance.PricingDenial`, `governance.BudgetDenial`, `SharedDenial.Governance`, `TeamSources`, `LimitSource` (Task 3); test helpers `policyMux`, `policyDo`, `policyAudit`, `authorityIngresses`, `authorityBody` (`internal/server/policy_routing_test.go:51-72`, `internal/server/budget_authority_test.go:97-110`), `sharedProbe` (`internal/server/shared_governance_test.go:21-41`), `authorityProbe` (`internal/server/budget_authority_test.go:17-44`).
- Produces: `audit.DenyBudgetAuthorityUnavailable DenyReason = "budget_authority_unavailable"`; test helper `denialRecord(t *testing.T, buf *bytes.Buffer) audit.Record` in package `server`.

- [ ] **Step 1: Write the failing tests**

Create `internal/server/governance_denial_test.go`:

```go
package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/server/routingtest"
	"github.com/inferplane/inferplane/pkg/schema"
)

// denialRecord returns the single gateway denial record in an audit buffer.
func denialRecord(t *testing.T, buf *bytes.Buffer) audit.Record {
	t.Helper()
	var found []audit.Record
	sc := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	for sc.Scan() {
		var r audit.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.Event == "request_started" && r.Outcome != nil {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("denial records = %d: %s", len(found), buf)
	}
	return found[0]
}

// Every ingress writes the refusing limit onto its denial record (ADR-057):
// a PreCheck limit, an ADR-046 shared refusal and an ADR-045 authority
// refusal. Reservation refusals also carry the budget_authority_unavailable
// code on every ingress, as Responses already did.
func TestEveryIngressRecordsGovernanceDenialLimit(t *testing.T) {
	rule := &audit.PolicyRuleRef{Name: "team-rate", Generation: 3, Rule: "rpm"}
	reserved := &audit.GovernanceRef{Scope: audit.GovScopeKey, Limit: audit.GovLimitTPM, Source: audit.GovSourceKey}
	for _, ingress := range authorityIngresses() {
		t.Run(ingress.name()+"/precheck", func(t *testing.T) {
			f := routingtest.New(t, nil)
			in, out := int64(2), int64(1)
			f.Public.Usage = &schema.Usage{InputTokens: &in, OutputTokens: &out}
			team := governance.TeamPolicy{RatePerMin: 1, RateBurst: 1,
				Sources: governance.TeamSources{Rate: governance.LimitSource{Source: audit.GovSourcePolicy, Policy: rule}}}
			gov := governance.NewGovernor(map[string]governance.TeamPolicy{"team": team}, limiter.NewMemory(), budget.NewMemory(), nil)
			if rec := policyDo(policyMux(f, nil, gov, nil, nil), ingress.path, authorityBody(ingress), true); rec.Code != 200 {
				t.Fatalf("first request status %d: %s", rec.Code, rec.Body)
			}
			aud, buf := policyAudit(t)
			rec := policyDo(policyMux(f, aud, gov, nil, nil), ingress.path, authorityBody(ingress), true)
			aud.Close()
			r := denialRecord(t, buf)
			g := r.Governance
			if rec.Code != 429 || g == nil || g.Scope != audit.GovScopeTeam || g.Limit != audit.GovLimitRPM || g.Source != audit.GovSourcePolicy || g.Policy == nil || *g.Policy != *rule {
				t.Fatalf("status %d governance %+v", rec.Code, g)
			}
		})
		for _, refusal := range []string{"shared", "authority"} {
			t.Run(ingress.name()+"/"+refusal, func(t *testing.T) {
				f := routingtest.New(t, nil)
				gov := governance.NewGovernor(nil, limiter.NewMemory(), budget.NewMemory(), nil)
				want := 429
				if refusal == "shared" {
					f.Router.SetRoutingPolicySnapshot(func(string, string) ([]*policy.Policy, string, error) { return nil, "generation", nil })
					gov.SetSharedAuthority(&sharedProbe{deny: &governance.SharedDenial{Status: 429, Reason: "shared rate or token quota exhausted", RetryAfter: 60, Governance: reserved}})
				} else {
					want = 402
					gov.SetBudgetAuthority(&authorityProbe{failure: &governance.BudgetDenial{Err: governance.ErrAuthorityExhausted, Governance: reserved}})
				}
				aud, buf := policyAudit(t)
				rec := policyDo(policyMux(f, aud, gov, nil, nil), ingress.path, authorityBody(ingress), true)
				aud.Close()
				r := denialRecord(t, buf)
				if rec.Code != want || r.Outcome.Error == nil || *r.Outcome.Error != "budget_authority_unavailable" {
					t.Fatalf("status %d outcome %+v", rec.Code, r.Outcome)
				}
				if r.Governance == nil || *r.Governance != *reserved {
					t.Fatalf("governance = %+v", r.Governance)
				}
			})
		}
	}
}
```

Replace the whole of `internal/server/requestpolicy/pricing_test.go` (26 lines) with:

```go
package requestpolicy

import (
	"testing"

	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/pricing"
)

func TestTieredPricingRefusesUnpricedServiceBeforeBilling(t *testing.T) {
	table := pricing.New(pricing.OnMissingBlock, map[pricing.Key]pricing.Rate{{Provider: "p", Model: "m"}: {InputPerMTok: 1, ShortContextLimit: 100, LongContext: &pricing.Rate{InputPerMTok: 2}}})
	targets := []governance.PricedTarget{{Provider: "p", Upstream: "m"}}
	for _, tc := range []struct {
		body    string
		allowed bool
	}{
		{`{"input":"hello"}`, true}, {`{"service_tier":null}`, true}, {`{"service_tier":"default"}`, true},
		{`{"service_tier":"ultrafast"}`, false}, {`{"service_tier":"priority"}`, false}, {`{"service_tier":"flex"}`, false}, {`{"service_tier":true}`, false},
		{`{"service_tier":"priority","service_tier":"default"}`, false}, {`{"service_tier":"default","service_tier":"priority"}`, false},
		{`{"meta":{"service_tier":"x"},"service_tier":"default"}`, true},
	} {
		if got := PricingGuard(table, targets, []byte(tc.body)); got.Allowed != tc.allowed {
			t.Fatalf("body=%s decision=%+v", tc.body, got)
		}
	}
}

// Every pricing refusal names pricing.on_missing as its source (ADR-057).
func TestPricingRefusalsNamePricingSetting(t *testing.T) {
	table := pricing.New(pricing.OnMissingBlock, map[pricing.Key]pricing.Rate{{Provider: "p", Model: "m"}: {InputPerMTok: 1, ShortContextLimit: 100, LongContext: &pricing.Rate{InputPerMTok: 2}}})
	for _, tc := range []struct {
		targets []governance.PricedTarget
		body    string
	}{
		{[]governance.PricedTarget{{Provider: "p", Upstream: "unpriced"}}, `{"input":"hello"}`},
		{[]governance.PricedTarget{{Provider: "p", Upstream: "m"}}, `{"service_tier":"priority"}`},
		{[]governance.PricedTarget{{Provider: "p", Upstream: "m"}}, `{"service_tier":"priority","service_tier":"default"}`},
	} {
		dec := PricingGuard(table, tc.targets, []byte(tc.body))
		if dec.Allowed || dec.Governance == nil || *dec.Governance != *governance.PricingDenial() {
			t.Fatalf("body=%s decision=%+v governance=%+v", tc.body, dec, dec.Governance)
		}
	}
}
```

In `internal/audit/deny_reason_test.go`, replace lines 13–24:

```go
		DenyModelNotAllowed:      "model_not_allowed",
		DenyTeamRateLimited:      "team_rate_limited",
		DenyTeamTokenRateLimited: "team_token_rate_limited",
		DenyTeamQuotaExceeded:    "team_quota_exceeded",
		DenyKeyRateLimited:       "key_rate_limited",
		DenyKeyTokenRateLimited:  "key_token_rate_limited",
		DenyTeamBudgetExceeded:   "team_budget_exceeded",
		DenyKeyBudgetExceeded:    "key_budget_exceeded",
		DenyUserBudgetExceeded:   "user_budget_exceeded",
		DenyRegionBlocked:        "region_blocked",
		DenyPricingMissing:       "pricing_missing",
		DenyInvalidTokenEstimate: "invalid_token_estimate",
```

with:

```go
		DenyModelNotAllowed:            "model_not_allowed",
		DenyTeamRateLimited:            "team_rate_limited",
		DenyTeamTokenRateLimited:       "team_token_rate_limited",
		DenyTeamQuotaExceeded:          "team_quota_exceeded",
		DenyKeyRateLimited:             "key_rate_limited",
		DenyKeyTokenRateLimited:        "key_token_rate_limited",
		DenyTeamBudgetExceeded:         "team_budget_exceeded",
		DenyKeyBudgetExceeded:          "key_budget_exceeded",
		DenyUserBudgetExceeded:         "user_budget_exceeded",
		DenyRegionBlocked:              "region_blocked",
		DenyPricingMissing:             "pricing_missing",
		DenyInvalidTokenEstimate:       "invalid_token_estimate",
		DenyBudgetAuthorityUnavailable: "budget_authority_unavailable",
```

In `internal/audit/deny_reason_test.go`, replace lines 31–32:

```go
	if len(cases) != 12 {
		t.Fatalf("expected 12 taxonomy constants, test table has %d", len(cases))
```

with:

```go
	if len(cases) != 13 {
		t.Fatalf("expected 13 taxonomy constants, test table has %d", len(cases))
```

- [ ] **Step 2: Run them and watch them fail**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run; mkdir -p $S
cd $W && go test -buildvcs=false ./internal/audit/ ./internal/server/ ./internal/server/requestpolicy/ -run 'TestDenyReasonValues|TestEveryIngressRecordsGovernanceDenialLimit|TestPricingRefusalsNamePricingSetting' > $S/t4-red.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL)|undefined|governance_denial_test.go:(62|82|85)|pricing_test.go' $S/t4-red.log
```
Expected: `exit=1`; `internal/audit [build failed]` with `undefined: DenyBudgetAuthorityUnavailable`; `TestEveryIngressRecordsGovernanceDenialLimit` fails on every ingress with `status 429 governance <nil>` and `outcome &{Status:429 … Error:<nil>}`; `TestPricingRefusalsNamePricingSetting` fails with `governance=<nil>`.

- [ ] **Step 3: Implement**

In `internal/audit/deny_reason.go`, replace line 32:

```go
	DenyInvalidTokenEstimate DenyReason = "invalid_token_estimate"
```

with:

```go
	DenyInvalidTokenEstimate DenyReason = "invalid_token_estimate"
	// DenyBudgetAuthorityUnavailable: the per-attempt money reservation
	// (local liability, ADR-045 authority or ADR-046 shared admission)
	// refused before dispatch. The record's governance field names the limit
	// when the refusing store reports one (ADR-057).
	DenyBudgetAuthorityUnavailable DenyReason = "budget_authority_unavailable"
```

In `internal/audit/deny_reason.go`, replace line 46:

```go
		DenyUserBudgetExceeded, DenyRegionBlocked, DenyPricingMissing, DenyInvalidTokenEstimate:
```

with:

```go
		DenyUserBudgetExceeded, DenyRegionBlocked, DenyPricingMissing, DenyInvalidTokenEstimate, DenyBudgetAuthorityUnavailable:
```

In `internal/server/anthropicapi/messages.go`, replace line 323:

```go
	if dec := requestpolicy.PricingGuard(table, pricedTargets(chain), raw); !dec.Allowed {
```

with:

```go
	if dec := requestpolicy.PricingGuard(table, pricedTargets(chain), raw); !dec.Allowed {
		audit.SetGovernance(req.Context(), dec.Governance)
```

In `internal/server/anthropicapi/messages.go`, replace line 332:

```go
		if !dec.Allowed {
```

with:

```go
		if !dec.Allowed {
			audit.SetGovernance(req.Context(), dec.Governance)
```

In `internal/server/anthropicapi/messages.go`, replace line 392:

```go
			h.audit(req.Context(), p, model, ct.Upstream, &audit.OutcomeRef{Status: status}, false, traceID)
```

with:

```go
			audit.SetGovernance(req.Context(), governance.DenialGovernance(reserveErr))
			h.audit(req.Context(), p, model, ct.Upstream, &audit.OutcomeRef{Status: status, Error: audit.DenyBudgetAuthorityUnavailable.Ptr()}, false, traceID)
```

In `internal/server/openaiapi/chat.go`, replace line 295:

```go
	if dec := requestpolicy.PricingGuard(table, pricedTargets(chain), raw); !dec.Allowed {
```

with:

```go
	if dec := requestpolicy.PricingGuard(table, pricedTargets(chain), raw); !dec.Allowed {
		audit.SetGovernance(req.Context(), dec.Governance)
```

In `internal/server/openaiapi/chat.go`, replace line 304:

```go
		if !dec.Allowed {
```

with:

```go
		if !dec.Allowed {
			audit.SetGovernance(req.Context(), dec.Governance)
```

In `internal/server/openaiapi/chat.go`, replace line 347:

```go
			h.audit(req.Context(), p, model, ct.Upstream, &audit.OutcomeRef{Status: status}, traceID)
```

with:

```go
			audit.SetGovernance(req.Context(), governance.DenialGovernance(reserveErr))
			h.audit(req.Context(), p, model, ct.Upstream, &audit.OutcomeRef{Status: status, Error: audit.DenyBudgetAuthorityUnavailable.Ptr()}, traceID)
```

In `internal/server/bedrockapi/invoke.go`, replace line 332:

```go
	if dec := requestpolicy.PricingGuard(table, pricedTargets(chain), raw); !dec.Allowed {
```

with:

```go
	if dec := requestpolicy.PricingGuard(table, pricedTargets(chain), raw); !dec.Allowed {
		audit.SetGovernance(req.Context(), dec.Governance)
```

In `internal/server/bedrockapi/invoke.go`, replace line 341:

```go
		if !dec.Allowed {
```

with:

```go
		if !dec.Allowed {
			audit.SetGovernance(req.Context(), dec.Governance)
```

In `internal/server/bedrockapi/invoke.go`, replace line 379:

```go
			h.audit(req.Context(), p, model, ct.Upstream, &audit.OutcomeRef{Status: status}, false, traceID)
```

with:

```go
			audit.SetGovernance(req.Context(), governance.DenialGovernance(reserveErr))
			h.audit(req.Context(), p, model, ct.Upstream, &audit.OutcomeRef{Status: status, Error: audit.DenyBudgetAuthorityUnavailable.Ptr()}, false, traceID)
```

In `internal/server/responsesapi/handler.go`, replace line 183:

```go
	if dec := requestpolicy.PricingGuard(table, priced, raw); !dec.Allowed {
```

with:

```go
	if dec := requestpolicy.PricingGuard(table, priced, raw); !dec.Allowed {
		audit.SetGovernance(req.Context(), dec.Governance)
```

In `internal/server/responsesapi/handler.go`, replace lines 191–192:

```go
		if !dec.Allowed {
			h.denied(req, p, model, dec.Status, string(dec.Code), start)
```

with:

```go
		if !dec.Allowed {
			audit.SetGovernance(req.Context(), dec.Governance)
			h.denied(req, p, model, dec.Status, string(dec.Code), start)
```

In `internal/server/responsesapi/handler.go`, replace line 210:

```go
			if !dec.Allowed {
```

with:

```go
			if !dec.Allowed {
				audit.SetGovernance(req.Context(), dec.Governance)
```

In `internal/server/responsesapi/handler.go`, replace line 246:

```go
			h.denied(req, p, model, status, "budget_authority_unavailable", start)
```

with:

```go
			audit.SetGovernance(req.Context(), governance.DenialGovernance(reserveErr))
			h.denied(req, p, model, status, string(audit.DenyBudgetAuthorityUnavailable), start)
```

In `internal/server/requestpolicy/pricing.go`, replace line 31:

```go
		return governance.GovDecision{Status: 400, Reason: "invalid request for configured pricing", Code: audit.DenyPricingMissing}
```

with:

```go
		return governance.GovDecision{Status: 400, Reason: "invalid request for configured pricing", Code: audit.DenyPricingMissing, Governance: governance.PricingDenial()}
```

In `internal/server/requestpolicy/pricing.go`, replace line 41:

```go
	return governance.GovDecision{Status: 402, Reason: "no configured price for the requested service tier", Code: audit.DenyPricingMissing}
```

with:

```go
	return governance.GovDecision{Status: 402, Reason: "no configured price for the requested service tier", Code: audit.DenyPricingMissing, Governance: governance.PricingDenial()}
```

- [ ] **Step 4: Run the server packages and watch them pass**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run
cd $W && gofmt -l internal/audit internal/server; go test -buildvcs=false -race ./internal/audit/... ./internal/server/... > $S/t4.log 2>&1; echo "exit=$?"; grep -E '^(FAIL|---)' $S/t4.log; grep -c '^ok' $S/t4.log
```
Expected: no `gofmt` output, `exit=0`, no `FAIL` or `---` lines, and `16` (every package with tests under those paths is `ok`).

- [ ] **Step 5: Module note**

In `internal/CLAUDE.md`, replace line 88:

```markdown
- `server/` — HTTP data plane + admin plane. Ingress handlers: `anthropicapi/`, `openaiapi/`, `responsesapi/`, `bedrockapi/` (ADR-024/052); `usageapi/` self-service `GET /v1/usage` (ADR-021); `authapi/` opt-in CLI login (ADR-028); `adminapi/`, `configapi/`, `auditapi/`, `analyticsapi/`, `auditconsoleapi/` (read-only full-admin `/admin/audit/*`, ADR-057, mounted only when the analytics store implements `AuditEventQuerier`); `adminui/` embedded console (ADR-001, SSO ADR-026, i18n ADR-027). A mid-stream upstream failure surfaces as a wire-appropriate error frame (SSE `error` event / eventstream exception), never a silent truncation, and the audit record is marked `partial: true`. **Every observability sink sees the same settled usage.** Invalid quantities are omitted from settled audit usage and span token attributes; completion and cost evidence remain (`pricing_missing: true` on rejected settlement), and valid partial TTL counts are preserved. A partial stream's usage is settled, so it must also be `observeTokens`'d — metering only the clean path left `gen_ai_client_token_usage_total` below the budget spend, the audit ledger, and the usage windows on every interrupted stream. `recordSpanSettled` (not `recordSpanResponse`) is the span call at the three COMMITTED sites; `recordSpanResponse` stays for the terminal pre-commit rejects, where there is no usage or cost to report.
```

with:

```markdown
- `server/` — HTTP data plane + admin plane. Ingress handlers: `anthropicapi/`, `openaiapi/`, `responsesapi/`, `bedrockapi/` (ADR-024/052); `usageapi/` self-service `GET /v1/usage` (ADR-021); `authapi/` opt-in CLI login (ADR-028); `adminapi/`, `configapi/`, `auditapi/`, `analyticsapi/`, `auditconsoleapi/` (read-only full-admin `/admin/audit/*`, ADR-057, mounted only when the analytics store implements `AuditEventQuerier`); `adminui/` embedded console (ADR-001, SSO ADR-026, i18n ADR-027). A mid-stream upstream failure surfaces as a wire-appropriate error frame (SSE `error` event / eventstream exception), never a silent truncation, and the audit record is marked `partial: true`. **Every observability sink sees the same settled usage.** Invalid quantities are omitted from settled audit usage and span token attributes; completion and cost evidence remain (`pricing_missing: true` on rejected settlement), and valid partial TTL counts are preserved. A partial stream's usage is settled, so it must also be `observeTokens`'d — metering only the clean path left `gen_ai_client_token_usage_total` below the budget spend, the audit ledger, and the usage windows on every interrupted stream. `recordSpanSettled` (not `recordSpanResponse`) is the span call at the three COMMITTED sites; `recordSpanResponse` stays for the terminal pre-commit rejects, where there is no usage or cost to report. Every pricing, `PreCheck` and reservation refusal calls `audit.SetGovernance` before writing its denial record (ADR-057), and a reservation refusal carries `outcome.error` `budget_authority_unavailable` on all four ingresses.
```

- [ ] **Step 6: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree
git -C $W add internal/audit/deny_reason.go internal/audit/deny_reason_test.go internal/server/governance_denial_test.go internal/server/anthropicapi/messages.go internal/server/openaiapi/chat.go internal/server/bedrockapi/invoke.go internal/server/responsesapi/handler.go internal/server/requestpolicy/pricing.go internal/server/requestpolicy/pricing_test.go internal/CLAUDE.md
git -C $W commit -s -m "feat(server): write the refusing limit on every ingress denial (ADR-057)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: The gateway names the layer whose limit binds

**Files:**
- Create: `cmd/mayu/limit_sources.go`
- Modify (whole files): `cmd/mayu/rate_overlay.go`, `cmd/mayu/budget_overlay.go`, `cmd/mayu/rate_overlay_test.go`; `cmd/mayu/budget_overlay_test.go:16-30`
- Modify: `cmd/mayu/gateway.go:471` (`lookupBase` team record), `:493-495`, `:502`, `:510` (policy overlay), `:528-531`, `:543-544` (lease clamp), `:568` (`PolicyFromLimits`), `:622` (`UserPolicy`)
- Test: `cmd/mayu/governance_denial_e2e_test.go`

**Interfaces:**
- Consumes: `policy.RuleRef`, `TeamLimits.*Rule`, `UserLimits.*Rule` (Task 2); `governance.LimitSource`, `TeamSources`, `UserSources`, `Limits.Sources` (Task 3); `audit.Gov*` (Task 1); the denial record written by Task 4; E2E helpers `bootGateway`, `newAnthropicUpstream`, `withAnthropicProvider`, `createKey`, `postMessages` (`cmd/mayu/e2e_test.go:35-218`), `teamsAPIConfig`, `putTeam` (`cmd/mayu/teams_test.go:19-41`), `getUsage` (`cmd/mayu/e2e_test.go:990`), `budgetLimitMicros` (`cmd/mayu/leasewindow_test.go:35`), `controlplane.NewServer`.
- Produces: `restrictiveRate(base, overlay int64) (limit int64, fromOverlay bool)`; `restrictiveBudget(base int64, baseMode string, overlay int64, overlayMode string) (limit int64, mode string, fromOverlay bool)`; `policySource(r policy.RuleRef) governance.LimitSource`; `teamRecordSources() governance.TeamSources`.

- [ ] **Step 1: Write the failing tests**

Replace the whole of `cmd/mayu/rate_overlay_test.go` (13 lines) with:

```go
package main

import "testing"

func TestRestrictiveRate(t *testing.T) {
	for _, tc := range []struct {
		base, overlay, want int64
		fromOverlay         bool
	}{
		{0, 0, 0, false}, {10, 0, 10, false}, {0, 10, 10, true}, {10, 1000, 10, false}, {1000, 10, 10, true}, {10, 10, 10, false},
	} {
		got, fromOverlay := restrictiveRate(tc.base, tc.overlay)
		if got != tc.want || fromOverlay != tc.fromOverlay {
			t.Fatalf("base=%d overlay=%d got=%d,%v want=%d,%v", tc.base, tc.overlay, got, fromOverlay, tc.want, tc.fromOverlay)
		}
	}
}
```

In `cmd/mayu/budget_overlay_test.go`, replace lines 16–30:

```go
		baseMode, mode, wantMode string
	}{
		{name: "omitted-base-block", base: 3_000, overlay: 1_000, want: 3_000, mode: "warn", wantMode: "block"},
		{name: "explicit-base-block", base: 3_000, overlay: 1_000, want: 3_000, baseMode: "block", mode: "warn", wantMode: "block"},
		{name: "hard-lease-wider-than-soft", base: 1_000, overlay: 3_000, want: 3_000, baseMode: "warn", mode: "block", wantMode: "block"},
		{name: "hard-lease-equal-soft", base: 1_000, overlay: 1_000, want: 1_000, baseMode: "warn", mode: "block", wantMode: "block"},
		{name: "hard-caps-narrow", base: 3_000, overlay: 2_000, want: 2_000, baseMode: "block", mode: "block", wantMode: "block"},
		{name: "soft-meters-narrow", base: 3_000, overlay: 1_000, want: 1_000, baseMode: "warn", mode: "warn", wantMode: "warn"},
		{name: "absent-base-default-does-not-harden-soft", overlay: 1_000, want: 1_000, mode: "warn", wantMode: "warn"},
		{name: "absent-overlay-preserves-base", base: 3_000, want: 3_000, baseMode: "warn", wantMode: "warn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limit, mode := restrictiveBudget(tc.base, tc.baseMode, tc.overlay, tc.mode)
			if limit != tc.want || mode != tc.wantMode {
				t.Fatalf("got limit=%d mode=%q, want limit=%d mode=%q", limit, mode, tc.want, tc.wantMode)
```

with:

```go
		baseMode, mode, wantMode string
		fromOverlay              bool
	}{
		{name: "omitted-base-block", base: 3_000, overlay: 1_000, want: 3_000, mode: "warn", wantMode: "block"},
		{name: "explicit-base-block", base: 3_000, overlay: 1_000, want: 3_000, baseMode: "block", mode: "warn", wantMode: "block"},
		{name: "hard-lease-wider-than-soft", base: 1_000, overlay: 3_000, want: 3_000, baseMode: "warn", mode: "block", wantMode: "block", fromOverlay: true},
		{name: "hard-lease-equal-soft", base: 1_000, overlay: 1_000, want: 1_000, baseMode: "warn", mode: "block", wantMode: "block", fromOverlay: true},
		{name: "hard-caps-narrow", base: 3_000, overlay: 2_000, want: 2_000, baseMode: "block", mode: "block", wantMode: "block", fromOverlay: true},
		{name: "hard-caps-tie-keeps-base", base: 2_000, overlay: 2_000, want: 2_000, baseMode: "block", mode: "block", wantMode: "block"},
		{name: "soft-meters-narrow", base: 3_000, overlay: 1_000, want: 1_000, baseMode: "warn", mode: "warn", wantMode: "warn", fromOverlay: true},
		{name: "absent-base-default-does-not-harden-soft", overlay: 1_000, want: 1_000, mode: "warn", wantMode: "warn", fromOverlay: true},
		{name: "absent-overlay-preserves-base", base: 3_000, want: 3_000, baseMode: "warn", wantMode: "warn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limit, mode, fromOverlay := restrictiveBudget(tc.base, tc.baseMode, tc.overlay, tc.mode)
			if limit != tc.want || mode != tc.wantMode || fromOverlay != tc.fromOverlay {
				t.Fatalf("got limit=%d mode=%q overlay=%v, want limit=%d mode=%q overlay=%v", limit, mode, fromOverlay, tc.want, tc.wantMode, tc.fromOverlay)
```

Create `cmd/mayu/governance_denial_e2e_test.go`:

```go
package main

// ADR-057 end to end: a governance denial's audit record names the limit that
// refused it, for each layer that can set a team or key rate limit.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/controlplane"
)

// createKeyJSON issues a key from a raw /admin/keys body and returns its
// plaintext.
func createKeyJSON(t *testing.T, adminURL, body string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, adminURL+"/admin/keys", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+e2eAdminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Plaintext string `json:"plaintext"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || !strings.HasPrefix(out.Plaintext, "ik_") {
		t.Fatalf("create key: status %d err %v", resp.StatusCode, err)
	}
	return out.Plaintext
}

// writeRatePolicy writes a GovernancePolicy whose rule "rpm" limits gov-team.
func writeRatePolicy(t *testing.T, dir string, rpm int) string {
	t.Helper()
	doc := fmt.Sprintf(`apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: {name: team-rate, generation: 2}
spec:
  subject: {team: gov-team}
  rules:
  - name: rpm
    failurePolicy: FailOpen
    rate: {rpm: %d}
`, rpm)
	path := filepath.Join(dir, "rate.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// denialRecordIn returns the first gateway denial with the given status in an
// audit file, after checking the file's chain.
func denialRecordIn(t *testing.T, auditPath string, status int) audit.Record {
	t.Helper()
	raw, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := audit.Verify(bytes.NewReader(raw)); err != nil || !res.OK {
		t.Fatalf("audit chain broken: %+v %v", res, err)
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		var r audit.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.Event == "request_started" && r.Outcome != nil && r.Outcome.Status == status {
			return r
		}
	}
	t.Fatalf("no %d denial record", status)
	return audit.Record{}
}

// rateDenialRecord boots a gateway, lets setup add limits and issue a key,
// sends two Messages requests and returns the 429 denial record.
func rateDenialRecord(t *testing.T, mutate func(cfg map[string]any, dir string), setup func(adminURL string) string) audit.Record {
	t.Helper()
	up := newAnthropicUpstream(t)
	var auditPath string
	dataURL, adminURL, shutdown := bootGateway(t, func(cfg map[string]any, dir string) {
		withAnthropicProvider(up.srv.URL)(cfg, dir)
		auditPath = filepath.Join(dir, "audit.jsonl")
		if mutate != nil {
			mutate(cfg, dir)
		}
	})
	key := setup(adminURL)
	for i, want := range []int{http.StatusOK, http.StatusTooManyRequests} {
		resp := postMessages(t, dataURL, key, "claude-test")
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("request %d status = %d, want %d", i+1, resp.StatusCode, want)
		}
	}
	shutdown()
	return denialRecordIn(t, auditPath, http.StatusTooManyRequests)
}

func TestE2EGovernanceDenialNamesItsLimit(t *testing.T) {
	teamKey := func(adminURL string) string {
		_, key := createKey(t, adminURL, "gov-team", []string{"*"})
		return key
	}
	configTeam := func(rpm int) map[string]any {
		return map[string]any{"gov-team": map[string]any{"allowed_models": []any{"*"}, "rate_limit": map[string]any{"requests_per_minute": rpm}}}
	}
	for _, tc := range []struct {
		name   string
		mutate func(cfg map[string]any, dir string)
		setup  func(adminURL string) string
		want   audit.GovernanceRef
		rule   *audit.PolicyRuleRef
	}{
		{
			name: "policy rule narrower than config",
			mutate: func(cfg map[string]any, dir string) {
				cfg["teams"] = configTeam(100)
				cfg["policies"] = []any{writeRatePolicy(t, dir, 1)}
			},
			setup: teamKey,
			want:  audit.GovernanceRef{Scope: audit.GovScopeTeam, Limit: audit.GovLimitRPM, Source: audit.GovSourcePolicy},
			rule:  &audit.PolicyRuleRef{Name: "team-rate", Generation: 2, Rule: "rpm"},
		},
		{
			name: "config narrower than policy",
			mutate: func(cfg map[string]any, dir string) {
				cfg["teams"] = configTeam(1)
				cfg["policies"] = []any{writeRatePolicy(t, dir, 100)}
			},
			setup: teamKey,
			want:  audit.GovernanceRef{Scope: audit.GovScopeTeam, Limit: audit.GovLimitRPM, Source: audit.GovSourceConfig},
		},
		{
			name: "team record",
			setup: func(adminURL string) string {
				resp := putTeam(t, adminURL, "gov-team", `{"rpm":1}`)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("PUT team: %d", resp.StatusCode)
				}
				return teamKey(adminURL)
			},
			want: audit.GovernanceRef{Scope: audit.GovScopeTeam, Limit: audit.GovLimitRPM, Source: audit.GovSourceTeamRecord},
		},
		{
			name: "key option",
			setup: func(adminURL string) string {
				return createKeyJSON(t, adminURL, `{"team":"gov-team","allowed_models":["*"],"rpm":1}`)
			},
			want: audit.GovernanceRef{Scope: audit.GovScopeKey, Limit: audit.GovLimitRPM, Source: audit.GovSourceKey},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := rateDenialRecord(t, tc.mutate, tc.setup)
			g := r.Governance
			if g == nil || g.Scope != tc.want.Scope || g.Limit != tc.want.Limit || g.Source != tc.want.Source {
				t.Fatalf("governance = %+v, want %+v", g, tc.want)
			}
			if (g.Policy == nil) != (tc.rule == nil) || (g.Policy != nil && *g.Policy != *tc.rule) {
				t.Fatalf("policy = %+v, want %+v", g.Policy, tc.rule)
			}
		})
	}
}

// A hard cap distributed by the control plane binds through its lease
// (ADR-034): once local spend passes the granted allowance, the 402 names the
// lease, not the policy rule the lease was granted for.
func TestE2ELeaseBoundDenialNamesTheLease(t *testing.T) {
	t.Setenv("GD_CP_TOKEN", "gd-tok")
	polDir := t.TempDir()
	doc := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: lease-cap }
spec:
  subject: { team: gov-team }
  rules:
  - name: monthly
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 1000000, hardCap: true, lease: { renewInterval: "1s" } }
`
	if err := os.WriteFile(filepath.Join(polDir, "p.yaml"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cp, err := controlplane.NewServer("gd-tok", polDir)
	if err != nil {
		t.Fatal(err)
	}
	cpMux := http.NewServeMux()
	cp.Mount(cpMux)
	cpSrv := httptest.NewServer(cpMux)
	defer cpSrv.Close()

	up := newAnthropicUpstream(t)
	var auditPath string
	dataURL, adminURL, shutdown := bootGateway(t, func(cfg map[string]any, dir string) {
		teamsAPIConfig(up.srv.URL)(cfg, dir)
		auditPath = filepath.Join(dir, "audit.jsonl")
		cfg["control_plane"] = map[string]any{
			"url":       cpSrv.URL,
			"token_ref": map[string]any{"env": "GD_CP_TOKEN"},
			"dataplane": "gd-dp",
		}
	})
	_, key := createKey(t, adminURL, "gov-team", []string{"*"})
	// Wait for the first heartbeat to install the policy and its lease.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if limit, ok := budgetLimitMicros(getUsage(t, dataURL, key), "team_budget"); ok && limit < 1_000_000_000 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lease never clamped the team budget")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// teamsAPIConfig prices a token at $1, so the first request spends far
	// past the 0.1% allowance and the second is refused.
	for i, want := range []int{http.StatusOK, http.StatusPaymentRequired} {
		resp := postMessages(t, dataURL, key, "claude-test")
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("request %d status = %d, want %d", i+1, resp.StatusCode, want)
		}
	}
	shutdown()
	r := denialRecordIn(t, auditPath, http.StatusPaymentRequired)
	g := r.Governance
	if g == nil || g.Scope != audit.GovScopeTeam || g.Source != audit.GovSourceLease || g.Policy != nil ||
		(g.Limit != audit.GovLimitBudgetMonth && g.Limit != audit.GovLimitBudget) {
		t.Fatalf("governance = %+v", g)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run; mkdir -p $S
cd $W && go test -buildvcs=false ./cmd/mayu/ -run 'TestRestrictive|TestE2EGovernanceDenialNamesItsLimit|TestE2ELeaseBoundDenialNamesTheLease' > $S/t5-red.log 2>&1; echo "exit=$?"; grep -E 'FAIL|mismatch' $S/t5-red.log
```
Expected: `exit=1`, `cmd/mayu [build failed]`, `assignment mismatch: 2 variables but restrictiveRate returns 1 value` and `assignment mismatch: 3 variables but restrictiveBudget returns 2 values`.

- [ ] **Step 3: Implement**

Replace the whole of `cmd/mayu/rate_overlay.go` (10 lines) with:

```go
package main

// restrictiveRate composes independently configured upper bounds. Zero means
// this layer adds no rate limit; it never removes a nonzero limit underneath.
// fromOverlay reports that the overlay's value binds, so the denial it
// produces names the overlay's rule (ADR-057).
func restrictiveRate(base, overlay int64) (limit int64, fromOverlay bool) {
	if base == 0 || (overlay > 0 && overlay < base) {
		return overlay, overlay > 0
	}
	return base, false
}
```

Replace the whole of `cmd/mayu/budget_overlay.go` (28 lines) with:

```go
package main

// restrictiveBudget composes independent admission caps for one window.
// Only explicit warn is soft; omitted on_exceeded has the governor's default
// blocking meaning. Soft meters cannot erase or narrow a hard cap.
// fromOverlay reports that the overlay's limit binds, so the denial it
// produces names the overlay as its source (ADR-057).
func restrictiveBudget(base int64, baseMode string, overlay int64, overlayMode string) (limit int64, mode string, fromOverlay bool) {
	if overlay == 0 {
		return base, baseMode, false
	}
	if base == 0 {
		return overlay, overlayMode, true
	}
	baseHard, overlayHard := baseMode != "warn", overlayMode != "warn"
	if baseHard != overlayHard {
		if overlayHard {
			return overlay, "block", true
		}
		return base, "block", false
	}
	mode = "warn"
	if baseHard {
		mode = "block"
	}
	if overlay < base {
		return overlay, mode, true
	}
	return base, mode, false
}
```

Create `cmd/mayu/limit_sources.go`:

```go
package main

import (
	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/policy"
)

// policySource names a GovernancePolicy rule as the source of one limit, so
// the denial it produces names that rule (ADR-057). A zero RuleRef (no rule
// sets the limit) names nothing.
func policySource(r policy.RuleRef) governance.LimitSource {
	if r.Rule == "" {
		return governance.LimitSource{}
	}
	return governance.LimitSource{Source: audit.GovSourcePolicy,
		Policy: &audit.PolicyRuleRef{Name: r.Policy, Generation: r.Generation, Rule: r.Rule}}
}

// teamRecordSources marks every limit of a keystore team record (the
// /admin/teams console edit) as its own source.
func teamRecordSources() governance.TeamSources {
	r := governance.LimitSource{Source: audit.GovSourceTeamRecord}
	return governance.TeamSources{Rate: r, TokenRate: r, Quota: r, Budget: r, BudgetDay: r}
}
```

In `cmd/mayu/gateway.go`, replace line 471:

```go
				BudgetDayExceeded:  rec.BudgetOnExceeded,
```

with:

```go
				BudgetDayExceeded:  rec.BudgetOnExceeded,
				Sources:            teamRecordSources(),
```

In `cmd/mayu/gateway.go`, replace lines 493–495:

```go
		// not replace an already stricter config/keystore rate.
		rpm := restrictiveRate(base.RatePerMin, tl.RPM)
		tpm := restrictiveRate(base.TokensPerMinute, tl.TPM)
```

with:

```go
		// not replace an already stricter config/keystore rate. Each
		// dimension keeps the source of whichever layer binds (ADR-057).
		sources := base.Sources
		rpm, rpmFromPolicy := restrictiveRate(base.RatePerMin, tl.RPM)
		if rpmFromPolicy {
			sources.Rate = policySource(tl.RPMRule)
		}
		tpm, tpmFromPolicy := restrictiveRate(base.TokensPerMinute, tl.TPM)
		if tpmFromPolicy {
			sources.TokenRate = policySource(tl.TPMRule)
		}
```

In `cmd/mayu/gateway.go`, replace line 502:

```go
			budgetMicros, budgetExceeded = restrictiveBudget(budgetMicros, budgetExceeded, tl.BudgetMicrosPerMonth, mode)
```

with:

```go
			var fromPolicy bool
			budgetMicros, budgetExceeded, fromPolicy = restrictiveBudget(budgetMicros, budgetExceeded, tl.BudgetMicrosPerMonth, mode)
			if fromPolicy {
				sources.Budget = policySource(tl.BudgetRule)
			}
```

In `cmd/mayu/gateway.go`, replace line 510:

```go
			budgetDayMicros, budgetDayExceeded = restrictiveBudget(budgetDayMicros, budgetDayExceeded, tl.BudgetMicrosPerDay, mode)
```

with:

```go
			var fromPolicy bool
			budgetDayMicros, budgetDayExceeded, fromPolicy = restrictiveBudget(budgetDayMicros, budgetDayExceeded, tl.BudgetMicrosPerDay, mode)
			if fromPolicy {
				sources.BudgetDay = policySource(tl.BudgetDayRule)
			}
```

In `cmd/mayu/gateway.go`, replace lines 528–531:

```go
			clamp := func(limit int64, exceeded string, period v1alpha1.BudgetPeriod) (int64, string) {
				l, ok := leases.Get(team, period)
				if !ok {
					return limit, exceeded
```

with:

```go
			clamp := func(limit int64, exceeded string, period v1alpha1.BudgetPeriod) (int64, string, bool) {
				l, ok := leases.Get(team, period)
				if !ok {
					return limit, exceeded, false
```

In `cmd/mayu/gateway.go`, replace lines 543–544:

```go
			budgetMicros, budgetExceeded = clamp(budgetMicros, budgetExceeded, v1alpha1.PeriodCalendarMonth)
			budgetDayMicros, budgetDayExceeded = clamp(budgetDayMicros, budgetDayExceeded, v1alpha1.PeriodCalendarDay)
```

with:

```go
			var fromLease bool
			if budgetMicros, budgetExceeded, fromLease = clamp(budgetMicros, budgetExceeded, v1alpha1.PeriodCalendarMonth); fromLease {
				sources.Budget = governance.LimitSource{Source: audit.GovSourceLease}
			}
			if budgetDayMicros, budgetDayExceeded, fromLease = clamp(budgetDayMicros, budgetDayExceeded, v1alpha1.PeriodCalendarDay); fromLease {
				sources.BudgetDay = governance.LimitSource{Source: audit.GovSourceLease}
			}
```

In `cmd/mayu/gateway.go`, replace line 568:

```go
			BudgetDayExceeded:    budgetDayExceeded,
```

with:

```go
			BudgetDayExceeded:    budgetDayExceeded,
			Sources:              sources,
```

In `cmd/mayu/gateway.go`, replace line 622:

```go
			AdminContact:         contact,
```

with:

```go
			AdminContact:         contact,
			Sources:              governance.UserSources{Budget: policySource(ul.BudgetRule), BudgetDay: policySource(ul.BudgetDayRule)},
```

- [ ] **Step 4: Run the package and watch it pass**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run
cd $W && gofmt -l cmd/mayu; go test -buildvcs=false -race ./cmd/mayu/... > $S/t5.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL)|--- FAIL' $S/t5.log; go test -buildvcs=false ./cmd/mayu/ -run 'TestE2EGovernanceDenialNamesItsLimit|TestE2ELeaseBoundDenialNamesTheLease' -v > $S/t5-v.log 2>&1; grep -E -- '--- (PASS|FAIL)' $S/t5-v.log
```
Expected: no `gofmt` output, `exit=0`, `ok  	github.com/inferplane/inferplane/cmd/mayu`, and `--- PASS` for `TestE2EGovernanceDenialNamesItsLimit` with its four subtests (`policy_rule_narrower_than_config`, `config_narrower_than_policy`, `team_record`, `key_option`) and for `TestE2ELeaseBoundDenialNamesTheLease`. The existing lease and budget E2E tests (`TestE2ELeaseClampPerWindow`, `TestE2EBudgetOverlayPreservesIndependentHardLimit`) still pass: the composed values did not change.

- [ ] **Step 5: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree
git -C $W add cmd/mayu/limit_sources.go cmd/mayu/governance_denial_e2e_test.go cmd/mayu/gateway.go cmd/mayu/rate_overlay.go cmd/mayu/budget_overlay.go cmd/mayu/rate_overlay_test.go cmd/mayu/budget_overlay_test.go
git -C $W commit -s -m "feat(mayu): name the binding layer of each team limit (ADR-057)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Shared and durable authority refusals name their limit

**Files:**
- Modify: `internal/authority/pgstore/shared_constraints.go:11` (imports), `:25-27` (`sharedDefinition`, `denial`, `limitName`), `:127-133` (generations), `:160` (fill generations), `:284`, `:290` (`sharedCheckCapacity` denials)
- Modify: `internal/authority/local/admission.go:5-12` (imports and `budgetDenial`), `:82` (exhaustion)
- Modify: `internal/CLAUDE.md:161` (ADR-045 section), `:173` (ADR-046 section)
- Test: `internal/authority/pgstore/shared_denial_test.go` (Postgres), `internal/authority/local/denial_test.go`

**Interfaces:**
- Consumes: `governance.BudgetDenial`, `SharedDenial.Governance`, `governance.DenialGovernance` (Task 3); `audit.Gov*`, `audit.PolicyRuleRef` (Task 1); test helpers `newSharedFixture`, `sharedDoc`, `sharedQuota`, `sharedReserve`, `putDocument` (`internal/authority/pgstore/shared_test.go`, `pgstore_test.go:128`), `openTest`, `definition`, `response`, `applyTest`, `requestTest`, `testSubject` (`internal/authority/local/store_test.go:18-71`).
- Produces: `sharedDefinition.generation int64`, `(sharedDefinition).denial() *audit.GovernanceRef`, `(sharedDefinition).limitName() string`; `local.budgetDenial(b policy.AuthorityBudget) *audit.GovernanceRef`.

- [ ] **Step 1: Start Postgres (loopback only)**

```bash
S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run; mkdir -p $S
docker run -d --rm --name inferplane-audit-pg-fix -e POSTGRES_PASSWORD=audit-test -p 127.0.0.1:55432:5432 postgres:17-alpine > $S/pg.log 2>&1; echo "run=$?"
docker exec inferplane-audit-pg-fix sh -c 'for i in $(seq 1 60); do pg_isready -h 127.0.0.1 -U postgres -q && exit 0; sleep 1; done; exit 1'; echo "ready=$?"
```
Expected: `run=0`, `ready=0`. If `run=125` because the name is taken, run `docker stop inferplane-audit-pg-fix` and repeat. If Docker is unavailable, stop and report: the Postgres test cannot run.

- [ ] **Step 2: Write the failing tests**

Create `internal/authority/local/denial_test.go`:

```go
package local

import (
	"context"
	"errors"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/policy"
)

// An exhausted hard budget names its policy rule (ADR-057) and keeps the
// sentinel the 402 mapping reads.
func TestExhaustedAuthorityNamesItsBudget(t *testing.T) {
	s, _ := openTest(t)
	r := response(definition("hard", true))
	applyTest(t, s, r)
	if _, err := s.Reserve(context.Background(), testSubject, 0); !errors.Is(err, governance.ErrAuthorityUnavailable) {
		t.Fatalf("first reserve: %v", err)
	}
	want := requestTest(t, s).Requests[0]
	r.Denied = []policy.AuthorityDenial{{RequestID: want.RequestID, Key: want.Key, Reason: "exhausted"}}
	applyTest(t, s, r)
	_, err := s.Reserve(context.Background(), testSubject, 0)
	if !errors.Is(err, governance.ErrAuthorityExhausted) {
		t.Fatalf("err = %v", err)
	}
	g := governance.DenialGovernance(err)
	if g == nil || g.Scope != audit.GovScopeTeam || g.Limit != audit.GovLimitBudgetDay || g.Source != audit.GovSourcePolicy ||
		g.Policy == nil || *g.Policy != (audit.PolicyRuleRef{Name: "policy", Rule: "hard"}) {
		t.Fatalf("governance = %+v", g)
	}
}
```

Create `internal/authority/pgstore/shared_denial_test.go`:

```go
package pgstore

import (
	"context"
	"errors"
	"testing"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/keystore"
)

// A shared refusal names the definition that refused it (ADR-057): the team
// record, the key option, or the policy rule with its generation.
func TestSharedDenialNamesItsLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		team keystore.TeamRecord
		opts keystore.KeyOptions
		doc  bool
		want audit.GovernanceRef
		rule *audit.PolicyRuleRef
	}{
		{name: "team record rpm", team: keystore.TeamRecord{Name: "alpha", RPM: 1},
			want: audit.GovernanceRef{Scope: audit.GovScopeTeam, Limit: audit.GovLimitRPM, Source: audit.GovSourceTeamRecord}},
		{name: "key day budget", team: keystore.TeamRecord{Name: "alpha"}, opts: keystore.KeyOptions{BudgetUSDMicrosPerDay: 1000},
			want: audit.GovernanceRef{Scope: audit.GovScopeKey, Limit: audit.GovLimitBudgetDay, Source: audit.GovSourceKey}},
		{name: "policy month quota", team: keystore.TeamRecord{Name: "alpha"}, doc: true,
			want: audit.GovernanceRef{Scope: audit.GovScopeTeam, Limit: audit.GovLimitTokensPerMonth, Source: audit.GovSourcePolicy},
			rule: &audit.PolicyRuleRef{Name: "tokens", Generation: 4, Rule: "cap"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSharedFixture(t)
			if tc.doc {
				doc := sharedDoc("tokens", v1alpha1.Subject{Team: "alpha"}, sharedQuota("cap", 10, v1alpha1.PeriodCalendarMonth))
				doc.Metadata.Generation = 4
				putDocument(t, f.db, doc)
			}
			f.team(t, tc.team)
			_, req := f.key(t, "alpha", tc.opts)
			sharedReserve(t, f.a, req)
			_, err := f.b.ReserveShared(context.Background(), req)
			var denial *governance.SharedDenial
			if !errors.As(err, &denial) {
				t.Fatalf("second admission: %v", err)
			}
			g := denial.Governance
			if g == nil || g.Scope != tc.want.Scope || g.Limit != tc.want.Limit || g.Source != tc.want.Source {
				t.Fatalf("governance = %+v, want %+v", g, tc.want)
			}
			if (g.Policy == nil) != (tc.rule == nil) || (g.Policy != nil && *g.Policy != *tc.rule) {
				t.Fatalf("policy = %+v, want %+v", g.Policy, tc.rule)
			}
		})
	}
}
```

- [ ] **Step 3: Run them and watch them fail**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run
cd $W && INFERPLANE_TEST_PG_DSN='postgres://postgres:audit-test@127.0.0.1:55432/postgres?sslmode=disable' go test -buildvcs=false ./internal/authority/local/ ./internal/authority/pgstore/ -run 'TestExhaustedAuthorityNamesItsBudget|TestSharedDenialNamesItsLimit' > $S/t6-red.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL)|denial_test.go' $S/t6-red.log
```
Expected: `exit=1`; `denial_test.go:32: governance = <nil>` (local) and three `shared_denial_test.go:50: governance = <nil>, want {…}` lines (team record rpm, key day budget, policy month quota).

- [ ] **Step 4: Implement**

In `internal/authority/pgstore/shared_constraints.go`, replace line 11:

```go
	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
```

with:

```go
	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/audit"
```

In `internal/authority/pgstore/shared_constraints.go`, replace lines 25–27:

```go
	limit                                       int64
	hard                                        bool
	start, end                                  time.Time
```

with:

```go
	limit, generation                           int64
	hard                                        bool
	start, end                                  time.Time
}

// denial names this definition as the limit that refused an admission
// (ADR-057): a policy rule, else the key option or the team record the
// authenticated snapshot carries. Names only, never the limit or counters.
func (d sharedDefinition) denial() *audit.GovernanceRef {
	ref := &audit.GovernanceRef{Scope: audit.GovScopeTeam, Limit: d.limitName(), Source: audit.GovSourceTeamRecord}
	switch d.scope {
	case "key":
		ref.Scope, ref.Source = audit.GovScopeKey, audit.GovSourceKey
	case "user", "team-user":
		ref.Scope = audit.GovScopeUser
	}
	if d.policy != "" {
		ref.Source = audit.GovSourcePolicy
		ref.Policy = &audit.PolicyRuleRef{Name: d.policy, Generation: d.generation, Rule: d.rule}
	}
	return ref
}

func (d sharedDefinition) limitName() string {
	day := d.window == string(v1alpha1.PeriodCalendarDay)
	switch d.kind {
	case "rpm":
		return audit.GovLimitRPM
	case "tpm":
		return audit.GovLimitTPM
	case "tokens":
		if day {
			return audit.GovLimitTokensPerDay
		}
		return audit.GovLimitTokensPerMonth
	}
	if day {
		return audit.GovLimitBudgetDay
	}
	return audit.GovLimitBudgetMonth
```

In `internal/authority/pgstore/shared_constraints.go`, replace lines 127–133:

```go
	add("key", p.KeyID, "microUSD", "", "", p.BudgetUSDMicrosPerDay, true, v1alpha1.PeriodCalendarDay)
	for _, doc := range docs {
		parsed, err := policy.FromV1Alpha1(&doc)
		if err != nil {
			return nil, nil, sharedError(err)
		}
		if !sharedMatch(parsed.Subject.Team, parsed.Subject.User, p) {
```

with:

```go
	add("key", p.KeyID, "microUSD", "", "", p.BudgetUSDMicrosPerDay, true, v1alpha1.PeriodCalendarDay)
	generations := make(map[string]int64, len(docs))
	for _, doc := range docs {
		parsed, err := policy.FromV1Alpha1(&doc)
		if err != nil {
			return nil, nil, sharedError(err)
		}
		generations[parsed.Name] = parsed.Generation
		if !sharedMatch(parsed.Subject.Team, parsed.Subject.User, p) {
```

In `internal/authority/pgstore/shared_constraints.go`, replace line 160:

```go
			limit: b.LimitMicroUSD, hard: b.HardCap, start: b.WindowStart, end: b.WindowEnd})
```

with:

```go
			limit: b.LimitMicroUSD, hard: b.HardCap, start: b.WindowStart, end: b.WindowEnd})
	}
	for i := range defs {
		defs[i].generation = generations[defs[i].policy] // zero for team/key settings
```

In `internal/authority/pgstore/shared_constraints.go`, replace line 284:

```go
			return &governance.SharedDenial{Status: 402, Reason: "shared monetary budget exhausted"}
```

with:

```go
			return &governance.SharedDenial{Status: 402, Reason: "shared monetary budget exhausted", Governance: d.denial()}
```

In `internal/authority/pgstore/shared_constraints.go`, replace line 290:

```go
		return &governance.SharedDenial{Status: 429, Reason: "shared rate or token quota exhausted", RetryAfter: retry}
```

with:

```go
		return &governance.SharedDenial{Status: 429, Reason: "shared rate or token quota exhausted", RetryAfter: retry, Governance: d.denial()}
```

In `internal/authority/local/admission.go`, replace lines 5–12:

```go
	"encoding/json"
	"fmt"
	"time"

	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/policy"
)

```

with:

```go
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/policy"
)

// budgetDenial names the policy budget that ran out of authority. ADR-045
// grants carry the policy and rule name but no document generation.
func budgetDenial(b policy.AuthorityBudget) *audit.GovernanceRef {
	ref := &audit.GovernanceRef{Scope: audit.GovScopeTeam, Limit: audit.GovLimitBudgetMonth, Source: audit.GovSourcePolicy,
		Policy: &audit.PolicyRuleRef{Name: b.Policy, Rule: b.Rule}}
	if b.User != "" {
		ref.Scope = audit.GovScopeUser
	}
	if b.WindowEnd.Sub(b.WindowStart) == 24*time.Hour {
		ref.Limit = audit.GovLimitBudgetDay
	}
	return ref
}

```

In `internal/authority/local/admission.go`, replace line 82:

```go
				denied = governance.ErrAuthorityExhausted
```

with:

```go
				// The first exhausted hard budget names the refusal (ADR-057).
				if !errors.Is(denied, governance.ErrAuthorityExhausted) {
					denied = &governance.BudgetDenial{Err: governance.ErrAuthorityExhausted, Governance: budgetDenial(b)}
				}
```

- [ ] **Step 5: Run the packages and watch them pass, then stop Postgres**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run
cd $W && gofmt -l internal/authority; INFERPLANE_TEST_PG_DSN='postgres://postgres:audit-test@127.0.0.1:55432/postgres?sslmode=disable' go test -buildvcs=false -race ./internal/authority/pgstore/ > $S/t6-pg.log 2>&1; echo "pg=$?"; go test -buildvcs=false ./internal/authority/local/ > $S/t6-local.log 2>&1; echo "local=$?"; grep -E '^(ok|FAIL)|--- FAIL' $S/t6-pg.log $S/t6-local.log; INFERPLANE_TEST_PG_DSN='postgres://postgres:audit-test@127.0.0.1:55432/postgres?sslmode=disable' go test -buildvcs=false ./internal/authority/pgstore/ -v > $S/t6-v.log 2>&1; echo "skips=$(grep -c -- '--- SKIP' $S/t6-v.log)"
docker stop inferplane-audit-pg-fix > /dev/null; echo "left=$(docker ps -a --filter name=inferplane-audit-pg-fix -q | wc -l)"
```
Expected: no `gofmt` output, `pg=0`, `local=0`, `ok` for both packages, `skips=0`, `left=0`. (`internal/authority/local` takes several minutes under `-race`; Task 14 runs it with `-race`.)

- [ ] **Step 6: Module notes**

In `internal/CLAUDE.md`, replace line 161:

```markdown
the request path under a process mutex; benchmark that serialized work separately.
```

with:

```markdown
the request path under a process mutex; benchmark that serialized work separately.
An exhausted hard budget refuses with `governance.BudgetDenial` naming its policy
and rule for the audit record (ADR-057); grants carry no document generation.
```

In `internal/CLAUDE.md`, replace line 173:

```markdown
unsupported. Separate per-replica audit segments are still required.
```

with:

```markdown
unsupported. Separate per-replica audit segments are still required.
A refusal names its definition in `SharedDenial.Governance` (a policy rule with
its generation, the team snapshot or the key option; ADR-057), for the audit
record only, never the client response.
```

- [ ] **Step 7: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree
git -C $W add internal/authority/pgstore/shared_constraints.go internal/authority/pgstore/shared_denial_test.go internal/authority/local/admission.go internal/authority/local/denial_test.go internal/CLAUDE.md
git -C $W commit -s -m "feat(authority): name the refusing budget or limit (ADR-057)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Console projection of governance denials, with migrations

**Files:**
- Modify: `internal/analytics/auditquery.go:37` (DDL), `:44-48` (migration columns, `auditColumns`), `:54` (`auditSelect`), `:112-115` (`AuditDenial`), `:225` (`InsertAuditEvent`), `:248` (`scanAuditEvent`), `:330` (`RunAuditEvent` scan), `:421-424` (summary denials)
- Modify: `internal/analytics/auditevents.go:183` (`AuditEvent`), `:226`, `:236` (`ProjectAuditEvent`), `:279` (new `governancePolicyRef`)
- Modify: `internal/analytics/index.go:68`, `:88-90`, `:97` (`migrateEventsColumns` → `migrateColumns`), `:128` (`OpenSQLite`)
- Modify: `internal/analytics/pgstore/pgstore.go:244` (`ensureSchema`)
- Modify: `docs/reference/data.md:166`, `:201-202`, `:208`, `:222-227`; `docs/reference/data.ko.md:114`, `:144-145`, `:151`, `:163-168`; `docs/reference/api.md:219-223`; `docs/reference/api.ko.md:189-193`; `docs/reference/security.md:185-186`; `docs/reference/security.ko.md:165-166`; `internal/CLAUDE.md:99` (`analytics/` bullet)
- Test: `internal/analytics/governance_projection_test.go`, `internal/analytics/pgstore/governance_projection_test.go` (Postgres)

**Interfaces:**
- Consumes: `audit.Record.Governance`, `audit.GovernanceRef`, `audit.PolicyRuleRef`, `audit.KnownDenyReason` (Tasks 1, 4); test helpers `rec`, `str`, `openIndex` (`internal/analytics/auditevents_test.go:11-180`), `newTestStore`, `acquire`, `auditRec`, `auditStr` (`internal/analytics/pgstore/pgstore_test.go:34`, `audit_events_test.go:21-75`).
- Produces: `analytics.AuditEventsMigrationColumns []string`; `AuditEvent.GovScope, GovLimit, GovSource string` (`json:"gov_scope,omitempty"` …); `AuditDenial.Scope, Limit, Source string`; `migrateColumns(db *sql.DB, table string, stmts []string) error`; `migrateAuditEventColumns(db *sql.DB) error`; `governancePolicyRef(p audit.PolicyRuleRef) string`.

- [ ] **Step 1: Start Postgres (loopback only)**

```bash
S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run; mkdir -p $S
docker run -d --rm --name inferplane-audit-pg-fix -e POSTGRES_PASSWORD=audit-test -p 127.0.0.1:55432:5432 postgres:17-alpine > $S/pg.log 2>&1; echo "run=$?"
docker exec inferplane-audit-pg-fix sh -c 'for i in $(seq 1 60); do pg_isready -h 127.0.0.1 -U postgres -q && exit 0; sleep 1; done; exit 1'; echo "ready=$?"
```
Expected: `run=0`, `ready=0` (on `run=125`, `docker stop inferplane-audit-pg-fix` and repeat).

- [ ] **Step 2: Write the failing tests**

Create `internal/analytics/governance_projection_test.go`:

```go
package analytics

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
)

// legacyAuditEventsTable is the console table as first shipped, before the
// ADR-057 governance columns.
const legacyAuditEventsTable = `CREATE TABLE audit_events (
  id TEXT PRIMARY KEY, ts TEXT NOT NULL DEFAULT '', ts_ms BIGINT NOT NULL DEFAULT 0, event TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT '', team TEXT NOT NULL DEFAULT '', key_id TEXT NOT NULL DEFAULT '', ingress TEXT NOT NULL DEFAULT '',
  stream INTEGER NOT NULL DEFAULT 0, status INTEGER NOT NULL DEFAULT 0, deny_code TEXT NOT NULL DEFAULT '',
  routing_reason TEXT NOT NULL DEFAULT '', privacy TEXT NOT NULL DEFAULT '', masked INTEGER NOT NULL DEFAULT 0,
  would_restrict INTEGER NOT NULL DEFAULT 0, pii INTEGER NOT NULL DEFAULT 0, policy_ref TEXT NOT NULL DEFAULT '',
  planned_boundary TEXT NOT NULL DEFAULT '', actual_boundary TEXT NOT NULL DEFAULT '', request_class TEXT NOT NULL DEFAULT '',
  session_ref TEXT NOT NULL DEFAULT '', trace_id TEXT NOT NULL DEFAULT '', model_requested TEXT NOT NULL DEFAULT '',
  model_actual TEXT NOT NULL DEFAULT '', provider TEXT NOT NULL DEFAULT '', fallback INTEGER NOT NULL DEFAULT 0,
  attempt_count INTEGER NOT NULL DEFAULT 0, auto_verdict TEXT NOT NULL DEFAULT '', auto_stage TEXT NOT NULL DEFAULT '',
  auto_signature TEXT NOT NULL DEFAULT '', input_tokens BIGINT NOT NULL DEFAULT 0, output_tokens BIGINT NOT NULL DEFAULT 0,
  cost_micros BIGINT NOT NULL DEFAULT 0, total_ms BIGINT NOT NULL DEFAULT 0, ttft_ms BIGINT NOT NULL DEFAULT 0,
  has_body INTEGER NOT NULL DEFAULT 0, record_json TEXT NOT NULL DEFAULT ''
);
CREATE TABLE audit_event_categories (id TEXT NOT NULL, category TEXT NOT NULL, PRIMARY KEY (id, category));`

// governanceDenial is a 429 whose record names a policy rule; the request
// also matched a privacy rule that did not refuse it.
func governanceDenial(id string) audit.Record {
	return rec(id, "request_started", 429, func(r *audit.Record) {
		r.Outcome.Error = str("team_rate_limited")
		r.Request.Routing = &audit.RoutingRef{Reason: "unchanged", Policies: []audit.RoutingPolicyRef{{Name: "privacy", Generation: 7, Rule: "protect"}}}
		r.Governance = &audit.GovernanceRef{Scope: audit.GovScopeTeam, Limit: audit.GovLimitRPM, Source: audit.GovSourcePolicy,
			Policy: &audit.PolicyRuleRef{Name: "team-rate", Generation: 2, Rule: "rpm"}}
	})
}

func TestProjectGovernanceDenial(t *testing.T) {
	row, ok := ProjectAuditEvent(governanceDenial("01G"), nil)
	if !ok || row.GovScope != "team" || row.GovLimit != "rpm" || row.GovSource != "policy" || row.PolicyRef != "team-rate#2·rpm" {
		t.Fatalf("row = %+v", row.AuditEvent)
	}
	authority := governanceDenial("01H")
	authority.Governance.Policy.Generation = 0
	if row, _ := ProjectAuditEvent(authority, nil); row.PolicyRef != "team-rate·rpm" {
		t.Fatalf("generation-less rule = %q", row.PolicyRef)
	}
	setting := governanceDenial("01S")
	setting.Governance = &audit.GovernanceRef{Scope: audit.GovScopeKey, Limit: audit.GovLimitRPM, Source: audit.GovSourceKey}
	if row, _ := ProjectAuditEvent(setting, nil); row.PolicyRef != "" || row.GovSource != "key" || row.GovScope != "key" {
		t.Fatalf("setting denial = %+v", row.AuditEvent)
	}
	// A denial written before the field, which also matched a privacy rule:
	// no source, and the privacy rule is not named as the refuser.
	legacy := rec("01L", "request_started", 429, func(r *audit.Record) {
		r.Outcome.Error = str("key_rate_limited")
		r.Request.Routing = &audit.RoutingRef{Reason: "unchanged", Policies: []audit.RoutingPolicyRef{{Name: "privacy", Generation: 7, Rule: "protect"}}}
	})
	if row, _ := ProjectAuditEvent(legacy, nil); row.GovScope != "" || row.GovLimit != "" || row.GovSource != "" || row.PolicyRef != "" {
		t.Fatalf("legacy denial = %+v", row.AuditEvent)
	}
	// A routing denial still names its routing rule.
	blocked := rec("01B", "request_started", 403, func(r *audit.Record) {
		r.Request.Routing = &audit.RoutingRef{Reason: "sensitive_blocked", Privacy: "block", Policies: []audit.RoutingPolicyRef{{Name: "privacy", Generation: 7, Rule: "protect"}}}
	})
	if row, _ := ProjectAuditEvent(blocked, nil); row.PolicyRef != "privacy#7·protect" {
		t.Fatalf("routing denial policy = %q", row.PolicyRef)
	}
}

// A console table created before the governance columns gains them; its rows
// stay, read as "not recorded", and boot replay projects new records into it.
func TestAuditEventsMigrationKeepsRowsAndReplays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "console.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	old := rec("01JC0000000000000000000041", "request_started", 429, func(r *audit.Record) { r.Outcome.Error = str("key_rate_limited") })
	oldLine, err := old.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(legacyAuditEventsTable); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_events(id, ts, ts_ms, event, kind, team, key_id, status, deny_code, record_json)
		VALUES (?, '2026-10-29T01:00:00Z', 1793235600000, 'request_started', 'policy_denied', 'platform', 'ik_a', 429, 'key_rate_limited', ?)`,
		old.ID, string(oldLine)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for open := 0; open < 2; open++ { // the second open re-runs an idempotent migration
		ix, err := OpenSQLite(path)
		if err != nil {
			t.Fatalf("open %d: %v", open, err)
		}
		newer := governanceDenial("01JC0000000000000000000042")
		line, err := newer.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ix.Replay(strings.NewReader(string(oldLine) + "\n" + string(line) + "\n")); err != nil {
			t.Fatal(err)
		}
		kept, ok, err := ix.AuditEvent(old.ID)
		if err != nil || !ok || kept.Event.DenyCode != "key_rate_limited" || kept.Event.GovSource != "" || kept.Event.PolicyRef != "" {
			t.Fatalf("open %d: legacy row = %+v ok=%v err=%v", open, kept.Event, ok, err)
		}
		sum, err := ix.AuditSummary(AuditFilter{})
		if err != nil || len(sum.Denials) != 2 {
			t.Fatalf("open %d: denials = %+v err=%v", open, sum.Denials, err)
		}
		for _, d := range sum.Denials {
			switch d.Code {
			case "team_rate_limited":
				if d.Policy != "team-rate#2·rpm" || d.Scope != "team" || d.Limit != "rpm" || d.Source != "policy" {
					t.Fatalf("open %d: governance denial = %+v", open, d)
				}
			case "key_rate_limited":
				if d.Policy != "" || d.Scope != "" || d.Limit != "" || d.Source != "" {
					t.Fatalf("open %d: legacy denial = %+v", open, d)
				}
			default:
				t.Fatalf("open %d: unexpected denial %+v", open, d)
			}
		}
		if err := ix.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
```

Create `internal/analytics/pgstore/governance_projection_test.go`:

```go
package pgstore

import (
	"context"
	"testing"

	"github.com/inferplane/inferplane/internal/analytics"
	"github.com/inferplane/inferplane/internal/audit"
)

// A Mode B console table created before the governance columns gains them
// once (migration marker 2); its rows stay and read as "not recorded", and
// later ticks fill the columns from new records (ADR-057).
func TestPgstoreAuditEventsMigrationKeepsRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	epoch := acquire(t, s)
	old := auditRec("01JC0000000000000000000061", "request_started", 429, func(r *audit.Record) { r.Outcome.Error = auditStr("key_rate_limited") })
	if err := s.ingestBatch(ctx, "holder", epoch, []audit.Record{old}, nil); err != nil {
		t.Fatal(err)
	}
	// Recreate a database from before the governance columns.
	if _, err := s.db.Exec(ctx, `ALTER TABLE audit_events DROP COLUMN gov_scope, DROP COLUMN gov_limit, DROP COLUMN gov_source;
DELETE FROM analytics_schema_migrations WHERE version = 2`); err != nil {
		t.Fatal(err)
	}
	for migration := 0; migration < 2; migration++ {
		if err := s.ensureSchema(ctx); err != nil {
			t.Fatalf("migration %d: %v", migration, err)
		}
	}
	var markers int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM analytics_schema_migrations WHERE version = 2`).Scan(&markers); err != nil || markers != 1 {
		t.Fatalf("markers = %d err=%v", markers, err)
	}
	kept, ok, err := s.AuditEvent(old.ID)
	if err != nil || !ok || kept.Event.DenyCode != "key_rate_limited" || kept.Event.GovSource != "" {
		t.Fatalf("legacy row = %+v ok=%v err=%v", kept.Event, ok, err)
	}
	newer := auditRec("01JC0000000000000000000062", "request_started", 402, func(r *audit.Record) {
		r.Outcome.Error = auditStr("team_budget_exceeded")
		r.Governance = &audit.GovernanceRef{Scope: audit.GovScopeTeam, Limit: audit.GovLimitBudgetMonth, Source: audit.GovSourcePolicy,
			Policy: &audit.PolicyRuleRef{Name: "money", Generation: 3, Rule: "month"}}
	})
	if err := s.ingestBatch(ctx, "holder", epoch, []audit.Record{newer}, nil); err != nil {
		t.Fatal(err)
	}
	sum, err := s.AuditSummary(analytics.AuditFilter{})
	if err != nil || len(sum.Denials) != 2 {
		t.Fatalf("denials = %+v err=%v", sum.Denials, err)
	}
	for _, d := range sum.Denials {
		switch d.Code {
		case "team_budget_exceeded":
			if d.Policy != "money#3·month" || d.Scope != "team" || d.Limit != "budget_month" || d.Source != "policy" {
				t.Fatalf("governance denial = %+v", d)
			}
		case "key_rate_limited":
			if d.Policy != "" || d.Scope != "" || d.Limit != "" || d.Source != "" {
				t.Fatalf("legacy denial = %+v", d)
			}
		default:
			t.Fatalf("unexpected denial %+v", d)
		}
	}
}
```

- [ ] **Step 3: Run them and watch them fail**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run
cd $W && INFERPLANE_TEST_PG_DSN='postgres://postgres:audit-test@127.0.0.1:55432/postgres?sslmode=disable' go test -buildvcs=false ./internal/analytics/ ./internal/analytics/pgstore/ -run 'TestProjectGovernanceDenial|TestAuditEventsMigrationKeepsRowsAndReplays|TestPgstoreAuditEventsMigrationKeepsRows' > $S/t7-red.log 2>&1; echo "exit=$?"; grep -E 'FAIL|undefined' $S/t7-red.log
```
Expected: `exit=1`, both packages `[build failed]` with `row.GovScope undefined (type AuditEventRow has no field or method GovScope)` and `d.Scope undefined (type analytics.AuditDenial has no field or method Scope)`.

- [ ] **Step 4: Implement**

In `internal/analytics/auditquery.go`, replace line 37:

```go
  has_body INTEGER NOT NULL DEFAULT 0, record_json TEXT NOT NULL DEFAULT ''
```

with:

```go
  has_body INTEGER NOT NULL DEFAULT 0, record_json TEXT NOT NULL DEFAULT '',
  gov_scope TEXT NOT NULL DEFAULT '', gov_limit TEXT NOT NULL DEFAULT '', gov_source TEXT NOT NULL DEFAULT ''
```

In `internal/analytics/auditquery.go`, replace lines 44–48:

```go

const auditColumns = `id, ts, ts_ms, event, kind, team, key_id, ingress, stream, status, deny_code, routing_reason, privacy, masked,
 would_restrict, pii, policy_ref, planned_boundary, actual_boundary, request_class, session_ref, trace_id, model_requested,
 model_actual, provider, fallback, attempt_count, auto_verdict, auto_stage, auto_signature, input_tokens, output_tokens,
 cost_micros, total_ms, ttft_ms, has_body, record_json`
```

with:

```go

// AuditEventsMigrationColumns are console columns added after the table
// shipped. A database created before them gains each with its empty default,
// so existing rows (records written before ADR-057 governance evidence) keep
// their values and read as "not recorded".
var AuditEventsMigrationColumns = []string{
	`gov_scope TEXT NOT NULL DEFAULT ''`,
	`gov_limit TEXT NOT NULL DEFAULT ''`,
	`gov_source TEXT NOT NULL DEFAULT ''`,
}

const auditColumns = `id, ts, ts_ms, event, kind, team, key_id, ingress, stream, status, deny_code, routing_reason, privacy, masked,
 would_restrict, pii, policy_ref, planned_boundary, actual_boundary, request_class, session_ref, trace_id, model_requested,
 model_actual, provider, fallback, attempt_count, auto_verdict, auto_stage, auto_signature, input_tokens, output_tokens,
 cost_micros, total_ms, ttft_ms, has_body, gov_scope, gov_limit, gov_source, record_json`
```

In `internal/analytics/auditquery.go`, replace line 54:

```go
 e.has_body FROM audit_events e`
```

with:

```go
 e.has_body, e.gov_scope, e.gov_limit, e.gov_source FROM audit_events e`
```

In `internal/analytics/auditquery.go`, replace lines 112–115:

```go
}
type AuditDenial struct {
	Code   string `json:"code"`
	Policy string `json:"policy,omitempty"`
```

with:

```go
}

// AuditDenial is one Denials row. Scope, Limit and Source name the refusing
// governance limit (ADR-057); they are empty for routing denials and for
// governance denials recorded before that evidence existed.
type AuditDenial struct {
	Code   string `json:"code"`
	Policy string `json:"policy,omitempty"`
	Scope  string `json:"scope,omitempty"`
	Limit  string `json:"limit,omitempty"`
	Source string `json:"source,omitempty"`
```

In `internal/analytics/auditquery.go`, replace line 225:

```go
		e.CostMicros, e.TotalMs, e.TTFTMs, b2i(e.HasBody), row.RecordJSON}
```

with:

```go
		e.CostMicros, e.TotalMs, e.TTFTMs, b2i(e.HasBody), e.GovScope, e.GovLimit, e.GovSource, row.RecordJSON}
```

In `internal/analytics/auditquery.go`, replace line 248:

```go
		&e.TotalMs, &e.TTFTMs, &hasBody)
```

with:

```go
		&e.TotalMs, &e.TTFTMs, &hasBody, &e.GovScope, &e.GovLimit, &e.GovSource)
```

In `internal/analytics/auditquery.go`, replace line 330:

```go
			&e.TotalMs, &e.TTFTMs, &hasBody, &raw); err != nil {
```

with:

```go
			&e.TotalMs, &e.TTFTMs, &hasBody, &e.GovScope, &e.GovLimit, &e.GovSource, &raw); err != nil {
```

In `internal/analytics/auditquery.go`, replace lines 421–424:

```go
	if err := run("SELECT e.deny_code, e.policy_ref, e.team, e.key_id, COUNT(*), MAX(e.ts) FROM audit_events e"+b.clause()+
		" GROUP BY e.deny_code, e.policy_ref, e.team, e.key_id ORDER BY COUNT(*) DESC, MAX(e.ts) DESC, e.deny_code, e.policy_ref, e.team, e.key_id LIMIT 50", b.args, func(s RowScanner) error {
		var x AuditDenial
		err := s.Scan(&x.Code, &x.Policy, &x.Team, &x.KeyID, &x.Count, &x.LastTS)
```

with:

```go
	if err := run("SELECT e.deny_code, e.policy_ref, e.gov_scope, e.gov_limit, e.gov_source, e.team, e.key_id, COUNT(*), MAX(e.ts) FROM audit_events e"+b.clause()+
		" GROUP BY e.deny_code, e.policy_ref, e.gov_scope, e.gov_limit, e.gov_source, e.team, e.key_id"+
		" ORDER BY COUNT(*) DESC, MAX(e.ts) DESC, e.deny_code, e.policy_ref, e.gov_scope, e.gov_limit, e.gov_source, e.team, e.key_id LIMIT 50", b.args, func(s RowScanner) error {
		var x AuditDenial
		err := s.Scan(&x.Code, &x.Policy, &x.Scope, &x.Limit, &x.Source, &x.Team, &x.KeyID, &x.Count, &x.LastTS)
```

In `internal/analytics/auditevents.go`, replace line 183:

```go
	Categories      []string `json:"categories,omitempty"`
```

with:

```go
	Categories      []string `json:"categories,omitempty"`
	// GovScope, GovLimit and GovSource name the limit behind a governance
	// denial (ADR-057 record field governance); empty when not recorded.
	GovScope  string `json:"gov_scope,omitempty"`
	GovLimit  string `json:"gov_limit,omitempty"`
	GovSource string `json:"gov_source,omitempty"`
```

In `internal/analytics/auditevents.go`, replace line 226:

```go
		if len(rt.Policies) > 0 {
```

with:

```go
		// A routing policy names a routing decision. A denial with a
		// governance or RBAC code was refused by something else, even when the
		// request also matched a routing rule (ADR-057).
		if len(rt.Policies) > 0 && r.Governance == nil && !(row.Status >= 400 && audit.KnownDenyReason(row.DenyCode)) {
```

In `internal/analytics/auditevents.go`, replace line 236:

```go
			}
```

with:

```go
			}
		}
	}
	// A governance denial names its own limit; a routing policy the request
	// also matched did not refuse it.
	if g := r.Governance; g != nil {
		row.GovScope, row.GovLimit, row.GovSource = boundedCode(g.Scope), boundedCode(g.Limit), boundedCode(g.Source)
		if p := g.Policy; p != nil {
			row.PolicyRef = auditText(governancePolicyRef(*p))
```

In `internal/analytics/auditevents.go`, replace line 279:

```go
	return row, true
```

with:

```go
	return row, true
}

// governancePolicyRef renders a governance rule like a routing one
// (name#generation·rule), without "#0" when the authority reports no
// generation (ADR-045 grants).
func governancePolicyRef(p audit.PolicyRuleRef) string {
	if p.Generation > 0 {
		return fmt.Sprintf("%s#%d·%s", p.Name, p.Generation, p.Rule)
	}
	return p.Name + "·" + p.Rule
```

In `internal/analytics/index.go`, replace line 68:

```go
	rows, err := db.Query(`PRAGMA table_info(events)`)
```

with:

```go
	return migrateColumns(db, "events", eventsMigrationColumns)
}

// migrateAuditEventColumns adds the console columns of
// AuditEventsMigrationColumns to an audit_events table created before them.
func migrateAuditEventColumns(db *sql.DB) error {
	stmts := make([]string, len(AuditEventsMigrationColumns))
	for i, col := range AuditEventsMigrationColumns {
		stmts[i] = "ALTER TABLE audit_events ADD COLUMN " + col
	}
	return migrateColumns(db, "audit_events", stmts)
}

// migrateColumns runs each "ALTER TABLE <table> ADD COLUMN <name> ..."
// statement whose column the table does not have yet.
func migrateColumns(db *sql.DB, table string, stmts []string) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
```

In `internal/analytics/index.go`, replace lines 88–90:

```go
	for _, stmt := range eventsMigrationColumns {
		// Each ALTER statement's target column name is token index 5:
		// "ALTER(0) TABLE(1) events(2) ADD(3) COLUMN(4) <name>(5) ...".
```

with:

```go
	for _, stmt := range stmts {
		// Each ALTER statement's target column name is token index 5:
		// "ALTER(0) TABLE(1) <table>(2) ADD(3) COLUMN(4) <name>(5) ...".
```

In `internal/analytics/index.go`, replace line 97:

```go
			return fmt.Errorf("migrate events.%s: %w", col, err)
```

with:

```go
			return fmt.Errorf("migrate %s.%s: %w", table, col, err)
```

In `internal/analytics/index.go`, replace line 128:

```go
	if _, err := db.Exec(AuditEventsDDL); err != nil {
```

with:

```go
	if _, err := db.Exec(AuditEventsDDL); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateAuditEventColumns(db); err != nil {
```

In `internal/analytics/pgstore/pgstore.go`, replace line 244:

```go
		return fmt.Errorf("create audit console tables: %w", err)
```

with:

```go
		return fmt.Errorf("create audit console tables: %w", err)
	}
	// Console columns added after the console tables shipped (ADR-057
	// governance evidence). The marker keeps later boots from taking the ALTER
	// lock again; existing rows keep the empty default.
	var consoleColumnsInstalled bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM analytics_schema_migrations WHERE version = 2)`).Scan(&consoleColumnsInstalled); err != nil {
		return fmt.Errorf("read audit console migration marker: %w", err)
	}
	if !consoleColumnsInstalled {
		for _, col := range analytics.AuditEventsMigrationColumns {
			if _, err := tx.Exec(ctx, `ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS `+col); err != nil {
				return fmt.Errorf("migrate audit console columns: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO analytics_schema_migrations(version) VALUES (2)`); err != nil {
			return fmt.Errorf("record audit console migration: %w", err)
		}
```

- [ ] **Step 5: Run both modes and watch them pass, then stop Postgres**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run
cd $W && gofmt -l internal/analytics; INFERPLANE_TEST_PG_DSN='postgres://postgres:audit-test@127.0.0.1:55432/postgres?sslmode=disable' go test -buildvcs=false -race ./internal/analytics/... ./internal/server/auditconsoleapi/... > $S/t7.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL)|--- FAIL' $S/t7.log; INFERPLANE_TEST_PG_DSN='postgres://postgres:audit-test@127.0.0.1:55432/postgres?sslmode=disable' go test -buildvcs=false ./internal/analytics/pgstore/ -v > $S/t7-v.log 2>&1; echo "skips=$(grep -c -- '--- SKIP' $S/t7-v.log)"
docker stop inferplane-audit-pg-fix > /dev/null; echo "left=$(docker ps -a --filter name=inferplane-audit-pg-fix -q | wc -l)"
```
Expected: no `gofmt` output, `exit=0`, `ok` for `internal/analytics`, `internal/analytics/pgstore` and `internal/server/auditconsoleapi`, `skips=0`, `left=0`. `TestPostgresDialectPlaceholdersMatchArgs` still passes with the three new columns.

- [ ] **Step 6: Reference docs and module note**

In `docs/reference/data.md`, replace line 166:

```markdown
The console reads three append-only, `omitempty` additions to terminal audit
```

with:

```markdown
The console reads four append-only, `omitempty` additions to terminal audit
```

In `docs/reference/data.md`, replace lines 201–202:

```markdown

### Console tables
```

with:

```markdown

### Governance denial source

A gateway denial record also carries `governance` when the refusing component
names its limit. It is written on `request_started` records with an outcome
only.

| Field | Type | Values and bounds |
|---|---|---|
| `governance.scope` | string | `team`, `key`, `user`, `gateway` (a setting of the whole gateway, such as pricing). |
| `governance.limit` | string | `rpm`, `tpm`, `tokens_per_day`, `tokens_per_month`, `budget_month`, `budget_day`, `budget` (a lease gate, which reports no window), `pricing`. |
| `governance.source` | string | `config` (config file `teams` or `pricing.on_missing`), `team_record` (`/admin/teams`, or the shared team snapshot), `policy`, `lease` (control-plane budget lease), `key` (the key's own option), `budget_store` (the budget store or the local liability ledger was full; no limit refused). Omitted when the governor was built without sources. |
| `governance.policy` | object | `{name, generation, rule}` of the binding GovernancePolicy rule when `source` is `policy`. `generation` is omitted when the authority does not report one (ADR-045 grants). |

The source is the layer whose value binds after composition: a policy rule
narrower than the config or team record, the config or team record when it is
stricter or equal, a lease allowance that clamps a budget, or the key's option.
Local `PreCheck`, the pricing guard, the ADR-054 local liability reservation,
the ADR-045 authority and ADR-046 shared admission all name their limit; a
refusal by an unavailable store names none. Reservation refusals carry
`outcome.error` `budget_authority_unavailable` on every ingress. The field
holds names only: never the limit value, a counter, key material or a user
identifier. Policy and rule names are operator text and enter the chain
verbatim. A denial written before this field has no `governance`.

### Console tables
```

In `docs/reference/data.md`, replace line 208:

```markdown
segment, not a re-encoding). Projection is first-write-wins on `id`.
```

with:

```markdown
segment, not a re-encoding). Projection is first-write-wins on `id`. A table
created before the governance columns gains `gov_scope`, `gov_limit` and
`gov_source` when the index opens (SQLite adds each missing column; Postgres
adds them once, under migration marker 2). Existing rows keep empty values,
which the console shows as not recorded, and boot replay does not rewrite them.
```

In `docs/reference/data.md`, replace lines 222–227:

```markdown
| Decision | `deny_code`, `routing_reason`, `privacy`, `masked`, `would_restrict`, `pii`, `policy_ref`, `planned_boundary`, `actual_boundary` |
| Client and trace | `request_class`, `session_ref`, `trace_id` |
| Routing | `model_requested`, `model_actual`, `provider`, `fallback`, `attempt_count` |
| Auto mode | `auto_verdict`, `auto_stage`, `auto_signature` |
| Usage | `input_tokens`, `output_tokens`, `cost_micros`, `total_ms`, `ttft_ms`, `has_body` |
| Source | `record_json` |
```

with:

```markdown
| Decision | `deny_code`, `routing_reason`, `privacy`, `masked`, `would_restrict`, `pii`, `policy_ref`, `planned_boundary`, `actual_boundary`, `gov_scope`, `gov_limit`, `gov_source` |
| Client and trace | `request_class`, `session_ref`, `trace_id` |
| Routing | `model_requested`, `model_actual`, `provider`, `fallback`, `attempt_count` |
| Auto mode | `auto_verdict`, `auto_stage`, `auto_signature` |
| Usage | `input_tokens`, `output_tokens`, `cost_micros`, `total_ms`, `ttft_ms`, `has_body` |
| Source | `record_json` |

`policy_ref` names the governance rule of a governance denial
(`name#generation·rule`, or `name·rule` without a generation). Otherwise it is
the first routing policy, except on a denial with a governance or RBAC deny
code: a routing rule the request also matched did not refuse it.
```

In `docs/reference/data.ko.md`, replace line 114:

```markdown
세 가지와 분석 인덱스의 콘솔 전용 투영을 읽습니다. 기존 기록과 정확 바이트 체인 검증은
```

with:

```markdown
네 가지와 분석 인덱스의 콘솔 전용 투영을 읽습니다. 기존 기록과 정확 바이트 체인 검증은
```

In `docs/reference/data.ko.md`, replace lines 144–145:

```markdown

### 콘솔 테이블
```

with:

```markdown

### 거버넌스 거부 출처

게이트웨이 거부 기록은 거부한 구성 요소가 자기 한도를 알려 줄 때 `governance`도 가집니다.
결과를 가진 `request_started` 기록에만 기록합니다.

| 필드 | 타입 | 값과 한계 |
|---|---|---|
| `governance.scope` | 문자열 | `team`, `key`, `user`, `gateway`(가격처럼 게이트웨이 전체에 걸친 설정). |
| `governance.limit` | 문자열 | `rpm`, `tpm`, `tokens_per_day`, `tokens_per_month`, `budget_month`, `budget_day`, `budget`(구간을 알리지 않는 리스 게이트), `pricing`. |
| `governance.source` | 문자열 | `config`(설정 파일의 `teams` 또는 `pricing.on_missing`), `team_record`(`/admin/teams` 또는 공유 팀 스냅샷), `policy`, `lease`(control plane 예산 리스), `key`(키 자체 옵션), `budget_store`(예산 저장소나 로컬 부채 원장이 가득 찬 경우로, 한도가 거부한 것이 아님). 거버너가 출처 없이 만들어졌으면 생략합니다. |
| `governance.policy` | 객체 | `source`가 `policy`일 때 적용된 GovernancePolicy 규칙의 `{name, generation, rule}`. 권한 저장소가 세대를 알려 주지 않으면(ADR-045 할당) `generation`을 생략합니다. |

출처는 합성 뒤 실제로 적용되는 계층입니다. 설정이나 팀 레코드보다 좁은 정책 규칙, 더
엄격하거나 같은 설정·팀 레코드, 예산을 제한하는 리스 할당량, 또는 키 옵션입니다. 로컬
`PreCheck`, 가격 가드, ADR-054 로컬 부채 예약, ADR-045 권한 저장소, ADR-046 공유 승인이
모두 자기 한도를 기록하며, 사용할 수 없는 저장소의 거부는 한도를 기록하지 않습니다. 예약
거부는 모든 수신 계층에서 `outcome.error` `budget_authority_unavailable`을 가집니다. 이
필드에는 이름만 있고 한도 값, 카운터, 키 자료, 사용자 식별자는 없습니다. 정책과 규칙 이름은
운영자가 정한 텍스트로 체인에 그대로 들어갑니다. 이 필드가 생기기 전의 거부 기록에는
`governance`가 없습니다.

### 콘솔 테이블
```

In `docs/reference/data.ko.md`, replace line 151:

```markdown
대해 먼저 쓴 값이 우선입니다.
```

with:

```markdown
대해 먼저 쓴 값이 우선입니다. 거버넌스 열이 생기기 전에 만든 테이블은 인덱스를 열 때
`gov_scope`, `gov_limit`, `gov_source`를 얻습니다(SQLite는 없는 열마다 추가하고, Postgres는
마이그레이션 표식 2로 한 번 추가). 기존 행은 빈 값을 유지하며 콘솔은 이를 기록 없음으로
보여 주고, 부팅 재생은 이 행을 다시 쓰지 않습니다.
```

In `docs/reference/data.ko.md`, replace lines 163–168:

```markdown
| 판단 | `deny_code`, `routing_reason`, `privacy`, `masked`, `would_restrict`, `pii`, `policy_ref`, `planned_boundary`, `actual_boundary` |
| 클라이언트·추적 | `request_class`, `session_ref`, `trace_id` |
| 라우팅 | `model_requested`, `model_actual`, `provider`, `fallback`, `attempt_count` |
| 자동 모드 | `auto_verdict`, `auto_stage`, `auto_signature` |
| 사용량 | `input_tokens`, `output_tokens`, `cost_micros`, `total_ms`, `ttft_ms`, `has_body` |
| 원본 | `record_json` |
```

with:

```markdown
| 판단 | `deny_code`, `routing_reason`, `privacy`, `masked`, `would_restrict`, `pii`, `policy_ref`, `planned_boundary`, `actual_boundary`, `gov_scope`, `gov_limit`, `gov_source` |
| 클라이언트·추적 | `request_class`, `session_ref`, `trace_id` |
| 라우팅 | `model_requested`, `model_actual`, `provider`, `fallback`, `attempt_count` |
| 자동 모드 | `auto_verdict`, `auto_stage`, `auto_signature` |
| 사용량 | `input_tokens`, `output_tokens`, `cost_micros`, `total_ms`, `ttft_ms`, `has_body` |
| 원본 | `record_json` |

`policy_ref`는 거버넌스 거부에서 거버넌스 규칙을 가리킵니다(`name#generation·rule`, 세대가
없으면 `name·rule`). 그 밖에는 첫 라우팅 정책이며, 거버넌스나 RBAC 거부 코드를 가진 거부는
예외입니다. 요청이 함께 일치한 라우팅 규칙이 그 요청을 거부한 것이 아니기 때문입니다.
```

In `docs/reference/api.md`, replace lines 219–223:

```markdown
| `/admin/audit/events` | `{"events":[...],"next":"<id>"}`, newest first; pass `next` as `before` for the following page. |
| `/admin/audit/events/{id}` | `{"event":{...},"record":{...},"hash":"sha256:...","waterfall":{...}}`. `record` is the stored audit line. `hash` is the SHA-256 of that line, shown as the record's chain position; it is not a chain verification (use `/admin/audit/verify`). `waterfall` is the timing timeline derived from `latency` and `attempts` (rows of kind `gateway`, `attempt`, `settle` or `denied`; no rows when the record has no latency). |
| `/admin/audit/sessions` | `{"sessions":[...],"next":"<id>"}`, one row per `session_ref` (records without one are not sessions) with request, denial, PII, auto-mode-block and subagent counts, cost and first/last time. |
| `/admin/audit/sessions/{ref}` | `{"events":[...],"next":"<id>"}`, oldest first, for one `s1:` session digest; only `after` and `limit` are applied. |
| `/admin/audit/summary` | Counts by kind, top 10 categories, PII counts (`masked`, `internal`, `blocked`, `shadow`), top 50 denials, auto mode counts (`block`, `allow`, `unknown`, `signatures`), 12 equal time buckets and `coverage_start_ms`, the earliest time in the index. |
```

with:

```markdown
| `/admin/audit/events` | `{"events":[...],"next":"<id>"}`, newest first; pass `next` as `before` for the following page. A governance denial row also carries `gov_scope`, `gov_limit` and `gov_source`, the names in its record's `governance`; other rows and denials recorded before that field omit them. |
| `/admin/audit/events/{id}` | `{"event":{...},"record":{...},"hash":"sha256:...","waterfall":{...}}`. `record` is the stored audit line. `hash` is the SHA-256 of that line, shown as the record's chain position; it is not a chain verification (use `/admin/audit/verify`). `waterfall` is the timing timeline derived from `latency` and `attempts` (rows of kind `gateway`, `attempt`, `settle` or `denied`; no rows when the record has no latency). |
| `/admin/audit/sessions` | `{"sessions":[...],"next":"<id>"}`, one row per `session_ref` (records without one are not sessions) with request, denial, PII, auto-mode-block and subagent counts, cost and first/last time. |
| `/admin/audit/sessions/{ref}` | `{"events":[...],"next":"<id>"}`, oldest first, for one `s1:` session digest; only `after` and `limit` are applied. |
| `/admin/audit/summary` | Counts by kind, top 10 categories, PII counts (`masked`, `internal`, `blocked`, `shadow`), top 50 denials (each with `code`, `policy`, `scope`, `limit`, `source`, `team`, `key_id`, `count` and `last_ts`; `scope`, `limit` and `source` are set only when the record names its governance limit), auto mode counts (`block`, `allow`, `unknown`, `signatures`), 12 equal time buckets and `coverage_start_ms`, the earliest time in the index. |
```

In `docs/reference/api.ko.md`, replace lines 189–193:

```markdown
| `/admin/audit/events` | `{"events":[...],"next":"<id>"}`, 최신순. 다음 페이지는 `next`를 `before`로 전달합니다. |
| `/admin/audit/events/{id}` | `{"event":{...},"record":{...},"hash":"sha256:...","waterfall":{...}}`. `record`는 저장된 감사 행입니다. `hash`는 그 행의 SHA-256으로 기록의 체인 위치를 보여 줄 뿐 체인 검증이 아닙니다(검증은 `/admin/audit/verify`). `waterfall`은 `latency`와 `attempts`에서 만든 타임라인입니다(행 종류 `gateway`, `attempt`, `settle`, `denied`. 기록에 지연 시간이 없으면 행이 없습니다). |
| `/admin/audit/sessions` | `{"sessions":[...],"next":"<id>"}`. `session_ref`별 한 행(없는 기록은 세션이 아님)과 요청·거부·PII·자동 모드 차단·서브에이전트 수, 비용, 처음·마지막 시각. |
| `/admin/audit/sessions/{ref}` | `{"events":[...],"next":"<id>"}`, 오래된 순. `s1:` 세션 다이제스트 하나의 이벤트이며 `after`와 `limit`만 적용합니다. |
| `/admin/audit/summary` | 종류별 건수, 상위 10개 카테고리, PII 건수(`masked`, `internal`, `blocked`, `shadow`), 상위 50개 거부, 자동 모드 건수(`block`, `allow`, `unknown`, `signatures`), 12개 균등 시간 구간, 인덱스가 가진 가장 이른 시각 `coverage_start_ms`. |
```

with:

```markdown
| `/admin/audit/events` | `{"events":[...],"next":"<id>"}`, 최신순. 다음 페이지는 `next`를 `before`로 전달합니다. 거버넌스 거부 행은 기록의 `governance` 이름인 `gov_scope`, `gov_limit`, `gov_source`도 가지며, 다른 행과 그 필드 이전에 기록된 거부에는 없습니다. |
| `/admin/audit/events/{id}` | `{"event":{...},"record":{...},"hash":"sha256:...","waterfall":{...}}`. `record`는 저장된 감사 행입니다. `hash`는 그 행의 SHA-256으로 기록의 체인 위치를 보여 줄 뿐 체인 검증이 아닙니다(검증은 `/admin/audit/verify`). `waterfall`은 `latency`와 `attempts`에서 만든 타임라인입니다(행 종류 `gateway`, `attempt`, `settle`, `denied`. 기록에 지연 시간이 없으면 행이 없습니다). |
| `/admin/audit/sessions` | `{"sessions":[...],"next":"<id>"}`. `session_ref`별 한 행(없는 기록은 세션이 아님)과 요청·거부·PII·자동 모드 차단·서브에이전트 수, 비용, 처음·마지막 시각. |
| `/admin/audit/sessions/{ref}` | `{"events":[...],"next":"<id>"}`, 오래된 순. `s1:` 세션 다이제스트 하나의 이벤트이며 `after`와 `limit`만 적용합니다. |
| `/admin/audit/summary` | 종류별 건수, 상위 10개 카테고리, PII 건수(`masked`, `internal`, `blocked`, `shadow`), 상위 50개 거부(`code`, `policy`, `scope`, `limit`, `source`, `team`, `key_id`, `count`, `last_ts`. `scope`·`limit`·`source`는 기록이 거버넌스 한도를 알릴 때만 값이 있음), 자동 모드 건수(`block`, `allow`, `unknown`, `signatures`), 12개 균등 시간 구간, 인덱스가 가진 가장 이른 시각 `coverage_start_ms`. |
```

In `docs/reference/security.md`, replace lines 185–186:

```markdown

**Access.** The five `/admin/audit/*` routes are read-only and full-admin only.
```

with:

```markdown

**Governance denials.** A denial record names the refusing limit in
`governance`: its scope, the kind of limit, where it was set and, for a policy,
the policy name, generation and rule. It never holds the limit value, counters,
key material or user identifiers. Policy and rule names are operator text stored
verbatim in the chain and the analytics index, so keep secrets out of them.

**Access.** The five `/admin/audit/*` routes are read-only and full-admin only.
```

In `docs/reference/security.ko.md`, replace lines 165–166:

```markdown

**접근.** `/admin/audit/*` 다섯 경로는 읽기 전용이며 full-admin 전용입니다. 응답에는
```

with:

```markdown

**거버넌스 거부.** 거부 기록은 `governance`에 거부한 한도를 적습니다. 범위, 한도 종류,
설정된 곳, 정책이면 정책 이름·세대·규칙입니다. 한도 값, 카운터, 키 자료, 사용자 식별자는
들어가지 않습니다. 정책과 규칙 이름은 운영자가 정한 텍스트로 체인과 분석 인덱스에 그대로
저장되므로 비밀을 넣지 마세요.

**접근.** `/admin/audit/*` 다섯 경로는 읽기 전용이며 full-admin 전용입니다. 응답에는
```

In `internal/CLAUDE.md`, replace line 99:

```markdown
- `analytics/` — derived usage read-model backing `GET /admin/logs`; `pgstore/` is the shared-Postgres backend (ADR-015). ADR-057 adds the console-only `audit_events` and `audit_event_categories` projection of terminal records (`auditevents.go`: `KindOf`, `ProjectAuditEvent`, `WaterfallOf`), with the shared `Dialect`/`Run*` query builders in `auditquery.go` used by both SQLite and Postgres and the `AuditEventQuerier` interface both stores implement. `events` and Usage numbers are unchanged. Console rows are best-effort per record: Mode A `Replay` and Mode B `ingestBatch` give each row its own savepoint, so a row the store refuses is skipped and logged by ID and store code (never the message, which can quote request bytes) and never blocks usage rows or the checkpoint; record free text passes `auditText` (NUL dropped, invalid UTF-8 replaced, 256-byte cap) because client model strings reach denial records unvalidated.
```

with:

```markdown
- `analytics/` — derived usage read-model backing `GET /admin/logs`; `pgstore/` is the shared-Postgres backend (ADR-015). ADR-057 adds the console-only `audit_events` and `audit_event_categories` projection of terminal records (`auditevents.go`: `KindOf`, `ProjectAuditEvent`, `WaterfallOf`), with the shared `Dialect`/`Run*` query builders in `auditquery.go` used by both SQLite and Postgres and the `AuditEventQuerier` interface both stores implement. `events` and Usage numbers are unchanged. Console rows are best-effort per record: Mode A `Replay` and Mode B `ingestBatch` give each row its own savepoint, so a row the store refuses is skipped and logged by ID and store code (never the message, which can quote request bytes) and never blocks usage rows or the checkpoint; record free text passes `auditText` (NUL dropped, invalid UTF-8 replaced, 256-byte cap) because client model strings reach denial records unvalidated. `audit_events` also carries `gov_scope`/`gov_limit`/`gov_source` from the record's `governance` (ADR-057); `AuditEventsMigrationColumns` adds them, empty, to older tables (SQLite on open, Postgres once under migration marker 2), and `policy_ref` then names the governance rule, never a routing rule on a governance- or RBAC-coded denial.
```

Refresh the Korean companions' source hashes and check the pairs:

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run
cd $W && python3 - docs/reference/data.md docs/reference/data.ko.md docs/reference/api.md docs/reference/api.ko.md docs/reference/security.md docs/reference/security.ko.md <<'EOF'
import hashlib, re, sys
for en, ko in zip(sys.argv[1::2], sys.argv[2::2]):
    digest = hashlib.sha256(open(en, 'rb').read()).hexdigest()
    text = open(ko, encoding='utf-8').read()
    text, n = re.subn(r'(?m)^translation_source_sha256: [0-9a-f]{64}$', 'translation_source_sha256: ' + digest, text, count=1)
    assert n == 1, ko
    open(ko, 'w', encoding='utf-8').write(text)
    print(ko, digest[:12])
EOF
python3 scripts/check_docs_translations.py > $S/t7-docs.log 2>&1; echo "docs=$?"; cat $S/t7-docs.log
```
Expected: three hash lines, `docs=0`, `Validated 48 Korean/English document pairs and both ADR indexes.`

- [ ] **Step 7: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree
git -C $W add internal/analytics/auditquery.go internal/analytics/auditevents.go internal/analytics/index.go internal/analytics/pgstore/pgstore.go internal/analytics/governance_projection_test.go internal/analytics/pgstore/governance_projection_test.go docs/reference/data.md docs/reference/data.ko.md docs/reference/api.md docs/reference/api.ko.md docs/reference/security.md docs/reference/security.ko.md internal/CLAUDE.md
git -C $W commit -s -m "feat(analytics): project governance denial sources (ADR-057)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Mode A live sink commits each console row atomically and logs refusals (item 4)

**Files:**
- Modify: `internal/analytics/auditevents_sqlite.go:72-79` (`IngestAudit`)
- Modify: `docs/reference/data.md:242-245`, `docs/reference/data.ko.md:181-182` (best-effort paragraph), `internal/CLAUDE.md:99` (`analytics/` bullet)
- Test: `internal/analytics/ingest_audit_test.go`

**Interfaces:**
- Consumes: `ingestAudit`, `LogAuditSkip`, `sqliteCode`, `markIngest` (existing, `internal/analytics/auditevents_sqlite.go:34-79`); test helpers `openIndex`, `rec` (`internal/analytics/auditevents_test.go`).
- Produces: `(*Index).ingestAuditTx(r audit.Record, line []byte) error`; `IngestAudit` keeps its signature `func (ix *Index) IngestAudit(r audit.Record, line []byte) error`.

- [ ] **Step 1: Write the failing test**

Create `internal/analytics/ingest_audit_test.go`:

```go
package analytics

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
)

// captureStderr returns what fn writes to os.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = old
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// The live sink path commits a console row with its categories; a row the
// store refuses leaves neither, is logged by record ID and driver code only,
// and lands on a later attempt.
func TestIngestAuditIsAtomicAndLogsRefusal(t *testing.T) {
	ix := openIndex(t)
	if _, err := ix.db.Exec(`CREATE TRIGGER audit_probe_fail BEFORE INSERT ON audit_event_categories BEGIN SELECT RAISE(ABORT, 'probe'); END`); err != nil {
		t.Fatal(err)
	}
	r := rec("01JC0000000000000000000091", "request_completed", 200, func(r *audit.Record) {
		r.Request.ModelRequested = "request-text-must-not-log"
		r.Request.Routing = &audit.RoutingRef{Masked: true, Categories: []string{"email"}}
	})
	var err error
	logged := captureStderr(t, func() { err = ix.IngestAudit(r, nil) })
	if err == nil {
		t.Fatal("a refused row reported success")
	}
	if _, ok, _ := ix.AuditEvent(r.ID); ok {
		t.Fatal("row committed without its categories")
	}
	if !strings.Contains(logged, `skipped record "01JC0000000000000000000091" (sqlite code`) ||
		strings.Contains(logged, "request-text-must-not-log") || strings.Contains(logged, "probe") {
		t.Fatalf("log = %q", logged)
	}
	if _, err := ix.db.Exec(`DROP TRIGGER audit_probe_fail`); err != nil {
		t.Fatal(err)
	}
	if err := ix.IngestAudit(r, nil); err != nil {
		t.Fatal(err)
	}
	if d, ok, err := ix.AuditEvent(r.ID); err != nil || !ok || len(d.Event.Categories) != 1 {
		t.Fatalf("retried row ok=%v err=%v categories=%v", ok, err, d.Event.Categories)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run; mkdir -p $S
cd $W && go test -buildvcs=false ./internal/analytics/ -run TestIngestAuditIsAtomicAndLogsRefusal > $S/t8-red.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL)|ingest_audit_test.go' $S/t8-red.log
```
Expected: `exit=1`, `ingest_audit_test.go:51: row committed without its categories`.

- [ ] **Step 3: Implement**

In `internal/analytics/auditevents_sqlite.go`, replace lines 72–79:

```go
// IngestAudit projects one terminal record into the console table.
func (ix *Index) IngestAudit(r audit.Record, line []byte) error {
	if err := ingestAudit(ix.db, r, line); err != nil {
		return err
	}
	ix.markIngest(time.Now().UTC())
	return nil
}
```

with:

```go
// IngestAudit projects one terminal record into the console table; the live
// sink calls it for every terminal record. The row and its categories commit
// in one transaction, so a refused category never leaves a row without it. A
// failure is logged by record ID and driver code only (store messages can
// quote request bytes) and returned; the sink drops it and the next boot's
// Replay offers the record again.
func (ix *Index) IngestAudit(r audit.Record, line []byte) error {
	if err := ix.ingestAuditTx(r, line); err != nil {
		LogAuditSkip(r.ID, sqliteCode(err))
		return err
	}
	ix.markIngest(time.Now().UTC())
	return nil
}

func (ix *Index) ingestAuditTx(r audit.Record, line []byte) error {
	tx, err := ix.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after a successful Commit
	if err := ingestAudit(tx, r, line); err != nil {
		return err
	}
	return tx.Commit()
}
```

- [ ] **Step 4: Run the package and watch it pass**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run
cd $W && gofmt -l internal/analytics; go test -buildvcs=false -race ./internal/analytics/ > $S/t8.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL)|--- FAIL' $S/t8.log
```
Expected: no `gofmt` output, `exit=0`, `ok  	github.com/inferplane/inferplane/internal/analytics`. `TestSinkLiveWritesAuditEvents` and `TestAuditSummaryTieOrderingIsDeterministic` (which call `IngestAudit`) still pass.

- [ ] **Step 5: Docs and module note**

In `docs/reference/data.md`, replace lines 242–245:

```markdown
unvalidated. The projection is best-effort per record: each row has its own
savepoint, so a row the store refuses is skipped and logged by record ID and
store code only, and never blocks the Usage rows or the Mode B checkpoint of
the same batch.
```

with:

```markdown
unvalidated. The projection is best-effort per record. Mode A boot replay gives
each row its own savepoint in the replay transaction, and the live sink writes
each row with its categories in one transaction; a row the store refuses is
skipped and logged by record ID and store code only, never blocks the Usage
rows, and is offered again by the next boot replay. Mode B gives each row its
own savepoint, so a refused row never blocks the Usage rows or the checkpoint
of the same batch.
```

In `docs/reference/data.ko.md`, replace lines 181–182:

```markdown
최선 노력입니다. 행마다 savepoint를 두므로 저장소가 거부한 행은 기록 ID와 저장소 코드만
로그에 남기고 건너뛰며, 같은 배치의 Usage 행이나 Mode B 체크포인트를 막지 않습니다.
```

with:

```markdown
최선 노력입니다. Mode A 부팅 재생은 재생 트랜잭션 안에서 행마다 savepoint를 두고, 실시간
싱크는 행과 카테고리를 한 트랜잭션으로 씁니다. 저장소가 거부한 행은 기록 ID와 저장소 코드만
로그에 남기고 건너뛰며, Usage 행을 막지 않고, 다음 부팅 재생에서 다시 시도됩니다. Mode B는
행마다 savepoint를 두므로 거부된 행이 같은 배치의 Usage 행이나 체크포인트를 막지 않습니다.
```

In `internal/CLAUDE.md`, replace line 99:

```markdown
- `analytics/` — derived usage read-model backing `GET /admin/logs`; `pgstore/` is the shared-Postgres backend (ADR-015). ADR-057 adds the console-only `audit_events` and `audit_event_categories` projection of terminal records (`auditevents.go`: `KindOf`, `ProjectAuditEvent`, `WaterfallOf`), with the shared `Dialect`/`Run*` query builders in `auditquery.go` used by both SQLite and Postgres and the `AuditEventQuerier` interface both stores implement. `events` and Usage numbers are unchanged. Console rows are best-effort per record: Mode A `Replay` and Mode B `ingestBatch` give each row its own savepoint, so a row the store refuses is skipped and logged by ID and store code (never the message, which can quote request bytes) and never blocks usage rows or the checkpoint; record free text passes `auditText` (NUL dropped, invalid UTF-8 replaced, 256-byte cap) because client model strings reach denial records unvalidated. `audit_events` also carries `gov_scope`/`gov_limit`/`gov_source` from the record's `governance` (ADR-057); `AuditEventsMigrationColumns` adds them, empty, to older tables (SQLite on open, Postgres once under migration marker 2), and `policy_ref` then names the governance rule, never a routing rule on a governance- or RBAC-coded denial.
```

with:

```markdown
- `analytics/` — derived usage read-model backing `GET /admin/logs`; `pgstore/` is the shared-Postgres backend (ADR-015). ADR-057 adds the console-only `audit_events` and `audit_event_categories` projection of terminal records (`auditevents.go`: `KindOf`, `ProjectAuditEvent`, `WaterfallOf`), with the shared `Dialect`/`Run*` query builders in `auditquery.go` used by both SQLite and Postgres and the `AuditEventQuerier` interface both stores implement. `events` and Usage numbers are unchanged. Console rows are best-effort per record: Mode A `Replay` gives each row its own savepoint and the live sink (`IngestAudit`) one transaction per row with its categories, and Mode B `ingestBatch` gives each row its own savepoint, so a row the store refuses is skipped and logged by ID and store code (never the message, which can quote request bytes) and never blocks usage rows or the checkpoint; record free text passes `auditText` (NUL dropped, invalid UTF-8 replaced, 256-byte cap) because client model strings reach denial records unvalidated. `audit_events` also carries `gov_scope`/`gov_limit`/`gov_source` from the record's `governance` (ADR-057); `AuditEventsMigrationColumns` adds them, empty, to older tables (SQLite on open, Postgres once under migration marker 2), and `policy_ref` then names the governance rule, never a routing rule on a governance- or RBAC-coded denial.
```

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run
cd $W && python3 - docs/reference/data.md docs/reference/data.ko.md <<'EOF'
import hashlib, re, sys
for en, ko in zip(sys.argv[1::2], sys.argv[2::2]):
    digest = hashlib.sha256(open(en, 'rb').read()).hexdigest()
    text = open(ko, encoding='utf-8').read()
    text, n = re.subn(r'(?m)^translation_source_sha256: [0-9a-f]{64}$', 'translation_source_sha256: ' + digest, text, count=1)
    assert n == 1, ko
    open(ko, 'w', encoding='utf-8').write(text)
    print(ko, digest[:12])
EOF
python3 scripts/check_docs_translations.py > $S/t8-docs.log 2>&1; echo "docs=$?"
```
Expected: one hash line, `docs=0`.

- [ ] **Step 6: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree
git -C $W add internal/analytics/auditevents_sqlite.go internal/analytics/ingest_audit_test.go docs/reference/data.md docs/reference/data.ko.md internal/CLAUDE.md
git -C $W commit -s -m "fix(analytics): commit live console rows atomically and log refusals (ADR-057)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: Mode B writes a tick's console rows in one savepoint and skips only data errors (items 2–3)

**Files:**
- Modify: `internal/analytics/pgstore/pgstore.go:494`, `:500-502` (collect rows), `:523-524` (insert after usage), `:543-569` (`projectAuditEvent` → `projectAuditEvents`, `insertAuditEvent`, `inSavepoint`, `dataError`, `consoleError`)
- Modify: `internal/analytics/pgstore/audit_isolation_test.go:47`, `:57` (the probe now raises a data error)
- Modify: `docs/reference/data.md:246-248`, `docs/reference/data.ko.md:184`, `internal/CLAUDE.md:99`
- Test: `internal/analytics/pgstore/console_batch_test.go` (Postgres)

**Interfaces:**
- Consumes: `analytics.ProjectAuditEvent`, `analytics.InsertAuditEvent`, `analytics.PostgresDialect`, `analytics.LogAuditSkip`, `sqlState` (existing); test helpers `newTestStore`, `rawPool`, `acquire`, `auditRec`, `auditStr`, `auditCount`, `checkpointOf` (`pgstore_test.go`, `audit_events_test.go`, `audit_isolation_test.go:11`).
- Produces: `projectAuditEvents(ctx context.Context, tx pgx.Tx, rows []analytics.AuditEventRow) error`, `insertAuditEvent(ctx context.Context, tx pgx.Tx, row analytics.AuditEventRow) error`, `inSavepoint(ctx context.Context, tx pgx.Tx, fn func(pgx.Tx) error) error`, `dataError(err error) bool`, `consoleError(err error) error`. `projectAuditEvent` is removed.

- [ ] **Step 1: Start Postgres (loopback only)**

```bash
S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run; mkdir -p $S
docker run -d --rm --name inferplane-audit-pg-fix -e POSTGRES_PASSWORD=audit-test -p 127.0.0.1:55432:5432 postgres:17-alpine > $S/pg.log 2>&1; echo "run=$?"
docker exec inferplane-audit-pg-fix sh -c 'for i in $(seq 1 60); do pg_isready -h 127.0.0.1 -U postgres -q && exit 0; sleep 1; done; exit 1'; echo "ready=$?"
```
Expected: `run=0`, `ready=0` (on `run=125`, `docker stop inferplane-audit-pg-fix` and repeat).

- [ ] **Step 2: Write the failing tests**

Create `internal/analytics/pgstore/console_batch_test.go`:

```go
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inferplane/inferplane/internal/audit"
)

// consoleBatch is n completed records with distinct ULIDs.
func consoleBatch(n int) []audit.Record {
	out := make([]audit.Record, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, auditRec(fmt.Sprintf("01JC00000000000000000007%02d", i), "request_completed", 200, nil))
	}
	return out
}

// A tick of 70 console rows holds at most one subtransaction. The checkpoint
// trigger pauses the open transaction so a second session can read the
// ingesting backend's subtransaction cache (pg_stat_get_backend_subxact, PG 16+).
func TestPgstoreConsoleBatchHoldsOneSubtransaction(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	cleanup := func() {
		_, _ = s.db.Exec(context.Background(), `DROP TRIGGER IF EXISTS audit_probe_pause ON checkpoints; DROP FUNCTION IF EXISTS audit_probe_pause()`)
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := s.db.Exec(ctx, `CREATE FUNCTION audit_probe_pause() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(2); RETURN NEW; END $$;
CREATE TRIGGER audit_probe_pause AFTER INSERT ON checkpoints FOR EACH ROW EXECUTE FUNCTION audit_probe_pause()`); err != nil {
		t.Fatal(err)
	}
	epoch := acquire(t, s)
	done := make(chan error, 1)
	go func() { done <- s.ingestBatch(ctx, "holder", epoch, consoleBatch(70), map[string]int64{"seg": 1}) }()
	probe := rawPool(t)
	var count int
	var overflowed bool
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := probe.QueryRow(ctx, `SELECT s.subxact_count, s.subxact_overflowed
			FROM pg_stat_get_backend_idset() AS b(id), LATERAL pg_stat_get_backend_subxact(b.id) AS s
			WHERE pg_stat_get_backend_pid(b.id) = (SELECT pid FROM pg_stat_activity
				WHERE datname = current_database() AND wait_event = 'PgSleep' AND query LIKE 'INSERT INTO checkpoints%' LIMIT 1)`).Scan(&count, &overflowed)
		if err == nil {
			break
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("ingest never paused in the checkpoint trigger")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if count > 1 || overflowed {
		t.Fatalf("one tick held %d subtransactions (overflowed=%v), want at most one", count, overflowed)
	}
	if n := auditCount(t, s, "audit_events"); n != 70 {
		t.Fatalf("console rows = %d, want 70", n)
	}
}

// A console failure that is not a data error aborts the tick: nothing
// commits, the checkpoint stays, the error names only the SQLSTATE, and the
// next tick ingests the same records.
func TestPgstoreConsoleTransientErrorAbortsTick(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	cleanup := func() {
		_, _ = s.db.Exec(context.Background(), `DROP TRIGGER IF EXISTS audit_probe_deadlock ON audit_events; DROP FUNCTION IF EXISTS audit_probe_deadlock()`)
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := s.db.Exec(ctx, `CREATE FUNCTION audit_probe_deadlock() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'probe %', NEW.model_requested USING ERRCODE = '40P01'; END $$;
CREATE TRIGGER audit_probe_deadlock BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION audit_probe_deadlock()`); err != nil {
		t.Fatal(err)
	}
	epoch := acquire(t, s)
	batch := []audit.Record{
		auditRec("01JC0000000000000000000081", "request_completed", 200, func(r *audit.Record) { r.Request.ModelRequested = "request-text-must-not-log" }),
		auditRec("01JC0000000000000000000082", "request_started", 429, func(r *audit.Record) { r.Outcome.Error = auditStr("key_rate_limited") }),
	}
	err := s.ingestBatch(ctx, "holder", epoch, batch, map[string]int64{"seg": 30})
	if err == nil || !strings.Contains(err.Error(), "SQLSTATE 40P01") || strings.Contains(err.Error(), "request-text-must-not-log") {
		t.Fatalf("transient console error = %v", err)
	}
	if n := auditCount(t, s, "events"); n != 0 {
		t.Fatalf("aborted tick committed %d usage rows", n)
	}
	if n := auditCount(t, s, "checkpoints"); n != 0 {
		t.Fatalf("aborted tick advanced %d checkpoints", n)
	}
	cleanup()
	if err := s.ingestBatch(ctx, "holder", epoch, batch, map[string]int64{"seg": 30}); err != nil {
		t.Fatalf("retry tick: %v", err)
	}
	if n := auditCount(t, s, "audit_events"); n != 2 || checkpointOf(t, s, "seg") != 30 {
		t.Fatalf("retry rows = %d checkpoint = %d", n, checkpointOf(t, s, "seg"))
	}
}
```

The existing isolation probe raises `P0001`, which is no longer skippable; make it a data error so it keeps testing the skip path:

In `internal/analytics/pgstore/audit_isolation_test.go`, replace line 47:

```go
// Any console insert failure rolls back to its savepoint; usage rows and the
```

with:

```go
// A console row Postgres rejects as data (SQLSTATE class 22) fails the batch
// savepoint; the row-by-row retry skips only that row, and usage rows and the
```

In `internal/analytics/pgstore/audit_isolation_test.go`, replace line 57:

```go
	if _, err := s.db.Exec(ctx, `CREATE FUNCTION audit_probe_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'probe'; END $$;
```

with:

```go
	if _, err := s.db.Exec(ctx, `CREATE FUNCTION audit_probe_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'probe' USING ERRCODE = '22023'; END $$;
```

- [ ] **Step 3: Run them and watch them fail**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run
cd $W && INFERPLANE_TEST_PG_DSN='postgres://postgres:audit-test@127.0.0.1:55432/postgres?sslmode=disable' go test -buildvcs=false ./internal/analytics/pgstore/ -run 'TestPgstoreConsole|TestPgstoreAuditProjectionFailureKeepsUsage' -v > $S/t9-red.log 2>&1; echo "exit=$?"; grep -E -- '--- (PASS|FAIL)|console_batch_test.go' $S/t9-red.log
```
Expected: `exit=1`; `console_batch_test.go:67: one tick held 64 subtransactions (overflowed=true), want at most one`; `console_batch_test.go:97: transient console error = <nil>`; `TestPgstoreAuditProjectionFailureKeepsUsage` passes.

- [ ] **Step 4: Implement**

In `internal/analytics/pgstore/pgstore.go`, replace line 494:

```go
	touched := map[rollupKey]struct{}{}
```

with:

```go
	touched := map[rollupKey]struct{}{}
	var rows []analytics.AuditEventRow
```

In `internal/analytics/pgstore/pgstore.go`, replace lines 500–502:

```go
			if err := projectAuditEvent(ctx, tx, row); err != nil {
				return err
			}
```

with:

```go
			rows = append(rows, row)
```

In `internal/analytics/pgstore/pgstore.go`, replace lines 523–524:

```go
	}
	for key := range touched {
```

with:

```go
	}
	if err := projectAuditEvents(ctx, tx, rows); err != nil {
		return err
	}
	for key := range touched {
```

In `internal/analytics/pgstore/pgstore.go`, replace lines 543–569:

```go
// projectAuditEvent stores one console row inside a savepoint of the ingest
// transaction. The console projection is best-effort per record: a failed
// insert rolls back to the savepoint, is logged by record ID and SQLSTATE only
// (Postgres messages can quote the offending bytes), and never blocks the
// usage rows or the checkpoint of the same batch. Only a failing savepoint
// statement (an unusable transaction) is returned.
func projectAuditEvent(ctx context.Context, tx pgx.Tx, row analytics.AuditEventRow) error {
	sp, err := tx.Begin(ctx) // pgx: SAVEPOINT inside an open transaction
	if err != nil {
		return fmt.Errorf("begin audit projection savepoint: %w", err)
	}
	err = analytics.InsertAuditEvent(analytics.PostgresDialect, func(q string, args ...any) error {
		_, err := sp.Exec(ctx, q, args...)
		return err
	}, row)
	if err == nil {
		if err := sp.Commit(ctx); err != nil { // RELEASE SAVEPOINT
			return fmt.Errorf("release audit projection savepoint: %w", err)
		}
		return nil
	}
	if rbErr := sp.Rollback(ctx); rbErr != nil { // ROLLBACK TO SAVEPOINT
		return fmt.Errorf("roll back audit projection savepoint: %w", rbErr)
	}
	analytics.LogAuditSkip(row.ID, sqlState(err))
	return nil
}
```

with:

```go
// projectAuditEvents stores a batch's console rows inside ONE savepoint of the
// ingest transaction, so a tick holds one subtransaction instead of one per
// row (Postgres caches 64 per backend; overflowing that cache slows snapshots
// for every session on the server, ADR-046 admission included). Only when the
// batch fails with a data error does it retry row by row, each row in its own
// savepoint. A row Postgres rejects as data (SQLSTATE class 22 or 23) is
// skipped and logged by record ID and SQLSTATE only; any other failure (a
// timeout, a deadlock, a lost connection) is returned, so the tick rolls back
// without advancing its checkpoint and the next tick retries the records.
func projectAuditEvents(ctx context.Context, tx pgx.Tx, rows []analytics.AuditEventRow) error {
	if len(rows) == 0 {
		return nil
	}
	err := inSavepoint(ctx, tx, func(sp pgx.Tx) error {
		for _, row := range rows {
			if err := insertAuditEvent(ctx, sp, row); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		return nil
	}
	if !dataError(err) {
		return consoleError(err)
	}
	for _, row := range rows {
		err := inSavepoint(ctx, tx, func(sp pgx.Tx) error { return insertAuditEvent(ctx, sp, row) })
		switch {
		case err == nil:
		case dataError(err):
			analytics.LogAuditSkip(row.ID, sqlState(err))
		default:
			return consoleError(err)
		}
	}
	return nil
}

func insertAuditEvent(ctx context.Context, tx pgx.Tx, row analytics.AuditEventRow) error {
	return analytics.InsertAuditEvent(analytics.PostgresDialect, func(q string, args ...any) error {
		_, err := tx.Exec(ctx, q, args...)
		return err
	}, row)
}

// inSavepoint runs fn inside a savepoint of tx; when fn fails, tx is rolled
// back to the savepoint and fn's error is returned.
func inSavepoint(ctx context.Context, tx pgx.Tx, fn func(pgx.Tx) error) error {
	sp, err := tx.Begin(ctx) // pgx: SAVEPOINT inside an open transaction
	if err != nil {
		return fmt.Errorf("begin audit projection savepoint: %w", err)
	}
	if err := fn(sp); err != nil {
		if rbErr := sp.Rollback(ctx); rbErr != nil { // ROLLBACK TO SAVEPOINT
			return fmt.Errorf("roll back audit projection savepoint: %w", rbErr)
		}
		return err
	}
	if err := sp.Commit(ctx); err != nil { // RELEASE SAVEPOINT
		return fmt.Errorf("release audit projection savepoint: %w", err)
	}
	return nil
}

// dataError reports Postgres rejecting the row's own data: SQLSTATE class 22
// (data exception) or 23 (integrity constraint violation). Retrying such a
// row cannot succeed; every other failure may.
func dataError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (strings.HasPrefix(pgErr.Code, "22") || strings.HasPrefix(pgErr.Code, "23"))
}

// consoleError names a failed console write by SQLSTATE only: the tick error
// is logged, and a Postgres message can quote the offending request bytes.
func consoleError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return fmt.Errorf("project audit console rows: SQLSTATE %s", pgErr.Code)
	}
	return fmt.Errorf("project audit console rows: %w", err)
}
```

- [ ] **Step 5: Run Mode B and watch it pass, then stop Postgres**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run
cd $W && gofmt -l internal/analytics/pgstore; INFERPLANE_TEST_PG_DSN='postgres://postgres:audit-test@127.0.0.1:55432/postgres?sslmode=disable' go test -buildvcs=false -race ./internal/analytics/... > $S/t9.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL)|--- FAIL' $S/t9.log; INFERPLANE_TEST_PG_DSN='postgres://postgres:audit-test@127.0.0.1:55432/postgres?sslmode=disable' go test -buildvcs=false ./internal/analytics/pgstore/ -v > $S/t9-v.log 2>&1; echo "skips=$(grep -c -- '--- SKIP' $S/t9-v.log)"; grep -E -- '--- PASS: TestPgstore(Console|AuditProjectionFailure|NULModel)' $S/t9-v.log
docker stop inferplane-audit-pg-fix > /dev/null; echo "left=$(docker ps -a --filter name=inferplane-audit-pg-fix -q | wc -l)"
```
Expected: no `gofmt` output, `exit=0`, `ok` for `internal/analytics` and `internal/analytics/pgstore`, `skips=0`, `--- PASS` for `TestPgstoreConsoleBatchHoldsOneSubtransaction`, `TestPgstoreConsoleTransientErrorAbortsTick`, `TestPgstoreAuditProjectionFailureKeepsUsage`, `TestPgstoreNULModelDenialKeepsUsage`, and `left=0`.

- [ ] **Step 6: Docs and module note**

In `docs/reference/data.md`, replace lines 246–248:

```markdown
rows, and is offered again by the next boot replay. Mode B gives each row its
own savepoint, so a refused row never blocks the Usage rows or the checkpoint
of the same batch.
```

with:

```markdown
rows, and is offered again by the next boot replay. Mode B writes a tick's rows
in one savepoint and, only when that fails with a data error (SQLSTATE class 22
or 23), retries them row by row, each in its own savepoint; a row refused that
way is skipped and logged the same way and never blocks the Usage rows or the
checkpoint. Any other Mode B failure (a timeout, a deadlock, a lost connection)
aborts the tick without advancing the checkpoint, so the next tick retries it;
the logged error names only the SQLSTATE.
```

In `docs/reference/data.ko.md`, replace line 184:

```markdown
행마다 savepoint를 두므로 거부된 행이 같은 배치의 Usage 행이나 체크포인트를 막지 않습니다.
```

with:

```markdown
한 틱의 행을 savepoint 하나로 쓰고, 데이터 오류(SQLSTATE 클래스 22 또는 23)로 실패할 때만
행마다 savepoint를 두어 다시 씁니다. 그렇게 거부된 행은 같은 방식으로 로그에 남기고 건너뛰며
Usage 행이나 체크포인트를 막지 않습니다. 그 밖의 Mode B 실패(시간 초과, 교착, 연결 끊김)는
체크포인트를 진행하지 않고 틱을 중단하므로 다음 틱이 다시 시도하며, 로그의 오류에는 SQLSTATE만
남습니다.
```

In `internal/CLAUDE.md`, replace line 99:

```markdown
- `analytics/` — derived usage read-model backing `GET /admin/logs`; `pgstore/` is the shared-Postgres backend (ADR-015). ADR-057 adds the console-only `audit_events` and `audit_event_categories` projection of terminal records (`auditevents.go`: `KindOf`, `ProjectAuditEvent`, `WaterfallOf`), with the shared `Dialect`/`Run*` query builders in `auditquery.go` used by both SQLite and Postgres and the `AuditEventQuerier` interface both stores implement. `events` and Usage numbers are unchanged. Console rows are best-effort per record: Mode A `Replay` gives each row its own savepoint and the live sink (`IngestAudit`) one transaction per row with its categories, and Mode B `ingestBatch` gives each row its own savepoint, so a row the store refuses is skipped and logged by ID and store code (never the message, which can quote request bytes) and never blocks usage rows or the checkpoint; record free text passes `auditText` (NUL dropped, invalid UTF-8 replaced, 256-byte cap) because client model strings reach denial records unvalidated. `audit_events` also carries `gov_scope`/`gov_limit`/`gov_source` from the record's `governance` (ADR-057); `AuditEventsMigrationColumns` adds them, empty, to older tables (SQLite on open, Postgres once under migration marker 2), and `policy_ref` then names the governance rule, never a routing rule on a governance- or RBAC-coded denial.
```

with:

```markdown
- `analytics/` — derived usage read-model backing `GET /admin/logs`; `pgstore/` is the shared-Postgres backend (ADR-015). ADR-057 adds the console-only `audit_events` and `audit_event_categories` projection of terminal records (`auditevents.go`: `KindOf`, `ProjectAuditEvent`, `WaterfallOf`), with the shared `Dialect`/`Run*` query builders in `auditquery.go` used by both SQLite and Postgres and the `AuditEventQuerier` interface both stores implement. `events` and Usage numbers are unchanged. Console rows are best-effort per record: Mode A `Replay` gives each row its own savepoint and the live sink (`IngestAudit`) one transaction per row with its categories; Mode B `ingestItems` writes a tick's rows in one savepoint (one subtransaction, far under Postgres's 64-entry per-backend cache) and retries row by row only after a data error (SQLSTATE class 22/23). A refused row is skipped and logged by ID and store code (never the message, which can quote request bytes) and never blocks usage rows or the checkpoint; any other Mode B failure aborts the tick, so its checkpoint does not advance; record free text passes `auditText` (NUL dropped, invalid UTF-8 replaced, 256-byte cap) because client model strings reach denial records unvalidated. `audit_events` also carries `gov_scope`/`gov_limit`/`gov_source` from the record's `governance` (ADR-057); `AuditEventsMigrationColumns` adds them, empty, to older tables (SQLite on open, Postgres once under migration marker 2), and `policy_ref` then names the governance rule, never a routing rule on a governance- or RBAC-coded denial.
```

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run
cd $W && python3 - docs/reference/data.md docs/reference/data.ko.md <<'EOF'
import hashlib, re, sys
for en, ko in zip(sys.argv[1::2], sys.argv[2::2]):
    digest = hashlib.sha256(open(en, 'rb').read()).hexdigest()
    text = open(ko, encoding='utf-8').read()
    text, n = re.subn(r'(?m)^translation_source_sha256: [0-9a-f]{64}$', 'translation_source_sha256: ' + digest, text, count=1)
    assert n == 1, ko
    open(ko, 'w', encoding='utf-8').write(text)
    print(ko, digest[:12])
EOF
python3 scripts/check_docs_translations.py > $S/t9-docs.log 2>&1; echo "docs=$?"
```
Expected: one hash line, `docs=0`.

- [ ] **Step 7: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree
git -C $W add internal/analytics/pgstore/pgstore.go internal/analytics/pgstore/console_batch_test.go internal/analytics/pgstore/audit_isolation_test.go docs/reference/data.md docs/reference/data.ko.md internal/CLAUDE.md
git -C $W commit -s -m "fix(pgstore): one savepoint per console tick, skip only data errors (ADR-057)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: The console shows the refusing limit; ADR-057 and the spec are amended

**Files:**
- Modify: `internal/server/adminui/static/audit.js:62` (after `auditScope`: `auditDenyScope`, `auditDenySource`), `:360` (trace denial section), `:591-593` (Denials table)
- Modify: `internal/server/adminui/static/i18n.js:483` (`audit.policyNotRecorded` and new `audit.source.*`/`audit.limit.*`), `:524` (`audit.scope.gateway`)
- Modify: `internal/server/adminui/testdata/audit_console.cjs:41`, `internal/server/adminui/testdata/audit_qa.py:48`, `:73`, `:224`, `:288`
- Modify: `internal/server/adminui/audit_console_test.go:49-50`
- Modify: `docs/decisions/ADR-057-audit-console-evidence.md:72`, `:81`, `:114`, `:140-141` (English), `:213`, `:221`, `:249`, `:272-273` (Korean); `docs/superpowers/specs/2026-10-29-audit-console-design.md:291-293`, `:444-448` (English), `:727-729`, `:867-871` (Korean)

**Interfaces:**
- Consumes: event fields `gov_scope`, `gov_limit`, `gov_source` and summary denial fields `scope`, `limit`, `source` (Task 7); existing `msg`, `auditScope`, `auditKV`, `auditCell`, `auditKeyLabel` (`audit.js`).
- Produces: `auditDenyScope(code, scope)`, `auditDenySource(policy, source, limit)` (browser globals); i18n keys `audit.source.{config,team_record,policy,lease,key,budget_store}`, `audit.limit.{rpm,tpm,tokens_per_day,tokens_per_month,budget_month,budget_day,budget,pricing}`, `audit.scope.gateway`.

- [ ] **Step 1: Write the failing checks**

In `internal/server/adminui/testdata/audit_console.cjs`, replace line 41:

```javascript
assert.equal(run('auditScope("sensitive_blocked")'), "route");
```

with:

```javascript
assert.equal(run('auditScope("sensitive_blocked")'), "route");
// Governance denials name their limit (ADR-057); a denial recorded before that evidence says so.
assert.equal(run('auditDenySource("team-rate#2·rpm", "policy", "rpm")'), "team-rate#2·rpm");
assert.equal(run('auditDenySource("", "key", "rpm")'), "key setting · requests/min");
assert.equal(run('auditDenySource("", "lease", "budget")'), "control-plane lease · budget");
assert.equal(run('auditDenySource("", "budget_store", "budget_month")'), "budget store full · monthly budget");
assert.equal(run('auditDenySource("", "", "")'), run('msg("audit.policyNotRecorded")'));
assert.equal(run('auditDenyScope("budget_authority_unavailable", "key")'), "key");
assert.equal(run('auditDenyScope("key_rate_limited", "")'), "key");
assert.equal(run('auditDenyScope("pricing_missing", "gateway")'), "gateway");
```

In `internal/server/adminui/testdata/audit_qa.py`, replace line 48:

```python
     'input_tokens': 0, 'output_tokens': 0, 'cost_micros': 0, 'total_ms': 6},
```

with:

```python
     'input_tokens': 0, 'output_tokens': 0, 'cost_micros': 0, 'total_ms': 6,
     'gov_scope': 'key', 'gov_limit': 'rpm', 'gov_source': 'key'},
```

In `internal/server/adminui/testdata/audit_qa.py`, replace line 73:

```python
           'denials': [{'code': 'key_rate_limited', 'team': 'engineering', 'key_id': KEY_A, 'count': 1, 'last_ts': '2026-10-29T10:36:22Z'}],
```

with:

```python
           # A policy-rule denial, a key-setting reservation refusal, and a denial recorded before governance evidence.
           'denials': [{'code': 'team_rate_limited', 'policy': 'team-rate#2·rpm', 'scope': 'team', 'limit': 'rpm', 'source': 'policy',
                        'team': 'engineering', 'key_id': KEY_A, 'count': 3, 'last_ts': '2026-10-29T10:38:00Z'},
                       {'code': 'budget_authority_unavailable', 'scope': 'key', 'limit': 'budget_day', 'source': 'key',
                        'team': 'engineering', 'key_id': KEY_A, 'count': 2, 'last_ts': '2026-10-29T10:37:00Z'},
                       {'code': 'key_rate_limited', 'team': 'engineering', 'key_id': KEY_A, 'count': 1, 'last_ts': '2026-10-29T10:36:22Z'}],
```

In `internal/server/adminui/testdata/audit_qa.py`, replace line 224:

```python
    assert page.locator('#audit-detail .audit-wf-row').count() == 1
```

with:

```python
    assert page.locator('#audit-detail .audit-wf-row').count() == 1
    # The denial section names the refusing limit (ADR-057).
    assert en('audit.source.key') + ' · ' + en('audit.limit.rpm') in detail, detail
```

In `internal/server/adminui/testdata/audit_qa.py`, replace line 288:

```python
    assert 'key_rate_limited' in text('#audit-deny-table')
```

with:

```python
    deny_table = text('#audit-deny-table')
    assert 'key_rate_limited' in deny_table
    assert 'team-rate#2·rpm' in deny_table, deny_table
    assert en('audit.source.key') + ' · ' + en('audit.limit.budget_day') in deny_table, deny_table
    assert en('audit.policyNotRecorded') in deny_table, deny_table
```

In `internal/server/adminui/audit_console_test.go`, replace lines 49–50:

```go
	for _, scope := range []string{"team", "key", "user", "route"} {
		keys["audit.scope."+scope] = true
```

with:

```go
	for _, scope := range []string{"team", "key", "user", "route", "gateway"} {
		keys["audit.scope."+scope] = true
	}
	// Governance sources and limits are looked up by value (ADR-057).
	for _, source := range []string{"config", "team_record", "policy", "lease", "key", "budget_store"} {
		keys["audit.source."+source] = true
	}
	for _, limit := range []string{"rpm", "tpm", "tokens_per_day", "tokens_per_month", "budget_month", "budget_day", "budget", "pricing"} {
		keys["audit.limit."+limit] = true
```

- [ ] **Step 2: Run them and watch them fail**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run; mkdir -p $S/audit-qa
cd $W/internal/server/adminui && node testdata/audit_console.cjs > $S/t10-node-red.log 2>&1; echo "node=$?"; grep -E 'auditDenySource|Error' $S/t10-node-red.log
cd $W && go test -buildvcs=false ./internal/server/adminui/ -run TestAuditI18nKeysHaveFourLanguages > $S/t10-go-red.log 2>&1; echo "go=$?"; grep -c 'missing en:' $S/t10-go-red.log
```
Expected: `node=1` with `ReferenceError: auditDenySource is not defined`; `go=1` with a positive count of `i18n key audit.source.… missing en:` / `audit.limit.…` / `audit.scope.gateway` lines.

- [ ] **Step 3: Implement**

In `internal/server/adminui/static/audit.js`, replace line 62:

```javascript
  return "route";
```

with:

```javascript
  return "route";
}
// A governance denial names its own scope (ADR-057); older rows fall back to the code.
function auditDenyScope(code, scope) { return scope || auditScope(code); }
// Where the refusing limit was set: the policy rule, else the governance source
// and limit kind, else "not recorded" (a denial recorded before that evidence).
function auditDenySource(policy, source, limit) {
  if (policy) return policy;
  if (source) return msg("audit.source." + source) + (limit ? " · " + msg("audit.limit." + limit) : "");
  return msg("audit.policyNotRecorded");
```

In `internal/server/adminui/static/audit.js`, replace line 360:

```javascript
    auditKV(p, "audit.policy", e.policy_ref || msg("audit.policyNotRecorded"));
```

with:

```javascript
    auditKV(p, "audit.policy", auditDenySource(e.policy_ref, e.gov_source, e.gov_limit));
```

In `internal/server/adminui/static/audit.js`, replace lines 591–593:

```javascript
    auditCell(tr, x.policy || msg("audit.policyNotRecorded"));
    const scope = auditScope(x.code);
    auditCell(tr, msg("audit.scope." + scope) + " " + (scope === "team" ? x.team : auditKeyLabel(x.key_id)));
```

with:

```javascript
    auditCell(tr, auditDenySource(x.policy, x.source, x.limit));
    const scope = auditDenyScope(x.code, x.scope);
    const subject = scope === "team" ? x.team : scope === "gateway" ? "" : auditKeyLabel(x.key_id);
    auditCell(tr, msg("audit.scope." + scope) + (subject ? " " + subject : ""));
```

In `internal/server/adminui/static/i18n.js`, replace line 483:

```javascript
  "audit.policyNotRecorded": { en: "not recorded for governance denials", ko: "거버넌스 거부에는 기록되지 않음", zh: "治理拒绝不记录策略", ja: "ガバナンス拒否では記録されない" },
```

with:

```javascript
  "audit.policyNotRecorded": { en: "not recorded (older record)", ko: "기록 안 됨(이전 기록)", zh: "未记录（旧记录）", ja: "未記録（古い記録）" },
  "audit.source.config": { en: "config file", ko: "설정 파일", zh: "配置文件", ja: "設定ファイル" },
  "audit.source.team_record": { en: "team record", ko: "팀 레코드", zh: "团队记录", ja: "チームレコード" },
  "audit.source.policy": { en: "policy", ko: "정책", zh: "策略", ja: "ポリシー" },
  "audit.source.lease": { en: "control-plane lease", ko: "control plane 리스", zh: "控制平面租约", ja: "コントロールプレーンのリース" },
  "audit.source.key": { en: "key setting", ko: "키 설정", zh: "密钥设置", ja: "キー設定" },
  "audit.source.budget_store": { en: "budget store full", ko: "예산 저장소 가득 참", zh: "预算存储已满", ja: "予算ストアが満杯" },
  "audit.limit.rpm": { en: "requests/min", ko: "분당 요청", zh: "每分钟请求", ja: "毎分リクエスト" },
  "audit.limit.tpm": { en: "tokens/min", ko: "분당 토큰", zh: "每分钟 token", ja: "毎分トークン" },
  "audit.limit.tokens_per_day": { en: "tokens/day", ko: "일 토큰", zh: "每日 token", ja: "日次トークン" },
  "audit.limit.tokens_per_month": { en: "tokens/month", ko: "월 토큰", zh: "每月 token", ja: "月次トークン" },
  "audit.limit.budget_month": { en: "monthly budget", ko: "월 예산", zh: "月预算", ja: "月次予算" },
  "audit.limit.budget_day": { en: "daily budget", ko: "일 예산", zh: "日预算", ja: "日次予算" },
  "audit.limit.budget": { en: "budget", ko: "예산", zh: "预算", ja: "予算" },
  "audit.limit.pricing": { en: "pricing.on_missing", ko: "pricing.on_missing", zh: "pricing.on_missing", ja: "pricing.on_missing" },
```

In `internal/server/adminui/static/i18n.js`, replace line 524:

```javascript
  "audit.scope.route": { en: "route", ko: "라우팅", zh: "路由", ja: "ルーティング" },
```

with:

```javascript
  "audit.scope.route": { en: "route", ko: "라우팅", zh: "路由", ja: "ルーティング" },
  "audit.scope.gateway": { en: "gateway", ko: "게이트웨이", zh: "网关", ja: "ゲートウェイ" },
```

- [ ] **Step 4: Run Node, Go and Playwright checks and watch them pass**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run; mkdir -p $S/audit-qa
cd $W/internal/server/adminui && for f in audit_console analytics_evidence routing_forms; do node testdata/$f.cjs > $S/t10-$f.log 2>&1; echo "$f=$?"; done
cd $W && go test -buildvcs=false -race ./internal/server/adminui/ > $S/t10-go.log 2>&1; echo "go=$?"; grep -E '^(ok|FAIL)' $S/t10-go.log
cd $W && python3 internal/server/adminui/testdata/audit_qa.py --screenshots $S/audit-qa > $S/t10-qa.log 2>&1; echo "qa=$?"; grep -E 'ok|Error|assert' $S/t10-qa.log
```
Expected: `audit_console=0`, `analytics_evidence=0`, `routing_forms=0`; `go=0` and `ok  	github.com/inferplane/inferplane/internal/server/adminui`; `qa=0` and `audit_qa.py: ok`. Look at `$S/audit-qa/audit-1280-light-denials.png`: the Denials table shows `team-rate#2·rpm`, `key setting · daily budget` and `not recorded (older record)`.

- [ ] **Step 5: Amend ADR-057 and the spec (both languages)**

In `docs/decisions/ADR-057-audit-console-evidence.md`, replace line 72:

```markdown
1. Audit records gain three append-only, `omitempty` fields after
```

with:

```markdown
1. Audit records gain four append-only, `omitempty` fields after
```

In `docs/decisions/ADR-057-audit-console-evidence.md`, replace line 81:

```markdown
   - Terminal records also carry `latency{total_ms, ttft_ms}` on every ingress.
```

with:

```markdown
   - Terminal records also carry `latency{total_ms, ttft_ms}` on every ingress.
   - `governance{scope, limit, source, policy{name, generation, rule}}` on a
     gateway denial record (amended 2026-10-29): the limit that refused the
     request and where it was set, which is a policy rule, the config file, a
     team record, a lease, the key's option or the budget store. Names only.
```

In `docs/decisions/ADR-057-audit-console-evidence.md`, replace line 114:

```markdown
   written to the audit chain.
```

with:

```markdown
   written to the audit chain. The console table also carries `gov_scope`,
   `gov_limit` and `gov_source`; a migration adds them, empty, to tables created
   before them.
```

In `docs/decisions/ADR-057-audit-console-evidence.md`, replace lines 140–141:

```markdown
- Governance denials still carry no policy name; the console shows their code
  and scope only.
```

with:

```markdown
- Governance denials recorded before the `governance` field have no source;
  the console says so. Policy and rule names, which are operator text, now
  enter the chain on every governance denial.
```

In `docs/decisions/ADR-057-audit-console-evidence.md`, replace line 213:

```markdown
1. 감사 기록 `guardrail_version` 뒤에 append-only, `omitempty` 필드 세 개를
```

with:

```markdown
1. 감사 기록 `guardrail_version` 뒤에 append-only, `omitempty` 필드 네 개를
```

In `docs/decisions/ADR-057-audit-console-evidence.md`, replace line 221:

```markdown
   - 요청의 마지막 기록에는 모든 ingress에서 `latency{total_ms, ttft_ms}`도 붙습니다.
```

with:

```markdown
   - 요청의 마지막 기록에는 모든 ingress에서 `latency{total_ms, ttft_ms}`도 붙습니다.
   - 게이트웨이 거부 기록의 `governance{scope, limit, source, policy{name, generation, rule}}`
     (2026-10-29 추가). 요청을 거부한 한도와 그 한도를 정한 곳(정책 규칙, 설정 파일, 팀
     레코드, 리스, 키 옵션, 예산 저장소)이며 이름만 담습니다.
```

In `docs/decisions/ADR-057-audit-console-evidence.md`, replace line 249:

```markdown
   감사 체인에는 쓰지 않습니다.
```

with:

```markdown
   감사 체인에는 쓰지 않습니다. 콘솔 표에는 `gov_scope`, `gov_limit`, `gov_source`도
   있으며, 이전에 만든 표에는 마이그레이션이 빈 값으로 추가합니다.
```

In `docs/decisions/ADR-057-audit-console-evidence.md`, replace lines 272–273:

```markdown
- 거버넌스 거부에는 여전히 정책 이름이 없습니다. 콘솔은 코드와 범위만
  보여줍니다.
```

with:

```markdown
- `governance` 필드 이전에 기록된 거버넌스 거부에는 출처가 없으며 콘솔이 그렇게
  표시합니다. 운영자가 정한 텍스트인 정책·규칙 이름이 이제 모든 거버넌스 거부마다
  체인에 들어갑니다.
```

In `docs/superpowers/specs/2026-10-29-audit-console-design.md`, replace lines 291–293:

```markdown
- Policy name and rule appear for routing denials (they are in
  `routing.policies`). Governance denials show the code and its scope (team,
  key, user) only, because the audit record has no policy name for them.
```

with:

```markdown
- Policy name and rule appear for routing denials (`routing.policies`) and for
  governance denials (`governance.policy`). A governance denial set by the
  config file, a team record, a lease, a key option or the budget store shows
  that source and the limit kind instead; one recorded before the `governance`
  field shows "not recorded".
```

In `docs/superpowers/specs/2026-10-29-audit-console-design.md`, replace lines 444–448:

```markdown

## Out of scope

- Sub-project B: OTel export dashboard, exporter and rule editing.
- Policy name and rule for governance denials (needs a new governance field).
```

with:

```markdown

## Completion amendments (2026-10-29)

The completion plan (`docs/superpowers/plans/2026-10-29-audit-console-completion.md`)
changed these points:

1. Governance denials record the refusing limit in a new append-only
   `governance{scope, limit, source, policy}` field. The console table gains
   `gov_scope`, `gov_limit` and `gov_source`, and the Denials "policy · rule"
   column and the trace denial section show the rule or the setting; denials
   recorded earlier show "not recorded (older record)".
2. Reservation refusals on Messages, Chat and Invoke carry
   `budget_authority_unavailable`, as Responses did, so the console files them
   as policy denials.
3. Mode A's live sink writes a row and its categories in one transaction and
   logs a refused row. Mode B writes a tick's rows in one savepoint, skips only
   rows Postgres rejects as data (SQLSTATE class 22 or 23), and aborts the tick
   on any other error without advancing its checkpoint.
4. A Responses legacy-mask failure always writes its `mask_failed` denial.
5. `examples/policies/pii-email-mask.yaml` is the masking rule for success
   criterion 4.

## Out of scope

- Sub-project B: OTel export dashboard, exporter and rule editing.
```

In `docs/superpowers/specs/2026-10-29-audit-console-design.md`, replace lines 727–729:

```markdown
- 정책 이름과 rule은 라우팅 거부에서만 보입니다(`routing.policies`에 있음).
  거버넌스 거부는 감사 기록에 정책 이름이 없으므로 코드와 범위(팀, 키, 사용자)만
  보여줍니다.
```

with:

```markdown
- 정책 이름과 rule은 라우팅 거부(`routing.policies`)와 거버넌스 거부
  (`governance.policy`)에서 보입니다. 설정 파일, 팀 레코드, 리스, 키 옵션, 예산 저장소가
  정한 거버넌스 거부는 그 출처와 한도 종류를 보여주고, `governance` 필드 이전에 기록된
  거부는 "기록 안 됨"으로 보여줍니다.
```

In `docs/superpowers/specs/2026-10-29-audit-console-design.md`, replace lines 867–871:

```markdown

## 범위 밖

- 하위 프로젝트 B: OTel 내보내기 대시보드, exporter·규칙 편집
- 거버넌스 거부의 정책 이름과 rule(새 거버넌스 필드가 필요함)
```

with:

```markdown

## 완료 단계에서 바뀐 점 (2026-10-29)

완료 계획(`docs/superpowers/plans/2026-10-29-audit-console-completion.md`)에서 다음을
바꿨습니다.

1. 거버넌스 거부는 새 append-only 필드 `governance{scope, limit, source, policy}`에
   거부한 한도를 기록합니다. 콘솔 표에 `gov_scope`, `gov_limit`, `gov_source`가 생기고,
   거부 이력의 "정책 · rule" 열과 trace 상세의 거부 항목이 규칙이나 설정을 보여줍니다.
   이전에 기록된 거부는 "기록 안 됨(이전 기록)"으로 보입니다.
2. Messages, Chat, Invoke의 예약 거부도 Responses처럼 `budget_authority_unavailable`을
   가지므로 콘솔이 정책 거부로 분류합니다.
3. Mode A 실시간 싱크는 행과 category를 한 트랜잭션으로 쓰고 거부된 행을 로그에 남깁니다.
   Mode B는 한 틱의 행을 savepoint 하나로 쓰고, Postgres가 데이터로 거부한 행(SQLSTATE
   클래스 22 또는 23)만 건너뛰며, 그 밖의 오류에는 체크포인트를 진행하지 않고 틱을
   중단합니다.
4. Responses의 legacy mask 실패는 항상 `mask_failed` 거부 기록을 남깁니다.
5. 성공 기준 4의 마스킹 규칙은 `examples/policies/pii-email-mask.yaml`입니다.

## 범위 밖

- 하위 프로젝트 B: OTel 내보내기 대시보드, exporter·규칙 편집
```

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run
cd $W && python3 scripts/check_docs_translations.py > $S/t10-docs.log 2>&1; echo "docs=$?"; grep -c 'Policy name and rule for governance denials' docs/superpowers/specs/2026-10-29-audit-console-design.md
```
Expected: `docs=0` (ADR index titles unchanged) and `0` (the out-of-scope line is gone).

- [ ] **Step 6: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree
git -C $W add internal/server/adminui/static/audit.js internal/server/adminui/static/i18n.js internal/server/adminui/testdata/audit_console.cjs internal/server/adminui/testdata/audit_qa.py internal/server/adminui/audit_console_test.go docs/decisions/ADR-057-audit-console-evidence.md docs/superpowers/specs/2026-10-29-audit-console-design.md
git -C $W commit -s -m "feat(console): show the refusing limit in Denials and trace detail (ADR-057)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: Responses audits every legacy-mask refusal (item 5)

**Files:**
- Create: `internal/server/responsesapi/mask.go`
- Modify: `internal/server/responsesapi/handler.go:142`, `:150-154` (legacy masking block; line numbers after Task 4)
- Test: `internal/server/responsesapi/mask_test.go`

**Interfaces:**
- Consumes: `router.RequestRedactor` (`internal/router/request_routing.go:106`), `responses.RequestToCanonical`, `sensitivity.NewRedactorWithMasker`; test helpers `newStreamFixture`, `evidenceCtx`, `(*streamFixture).close` (`internal/server/responsesapi/stream_failure_test.go:99-153`, `audit_evidence_test.go:22`).
- Produces: `legacyMask(ctx context.Context, r router.RequestRedactor, raw []byte) ([]byte, *schema.ChatRequest, error)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/server/responsesapi/mask_test.go`:

```go
package responsesapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/filter"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
)

type fixedRedactor struct {
	out []byte
	err error
}

func (f fixedRedactor) Redact(context.Context, string, []byte) ([]byte, error) { return f.out, f.err }

// An unreadable masked body is refused like a redaction error.
func TestLegacyMaskRefusesUnreadableResult(t *testing.T) {
	raw := []byte(`{"model":"premium","input":"hello"}`)
	if _, _, err := legacyMask(context.Background(), fixedRedactor{out: []byte(`{"model":`)}, raw); err == nil {
		t.Fatal("an unreadable masked body was accepted")
	}
	if _, _, err := legacyMask(context.Background(), fixedRedactor{err: errors.New("redaction refused")}, raw); err == nil {
		t.Fatal("a redaction error was accepted")
	}
	body, parsed, err := legacyMask(context.Background(), fixedRedactor{out: []byte(`{"model":"premium","input":"[x]"}`)}, raw)
	if err != nil || parsed == nil || parsed.Model != "premium" || string(body) != `{"model":"premium","input":"[x]"}` {
		t.Fatalf("masked body = %s parsed = %+v err = %v", body, parsed, err)
	}
}

// breakArgs rewrites encoded function arguments into text the redactor's
// reinspection cannot read, so legacy masking refuses the request.
type breakArgs struct{}

func (breakArgs) Name() string { return "break-args" }
func (breakArgs) Mask(text string) (string, int) {
	if strings.Contains(text, `{"k":`) {
		return "MASKED", 1
	}
	return text, 0
}

// A legacy mask refusal writes one terminal mask_failed denial and reaches no
// provider.
func TestResponsesLegacyMaskFailureWritesDenial(t *testing.T) {
	f := newStreamFixture(t)
	f.h.SetMasking(&filter.Masking{Global: true, Filter: breakArgs{}})
	body := `{"model":"premium","input":[{"type":"function_call","call_id":"c1","name":"f","arguments":"{\"k\":1}"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	req = req.WithContext(principal.With(evidenceCtx(), keystore.Principal{Team: "team", KeyID: "opaque-key", AllowedModels: []string{"*"}}))
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	f.close(t)
	var denials []audit.Record
	sc := bufio.NewScanner(&f.audit)
	for sc.Scan() {
		var r audit.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.Event == "request_started" && r.Outcome != nil {
			denials = append(denials, r)
		}
	}
	if rec.Code != 403 || len(denials) != 1 || f.first.attempts+f.fallback.attempts != 0 {
		t.Fatalf("status %d denials %d attempts %d", rec.Code, len(denials), f.first.attempts+f.fallback.attempts)
	}
	d := denials[0]
	if d.Outcome.Error == nil || *d.Outcome.Error != "mask_failed" || d.Request.Routing == nil || d.Request.Routing.Reason != "mask_failed" {
		t.Fatalf("denial = %+v routing = %+v", d.Outcome, d.Request.Routing)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run; mkdir -p $S
cd $W && go test -buildvcs=false ./internal/server/responsesapi/ -run 'TestLegacyMask|TestResponsesLegacyMaskFailure' > $S/t11-red.log 2>&1; echo "exit=$?"; grep -E 'FAIL|undefined' $S/t11-red.log
```
Expected: `exit=1`, `[build failed]`, `undefined: legacyMask`. (`TestResponsesLegacyMaskFailureWritesDenial` pins the reachable redaction-error branch, which already audits; the helper test pins the unreadable-result branch that had no record.)

- [ ] **Step 3: Implement**

Create `internal/server/responsesapi/mask.go`:

```go
package responsesapi

import (
	"context"

	"github.com/inferplane/inferplane/internal/responses"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/pkg/schema"
)

// legacyMask runs the legacy text filter through the complete redactor and
// re-reads the result. A redaction error and an unreadable result are the
// same refusal, so the caller writes one mask_failed denial for either.
func legacyMask(ctx context.Context, r router.RequestRedactor, raw []byte) ([]byte, *schema.ChatRequest, error) {
	sanitized, err := r.Redact(ctx, "responses", raw)
	if err != nil {
		return nil, nil, err
	}
	parsed, err := responses.RequestToCanonical(sanitized)
	if err != nil {
		return nil, nil, err
	}
	return sanitized, parsed, nil
}
```

In `internal/server/responsesapi/handler.go`, replace line 142:

```go
		sanitized, maskErr := sensitivity.NewRedactorWithMasker(h.mask.Filter).Redact(req.Context(), "responses", raw)
```

with:

```go
		sanitized, masked, maskErr := legacyMask(req.Context(), sensitivity.NewRedactorWithMasker(h.mask.Filter), raw)
```

In `internal/server/responsesapi/handler.go`, replace lines 150–154:

```go
		parsed, err = responses.RequestToCanonical(sanitized)
		if err != nil {
			writeError(w, 403, "permission_error", "request could not be completely masked")
			return
		}
```

with:

```go
		parsed = masked
```

- [ ] **Step 4: Run the package and watch it pass**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run
cd $W && gofmt -l internal/server/responsesapi; go vet -buildvcs=false ./internal/server/responsesapi/ > $S/t11-vet.log 2>&1; echo "vet=$?"; go test -buildvcs=false -race ./internal/server/responsesapi/ ./internal/server/ > $S/t11.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL)|--- FAIL' $S/t11.log
```
Expected: no `gofmt` output, `vet=0`, `exit=0`, `ok` for both packages (`TestResponsesLegacyFilterHandlesCleanAndProtectedRequests` in `internal/server` still passes).

- [ ] **Step 5: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree
git -C $W add internal/server/responsesapi/mask.go internal/server/responsesapi/mask_test.go internal/server/responsesapi/handler.go
git -C $W commit -s -m "fix(responses): audit every legacy mask refusal (ADR-057)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 12: A reusable email-masking example policy (item 6, offline part)

**Files:**
- Create: `examples/policies/pii-email-mask.yaml`
- Test: `internal/server/example_pii_mask_test.go`; existing `TestExamplePoliciesLoad` (`internal/policy/load_test.go:96-100`) now also loads the new file

**Interfaces:**
- Consumes: `policy.LoadPaths`, `sensitivity.NewRedactor`, `routingtest.New`, `policyIngresses`, `policyMux`, `policyDo`, `policyAudit` (`internal/server/policy_routing_test.go:38-72`).
- Produces: the example policy `pii-email-mask` (generation 1, rule `mask-detected`, team `pii-demo`).

- [ ] **Step 1: Write the failing test**

Create `internal/server/example_pii_mask_test.go`:

```go
package server

import (
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/sensitivity"
	"github.com/inferplane/inferplane/internal/server/routingtest"
)

// The shipped email-masking example masks an email on every ingress and the
// audit record carries category email, masked and the example's rule (the
// audit console's PII row, spec success criterion 4).
func TestExamplePIIMaskPolicyMasksEmail(t *testing.T) {
	policies, _, err := policy.LoadPaths("../../examples/policies/pii-email-mask.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, ingress := range policyIngresses {
		t.Run(ingress.name(), func(t *testing.T) {
			f := routingtest.New(t, nil)
			f.Policies = policies
			f.Router.SetRequestRedactor(sensitivity.NewRedactor())
			body := `{"model":"premium","max_tokens":16,"messages":[{"role":"user","content":"contact alice@example.com"}]}`
			if ingress.stream {
				body = strings.TrimSuffix(body, "}") + `,"stream":true}`
			}
			aud, buf := policyAudit(t)
			rec := policyDo(policyMux(f, aud, nil, nil, nil), ingress.path, body, true)
			aud.Close()
			if rec.Code != 200 || len(f.Public.Calls) != 1 || strings.Contains(f.Public.Calls[0].Body, "alice@example.com") {
				t.Fatalf("status %d calls %d", rec.Code, len(f.Public.Calls))
			}
			for _, want := range []string{`"categories":["email"]`, `"masked":true`, `"policies":[{"name":"pii-email-mask","generation":1,"rule":"mask-detected"}]`} {
				if !strings.Contains(buf.String(), want) {
					t.Fatalf("audit lacks %s: %s", want, buf)
				}
			}
			if strings.Contains(buf.String(), "alice@example.com") {
				t.Fatal("the email reached the audit record")
			}
		})
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run; mkdir -p $S
cd $W && go test -buildvcs=false ./internal/server/ -run TestExamplePIIMaskPolicyMasksEmail > $S/t12-red.log 2>&1; echo "exit=$?"; grep -E 'FAIL|no such file|example_pii_mask_test.go' $S/t12-red.log
```
Expected: `exit=1`, `example_pii_mask_test.go:18: policy path ../../examples/policies/pii-email-mask.yaml: stat ../../examples/policies/pii-email-mask.yaml: no such file or directory`.

- [ ] **Step 3: Add the example**

Create `examples/policies/pii-email-mask.yaml`:

```yaml
# Email masking example for the audit console's PII view (ADR-043, ADR-057).
# Requests from keys of team pii-demo are inspected before routing; detected
# values such as an email address are replaced with [REDACTED_PII] before any
# provider call, and the audit record carries routing.categories ["email"]
# with routing.masked true. A request that cannot be inspected is blocked.
# Other teams are unaffected when this directory is loaded
# ("policies": ["examples/policies/"]); load this file alone to try masking
# with your own routes, because demo.yaml names quick-start models.
apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata:
  name: pii-email-mask
  generation: 1
spec:
  subject:
    team: pii-demo
  rules:
  - name: mask-detected
    failurePolicy: FailClosed
    sensitiveData:
      onDetected: Mask
      onUninspectable: Block
```

- [ ] **Step 4: Run and watch it pass**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run
cd $W && go test -buildvcs=false -race ./internal/server/ ./internal/policy/ ./internal/config/ -run 'TestExamplePIIMaskPolicyMasksEmail|TestExamplePoliciesLoad|TestExampleConfigsLoad' -v > $S/t12.log 2>&1; echo "exit=$?"; grep -E -- '--- (PASS|FAIL): TestExample' $S/t12.log
```
Expected: `exit=0`; `--- PASS` for `TestExamplePIIMaskPolicyMasksEmail` (six ingress subtests), `TestExamplePoliciesLoad` and `TestExampleConfigsLoad`.

- [ ] **Step 5: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree
git -C $W add examples/policies/pii-email-mask.yaml internal/server/example_pii_mask_test.go
git -C $W commit -s -m "docs(examples): email masking policy for the audit console (ADR-057)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 13: Live PII verification on a test gateway (item 6, spec success criterion 4)

Verification only: nothing in the repository changes and nothing is committed. Uses `V2=/home/atomoh/.tmp/inferplane-audit-console-20261029/verify2`, the Bedrock routes of `/home/atomoh/.tmp/inferplane-audit-console-20261029/verify/config.json` (instance-role credentials, no secret refs), ports 18381/19391, one `global.anthropic.claude-haiku-5-5` call (well under one cent). The running `mayu.service` is not touched.

**Files:**
- Create (outside the repo): `$V2/mkconfig.py`, `$V2/pii_qa.py`, `$V2/config.json`, test stores and logs.

- [ ] **Step 1: Build the branch binary and write the test config**

Create `/home/atomoh/.tmp/inferplane-audit-console-20261029/verify2/mkconfig.py`:

```python
"""Write the PII verification gateway config: the Bedrock routes of the audit
console verify config, separate ports and stores, and the email masking policy."""
import json
import pathlib
import sys

out, src_path, policy, listen, admin = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3], sys.argv[4], sys.argv[5]
src = json.load(open(src_path))
# Keep providers without a secret reference (the Bedrock ones; the stub503 test provider needs one).
providers = {n: p for n, p in src['providers'].items() if 'api_key_ref' not in p}
models = {n: m for n, m in src['models'].items() if all(t['provider'] in providers for t in m['targets'])}
pricing = dict(src['pricing'], overrides={n: o for n, o in src['pricing']['overrides'].items() if n in providers})
cfg = {
    'server': {'listen': listen, 'admin_listen': admin,
               'admin_auth': {'token_refs': [{'file': str(out / 'admin.txt')}]}},
    'key_store': {'type': 'sqlite', 'path': str(out / 'keys.db')},
    'audit': {'failure_mode': 'buffer_then_block', 'buffer': {'path': str(out / 'audit.wal')},
              'sinks': [{'type': 'file', 'path': str(out / 'audit.jsonl')}]},
    'policies': [policy],
    'providers': providers, 'models': models, 'pricing': pricing,
}
(out / 'config.json').write_text(json.dumps(cfg, indent=2))
print('providers', len(providers), 'models', len(models), 'haiku', 'global.anthropic.claude-haiku-5-5' in models)
```

```bash
L=/home/atomoh/.tmp/inferplane-audit-console-20261029; W=$L/worktree; V2=$L/verify2; mkdir -p $V2
cd $W && CGO_ENABLED=0 go build -buildvcs=false -trimpath -o $V2/mayu ./cmd/mayu > $V2/build.log 2>&1; echo "build=$?"
umask 077; python3 -c 'import secrets;print(secrets.token_hex(24))' > $V2/admin.txt
python3 $V2/mkconfig.py $V2 $L/verify/config.json $W/examples/policies/pii-email-mask.yaml 127.0.0.1:18381 127.0.0.1:19391
$V2/mayu config validate --config $V2/config.json > $V2/validate.log 2>&1; echo "validate=$?"; cat $V2/validate.log
```
Expected: `build=0`, `providers 5 models 54 haiku True`, `validate=0`, `configuration valid`. (`config validate` checks the config; the policy document is parsed at serve boot in Step 3.)

- [ ] **Step 2: Create a key for team pii-demo and header files (never printed)**

```bash
V2=/home/atomoh/.tmp/inferplane-audit-console-20261029/verify2; umask 077
$V2/mayu keys create --team pii-demo --models '*' --store $V2/keys.db > $V2/key.out 2> $V2/key.err; echo "key=$?"
grep -o 'ik_[A-Za-z0-9_-]*' $V2/key.out | head -n1 > $V2/key.txt; test -s $V2/key.txt && echo key-saved
printf 'x-api-key: %s\n' "$(cat $V2/key.txt)" > $V2/key.hdr; printf 'Authorization: Bearer %s\n' "$(cat $V2/admin.txt)" > $V2/admin.hdr
```
Expected: `key=0`, `key-saved`.

- [ ] **Step 3: Start the test gateway (background) and wait for it**

Run in the background (`run_in_background`):
```bash
V2=/home/atomoh/.tmp/inferplane-audit-console-20261029/verify2; $V2/mayu serve --config $V2/config.json > $V2/mayu.log 2>&1
```
Then:
```bash
V2=/home/atomoh/.tmp/inferplane-audit-console-20261029/verify2
curl -sS --retry 30 --retry-connrefused --retry-delay 1 -o /dev/null -w 'healthz=%{http_code}\n' http://127.0.0.1:19391/healthz; cat $V2/mayu.log
```
Expected: `healthz=200` (health is on the admin listener) and `inferplane serving data=127.0.0.1:18381 admin=127.0.0.1:19391`.

- [ ] **Step 4: Send one request with an email through the Bedrock route**

```bash
V2=/home/atomoh/.tmp/inferplane-audit-console-20261029/verify2
curl -sS -o $V2/pii.json -w 'messages=%{http_code}\n' http://127.0.0.1:18381/v1/messages -H @$V2/key.hdr -H 'content-type: application/json' -H 'anthropic-version: 2023-06-01' \
  -d '{"model":"global.anthropic.claude-haiku-5-5","max_tokens":32,"messages":[{"role":"user","content":"Reply with the single word ok. My contact address is alice@example.com."}]}'
curl -fsS -H @$V2/admin.hdr 'http://127.0.0.1:19391/admin/audit/events?pii=true&limit=10' > $V2/pii-events.json
python3 - $V2/pii-events.json <<'EOF'
import json, sys
ev = json.load(open(sys.argv[1]))['events']
rows = [e for e in ev if 'email' in (e.get('categories') or [])]
print('pii rows', len(rows), [(e['kind'], e.get('masked', False), e.get('policy_ref'), e['status']) for e in rows])
EOF
grep -c 'alice@example.com' $V2/audit.jsonl $V2/mayu.log
```
Expected: `messages=200`; `pii rows 1 [('privacy_masked', True, 'pii-email-mask#1·mask-detected', 200)]`; both grep counts `0` (the address never reaches the audit file or the log).

- [ ] **Step 5: Screenshot the PII tab with Playwright**

Create `/home/atomoh/.tmp/inferplane-audit-console-20261029/verify2/pii_qa.py`:

```python
"""Open the test console's PII tab and screenshot the email row.
Run: python3 pii_qa.py <dir with admin.txt> <admin base URL> <screenshot path>"""
import sys
from pathlib import Path
from playwright.sync_api import sync_playwright

base, out = sys.argv[2], sys.argv[3]
token = (Path(sys.argv[1]) / 'admin.txt').read_text().strip()
errors = []
with sync_playwright() as p:
    browser = p.chromium.launch(headless=True)
    page = browser.new_page(viewport={'width': 1280, 'height': 1000}, color_scheme='light', locale='en-US')
    page.on('pageerror', lambda e: errors.append(str(e)))
    page.on('console', lambda m: errors.append(m.text) if m.type == 'error' and not m.location.get('url', '').endswith('/admin/auth/config') else None)
    page.goto(base + '/admin/ui/')
    page.locator('#token').fill(token)
    page.locator('#token-form button').click()
    page.locator('#shell').wait_for(state='visible')
    page.locator('button.nav[data-view="audit"]').click()
    page.locator('#audit-tab-pii').click()
    row = page.locator('#audit-pii-table tbody tr', has_text='email').first
    row.wait_for(state='visible')
    text = row.inner_text()
    print('pii row:', text.replace('\n', ' | '))
    print('masked pill:', 'PII masked' in text)
    page.screenshot(path=out, full_page=False)
    browser.close()
print('console errors', errors)
```

```bash
V2=/home/atomoh/.tmp/inferplane-audit-console-20261029/verify2
python3 $V2/pii_qa.py $V2 http://127.0.0.1:19391 $V2/audit-pii-live.png > $V2/pii_qa.log 2>&1; echo "qa=$?"; cat $V2/pii_qa.log
```
Expected: `qa=0`; a `pii row:` line containing `pii-demo`, `email`, `PII masked` and `pii-email-mask#1·mask-detected`; `masked pill: True`; `console errors []`. Open `$V2/audit-pii-live.png` and confirm the PII table row shows category `email` and handling "PII masked".

- [ ] **Step 6: Verify the chain, stop the gateway, delete the secrets**

```bash
V2=/home/atomoh/.tmp/inferplane-audit-console-20261029/verify2
$V2/mayu audit verify --file $V2/audit.jsonl > $V2/verify.out 2>&1; echo "verify=$?"; cat $V2/verify.out
pkill -f "$V2/mayu serve"; echo "stopped=$?"
rm -f $V2/key.txt $V2/key.out $V2/key.err $V2/admin.txt $V2/key.hdr $V2/admin.hdr; ls $V2
```
Expected: `verify=0`, `chain OK (2 records)` (the request's started and completed records), `stopped=0`, and no `key.*`, `admin.*` files listed. Record in the run ledger: `Task 13: results — PII row email / privacy_masked / pii-email-mask#1·mask-detected; screenshot $V2/audit-pii-live.png; chain OK`.

---

### Task 14: Repository gates

**Files:** none changed.

- [ ] **Step 1: Go gates**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run; mkdir -p $S
cd $W && go test -buildvcs=false ./... -race > $S/gate-test.log 2>&1; echo "test=$?"; grep -E '^(FAIL|--- FAIL)|DATA RACE' $S/gate-test.log
go vet -buildvcs=false ./... > $S/gate-vet.log 2>&1; echo "vet=$?"
gofmt -l . > $S/gate-fmt.log; echo "fmt-files=$(wc -l < $S/gate-fmt.log)"
```
Run the first command in the background if the foreground limit is reached (it takes several minutes; `internal/authority/local` alone runs about four and a half minutes under `-race`). Expected: `test=0` with no output from the grep, `vet=0`, `fmt-files=0`.

- [ ] **Step 2: Postgres-backed gates**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run
docker run -d --rm --name inferplane-audit-pg-fix -e POSTGRES_PASSWORD=audit-test -p 127.0.0.1:55432:5432 postgres:17-alpine > $S/pg.log 2>&1; echo "run=$?"
docker exec inferplane-audit-pg-fix sh -c 'for i in $(seq 1 60); do pg_isready -h 127.0.0.1 -U postgres -q && exit 0; sleep 1; done; exit 1'; echo "ready=$?"
cd $W && INFERPLANE_TEST_PG_DSN='postgres://postgres:audit-test@127.0.0.1:55432/postgres?sslmode=disable' go test -buildvcs=false -race ./internal/analytics/... ./internal/authority/pgstore/... -v > $S/gate-pg.log 2>&1; echo "pg=$?"; grep -E '^(ok|FAIL)|--- FAIL' $S/gate-pg.log; echo "skips=$(grep -c -- '--- SKIP' $S/gate-pg.log)"
docker stop inferplane-audit-pg-fix > /dev/null; echo "left=$(docker ps -a --filter name=inferplane-audit-pg-fix -q | wc -l)"
```
Expected: `run=0`, `ready=0`, `pg=0`, `ok` for `internal/analytics`, `internal/analytics/pgstore`, `internal/authority/pgstore`, `skips=0`, `left=0`.

- [ ] **Step 3: Harness, docs and console gates**

```bash
W=/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree; S=/home/atomoh/.tmp/inferplane-audit-console-20261029/completion-run; D=/home/atomoh/.tmp/inferplane-audit-console-20261029/docs-venv; mkdir -p $S/audit-qa-final
cd $W && bash tests/run-all.sh > $S/gate-harness.log 2>&1; echo "harness=$?"; grep -c 'All tests passed' $S/gate-harness.log
python3 scripts/check_docs_translations.py > $S/gate-docs.log 2>&1; echo "docs=$?"; cat $S/gate-docs.log
$D/bin/python -B -m unittest discover -s tests/docs > $S/gate-docs-unittest.log 2>&1; echo "docs-unittest=$?"
$D/bin/python scripts/build_performance_docs.py --check > $S/gate-perf-docs.log 2>&1; echo "perf-docs=$?"
$D/bin/python scripts/build_client_compat_docs.py --check > $S/gate-compat-docs.log 2>&1; echo "compat-docs=$?"
$D/bin/python -m mkdocs build --strict --site-dir $S/site > $S/gate-mkdocs.log 2>&1; echo "mkdocs=$?"
$D/bin/python scripts/check_docs_site.py $S/site > $S/gate-docs-site.log 2>&1; echo "docs-site=$?"; cat $S/gate-docs-site.log
python3 internal/server/adminui/testdata/audit_qa.py --screenshots $S/audit-qa-final > $S/gate-audit-qa.log 2>&1; echo "audit-qa=$?"; cat $S/gate-audit-qa.log
git -C $W status --short
```
Expected: `harness=0` and `1`; `docs=0` with `Validated 48 Korean/English document pairs and both ADR indexes.`; `docs-unittest=0`; `perf-docs=0`; `compat-docs=0`; `mkdocs=0`; `docs-site=0` with `Validated links and assets in 209 HTML pages.`; `audit-qa=0` with `audit_qa.py: ok`; `git status --short` prints nothing (every change is committed; `$S` is outside the worktree).

- [ ] **Step 4: Report**

Report to the controller: the twelve commit SHAs (`git -C $W log --oneline dd3bfbf..HEAD`), every gate value above, the Task 13 results, and that `mayu.service`, `/home/atomoh/inferplane-local` and the shared control plane were not touched. `AGENTS.md` needs no regeneration (root `CLAUDE.md` unchanged).

---

## Self-review notes (plan author)

- Item coverage: 1 → Tasks 1–7 and 10 (field, provenance, ingress, assembly, authorities, projection with migrations, console, ADR and spec); 2–3 → Task 9; 4 → Task 8; 5 → Task 11; 6 → Tasks 12–13; repository gates → Task 14. Rulings for request class, auto mode block samples and the "fast" stage are Design decision 21.
- Every code block above was compiled and run against this worktree with `go test -overlay` before the plan was written, including the Postgres tests on a loopback `postgres:17-alpine`, the Node and Playwright console checks on a mirror of `internal/server/adminui`, the docs gates on a full mirror of the repository, and the Task 13 commands on a throwaway gateway (a real Bedrock call; PII row and screenshot as expected).
- Interfaces: `audit.SetGovernance`, `audit.GovernanceRef`, `audit.PolicyRuleRef`, `policy.RuleRef`, `governance.LimitSource`/`TeamSources`/`UserSources`/`BudgetDenial`/`DenialGovernance`/`PricingDenial`, `analytics.AuditEventsMigrationColumns` and the `gov_*` fields keep one name and signature across all tasks.
