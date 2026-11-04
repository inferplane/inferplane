# Observability Console Implementation Plan (sub-project B)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give full administrators an **Observability** view in the mayu console that shows governed OTLP export status (exporters, per-signal records, queue bytes, drops, last error, governed decisions, sampling) and lets them narrow export and edit console-owned observability rules at runtime — dry run first, persisted, audited, and never able to redirect a record or widen content export beyond the bootstrap config.

**Architecture:** `internal/otelexport` gains per-process stats beside its Prometheus series, a narrowing-only runtime `Limits` overlay applied at decision time (`AuditTargets`, `ContentTarget`, `TraceTargets`, rechecked in `emit`) and a `Status` snapshot; `internal/tracing` reports per-exporter span counts through an optional `Observer`. Runtime limits persist in a mayu-owned JSON state file (`observability.console.state_file`); console rules are GovernancePolicy documents in a console-owned file (`observability.console.policy_file`) that must also be listed in `policies`, so the existing local file channel loads, validates (ceiling check included) and hot-reloads them, and the control plane stays the only policy source when configured. A new `internal/server/observabilityapi` package serves `GET /admin/observability` and `PUT /admin/observability/{limits,rules}`, mounted full-admin-only by a new `server.WithFullAdminRoutes` layer (the `WithEvaluations` pattern). Every applied edit appends an `admin_observability_changed` audit record whose new `request.change` field carries the target and before/after SHA-256 revisions. The console view lives in a new `static/observability.js`.

**Tech Stack:** Go 1.26.10 (`CGO_ENABLED=0`), `go.opentelemetry.io/otel` SDK v1.45 (already present), `sigs.k8s.io/yaml` v1.6.0 (already present), Prometheus client, dependency-free browser JS, Node standard library and Python Playwright for console checks.

**Spec:** No separate spec, by user instruction. This plan argues from: `/home/atomoh/.tmp/inferplane-remediation-20261029/inputs/program.md` (Plan 4 and the common constraints); the base branch's `docs/operations/observability.md#governed-otlp-export`, `docs/superpowers/plans/2026-10-27-governed-observability-export.md` (Decisions 1–8: local ceiling, central choice, FailOpen, per-document rejection, merge rules); the audit console design, which is on the base since the merge of main: `docs/superpowers/specs/2026-10-29-audit-console-design.md` (Status: sub-project B is "OTel export dashboard and exporter/rule editing"; Console: "sub-project B adds its own item"; Out of scope) and ADR-057 (`docs/decisions/ADR-057-audit-console-evidence.md`); ADR-001 (data-free console), ADR-004 (admin auth), ADR-008 (validate-then-swap UI writes), ADR-033 (local policy file channel and its 2 s Watch), ADR-034 (control plane, `policies` and `control_plane` mutually exclusive), and the control-plane import (`internal/controlplane/import.go`: dry-run-first with `expected_generation`, #125).

## Design decisions

1. Decision: The dashboard shows only state mayu already holds per process — each declared exporter from the bootstrap ceiling (endpoint, protocol, TLS, header *names*, data boundary, declared and effective signals, content cap, queue limit and currently queued bytes), per exporter × signal record counts by outcome, the last success and last error time with the fixed error text, governed decision counts per signal, every observability rule with its sampling after limits, and the legacy `otel` exporter read-only — why: the pipelines and the routing processor already compute all of it; no query backend is needed — cost if wrong: counters reset on restart and are per replica; history stays in Prometheus (documented).
2. Decision: Add one Prometheus family `inferplane_otel_export_decisions_total{signal,decision}` (decision ∈ `selected`, `no_rule`, `content_conflict`, `withheld_limit`; 12 series at most) and two outcome values `withheld_limit` and `sampled_out` on the existing `inferplane_otel_export_records_total{exporter,signal,outcome}`; no team, user or key label — why: "what was withheld and why" had no counter, and a content conflict was silent because `ObservabilityDecision` dropped it — cost if wrong: dashboards keyed on the old outcome set see two more values.
3. Decision: Console counters live in a `sync.Map` keyed by (exporter, signal, outcome) and are bumped by the same call that bumps the Prometheus series; the last error text is kept only there — why: reading Prometheus collectors back is awkward, and an error text must never become a label — cost if wrong: one extra atomic add per record.
4. Decision: Runtime-editable per declared exporter: `paused`, `signals` ⊆ declared signals, `max_content_bytes` ≤ the declared cap, `max_sample_percent` 1–100. Bootstrap-only: the exporter set, endpoint, protocol, TLS, headers, `data_boundary`, `queue_bytes` (sizes a channel allocated at boot), `service_name`, and the legacy `otel` block — why: these are the incident knobs, and anything that names a destination or a credential is the blast radius — cost if wrong: pausing legacy `otel` traces still needs a config edit and restart.
5. Decision: Limits withhold at decision time (`Set.AuditTargets`, `ContentTarget`, `TraceTargets`, and again in `emit`), never through `SetObservabilityCeiling` — why: a ceiling failure rejects a whole control-plane document including its budget rules and fails a local reload — cost if wrong: a rule naming a withheld signal is accepted and shown as "withheld by limit" instead of being refused.
6. Decision: Limits persist in `observability.console.state_file` (JSON, written by mayu with 0600 via temp file, fsync, rename, directory fsync) and load at boot; no key means limits are read-only; a file that does not parse (unknown field, other version, trailing data) refuses boot; entries the current bootstrap no longer supports are clamped to the narrower reading and logged (`Set.Clamp`) — why: a kill switch must survive a restart and must never fail open — cost if wrong: a damaged file blocks boot until an operator fixes or removes it; the error names the file.
7. Decision: Console rules persist as GovernancePolicy documents in `observability.console.policy_file`, which must also be an entry of `policies`; the console writes the file and calls `Store.Reload` — why: one loader, one validation path (ceiling check included), one Watch, one boot path; and the one-policy-source rule is inherited because config already rejects `policies` with `control_plane` — cost if wrong: deployments with read-only policy mounts must place the console file on a writable volume and list it.
8. Decision: With `control_plane`, console rules are read-only (status reason `control_plane`, PUT 409 `read_only`) while runtime limits stay editable — why: rules are policy and the control plane is the policy source; limits only narrow the local ceiling, which is local config by design (base-branch Decision 2) — cost if wrong: a local full admin can withhold export that central policy selected; the change is in the local audit chain and visible in the status, but not reported upstream.
9. Decision: Console-owned documents are named `console-…` (DNS-label tail, ≤ 63 characters), each with exactly one FailOpen observability rule named `export` and nothing else; a console file holding anything else is read-only (`foreign_documents`); operator files are never written — why: a console edit can never touch budgets, routing or privacy, and never clobbers a hand edit — cost if wrong: hand-editing the console file into another shape disables console rule editing until restored.
10. Decision: A console rule may add or change a content selection only when `observability.console.content_rules: true`; an unchanged content selection may be kept and any may be removed; content exporters still need `data_boundary: internal` and TLS (unless loopback) in bootstrap — why: copying prompts stays a bootstrap decision and the console cannot redirect them — cost if wrong: operators who want console-managed content must opt in explicitly.
11. Decision: Edit flow = PUT with `dry_run: true` (changes, effects or per-subject decisions, result revision; nothing written) → PUT with `dry_run: false` and `expected_revision` equal to the current revision (else 409 `revision_conflict`) → revalidate → persist → install (limits: `SetLimits`; rules: `Store.Reload`, restoring the previous bytes if it fails) → append `admin_observability_changed`; an apply that changes nothing writes and records nothing — why: the control-plane import (#125) established dry-run-first with an expected generation — cost if wrong: two admins editing at once get 409 and re-preview.
12. Decision: Revisions are SHA-256 of persisted bytes: limits = canonical bytes mayu writes (`otelexport.EncodeLimits` of normalized limits), rules = console file bytes — why: an auditor can hash the file and match the audit record, and a hand edit changes the revision so a stale apply is refused; a running mayu also refuses a limits apply when the on-disk file no longer matches the installed limits (409 `limits_file_changed`) — cost if wrong: formatting-only hand edits to the console file force a re-preview.
13. Decision: Change evidence is `audit.RequestRef.Change *ChangeRef` (`target`, `before`, `after`, `names`), appended at the end of `request` with `omitempty`; actor in `principal.user`/`auth_method` — why: admin events already describe their action in `RequestRef` (`configapi.emitEvent`, `internal/server/configapi/write.go:304`), while the end of `Record` holds ADR-057 evidence of terminal request records (`Attempts`, `Client`, `AutoMode`, `Governance`); the audit console indexes only terminal request records (`analytics.AuditObservable`, `internal/analytics/auditevents.go:37`; the analytics sink drops everything else, `internal/analytics/sink.go:59`), so an `admin_observability_changed` record never appears there and needs no console label or renderer (pinned in Task 6) — cost if wrong: tools looking for change evidence at the top level must look under `request`.
14. Decision: Edits are refused with 503 `audit_unavailable` while `aud.Ready()` is false — why: a change that cannot be recorded should not be made — cost if wrong: during an audit outage the console cannot flip the kill switch (stop mayu and edit the state file still works).
15. Decision: Every `/admin/observability*` route, reads included, is `AdminAuth` + `requireAdmin` (full admin; team-mapped admins get 403 and `admin_denied`), mounted by a new generic `server.WithFullAdminRoutes(admin, routes, …)` layered after `WithEvaluations`, so `AdminMux`'s positional signature (36 call sites at `caf55fe`) is unchanged; each route gets the same `AdminAuth` + `requireAdmin` pair the audit console's `/admin/audit/*` routes use inside `AdminMux`; `/admin/capabilities` gains a plain `observability_export` bool (readable by any authenticated admin, like every other capability) — why: the status names collector endpoints and policy subjects; existing capability rules expose only booleans; the audit console could mount inside `AdminMux` because it needs only the analytics querier `AdminMux` already takes, while the console needs the export set, the policy store and its files — cost if wrong: team-mapped admins cannot see export health.
16. Decision: `policy.Store.Reload` becomes serialized and records file mtimes *before* reading — why: the console now reloads while the 2 s Watch may be mid-read; without both, an older read could install last, or a write landing during a read could be recorded as loaded and never picked up — cost if wrong: none for existing callers; one mutex on a cold path.
17. Decision: The API is mounted even without an `observability` block and reports `configured: false` — why: the view can say what is missing (affordance card plus legacy `otel` line) instead of failing with 404 — cost if wrong: one extra full-admin route on gateways without export.
18. Decision: Console code goes into a new `static/observability.js` wired the way `audit.js` and `evaluation.js` are: `app.js` gains `observability: "nav.observability"` in `VIEWS` (after `audit`) and `observability: refreshObservabilityView` in `showView`'s refreshers literal. The nav button sits between **Audit** and **Compare models**, where the audit console mockup shows it. Every other insertion point follows the same reading order: the `index.html` section after `view-audit`, the script tag after `audit.js`, the `i18n.js` block (with `nav.observability`, like `nav.audit` opens the audit block) after the audit keys at the end of `MSG`, the `style.css` block after the audit block at the end of the file, `adminui.go`'s entry after `audit.js` (gofmt realigns that map), `Capabilities.ObservabilityExport` after the ADR-057 fields, `RequestRef.Change` at the end of `request`, and each docs section next to the related one (data and security after their ADR-057 sections, the API reference after its audit console section, the API layer reference after the observability rule schema). The three harnesses that load the shipped scripts and switch views — `testdata/routing_forms.cjs`, `testdata/analytics_evidence.cjs` and `testdata/browser_qa.py` — load or serve `observability.js` too — why: the audit console is in the base, so there is no later merge to optimize for; one convention for views in their own file and one reading order is what a reader of the current files expects — cost if wrong: a harness that loads `app.js` without `observability.js` and switches views throws `ReferenceError: refreshObservabilityView is not defined`; Task 11 updates the three that exist, and a new one must load the file like it loads `audit.js`.
19. Decision: Error bodies are `{"error": "<message>", "code": "<stable code>"}` (+ `reason`, `current_revision` when relevant) — why: the console's existing `api()` helper shows `error`, and `observability.js` maps `code` to translated text; the audit console's read-only routes answer `{"error": "<closed code>"}` only, but a refused edit needs its reason (which exporter, which field) — cost if wrong: the two consoles' error bodies differ (the stable code is `error` there, `code` here); clients reading only `error` still get a readable message.
20. Decision: No new module dependency — why: `sigs.k8s.io/yaml`, the OTel SDK and the Prometheus client are already in `go.sum`; everything else is the standard library — cost if wrong: n/a.

## Global Constraints

- Worktree `/home/atomoh/.tmp/inferplane-observability-console-20261029/worktree`, branch `feat/observability-console`. Base: `feat/governed-observability-export` at `caf55fe`, which merges origin/main `a8a71f6` (ADR-057 audit console, #132 and #133). Every line anchor below was re-verified at `caf55fe`; when an earlier task in this plan has edited the same file, anchor on the quoted code, not the number. No push, no merge.
- Every Bash call starts with this preamble (the shell does not persist between calls):

  ```bash
  S=/home/atomoh/.tmp/inferplane-observability-console-20261029; W=$S/worktree
  export TMPDIR=/home/atomoh/.tmp GOTMPDIR=/home/atomoh/.tmp/gotmp; mkdir -p $GOTMPDIR; cd $W
  ```
- Go 1.26.10, module `github.com/inferplane/inferplane`; both binaries `CGO_ENABLED=0`; pure-Go dependencies only; no new module.
- Linked worktree: every `go build`/`go test`/`go vet` uses `-buildvcs=false`.
- Never pipe build/test output to `head`/`tail`; redirect to a log under `$S/` and grep it. Scratch files only under `$S/`, never `/tmp`. No `rm -rf`.
- Never run `git stash`, `git checkout`, `git restore`, `git reset`, `git clean` (refs/stash is shared across worktrees). To probe a mutation, copy files aside with `cp` or use `go test -overlay`.
- Commits: `git -C $W commit -s -m "<subject>" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"`. Stage only the files the task lists.
- Gates: `gofmt -l .` empty, `go vet -buildvcs=false ./...` clean, `go test -buildvcs=false ./... -race` green, `bash tests/run-all.sh`, `python3 scripts/check_docs_translations.py`.
- Secrets only via `env:`/`file:` refs. Header *values* never appear in status, diff, audit, logs, errors or metrics; only header names.
- `/metrics`: labels are exporter (config-bounded), signal, outcome, decision (fixed sets). No team, user, key id, policy or rule label.
- Audit record fields are append-only and `omitempty`; old audit files must still pass `mayu audit verify`.
- Authenticated `count_tokens` results stay HTTP 200; shared auth still 401s first (untouched by this plan).
- Cache invariant and provider isolation untouched (no provider or ingress change).
- A task that changes a layer updates `docs/reference/<layer>.md` (+ `.ko.md`) and the module `CLAUDE.md` in the same commit. The root `CLAUDE.md` changes once, in Task 13 Step 1 (two Project Structure lines). `AGENTS.md` is generated from it: never edit `AGENTS.md`; the controller regenerates it after this plan lands.
- Korean companions: Korean text in Korean, literal UTF-8; fenced code blocks identical to the English file. After editing an English doc, refresh its companion's source hash with:

  ```bash
  python3 -c 'import hashlib,re,sys,pathlib;s=pathlib.Path(sys.argv[1]);k=s.with_suffix(".ko.md");t=k.read_text(encoding="utf-8");h=hashlib.sha256(s.read_bytes()).hexdigest();k.write_text(re.sub(r"(?m)^translation_source_sha256: \S+$","translation_source_sha256: "+h,t,count=1),encoding="utf-8");print(h)' docs/<path>.md
  ```
- Console: dependency-free JS, textContent-only rendering, no `innerHTML`/`outerHTML`/`insertAdjacentHTML`/`eval(`, no browser storage (ADR-001), CSP unchanged, i18n keys in en/ko/zh/ja, one key per line in `i18n.js`.
- Live checks use a separate test `mayu` on 127.0.0.1:18381 (data) / 19391 (admin), an in-process OTLP receiver stub on 18390 and an upstream stub on 18399. Never touch `mayu.service`, `/home/atomoh/inferplane-local`, or the shared control plane. No external endpoint is contacted.

## Review Focus

1. **A console apply races the 2 s policy Watch** (or an operator saving a policy file): an older read installs last, or a write that lands mid-read is recorded as already loaded and never reloaded. Pinned by `TestReloadCapturesMtimesBeforeReading` and `TestConcurrentReloadKeepsTheLaterWrite` (Task 1).
2. **A pause does not survive a restart, or a damaged state file silently resumes export.** Pinned by `TestLoadLimitsRefusesADamagedFile` (Task 8), `TestGatewayConsoleLimitSurvivesRestart` and `TestGatewayConsoleDamagedStateFileRefusesBoot` (Task 10).
3. **A limit that narrows a signal makes a control-plane document fail the ceiling check**, dropping its budget rules with it. Pinned by `TestGatewayConsoleReadOnlyUnderControlPlane` (Task 10: pause, then `ApplyWire` a document with a budget rule and an observability rule → no rejection, budget applies, audit withheld).
4. **A rule apply while an operator policy file is broken leaves disk ≠ live**: the console file says one thing, the running set another. Pinned by `TestRulesApplyRestoresTheFileWhenReloadFails` (Task 9).
5. **A team-mapped admin reads collector endpoints and policy subjects, or edits export.** Pinned by `TestFullAdminRoutesGateEveryRoute` (Task 7: 401 without credential, 403 + `admin_denied` for a mapped member on every route including GET, 200 for full admin, unregistered methods fall through).

---

## File Structure

| File | Responsibility | Task |
|---|---|---|
| `internal/policy/observability.go` | `ObservabilityDecision.ContentConflict`; `MergeObservability` (explicit set) | 1 |
| `internal/policy/store.go` | `Store.CheckLocal`; serialized `Reload` recording mtimes before reading; `afterPolicyRead` test seam | 1 |
| `internal/policy/observability_console_test.go` (new) | Tests for the above | 1 |
| `internal/otelexport/export_test.go` | `TestAuditSinkNeverBlocksWriter` made structural (no wall-clock ratio) + `TestNonBlockingCheckCatchesABlockingSink` | 2 |
| `internal/metrics/metrics.go`, `otelexport_test.go`, `cardinality_test.go` | `inferplane_otel_export_decisions_total`; outcome help text | 2 |
| `internal/otelexport/stats.go` (new), `stats_test.go` (new) | Console counters, last success/error, decisions, `SignalStats`, `ObserveTrace` | 2 |
| `internal/otelexport/set.go`, `pipeline.go`, `records.go` | Route every count through stats; limits recheck in `emit`; lowered content cap | 2, 3 |
| `internal/otelexport/limits.go` (new), `limits_test.go` (new) | `Limit`, `Limits`, `Normalize`, `SetLimits`, `Clamp`, decision helpers, `EncodeLimits`/`DecodeLimits`/`Revision` | 3 |
| `internal/otelexport/status.go` (new), `status_test.go` (new) | `Status`/`ExporterStatus` snapshot (header names only) | 3 |
| `cmd/mayu/observability.go`, `cmd/mayu/gateway.go` | Decision closures through limits; observer; state file load; console wiring; capability; mount | 3, 4, 10 |
| `internal/tracing/route.go`, `tracing.go`, `observer_test.go` (new) | `Observer`, counting exporter, `drawSample` seam, `InitExporters(..., observe)` | 4 |
| `internal/config/observability.go`, `config.go`, `observability_console_test.go` (new) | `observability.console` block and validation | 5 |
| `internal/audit/record.go`, `change.go` (new), `change_test.go` (new) | `RequestRef.Change`, `ChangeRef` | 6 |
| `internal/analytics/observability_change_test.go` (new) | Pins that change records stay out of the audit console projection | 6 |
| `internal/server/fulladmin.go` (new), `fulladmin_test.go` (new) | `WithFullAdminRoutes` | 7 |
| `internal/server/observabilityapi/{observabilityapi,console,status}.go` (new) + `status_test.go` | Console options, rule model, `GET /admin/observability` | 7 |
| `internal/server/observabilityapi/{edit,limits}.go` (new) + `limits_test.go` | Shared edit helpers, `PUT …/limits`, `LoadLimits` | 8 |
| `internal/server/observabilityapi/rules.go` (new) + `rules_test.go` | `PUT …/rules` | 9 |
| `internal/server/configapi/capabilities.go`, `observability_caps_test.go` (new) | `observability_export` | 10 |
| `cmd/mayu/observability_console_test.go` (new) | Gateway acceptance | 10 |
| `internal/server/adminui/static/{index.html,app.js,i18n.js,style.css}`, `observability.js` (new), `adminui.go` | Observability view | 11 |
| `internal/server/adminui/observability_console_test.go` (new), `testdata/observability_console.cjs` (new) | Go wiring/i18n checks, Node helper checks | 11 |
| `internal/server/adminui/testdata/{routing_forms.cjs,analytics_evidence.cjs,browser_qa.py}` | Load or serve `observability.js` like `audit.js` | 11 |
| `internal/server/adminui/testdata/observability_qa.py` (new) | Playwright QA, screenshots 1280/390 | 12 |
| Docs: `docs/reference/{data,api,security,agent-llm}.md` (+ko), `docs/api-reference.md` (+ko), `docs/operations/observability.md` (+ko), `internal/CLAUDE.md`, `cmd/CLAUDE.md` | Folded into Tasks 1–11 | 1–11 |
| `CHANGELOG.md`, root `CLAUDE.md` | `Unreleased` entry; Project Structure lines (`AGENTS.md` regenerated by the controller) | 13 |

---

### Task 1: Policy merge, dry-run check and serialized reload

**Files:**
- Modify: `internal/policy/observability.go:39-46` (`ObservabilityDecision`), `:115-167` (`Store.Observability`)
- Modify: `internal/policy/store.go:3-14` (imports), `:97-113` (`Store` struct), `:288-325` (`Reload`); add `CheckLocal` after `Reload`
- Create: `internal/policy/observability_console_test.go`
- Modify: `internal/CLAUDE.md` (append a new section at the end, after line 192, the last line of `## Local monetary admission (ADR-054)`)

**Interfaces:**
- Consumes: existing `Subject.matches`, `checkEnforceable`, `validateIdentitySubject`, `enumerate`, `LoadPaths`, `Store.changed`.
- Produces:
  - `type ObservabilityDecision struct { Traces []TraceExport; Audit []string; Content string; ContentConflict bool }`
  - `func MergeObservability(policies []*Policy, team, user string) ObservabilityDecision`
  - `func (s *Store) CheckLocal(policies []*Policy) error`
  - test seam `var afterPolicyRead func()` (package-private)

- [ ] **Step 1: Write the failing tests**

`internal/policy/observability_console_test.go`:

```go
package policy

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
)

func TestMergeObservabilityMatchesStoreAndReportsConflict(t *testing.T) {
	s := NewEmptyStore()
	if rej := s.ApplyWire([]v1alpha1.GovernancePolicy{
		obsDoc("team", "eng", "", &v1alpha1.ObservabilityRule{Content: &v1alpha1.SignalExport{Exporter: "vault"}}),
		obsDoc("bob", "", "bob", &v1alpha1.ObservabilityRule{Content: &v1alpha1.SignalExport{Exporter: "other-vault"}}),
	}); len(rej) != 0 {
		t.Fatalf("rejections = %+v", rej)
	}
	bob := s.Observability("eng", "bob")
	if bob.Content != "" || !bob.ContentConflict {
		t.Fatalf("bob = %+v: disagreeing content exporters must disable content and say why", bob)
	}
	if alice := s.Observability("eng", "alice"); alice.Content != "vault" || alice.ContentConflict {
		t.Fatalf("alice = %+v", alice)
	}
	merged := MergeObservability(s.Policies(), "eng", "bob")
	if merged.Content != bob.Content || merged.ContentConflict != bob.ContentConflict {
		t.Fatalf("MergeObservability = %+v, store = %+v", merged, bob)
	}
	if none := MergeObservability(nil, "eng", ""); none.ContentConflict || len(none.Audit) != 0 || len(none.Traces) != 0 {
		t.Fatalf("empty set = %+v", none)
	}
}

func TestCheckLocalValidatesWithoutInstalling(t *testing.T) {
	body, err := json.Marshal(obsDoc("ok", "eng", "", &v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}}))
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(writePolicy(t, t.TempDir(), "ok.yaml", string(body)))
	if err != nil {
		t.Fatal(err)
	}
	s.SetObservabilityCeiling(func(exporter, signal string) error {
		if exporter == "corp" {
			return nil
		}
		return fmt.Errorf("exporter %q is not declared", exporter)
	})
	bad := obsDoc("bad", "eng", "", &v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "missing"}})
	p, err := FromV1Alpha1(&bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckLocal(append(s.Policies(), p)); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("CheckLocal = %v, want the ceiling refusal naming the exporter", err)
	}
	if err := s.CheckLocal(append(s.Policies(), s.Policies()[0])); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("CheckLocal duplicate = %v", err)
	}
	if got := s.Policies(); len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("CheckLocal installed something: %+v", got)
	}
}

// A write that lands while Reload is reading must not be recorded as loaded:
// Reload records mtimes before it reads, so the next Watch poll reloads it.
func TestReloadCapturesMtimesBeforeReading(t *testing.T) {
	first, _ := json.Marshal(obsDoc("first", "eng", "", &v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}}))
	second, _ := json.Marshal(obsDoc("second", "eng", "", &v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}}))
	path := writePolicy(t, t.TempDir(), "obs.yaml", string(first))
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { afterPolicyRead = nil })
	afterPolicyRead = func() {
		afterPolicyRead = nil
		if err := os.WriteFile(path, second, 0o600); err != nil {
			t.Error(err)
		}
		later := time.Now().Add(2 * time.Second)
		if err := os.Chtimes(path, later, later); err != nil {
			t.Error(err)
		}
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := s.Policies()[0].Name; got != "first" {
		t.Fatalf("the seam runs after the read; snapshot = %q", got)
	}
	if !s.changed() {
		t.Fatal("a write during the read was recorded as loaded; the Watch would never pick it up")
	}
}

// Two Reloads must not interleave: the console writes its file and reloads
// while the Watch may be mid-read of the old bytes. The later reload wins.
func TestConcurrentReloadKeepsTheLaterWrite(t *testing.T) {
	old, _ := json.Marshal(obsDoc("old", "eng", "", &v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}}))
	next, _ := json.Marshal(obsDoc("new", "eng", "", &v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}}))
	path := writePolicy(t, t.TempDir(), "obs.yaml", string(old))
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	read, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	t.Cleanup(func() { afterPolicyRead = nil })
	afterPolicyRead = func() {
		if calls.Add(1) == 1 { // only the first (stale) reader waits; later reloads pass straight through
			close(read)
			<-release
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = s.Reload() }() // reads the old bytes, then waits
	<-read
	if err := os.WriteFile(path, next, 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	go func() { defer wg.Done(); _ = s.Reload() }() // the console's reload after its write
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := s.Policies()[0].Name; got != "new" {
		t.Fatalf("snapshot = %q: the stale reload installed after the newer one", got)
	}
}
```

- [ ] **Step 2: Run the tests — expect compile failure**

```bash
go test -buildvcs=false ./internal/policy/ -run 'MergeObservability|CheckLocal|ReloadCaptures|ConcurrentReload' -race -count=1 > $S/t1.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|panic)|undefined' $S/t1.log
```
Expected: `exit=1` with `undefined: MergeObservability`, `ContentConflict`, `CheckLocal`, `afterPolicyRead`.

- [ ] **Step 3: Implement**

In `internal/policy/observability.go` replace the `ObservabilityDecision` type (lines 39–46) with:

```go
// ObservabilityDecision is the merged governed export for one request subject:
// trace targets (sorted by exporter, max percent per exporter), audit exporters
// (sorted, unique) and at most one content exporter. ContentConflict reports
// that matching rules named different content exporters, which is why Content
// is empty; the observability console counts that case separately from "no
// rule".
type ObservabilityDecision struct {
	Traces          []TraceExport
	Audit           []string
	Content         string
	ContentConflict bool
}
```

Replace `Store.Observability` (lines 115–167, from its doc comment to its closing brace) with:

```go
// Observability merges every observability rule whose subject matches the
// request. Traces and audit are unions (trace percent is the maximum per
// exporter). Content is exported only when all matching content selections
// name the same exporter: disagreement disables content for the request,
// because the safe answer for request bodies is not to copy them.
func (s *Store) Observability(team, user string) ObservabilityDecision {
	if s == nil {
		return ObservabilityDecision{}
	}
	snap := s.snap.Load()
	if snap == nil || !snap.observability {
		return ObservabilityDecision{}
	}
	return MergeObservability(snap.policies, team, user)
}

// MergeObservability is the Observability merge over an explicit policy set,
// so the console's dry run compares the decision before and after an edit
// with exactly the rules the request path uses.
func MergeObservability(policies []*Policy, team, user string) ObservabilityDecision {
	var d ObservabilityDecision
	traces := map[string]int{}
	audit := map[string]bool{}
	content := ""
	for _, p := range policies {
		if !p.Subject.matches(team, user) {
			continue
		}
		for _, r := range p.Rules {
			o := r.Observability
			if o == nil {
				continue
			}
			if o.Traces != nil && o.Traces.SamplePercent > traces[o.Traces.Exporter] {
				traces[o.Traces.Exporter] = o.Traces.SamplePercent
			}
			if o.Audit != "" {
				audit[o.Audit] = true
			}
			if o.Content != "" {
				if content != "" && content != o.Content {
					d.ContentConflict = true
				}
				content = o.Content
			}
		}
	}
	for name, pct := range traces {
		d.Traces = append(d.Traces, TraceExport{Exporter: name, SamplePercent: pct})
	}
	sort.Slice(d.Traces, func(i, j int) bool { return d.Traces[i].Exporter < d.Traces[j].Exporter })
	for name := range audit {
		d.Audit = append(d.Audit, name)
	}
	sort.Strings(d.Audit)
	if !d.ContentConflict {
		d.Content = content
	}
	return d
}
```

In `internal/policy/store.go` add `"sync"` to the import block (between `"os"` and `"sync/atomic"`). In the `Store` struct, after the `observabilityCeiling` field (line 112), add:

```go
	// reloadMu serializes Reload: the Watch and the observability console
	// both call it, and an older read must never install after a newer one.
	reloadMu sync.Mutex
```

Replace `Reload` (lines 288–325, doc comment included) with:

```go
// Reload re-reads every configured path and atomically swaps the snapshot.
// On error the previous snapshot (if any) stays in force. Reloads are
// serialized (the Watch and the observability console both call it), and
// every file's mtime is recorded BEFORE it is read: a write that lands during
// the read moves the mtime past the recorded one, so the next Watch poll
// reloads it instead of treating it as loaded.
func (s *Store) Reload() error {
	// Runtime guard, not just a doc comment (PR #50 review finding): on a
	// control-plane-fed store (NewEmptyStore) a stray generic Reload —
	// e.g. from a future SIGHUP handler — would otherwise silently WIPE
	// the distributed policy set with an empty file scan.
	if len(s.paths) == 0 {
		return errors.New("policy store has no file paths (control-plane-fed): Reload/Watch do not apply")
	}
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	files, err := enumerate(s.paths...)
	if err != nil {
		return err
	}
	mtimes := make(map[string]time.Time, len(files))
	for _, f := range files {
		if info, err := os.Stat(f); err == nil {
			mtimes[f] = info.ModTime()
		}
	}
	policies, _, err := LoadPaths(s.paths...)
	if err != nil {
		return err
	}
	if afterPolicyRead != nil {
		afterPolicyRead()
	}
	for _, p := range policies {
		if err := validateIdentitySubject(p.Subject.User, s.identityConfig); err != nil {
			return err
		}
		if err := checkEnforceable(p, s.routedAndPriced, s.observabilityCeiling, s.sharedEnforcement); err != nil {
			return err
		}
	}
	s.snap.Store(&snapshot{
		policies:      policies,
		files:         mtimes,
		teams:         mergeTeamLimits(policies),
		users:         mergeUserLimits(policies),
		observability: hasObservability(policies),
	})
	return nil
}

// afterPolicyRead is a test seam: it runs after Reload has read the policy
// files and before it validates and installs them.
var afterPolicyRead func()

// CheckLocal runs Reload's apply-time checks — unique names, identity
// subjects, enforceability and the observability ceiling — over policies
// without installing them. The observability console uses it for a dry run;
// Reload stays the authoritative check when the edit is applied.
func (s *Store) CheckLocal(policies []*Policy) error {
	seen := make(map[string]bool, len(policies))
	for _, p := range policies {
		if p.Name == "" {
			return errors.New("metadata.name is required")
		}
		if seen[p.Name] {
			return fmt.Errorf("duplicate policy name %q", p.Name)
		}
		seen[p.Name] = true
		if err := validateIdentitySubject(p.Subject.User, s.identityConfig); err != nil {
			return err
		}
		if err := checkEnforceable(p, s.routedAndPriced, s.observabilityCeiling, s.sharedEnforcement); err != nil {
			return err
		}
	}
	return nil
}
```

Append to `internal/CLAUDE.md` (after the last line, with one blank line before):

```markdown
## Observability console (sub-project B)

- `policy/`: `MergeObservability` is the request-path observability merge over an explicit set (the console's dry run uses it); `ObservabilityDecision.ContentConflict` marks content withheld because matching rules disagree. `Store.CheckLocal` runs Reload's apply-time checks without installing. `Store.Reload` is serialized and records file mtimes before reading, so a console write racing the 2 s Watch is never masked.
```

- [ ] **Step 4: Run the package tests**

```bash
go test -buildvcs=false ./internal/policy/ -race -count=1 > $S/t1.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|panic)' $S/t1.log
gofmt -l internal/policy
```
Expected: `exit=0`, `ok  	github.com/inferplane/inferplane/internal/policy`, no gofmt output.

- [ ] **Step 5: Commit**

```bash
git -C $W add internal/policy/observability.go internal/policy/store.go internal/policy/observability_console_test.go internal/CLAUDE.md
git -C $W commit -s -m "feat(policy): observability merge over a set, dry-run check, serialized reload" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 2: Export stats and the decision counter

**Files:**
- Modify: `internal/otelexport/export_test.go:365-398` (replace `TestAuditSinkNeverBlocksWriter`; Steps 1–3, own commit)
- Modify: `internal/metrics/metrics.go:45` (field), `:123-127` (otelExport help), `:129-132` (`MustRegister`), after `:220` (new method)
- Modify: `internal/metrics/otelexport_test.go` (append), `internal/metrics/cardinality_test.go:48` (two lines after `ObservePIIClassifier`)
- Create: `internal/otelexport/stats.go`, `internal/otelexport/stats_test.go`
- Modify: `internal/otelexport/set.go:63-73` (`Set`), `:83` and `:106` (`New`), `:186` (`emit`); `internal/otelexport/pipeline.go:16-17,54-80,95-111,197-199`; `internal/otelexport/records.go:100`
- Modify: `internal/CLAUDE.md` (append one bullet to the section from Task 1)

**Interfaces:**
- Consumes: `metrics.Metrics.ObserveOTelExport`; `audit.NewWriter`, `audit.Sink`.
- Produces:
  - test-only (package `otelexport`): `const nonBlockingDeadline`, `func writeAll(t *testing.T, sink audit.Sink, n int) <-chan error`, `type blockingSink`
  - `func (m *Metrics) ObserveOTelDecision(signal, decision string)`
  - `const otelexport.DecisionSelected = "selected"`, `DecisionNoRule = "no_rule"`, `DecisionConflict = "content_conflict"`, `DecisionWithheld = "withheld_limit"`
  - `type otelexport.SignalStats struct { Signal string; Outcomes map[string]uint64; LastSuccessAt, LastErrorAt, LastError string }` (JSON `signal`, `outcomes`, `last_success_at`, `last_error_at`, `last_error`)
  - `type otelexport.DecisionStats struct { Signal string; Decisions map[string]uint64 }` (JSON `signal`, `decisions`)
  - `func (s *Set) SignalStats(exporter, signal string) SignalStats`
  - `func (s *Set) ObserveTrace(exporter, outcome string, n int)` (nil-safe; accepts `sent`, `dropped_export_error`, `sampled_out`)
  - package-private: `stats`, `newStats`, `(*stats).count`, `.decision`, `.delivered`, `.failed`, `.signalStats`, `.decisions`; outcomes `outcomeWithheld = "withheld_limit"`, `outcomeSampledOut = "sampled_out"`

- [ ] **Step 1: Make the non-blocking audit test structural**

`TestAuditSinkNeverBlocksWriter` compared the writer's time against a discard-sink baseline (`blocked > 2*baseline+time.Second`) and failed once under host load (`blocked 4.81s vs baseline 1.80s`). The audit writer calls every sink synchronously on its one goroutine (`internal/audit/writer.go:142-210`), so "never blocks" can be shown without timing: hold the collector blocked, prove the writer drains and closes all N records while it is still blocked, then release it. The deadline only detects a hang.

In `internal/otelexport/export_test.go`, replace `TestAuditSinkNeverBlocksWriter` (lines 365–398, up to the blank line before `TestAllows`) with:

```go
// nonBlockingDeadline only detects a hang. The check below never compares
// timings, so a slow or loaded host cannot fail it; only a sink that waits for
// its collector can.
const nonBlockingDeadline = 30 * time.Second

// writeAll appends n records through an audit writer whose only sink is sink,
// then closes it. Close returns only after the writer goroutine has handed
// every record to the sink, so a value on the returned channel proves that all
// n writes completed. The channel yields Close's error.
func writeAll(t *testing.T, sink audit.Sink, n int) <-chan error {
	t.Helper()
	w, err := audit.NewWriter("node-1", t.TempDir()+"/wal", []audit.Sink{sink})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for i := 0; i < n; i++ {
			w.Append(audit.Record{SchemaVersion: 1, Event: "request_completed", ID: fmt.Sprint(i), Principal: audit.PrincipalRef{Team: "eng"}})
		}
		done <- w.Close()
	}()
	return done
}

// The audit writer hands each record to every sink on its single goroutine, so
// an OTLP copy that waited for its collector would stall the whole chain. The
// collector stays blocked until the writer has written and closed all n
// records; only then is it released.
func TestAuditSinkNeverBlocksWriter(t *testing.T) {
	const n = 400
	col := newCollector(t)
	col.block = make(chan struct{})
	m := metrics.New()
	s := newTestSet(t, m, Exporter{Name: "corp", Endpoint: col.endpoint(), Insecure: true, Signals: []string{"audit"}, QueueBytes: 64 << 10})
	done := writeAll(t, s.AuditSink(func(string, string) []string { return []string{"corp"} }), n)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(nonBlockingDeadline):
		t.Fatalf("the audit writer was still writing after %v with the collector blocked: the OTLP sink waits for its collector", nonBlockingDeadline)
	}
	if got := len(col.records()); got != 0 {
		t.Fatalf("the collector accepted %d records before it was released; the test did not hold it blocked", got)
	}
	if counter(m, "corp", "audit", "dropped_queue_full") == 0 {
		t.Fatal("the byte budget must drop once the blocked collector backs up")
	}
	close(col.block)
	shutdown(t, s)
}

// blockingSink waits on release for every record: the kind of sink
// TestAuditSinkNeverBlocksWriter exists to catch.
type blockingSink struct{ release chan struct{} }

func (b blockingSink) Write([]byte) error { <-b.release; return nil }
func (b blockingSink) Name() string       { return "blocking" }
func (b blockingSink) Required() bool     { return false }
func (b blockingSink) Close() error       { return nil }

// RED evidence for the check above, kept in the suite: the same drain check
// does not complete while a sink waits on something the test holds.
func TestNonBlockingCheckCatchesABlockingSink(t *testing.T) {
	sink := blockingSink{release: make(chan struct{})}
	done := writeAll(t, sink, 5)
	select {
	case <-done:
		t.Fatal("a blocking sink drained while it was held: the drain check would pass a sink that waits")
	case <-time.After(250 * time.Millisecond):
	}
	close(sink.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
```

The file's imports are unchanged (`io` is still used by the collector, `fmt` by `writeAll`).

- [ ] **Step 2: RED against a blocking sink, then GREEN**

RED, test-local sink: `TestNonBlockingCheckCatchesABlockingSink` drives the same `writeAll` check through `blockingSink` and must see it not complete. RED, real sink: an `-overlay` copy of `pipeline.go` whose `enqueue` waits until its queue has drained (what a sink that waits for its collector would do) must make `TestAuditSinkNeverBlocksWriter` fail at the deadline. No repository file changes.

```bash
mkdir -p $S/red-nonblocking
python3 - internal/otelexport/pipeline.go $S/red-nonblocking/pipeline.go <<'EOF'
import sys
src = open(sys.argv[1], encoding="utf-8").read()
old = "\tcase p.ch <- it:\n"
assert src.count(old) == 1, "anchor is not unique"
probe = "\t\tfor p.queued.Load() > 0 { // RED probe: wait for the collector, as a blocking sink would\n\t\t\ttime.Sleep(time.Millisecond)\n\t\t}\n"
open(sys.argv[2], "w", encoding="utf-8").write(src.replace(old, old + probe))
EOF
printf '{"Replace":{"%s":"%s"}}\n' "$W/internal/otelexport/pipeline.go" "$S/red-nonblocking/pipeline.go" > $S/red-nonblocking/overlay.json
go test -buildvcs=false -overlay $S/red-nonblocking/overlay.json ./internal/otelexport/ -run 'TestAuditSinkNeverBlocksWriter$' -count=1 -timeout 180s > $S/t2-red.log 2>&1; echo "red=$?"; grep -E '^(ok|FAIL|---)|still writing' $S/t2-red.log
go test -buildvcs=false ./internal/otelexport/ -run 'TestAuditSinkNeverBlocksWriter$|TestNonBlockingCheckCatchesABlockingSink$' -race -count=1 -v > $S/t2-green.log 2>&1; echo "green=$?"; grep -E '^(ok|FAIL|--- )' $S/t2-green.log
gofmt -l internal/otelexport
```
Expected: `red=1` with `--- FAIL: TestAuditSinkNeverBlocksWriter (30.00s)` and the `still writing after 30s` message; `green=0` with both tests `PASS` (the first takes a few seconds: 400 WAL fsyncs); no gofmt output. (Re-verification run at `caf55fe`: RED failed at 30.00 s, GREEN passed in 3.78 s and 0.28 s.)

- [ ] **Step 3: Commit**

```bash
git -C $W add internal/otelexport/export_test.go
git -C $W commit -s -m "test(otelexport): prove the audit sink never blocks without a timing ratio" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

- [ ] **Step 4: Write the failing tests**

Append to `internal/metrics/otelexport_test.go`:

```go
func TestObserveOTelDecision(t *testing.T) {
	m := New()
	m.ObserveOTelDecision("audit", "selected")
	m.ObserveOTelDecision("audit", "selected")
	m.ObserveOTelDecision("content", "content_conflict")
	if got := testutil.ToFloat64(m.otelDecisions.WithLabelValues("audit", "selected")); got != 2 {
		t.Fatalf("audit/selected = %v", got)
	}
	if got := testutil.ToFloat64(m.otelDecisions.WithLabelValues("content", "content_conflict")); got != 1 {
		t.Fatalf("content/content_conflict = %v", got)
	}
	var nilMetrics *Metrics
	nilMetrics.ObserveOTelDecision("audit", "selected")
}
```

In `internal/metrics/cardinality_test.go`, after line 48 (`m.ObservePIIClassifier("laya-ko", ...)`), add:

```go
	m.ObserveOTelExport("corp", "audit", "sent", 1)
	m.ObserveOTelDecision("audit", "selected")
```

Create `internal/otelexport/stats_test.go`:

```go
package otelexport

import (
	"net/http"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/metrics"
)

func decisionCounter(m *metrics.Metrics, signal, decision string) float64 {
	families, err := m.Registry().Gather()
	if err != nil {
		panic(err)
	}
	for _, f := range families {
		if f.GetName() != "inferplane_otel_export_decisions_total" {
			continue
		}
		for _, metric := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range metric.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["signal"] == signal && labels["decision"] == decision {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func TestStatsRecordFailureWithFixedText(t *testing.T) {
	fastRetry(t)
	col := newCollector(t, http.StatusBadRequest)
	m := metrics.New()
	s := newTestSet(t, m, Exporter{Name: "corp", Endpoint: col.endpoint(), Insecure: true, Signals: []string{"audit"}})
	rec, canon := auditRecord()
	if err := s.AuditSink(func(string, string) []string { return []string{"corp"} }).(audit.RecordSink).WriteRecord(rec, canon); err != nil {
		t.Fatal(err)
	}
	shutdown(t, s)
	got := s.SignalStats("corp", "audit")
	if got.Outcomes["dropped_export_error"] != 1 || got.Outcomes["sent"] != 0 || got.LastError != "collector status 400" ||
		got.LastErrorAt == "" || got.LastSuccessAt != "" {
		t.Fatalf("stats = %+v", got)
	}
	if len(got.Outcomes) != 6 {
		t.Fatalf("every outcome must be reported, zeros included: %v", got.Outcomes)
	}
	if counter(m, "corp", "audit", "dropped_export_error") != 1 {
		t.Fatal("the console count and the Prometheus series disagree")
	}
}

func TestStatsRecordLastSuccess(t *testing.T) {
	col := newCollector(t)
	s := newTestSet(t, metrics.New(), Exporter{Name: "corp", Endpoint: col.endpoint(), Insecure: true, Signals: []string{"audit"}})
	rec, canon := auditRecord()
	if err := s.AuditSink(func(string, string) []string { return []string{"corp"} }).(audit.RecordSink).WriteRecord(rec, canon); err != nil {
		t.Fatal(err)
	}
	shutdown(t, s)
	if got := s.SignalStats("corp", "audit"); got.Outcomes["sent"] != 1 || got.LastSuccessAt == "" || got.LastError != "" {
		t.Fatalf("stats = %+v", got)
	}
}

func TestObserveTraceKeepsAFixedOutcomeSet(t *testing.T) {
	m := metrics.New()
	s := newTestSet(t, m, Exporter{Name: "corp", Endpoint: "127.0.0.1:1", Insecure: true, Signals: []string{"traces"}})
	s.ObserveTrace("otel", "sent", 3)
	s.ObserveTrace("otel", "sampled_out", 2)
	s.ObserveTrace("corp", "dropped_export_error", 1)
	s.ObserveTrace("corp", "bogus", 7)
	if got := s.SignalStats("otel", "traces"); got.Outcomes["sent"] != 3 || got.Outcomes["sampled_out"] != 2 || got.LastSuccessAt == "" {
		t.Fatalf("legacy stats = %+v", got)
	}
	if got := s.SignalStats("corp", "traces"); got.Outcomes["dropped_export_error"] != 1 || got.LastError != "span export failed" {
		t.Fatalf("corp stats = %+v", got)
	}
	if counter(m, "corp", "traces", "bogus") != 0 {
		t.Fatal("an unknown outcome created a series")
	}
	var nilSet *Set
	nilSet.ObserveTrace("otel", "sent", 1)
	if got := nilSet.SignalStats("otel", "traces"); len(got.Outcomes) != 0 {
		t.Fatalf("nil set stats = %+v", got)
	}
}

func TestDecisionCountsMirrorPrometheus(t *testing.T) {
	m := metrics.New()
	s := newTestSet(t, m, Exporter{Name: "corp", Endpoint: "127.0.0.1:1", Insecure: true, Signals: []string{"traces"}})
	s.st.decision(SignalAudit, DecisionNoRule)
	s.st.decision(SignalAudit, DecisionNoRule)
	s.st.decision(SignalContent, DecisionConflict)
	rows := map[string]DecisionStats{}
	for _, d := range s.st.decisions() {
		rows[d.Signal] = d
	}
	if len(rows) != 3 || rows["audit"].Decisions["no_rule"] != 2 || rows["content"].Decisions["content_conflict"] != 1 ||
		rows["traces"].Decisions["selected"] != 0 || len(rows["traces"].Decisions) != 4 {
		t.Fatalf("decisions = %+v", rows)
	}
	if decisionCounter(m, "audit", "no_rule") != 2 {
		t.Fatal("the decision series does not match the console count")
	}
}
```

- [ ] **Step 5: Run — expect compile failure**

```bash
go test -buildvcs=false ./internal/metrics/ ./internal/otelexport/ -race -count=1 > $S/t2.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|panic)|undefined' $S/t2.log
```
Expected: `exit=1`, `undefined: …otelDecisions`, `ObserveOTelDecision`, `SignalStats`, `ObserveTrace`, `s.st`.

- [ ] **Step 6: Implement**

`internal/metrics/metrics.go`: after line 45 (`otelExport …`) add the field

```go
	otelDecisions      *prometheus.CounterVec // inferplane_otel_export_decisions_total
```

Replace the `otelExport` constructor (lines 123–127) with both counters:

```go
		otelExport: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "inferplane_otel_export_records_total",
			Help: "Governed OTLP export records by exporter, signal and outcome (sent, dropped_queue_full, dropped_oversize, dropped_export_error, withheld_limit, sampled_out)." +
				" Exporter names come from local config; no team, user or key label.",
		}, []string{"exporter", "signal", "outcome"}),
		otelDecisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "inferplane_otel_export_decisions_total",
			Help: "Governed OTLP export decisions by signal and decision (selected, no_rule, content_conflict, withheld_limit)." +
				" Audit counts records, content counts egress requests, traces count spans; no team, user or key label.",
		}, []string{"signal", "decision"}),
```

In `reg.MustRegister(...)` (lines 129–132) change the last line `m.otelExport, m.autoModeUnparsed)` to `m.otelExport, m.autoModeUnparsed, m.otelDecisions)`. After `ObserveOTelExport` (ends line 220, before ADR-057's `ObserveAutoModeUnparsed`) add:

```go
// ObserveOTelDecision counts one governed export decision. Both labels are
// fixed sets (signal, decision).
func (m *Metrics) ObserveOTelDecision(signal, decision string) {
	if m == nil {
		return
	}
	m.otelDecisions.WithLabelValues(signal, decision).Inc()
}
```

Create `internal/otelexport/stats.go`:

```go
package otelexport

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/inferplane/inferplane/internal/metrics"
)

// Outcomes the observability console adds to
// inferplane_otel_export_records_total (the delivery outcomes are in
// pipeline.go).
const (
	outcomeWithheld   = "withheld_limit" // a runtime limit withheld a policy selection
	outcomeSampledOut = "sampled_out"    // a trace target's sampling draw skipped the span
)

// Decision values of inferplane_otel_export_decisions_total: what governed
// export did with one audit record, one egress request or one span.
const (
	DecisionSelected = "selected"         // at least one exporter receives it
	DecisionNoRule   = "no_rule"          // no observability rule matched the subject
	DecisionConflict = "content_conflict" // matching rules named different content exporters
	DecisionWithheld = "withheld_limit"   // the policy selected exporters; runtime limits withheld all
)

var (
	allOutcomes  = []string{outcomeSent, outcomeQueueFull, outcomeOversize, outcomeExportError, outcomeWithheld, outcomeSampledOut}
	allDecisions = []string{DecisionSelected, DecisionNoRule, DecisionConflict, DecisionWithheld}
	allSignals   = []string{SignalTraces, SignalAudit, SignalContent}
)

// stats is the console's per-process view of export activity. Every count
// mirrors a Prometheus series; the last success and error times and the fixed
// error text exist only here. Keys are bounded by configuration (declared
// exporter names and the legacy "otel"), the three signals and the fixed
// outcome and decision sets, so the maps cannot grow with traffic.
type stats struct {
	m      *metrics.Metrics
	since  time.Time
	counts sync.Map // countKey → *atomic.Uint64
	last   sync.Map // lastKey → *lastState
	decide sync.Map // decideKey → *atomic.Uint64
}

type countKey struct{ exporter, signal, outcome string }

type lastKey struct{ exporter, signal string }

type decideKey struct{ signal, decision string }

type lastState struct {
	success atomic.Int64 // unix nanos of the last delivered batch
	failure atomic.Int64 // unix nanos of the last failed batch
	message atomic.Pointer[string]
}

func newStats(m *metrics.Metrics) *stats { return &stats{m: m, since: time.Now()} }

// loadCounter returns the counter for key, creating it on first use. The
// Load first keeps the request path allocation-free once a key exists.
func loadCounter(m *sync.Map, key any) *atomic.Uint64 {
	if v, ok := m.Load(key); ok {
		return v.(*atomic.Uint64)
	}
	v, _ := m.LoadOrStore(key, new(atomic.Uint64))
	return v.(*atomic.Uint64)
}

// count adds n records for one exporter, signal and outcome.
func (st *stats) count(exporter, signal, outcome string, n int) {
	if n <= 0 {
		return
	}
	st.m.ObserveOTelExport(exporter, signal, outcome, n)
	loadCounter(&st.counts, countKey{exporter, signal, outcome}).Add(uint64(n))
}

// decision counts one governed decision for signal.
func (st *stats) decision(signal, decision string) {
	st.m.ObserveOTelDecision(signal, decision)
	loadCounter(&st.decide, decideKey{signal, decision}).Add(1)
}

func (st *stats) lastOf(exporter, signal string) *lastState {
	key := lastKey{exporter, signal}
	if v, ok := st.last.Load(key); ok {
		return v.(*lastState)
	}
	v, _ := st.last.LoadOrStore(key, new(lastState))
	return v.(*lastState)
}

// delivered stamps a successful batch.
func (st *stats) delivered(exporter, signal string) {
	st.lastOf(exporter, signal).success.Store(time.Now().UnixNano())
}

// failed stamps a failed batch. message must be a fixed string or a status
// code (the client.go errors), never collector content.
func (st *stats) failed(exporter, signal, message string) {
	l := st.lastOf(exporter, signal)
	l.message.Store(&message)
	l.failure.Store(time.Now().UnixNano())
}

// SignalStats is one exporter × signal row of the console: records per
// outcome since the process started and the last delivery result.
type SignalStats struct {
	Signal        string            `json:"signal"`
	Outcomes      map[string]uint64 `json:"outcomes"`
	LastSuccessAt string            `json:"last_success_at,omitempty"`
	LastErrorAt   string            `json:"last_error_at,omitempty"`
	LastError     string            `json:"last_error,omitempty"`
}

// DecisionStats counts governed decisions for one signal.
type DecisionStats struct {
	Signal    string            `json:"signal"`
	Decisions map[string]uint64 `json:"decisions"`
}

func (st *stats) signalStats(exporter, signal string) SignalStats {
	out := SignalStats{Signal: signal, Outcomes: make(map[string]uint64, len(allOutcomes))}
	for _, o := range allOutcomes {
		out.Outcomes[o] = 0
		if v, ok := st.counts.Load(countKey{exporter, signal, o}); ok {
			out.Outcomes[o] = v.(*atomic.Uint64).Load()
		}
	}
	if v, ok := st.last.Load(lastKey{exporter, signal}); ok {
		l := v.(*lastState)
		if ns := l.success.Load(); ns > 0 {
			out.LastSuccessAt = stamp(ns)
		}
		if ns := l.failure.Load(); ns > 0 {
			out.LastErrorAt = stamp(ns)
			if msg := l.message.Load(); msg != nil {
				out.LastError = *msg
			}
		}
	}
	return out
}

func (st *stats) decisions() []DecisionStats {
	out := make([]DecisionStats, 0, len(allSignals))
	for _, sig := range allSignals {
		d := DecisionStats{Signal: sig, Decisions: make(map[string]uint64, len(allDecisions))}
		for _, k := range allDecisions {
			d.Decisions[k] = 0
			if v, ok := st.decide.Load(decideKey{sig, k}); ok {
				d.Decisions[k] = v.(*atomic.Uint64).Load()
			}
		}
		out = append(out, d)
	}
	return out
}

func stamp(ns int64) string { return time.Unix(0, ns).UTC().Format(time.RFC3339) }

// SignalStats reports one exporter × signal row. exporter may be the legacy
// "otel" name, whose span counts arrive through ObserveTrace. A nil Set
// reports an empty row.
func (s *Set) SignalStats(exporter, signal string) SignalStats {
	if s == nil {
		return SignalStats{Signal: signal, Outcomes: map[string]uint64{}}
	}
	return s.st.signalStats(exporter, signal)
}

// ObserveTrace records span export activity that internal/tracing reports for
// one exporter: "sent", "dropped_export_error" or "sampled_out". Any other
// outcome is ignored, so the label set stays fixed. nil-safe.
func (s *Set) ObserveTrace(exporter, outcome string, n int) {
	if s == nil {
		return
	}
	switch outcome {
	case outcomeSent:
		s.st.count(exporter, SignalTraces, outcome, n)
		s.st.delivered(exporter, SignalTraces)
	case outcomeExportError:
		s.st.count(exporter, SignalTraces, outcome, n)
		s.st.failed(exporter, SignalTraces, "span export failed")
	case outcomeSampledOut:
		s.st.count(exporter, SignalTraces, outcome, n)
	}
}
```

`internal/otelexport/set.go`: replace the `Set` struct (lines 63–68) with

```go
// Set is the declared exporter ceiling plus one log pipeline per exporter that
// allows audit or content.
type Set struct {
	exporters map[string]*declared
	st        *stats
}
```

In `New` change line 83 to `s := &Set{exporters: make(map[string]*declared, len(exporters)), st: newStats(m)}` and line 106 to `d.logs = newPipeline(e.Name, client, resource, e.QueueBytes, s.st)`. In `emit` change line 186 to `s.st.count(exporter, signal, outcomeExportError, 1)`.

`internal/otelexport/records.go` line 100: `s.st.count(exporter, SignalContent, outcomeOversize, 1)`.

`internal/otelexport/pipeline.go`: delete the `"github.com/inferplane/inferplane/internal/metrics"` import (line 17) and the blank line before it (line 16); in the `pipeline` struct replace field `m        *metrics.Metrics` with `st       *stats`; change `newPipeline` to

```go
func newPipeline(name string, client logClient, resource *resourcepb.Resource, limit int64, st *stats) *pipeline {
	ctx, cancel := context.WithCancel(context.Background())
	p := &pipeline{name: name, client: client, resource: resource, st: st, limit: limit,
		ch: make(chan item, queueCapacity(limit)), ctx: ctx, cancel: cancel,
		stop: make(chan struct{}), done: make(chan struct{})}
	go p.run()
	return p
}
```

replace each of the three `p.m.ObserveOTelExport(p.name, it.signal, outcomeQueueFull, 1)` in `enqueue` with `p.st.count(p.name, it.signal, outcomeQueueFull, 1)`, and replace the final loop of `export` (lines 197–199) with

```go
	for signal, n := range counts {
		p.st.count(p.name, signal, outcome, n)
		if err != nil {
			p.st.failed(p.name, signal, err.Error()) // status code or fixed text (client.go)
		} else {
			p.st.delivered(p.name, signal)
		}
	}
```

Append to the `internal/CLAUDE.md` section from Task 1:

```markdown
- `otelexport/` stats: every outcome count also lands in a per-process map behind `Set.SignalStats` with the last success/error time and the fixed error text (status code only, never collector content); `inferplane_otel_export_decisions_total{signal,decision}` counts governed decisions, and `withheld_limit`/`sampled_out` join the records outcomes. `ObserveTrace` takes span counts from `internal/tracing` (the legacy `otel` name included). Keys are config-bounded.
```

- [ ] **Step 7: Run**

```bash
go test -buildvcs=false ./internal/metrics/ ./internal/otelexport/ ./cmd/mayu/ -run 'OTel|Stats|ObserveTrace|Decision|Cardinality|NoMetricCarries|Observability' -race -count=1 > $S/t2.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|panic)' $S/t2.log
go test -buildvcs=false ./internal/otelexport/ -race -count=1 > $S/t2b.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL)' $S/t2b.log
gofmt -l internal/metrics internal/otelexport
```
Expected: both `exit=0`; `ok` for `internal/metrics`, `internal/otelexport`, `cmd/mayu`; no gofmt output.

- [ ] **Step 8: Commit**

```bash
git -C $W add internal/metrics/metrics.go internal/metrics/otelexport_test.go internal/metrics/cardinality_test.go internal/otelexport/stats.go internal/otelexport/stats_test.go internal/otelexport/set.go internal/otelexport/pipeline.go internal/otelexport/records.go internal/CLAUDE.md
git -C $W commit -s -m "feat(otelexport): per-process export stats and a governed decision counter" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 3: Runtime limits, decision helpers and the status snapshot

**Files:**
- Create: `internal/otelexport/limits.go`, `internal/otelexport/limits_test.go`, `internal/otelexport/status.go`, `internal/otelexport/status_test.go`
- Modify: `internal/otelexport/set.go:14-24` (imports), `:33-45` (`Exporter`), `:63-68` (`Set`), `:83` (`New`), `:180-191` (`emit`)
- Modify: `internal/otelexport/records.go:93-102` (`EmitContent` size check)
- Modify: `cmd/mayu/observability.go:49-52` (exporter literal), `:57-91` (`auditTargets`, `contentExport`, `traceDecider`), `:114` (`initTracing` call)
- Modify: `cmd/mayu/gateway.go:249` (`auditTargets` call)
- Modify: `internal/CLAUDE.md` (append one bullet)

**Interfaces:**
- Consumes: `stats` from Task 2; `policy.ObservabilityDecision.ContentConflict` from Task 1.
- Produces:
  - `type Limit struct { Paused bool; Signals []string; MaxContentBytes int64; MaxSamplePercent int }` (JSON `paused`, `signals`, `max_content_bytes`, `max_sample_percent`, all `omitempty`)
  - `type Limits map[string]Limit`
  - `func (s *Set) Normalize(l Limits) (Limits, error)`, `func (s *Set) SetLimits(l Limits) (Limits, error)`, `func (s *Set) Limits() Limits`, `func (s *Set) Clamp(l Limits) (Limits, []string)`
  - `func (s *Set) Exporting(l Limits, exporter, signal string) bool`, `func (s *Set) SamplePercent(l Limits, exporter string, percent int) int`
  - `type TraceSelection struct { Exporter string; SamplePercent int }`
  - `func (s *Set) AuditTargets(selected []string) []string`, `func (s *Set) ContentTarget(selected string, conflict bool) string`, `func (s *Set) TraceTargets(selected []TraceSelection) []TraceSelection`
  - `func EncodeLimits(l Limits) []byte`, `func DecodeLimits(b []byte) (Limits, error)`, `func Revision(b []byte) string`
  - `type ExporterStatus struct { Name, Endpoint, Protocol string; TLS bool; HeaderNames []string; DataBoundary string; Signals, EffectiveSignals []string; MaxContentBytes, EffectiveMaxContentBytes, QueueLimitBytes, QueuedBytes int64; Limit Limit; Stats []SignalStats }`
  - `type Status struct { ServiceName, CountersSince string; Exporters []ExporterStatus; Decisions []DecisionStats }`, `func (s *Set) Status() Status`
  - `Exporter.DataBoundary string`
  - cmd/mayu: `func auditTargets(set *otelexport.Set, pol *policy.Store) func(team, user string) []string`, `func traceDecider(set *otelexport.Set, pol *policy.Store, legacy *tracing.Target) tracing.Decider`

- [ ] **Step 1: Write the failing tests**

`internal/otelexport/limits_test.go`:

```go
package otelexport

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/metrics"
)

func limitSet(t *testing.T, m *metrics.Metrics) *Set {
	t.Helper()
	s := newTestSet(t, m,
		Exporter{Name: "corp", Endpoint: "127.0.0.1:1", Insecure: true, Signals: []string{"traces", "audit"}},
		Exporter{Name: "vault", Endpoint: "127.0.0.1:1", Insecure: true, Signals: []string{"content"}, DataBoundary: "internal", MaxContentBytes: 1 << 20, QueueBytes: 4 << 20})
	t.Cleanup(func() { shutdown(t, s) })
	return s
}

func TestNormalizeOnlyNarrows(t *testing.T) {
	s := limitSet(t, metrics.New())
	for _, tc := range []struct {
		name string
		l    Limits
		want string
	}{
		{"undeclared exporter", Limits{"other": {Paused: true}}, "not declared"},
		{"undeclared signal", Limits{"corp": {Signals: []string{"content"}}}, "not allowed"},
		{"empty signals", Limits{"corp": {Signals: []string{}}}, "use paused"},
		{"duplicate signal", Limits{"corp": {Signals: []string{"audit", "audit"}}}, "duplicate"},
		{"content cap raised", Limits{"vault": {MaxContentBytes: 2 << 20}}, "max_content_bytes"},
		{"negative content cap", Limits{"vault": {MaxContentBytes: -1}}, "max_content_bytes"},
		{"content cap without content", Limits{"corp": {MaxContentBytes: 10}}, "max_content_bytes"},
		{"sample percent above 100", Limits{"corp": {MaxSamplePercent: 101}}, "max_sample_percent"},
		{"sample percent without traces", Limits{"vault": {MaxSamplePercent: 50}}, "max_sample_percent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Normalize(tc.l); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Normalize(%v) = %v, want an error containing %q", tc.l, err, tc.want)
			}
		})
	}
	var nilSet *Set
	if _, err := nilSet.Normalize(Limits{"corp": {Paused: true}}); err == nil {
		t.Fatal("a data plane without exporters accepted a limit")
	}
}

func TestNormalizeCanonicalizes(t *testing.T) {
	s := limitSet(t, metrics.New())
	got, err := s.Normalize(Limits{
		"corp":  {Signals: []string{"traces", "audit"}, MaxSamplePercent: 100}, // the declared set and 100%: no limit
		"vault": {MaxContentBytes: 512},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["corp"]; ok || got["vault"].MaxContentBytes != 512 || len(got) != 1 {
		t.Fatalf("normalized = %+v", got)
	}
	a, _ := s.Normalize(Limits{"corp": {Signals: []string{"audit"}}, "vault": {MaxContentBytes: 1 << 20}})
	b, _ := s.Normalize(Limits{"corp": {Signals: []string{"audit"}, MaxSamplePercent: 100}})
	if Revision(EncodeLimits(a)) != Revision(EncodeLimits(b)) {
		t.Fatalf("equivalent limits must share a revision:\n%s\n%s", EncodeLimits(a), EncodeLimits(b))
	}
}

func TestLimitsWithholdAtDecisionTime(t *testing.T) {
	m := metrics.New()
	s := limitSet(t, m)
	if got := s.AuditTargets([]string{"corp"}); strings.Join(got, ",") != "corp" {
		t.Fatalf("audit before the limit = %v", got)
	}
	if _, err := s.SetLimits(Limits{"corp": {Paused: true}}); err != nil {
		t.Fatal(err)
	}
	if got := s.AuditTargets([]string{"corp"}); got != nil {
		t.Fatalf("a paused exporter still received audit: %v", got)
	}
	if got := s.TraceTargets([]TraceSelection{{Exporter: "corp", SamplePercent: 50}}); got != nil {
		t.Fatalf("a paused exporter still received traces: %v", got)
	}
	if got := s.AuditTargets(nil); got != nil {
		t.Fatal("no selection must export nothing")
	}
	if s.ContentTarget("vault", false) != "vault" || s.ContentTarget("vault", true) != "" || s.ContentTarget("", false) != "" {
		t.Fatal("content decision wrong")
	}
	rows := map[string]DecisionStats{}
	for _, d := range s.st.decisions() {
		rows[d.Signal] = d
	}
	if a := rows["audit"].Decisions; a["selected"] != 1 || a["withheld_limit"] != 1 || a["no_rule"] != 1 {
		t.Fatalf("audit decisions = %v", a)
	}
	if c := rows["content"].Decisions; c["selected"] != 1 || c["content_conflict"] != 1 || c["no_rule"] != 1 {
		t.Fatalf("content decisions = %v", c)
	}
	if rows["traces"].Decisions["withheld_limit"] != 1 {
		t.Fatalf("traces decisions = %v", rows["traces"].Decisions)
	}
	if got := s.SignalStats("corp", "audit").Outcomes["withheld_limit"]; got != 1 || counter(m, "corp", "audit", "withheld_limit") != 1 {
		t.Fatalf("withheld audit count = %d", got)
	}
	if _, err := s.SetLimits(Limits{}); err != nil {
		t.Fatal(err)
	}
	if got := s.AuditTargets([]string{"corp"}); strings.Join(got, ",") != "corp" {
		t.Fatalf("clearing the limit did not restore audit: %v", got)
	}
}

func TestTraceTargetsCapSamplePercent(t *testing.T) {
	s := limitSet(t, metrics.New())
	if _, err := s.SetLimits(Limits{"corp": {MaxSamplePercent: 10}}); err != nil {
		t.Fatal(err)
	}
	if got := s.TraceTargets([]TraceSelection{{Exporter: "corp", SamplePercent: 50}}); len(got) != 1 || got[0].SamplePercent != 10 {
		t.Fatalf("capped = %+v", got)
	}
	if got := s.TraceTargets([]TraceSelection{{Exporter: "corp", SamplePercent: 5}}); len(got) != 1 || got[0].SamplePercent != 5 {
		t.Fatalf("a lower percent must stay: %+v", got)
	}
	if _, err := s.SetLimits(Limits{"corp": {Signals: []string{"audit"}}}); err != nil {
		t.Fatal(err)
	}
	if got := s.TraceTargets([]TraceSelection{{Exporter: "corp", SamplePercent: 50}}); got != nil {
		t.Fatalf("traces dropped from the signal list still exported: %+v", got)
	}
	if got := s.AuditTargets([]string{"corp"}); len(got) != 1 {
		t.Fatalf("audit kept in the signal list was withheld: %v", got)
	}
}

func TestContentCapIsLoweredAtEmit(t *testing.T) {
	col := newCollector(t)
	m := metrics.New()
	s := newTestSet(t, m, Exporter{Name: "vault", Endpoint: col.endpoint(), Insecure: true, Signals: []string{"content"}, MaxContentBytes: 1 << 20})
	if _, err := s.SetLimits(Limits{"vault": {MaxContentBytes: 10}}); err != nil {
		t.Fatal(err)
	}
	s.EmitContent("vault", Content{RecordID: "big", Request: []byte(strings.Repeat("x", 20))})
	s.EmitContent("vault", Content{RecordID: "small", Request: []byte("ok")})
	shutdown(t, s)
	if n := len(col.records()); n != 1 {
		t.Fatalf("exported %d content records, want only the one under the lowered cap", n)
	}
	if counter(m, "vault", "content", "dropped_oversize") != 1 {
		t.Fatal("the oversize drop under the lowered cap was not counted")
	}
}

// A limit installed after the decision still stops the record in emit.
func TestEmitRechecksLimits(t *testing.T) {
	col := newCollector(t)
	m := metrics.New()
	s := newTestSet(t, m, Exporter{Name: "corp", Endpoint: col.endpoint(), Insecure: true, Signals: []string{"audit"}})
	sink := s.AuditSink(func(string, string) []string { return []string{"corp"} }).(audit.RecordSink)
	if _, err := s.SetLimits(Limits{"corp": {Paused: true}}); err != nil {
		t.Fatal(err)
	}
	rec, canon := auditRecord()
	if err := sink.WriteRecord(rec, canon); err != nil {
		t.Fatal(err)
	}
	shutdown(t, s)
	if n := len(col.records()); n != 0 || counter(m, "corp", "audit", "withheld_limit") != 1 {
		t.Fatalf("records = %d, withheld = %v", n, counter(m, "corp", "audit", "withheld_limit"))
	}
}

func TestLimitsEncodingIsCanonicalAndStrict(t *testing.T) {
	s := limitSet(t, metrics.New())
	l, err := s.Normalize(Limits{"corp": {Paused: true}, "vault": {MaxContentBytes: 4096}})
	if err != nil {
		t.Fatal(err)
	}
	b := EncodeLimits(l)
	back, err := DecodeLimits(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(EncodeLimits(back)) != string(b) || !strings.HasPrefix(Revision(b), "sha256:") {
		t.Fatalf("round trip changed the bytes:\n%s", b)
	}
	if got := string(EncodeLimits(nil)); got != "{\n  \"version\": 1,\n  \"exporters\": {}\n}\n" {
		t.Fatalf("empty limits = %q", got)
	}
	for _, bad := range []string{
		`{"version":1,"exporters":{"corp":{"pased":true}}}`,
		`{"version":2,"exporters":{}}`,
		`{"version":1,"exporters":{}} {}`,
		`not json`,
	} {
		if _, err := DecodeLimits([]byte(bad)); err == nil {
			t.Fatalf("DecodeLimits accepted %s", bad)
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil || decoded["version"] != float64(1) {
		t.Fatalf("encoded limits = %s (%v)", b, err)
	}
}

func TestClampNeverWidens(t *testing.T) {
	s := limitSet(t, metrics.New())
	got, notes := s.Clamp(Limits{
		"gone":  {Paused: true},
		"corp":  {Signals: []string{}, MaxContentBytes: 5},                                                // no signal kept; corp has no content
		"vault": {Signals: []string{"content", "traces"}, MaxContentBytes: 8 << 20, MaxSamplePercent: 20}, // traces undeclared; cap above bootstrap
	})
	if _, ok := got["gone"]; ok {
		t.Fatal("an undeclared exporter's limit survived the clamp")
	}
	if c := got["corp"]; !c.Paused || c.Signals != nil || c.MaxContentBytes != 0 {
		t.Fatalf("corp = %+v: a limit that keeps no declared signal must pause", c)
	}
	if v := got["vault"]; strings.Join(v.Signals, ",") != "content" || v.MaxContentBytes != 1<<20 || v.MaxSamplePercent != 0 || v.Paused {
		t.Fatalf("vault = %+v", v)
	}
	if len(notes) != 6 {
		t.Fatalf("notes = %q", notes)
	}
	if _, err := s.Normalize(got); err != nil {
		t.Fatalf("clamped limits must normalize: %v", err)
	}
}
```

`internal/otelexport/status_test.go`:

```go
package otelexport

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/metrics"
)

func TestStatusShowsDeclaredExportersWithoutHeaderValues(t *testing.T) {
	s := newTestSet(t, metrics.New(),
		Exporter{Name: "corp", Endpoint: "collector.internal:4318", Headers: map[string]string{"authorization": "Bearer status-secret"}, Signals: []string{"traces", "audit"}},
		Exporter{Name: "vault", Endpoint: "127.0.0.1:1", Protocol: "grpc", Insecure: true, Signals: []string{"content"}, DataBoundary: "internal", MaxContentBytes: 1 << 20, QueueBytes: 4 << 20})
	t.Cleanup(func() { shutdown(t, s) })
	if _, err := s.SetLimits(Limits{"corp": {Signals: []string{"audit"}}, "vault": {MaxContentBytes: 4096}}); err != nil {
		t.Fatal(err)
	}
	st := s.Status()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "status-secret") {
		t.Fatalf("status leaked a header value: %s", b)
	}
	if st.ServiceName != "mayu-test" || st.CountersSince == "" || len(st.Exporters) != 2 || len(st.Decisions) != 3 {
		t.Fatalf("status = %+v", st)
	}
	corp, vault := st.Exporters[0], st.Exporters[1]
	if corp.Name != "corp" || corp.Protocol != "http" || !corp.TLS || strings.Join(corp.HeaderNames, ",") != "authorization" ||
		strings.Join(corp.Signals, ",") != "audit,traces" || strings.Join(corp.EffectiveSignals, ",") != "audit" ||
		corp.QueueLimitBytes != defaultQueueBytes || corp.MaxContentBytes != 0 || len(corp.Stats) != 2 {
		t.Fatalf("corp = %+v", corp)
	}
	if vault.Protocol != "grpc" || vault.TLS || vault.DataBoundary != "internal" || vault.MaxContentBytes != 1<<20 ||
		vault.EffectiveMaxContentBytes != 4096 || vault.QueueLimitBytes != 4<<20 || vault.Limit.MaxContentBytes != 4096 {
		t.Fatalf("vault = %+v", vault)
	}
	var nilSet *Set
	if ns := nilSet.Status(); ns.Exporters == nil || len(ns.Exporters) != 0 || ns.CountersSince != "" {
		t.Fatalf("nil set status = %+v", ns)
	}
}
```

- [ ] **Step 2: Run — expect compile failure**

```bash
go test -buildvcs=false ./internal/otelexport/ -race -count=1 > $S/t3.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|panic)|undefined|unknown field' $S/t3.log
```
Expected: `exit=1`, `undefined: Limits`, `SetLimits`, `AuditTargets`, `Status`, unknown field `DataBoundary`.

- [ ] **Step 3: Implement**

Create `internal/otelexport/limits.go`:

```go
package otelexport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// Limit narrows one declared exporter at runtime (the observability console,
// sub-project B). It can only take away from the bootstrap ceiling: Paused
// stops the exporter, Signals keeps a subset of its declared signals, and
// MaxContentBytes and MaxSamplePercent only lower. Nothing here names an
// endpoint, protocol, TLS setting or header, so a limit can never redirect a
// record. The zero value narrows nothing.
type Limit struct {
	Paused           bool     `json:"paused,omitempty"`
	Signals          []string `json:"signals,omitempty"`            // nil = every declared signal
	MaxContentBytes  int64    `json:"max_content_bytes,omitempty"`  // 0 = the declared cap
	MaxSamplePercent int      `json:"max_sample_percent,omitempty"` // 0 = 100
}

// Limits is the runtime narrowing, keyed by declared exporter name.
type Limits map[string]Limit

// clone copies l; an empty non-nil signal list stays empty (it means "no
// signal", not "every signal").
func (l Limit) clone() Limit {
	if l.Signals != nil {
		l.Signals = append(make([]string, 0, len(l.Signals)), l.Signals...)
	}
	return l
}

func (l Limits) clone() Limits {
	out := make(Limits, len(l))
	for name, lim := range l {
		out[name] = lim.clone()
	}
	return out
}

func sortedNames(l Limits) []string {
	names := make([]string, 0, len(l))
	for name := range l {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Normalize validates l against the declared ceiling and returns its
// canonical form: signals sorted, a list equal to the declared set dropped,
// values equal to the bootstrap dropped and no-op entries removed, so two
// equivalent edits share one revision. Anything wider than the ceiling is an
// error, never clamped: an edit cannot widen.
func (s *Set) Normalize(l Limits) (Limits, error) {
	out := Limits{}
	if len(l) == 0 {
		return out, nil
	}
	if s == nil {
		return nil, errors.New("no observability exporters are declared on this data plane")
	}
	for _, name := range sortedNames(l) {
		lim := l[name].clone()
		d, ok := s.exporters[name]
		if !ok {
			return nil, fmt.Errorf("exporter %q is not declared in this data plane's observability ceiling", name)
		}
		if lim.Signals != nil {
			if len(lim.Signals) == 0 {
				return nil, fmt.Errorf("exporter %q: signals must keep at least one signal; use paused to stop the exporter", name)
			}
			seen := map[string]bool{}
			for _, sig := range lim.Signals {
				if !d.cfg.allows(sig) {
					return nil, fmt.Errorf("exporter %q: signal %q is not allowed by the bootstrap ceiling", name, sig)
				}
				if seen[sig] {
					return nil, fmt.Errorf("exporter %q: duplicate signal %q", name, sig)
				}
				seen[sig] = true
			}
			sort.Strings(lim.Signals)
			if len(lim.Signals) == len(d.cfg.Signals) {
				lim.Signals = nil // every declared signal: nothing narrowed
			}
		}
		if lim.MaxContentBytes != 0 {
			if !d.cfg.allows(SignalContent) {
				return nil, fmt.Errorf("exporter %q: max_content_bytes applies only to an exporter that allows content", name)
			}
			if lim.MaxContentBytes < 1 || lim.MaxContentBytes > d.cfg.MaxContentBytes {
				return nil, fmt.Errorf("exporter %q: max_content_bytes must be in [1, %d], the bootstrap cap", name, d.cfg.MaxContentBytes)
			}
			if lim.MaxContentBytes == d.cfg.MaxContentBytes {
				lim.MaxContentBytes = 0
			}
		}
		if lim.MaxSamplePercent != 0 {
			if !d.cfg.allows(SignalTraces) {
				return nil, fmt.Errorf("exporter %q: max_sample_percent applies only to an exporter that allows traces", name)
			}
			if lim.MaxSamplePercent < 1 || lim.MaxSamplePercent > 100 {
				return nil, fmt.Errorf("exporter %q: max_sample_percent must be in [1, 100]", name)
			}
			if lim.MaxSamplePercent == 100 {
				lim.MaxSamplePercent = 0
			}
		}
		if lim.Paused || lim.Signals != nil || lim.MaxContentBytes != 0 || lim.MaxSamplePercent != 0 {
			out[name] = lim
		}
	}
	return out, nil
}

// SetLimits validates and installs l and returns the installed form.
func (s *Set) SetLimits(l Limits) (Limits, error) {
	n, err := s.Normalize(l)
	if err != nil {
		return nil, err
	}
	if s != nil {
		s.limits.Store(&n)
	}
	return n.clone(), nil
}

// Limits returns a copy of the installed runtime limits (empty when none).
func (s *Set) Limits() Limits { return s.current().clone() }

// current is the installed limits map; callers must not modify it.
func (s *Set) current() Limits {
	if s == nil {
		return nil
	}
	if p := s.limits.Load(); p != nil {
		return *p
	}
	return nil
}

// Clamp intersects persisted limits with the current bootstrap ceiling before
// Normalize, choosing the narrower reading whenever the two disagree: an
// exporter no longer declared is dropped (its records cannot be sent anyway),
// a signal no longer declared is removed, a list left with no declared signal
// pauses the exporter, a content cap above the new bootstrap cap is lowered to
// it, and caps for signals the exporter no longer allows are dropped. It never
// widens what the file asked for. Each adjustment is returned as a note.
func (s *Set) Clamp(l Limits) (Limits, []string) {
	out := Limits{}
	var notes []string
	for _, name := range sortedNames(l) {
		lim := l[name].clone()
		var d *declared
		if s != nil {
			d = s.exporters[name]
		}
		if d == nil {
			notes = append(notes, fmt.Sprintf("exporter %q is no longer declared; its limit was dropped", name))
			continue
		}
		if lim.Signals != nil {
			kept := make([]string, 0, len(lim.Signals))
			for _, sig := range lim.Signals {
				if d.cfg.allows(sig) {
					kept = append(kept, sig)
				} else {
					notes = append(notes, fmt.Sprintf("exporter %q no longer declares %s; it stays off", name, sig))
				}
			}
			if len(kept) == 0 {
				kept, lim.Paused = nil, true
				notes = append(notes, fmt.Sprintf("exporter %q kept no declared signal; it is paused", name))
			}
			lim.Signals = kept
		}
		if lim.MaxContentBytes != 0 {
			switch {
			case !d.cfg.allows(SignalContent):
				lim.MaxContentBytes = 0
				notes = append(notes, fmt.Sprintf("exporter %q no longer allows content; max_content_bytes was dropped", name))
			case lim.MaxContentBytes > d.cfg.MaxContentBytes:
				lim.MaxContentBytes = d.cfg.MaxContentBytes
				notes = append(notes, fmt.Sprintf("exporter %q: max_content_bytes was lowered to the bootstrap cap %d", name, d.cfg.MaxContentBytes))
			}
		}
		if lim.MaxSamplePercent != 0 && !d.cfg.allows(SignalTraces) {
			lim.MaxSamplePercent = 0
			notes = append(notes, fmt.Sprintf("exporter %q no longer allows traces; max_sample_percent was dropped", name))
		}
		out[name] = lim
	}
	return out, notes
}

// Exporting reports whether exporter carries signal under l: declared,
// allowed by the bootstrap ceiling and not withheld by l. Pass s.Limits() for
// the live state, or a candidate for a dry run.
func (s *Set) Exporting(l Limits, exporter, signal string) bool {
	if s == nil {
		return false
	}
	return s.allowed(l, exporter, signal)
}

func (s *Set) allowed(l Limits, exporter, signal string) bool {
	d := s.exporters[exporter]
	if d == nil || !d.cfg.allows(signal) {
		return false
	}
	lim := l[exporter]
	if lim.Paused {
		return false
	}
	if lim.Signals == nil {
		return true
	}
	for _, sg := range lim.Signals {
		if sg == signal {
			return true
		}
	}
	return false
}

// SamplePercent lowers a policy trace percent to exporter's
// max_sample_percent under l.
func (s *Set) SamplePercent(l Limits, exporter string, percent int) int {
	if c := l[exporter].MaxSamplePercent; c > 0 && percent > c {
		return c
	}
	return percent
}

// contentLimit is exporter's per-record content cap under l.
func (s *Set) contentLimit(l Limits, exporter string) int64 {
	d := s.exporters[exporter]
	if d == nil {
		return 0
	}
	if c := l[exporter].MaxContentBytes; c > 0 && c < d.cfg.MaxContentBytes {
		return c
	}
	return d.cfg.MaxContentBytes
}

// TraceSelection is one policy trace target: an exporter and its sample
// percent (1..100).
type TraceSelection struct {
	Exporter      string
	SamplePercent int
}

// AuditTargets is the audit-copy half of the governed decision: the policy's
// selection minus exporters the runtime limits withhold. It counts the
// decision, and each withheld exporter, for the console and /metrics.
func (s *Set) AuditTargets(selected []string) []string {
	if s == nil {
		return nil
	}
	if len(selected) == 0 {
		s.st.decision(SignalAudit, DecisionNoRule)
		return nil
	}
	lim := s.current()
	out := make([]string, 0, len(selected))
	for _, name := range selected {
		if s.allowed(lim, name, SignalAudit) {
			out = append(out, name)
		} else {
			s.st.count(name, SignalAudit, outcomeWithheld, 1)
		}
	}
	if len(out) == 0 {
		s.st.decision(SignalAudit, DecisionWithheld)
		return nil
	}
	s.st.decision(SignalAudit, DecisionSelected)
	return out
}

// ContentTarget is the content half: the policy's single content exporter
// unless the matching rules disagreed (conflict) or a limit withholds it.
func (s *Set) ContentTarget(selected string, conflict bool) string {
	if s == nil {
		return ""
	}
	switch {
	case conflict:
		s.st.decision(SignalContent, DecisionConflict)
		return ""
	case selected == "":
		s.st.decision(SignalContent, DecisionNoRule)
		return ""
	case !s.allowed(s.current(), selected, SignalContent):
		s.st.count(selected, SignalContent, outcomeWithheld, 1)
		s.st.decision(SignalContent, DecisionWithheld)
		return ""
	}
	s.st.decision(SignalContent, DecisionSelected)
	return selected
}

// TraceTargets is the traces half: withheld exporters are dropped and a
// sample percent above an exporter's max_sample_percent is lowered to it. The
// sampling draw itself happens later, in internal/tracing.
func (s *Set) TraceTargets(selected []TraceSelection) []TraceSelection {
	if s == nil {
		return nil
	}
	if len(selected) == 0 {
		s.st.decision(SignalTraces, DecisionNoRule)
		return nil
	}
	lim := s.current()
	out := make([]TraceSelection, 0, len(selected))
	for _, t := range selected {
		if !s.allowed(lim, t.Exporter, SignalTraces) {
			s.st.count(t.Exporter, SignalTraces, outcomeWithheld, 1)
			continue
		}
		t.SamplePercent = s.SamplePercent(lim, t.Exporter, t.SamplePercent)
		out = append(out, t)
	}
	if len(out) == 0 {
		s.st.decision(SignalTraces, DecisionWithheld)
		return nil
	}
	s.st.decision(SignalTraces, DecisionSelected)
	return out
}

const limitsFileVersion = 1

// limitsFile is the on-disk form of observability.console.state_file.
type limitsFile struct {
	Version   int    `json:"version"`
	Exporters Limits `json:"exporters"`
}

// EncodeLimits returns the canonical file bytes of normalized limits: the
// exact bytes mayu writes to the state file, so their Revision is the limits
// revision the console and the audit record use.
func EncodeLimits(l Limits) []byte {
	if l == nil {
		l = Limits{}
	}
	b, _ := json.MarshalIndent(limitsFile{Version: limitsFileVersion, Exporters: l}, "", "  ") // cannot fail: bools, strings and integers only
	return append(b, '\n')
}

// DecodeLimits parses state file bytes strictly: an unknown field, another
// version or trailing data is an error, because a kill switch that silently
// fails to load would resume export after a restart.
func DecodeLimits(b []byte) (Limits, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var f limitsFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("decode limits: %w", err)
	}
	if dec.More() {
		return nil, errors.New("decode limits: trailing data after the JSON object")
	}
	if f.Version != limitsFileVersion {
		return nil, fmt.Errorf("decode limits: unsupported version %d (want %d)", f.Version, limitsFileVersion)
	}
	if f.Exporters == nil {
		f.Exporters = Limits{}
	}
	return f.Exporters, nil
}

// Revision is the console revision of persisted bytes: "sha256:<hex>".
func Revision(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
```

Create `internal/otelexport/status.go`:

```go
package otelexport

import (
	"sort"
	"time"
)

// ExporterStatus is the console's view of one declared exporter. Header values
// never appear, only header names.
type ExporterStatus struct {
	Name                     string        `json:"name"`
	Endpoint                 string        `json:"endpoint"`
	Protocol                 string        `json:"protocol"`
	TLS                      bool          `json:"tls"`
	HeaderNames              []string      `json:"header_names"`
	DataBoundary             string        `json:"data_boundary,omitempty"`
	Signals                  []string      `json:"signals"`
	EffectiveSignals         []string      `json:"effective_signals"`
	MaxContentBytes          int64         `json:"max_content_bytes,omitempty"`
	EffectiveMaxContentBytes int64         `json:"effective_max_content_bytes,omitempty"`
	QueueLimitBytes          int64         `json:"queue_limit_bytes,omitempty"`
	QueuedBytes              int64         `json:"queued_bytes"`
	Limit                    Limit         `json:"limit"`
	Stats                    []SignalStats `json:"stats"`
}

// Status is the console snapshot of the declared exporters and the governed
// decisions. Counters are per process since CountersSince.
type Status struct {
	ServiceName   string           `json:"service_name"`
	CountersSince string           `json:"counters_since"`
	Exporters     []ExporterStatus `json:"exporters"`
	Decisions     []DecisionStats  `json:"decisions"`
}

// Status returns the console snapshot. A nil Set has no exporters.
func (s *Set) Status() Status {
	if s == nil {
		return Status{Exporters: []ExporterStatus{}, Decisions: []DecisionStats{}}
	}
	lim := s.current()
	names := make([]string, 0, len(s.exporters))
	for name := range s.exporters {
		names = append(names, name)
	}
	sort.Strings(names)
	out := Status{ServiceName: s.serviceName, CountersSince: s.st.since.UTC().Format(time.RFC3339),
		Exporters: make([]ExporterStatus, 0, len(names)), Decisions: s.st.decisions()}
	for _, name := range names {
		d := s.exporters[name]
		e := d.cfg
		signals := append([]string(nil), e.Signals...)
		sort.Strings(signals)
		headers := make([]string, 0, len(e.Headers))
		for h := range e.Headers {
			headers = append(headers, h)
		}
		sort.Strings(headers)
		row := ExporterStatus{Name: name, Endpoint: e.Endpoint, Protocol: protocolName(e.Protocol), TLS: !e.Insecure,
			HeaderNames: headers, DataBoundary: e.DataBoundary, Signals: signals, EffectiveSignals: []string{},
			Limit: lim[name].clone(), Stats: make([]SignalStats, 0, len(signals))}
		for _, sig := range signals {
			if s.allowed(lim, name, sig) {
				row.EffectiveSignals = append(row.EffectiveSignals, sig)
			}
			row.Stats = append(row.Stats, s.st.signalStats(name, sig))
		}
		if e.allows(SignalContent) {
			row.MaxContentBytes = e.MaxContentBytes
			row.EffectiveMaxContentBytes = s.contentLimit(lim, name)
		}
		if d.logs != nil {
			row.QueueLimitBytes = d.logs.limit
			row.QueuedBytes = d.logs.queued.Load()
		}
		out.Exporters = append(out.Exporters, row)
	}
	return out
}

func protocolName(p string) string {
	if p == "" {
		return "http"
	}
	return p
}
```

`internal/otelexport/set.go`: add `"sync/atomic"` to the standard-library imports (after `"sort"`); in `Exporter` after the `Signals  []string` line add

```go
	// DataBoundary is the declared data boundary, shown by the console.
	DataBoundary string
```

replace the `Set` struct with

```go
// Set is the declared exporter ceiling plus one log pipeline per exporter that
// allows audit or content, and the runtime limits that narrow it.
type Set struct {
	exporters   map[string]*declared
	serviceName string
	st          *stats
	limits      atomic.Pointer[Limits] // runtime narrowing (limits.go); nil until SetLimits
}
```

change the `s := &Set{…}` line in `New` to `s := &Set{exporters: make(map[string]*declared, len(exporters)), serviceName: serviceName, st: newStats(m)}`, and replace `emit` with

```go
// emit enqueues one log record for exporter; it never blocks.
func (s *Set) emit(exporter, signal string, it item) {
	d := s.exporters[exporter]
	if d == nil || d.logs == nil || !d.cfg.allows(signal) {
		// Policy apply already checked the ceiling, so this is a programming
		// or reload-skew error: count it rather than export off-ceiling.
		s.st.count(exporter, signal, outcomeExportError, 1)
		return
	}
	if !s.allowed(s.current(), exporter, signal) {
		// A limit installed after the decision: still never export.
		s.st.count(exporter, signal, outcomeWithheld, 1)
		return
	}
	it.signal = signal
	d.logs.enqueue(it)
}
```

`internal/otelexport/records.go`: in `EmitContent` replace `if d != nil && size > d.cfg.MaxContentBytes {` with `if d != nil && size > s.contentLimit(s.current(), exporter) {`, and update its doc comment's second sentence to: `Records over the exporter's content cap (MaxContentBytes, or a lower runtime limit) are dropped and counted, never truncated.`

`cmd/mayu/observability.go`: in the `otelexport.Exporter{…}` literal of `buildObservability` add `DataBoundary: e.DataBoundary,` after `Signals: e.Signals,`. Replace `auditTargets`, `contentExport` and `traceDecider` (lines 57–91) with:

```go
// auditTargets selects audit-copy exporters for a record's subject: the
// policy selection after the runtime limits (Set.AuditTargets counts it).
func auditTargets(set *otelexport.Set, pol *policy.Store) func(team, user string) []string {
	return func(team, user string) []string { return set.AuditTargets(pol.Observability(team, user).Audit) }
}

// contentExport copies egress content to the subject's content exporter,
// unless matching rules disagree or a runtime limit withholds it.
func contentExport(set *otelexport.Set, pol *policy.Store) requestpolicy.ContentExport {
	return func(_ context.Context, c requestpolicy.ContentRecord) {
		d := pol.Observability(c.Team, c.Account)
		exporter := set.ContentTarget(d.Content, d.ContentConflict)
		if exporter == "" {
			return
		}
		set.EmitContent(exporter, otelexport.Content{RecordID: c.RecordID, Team: c.Team, Ingress: c.Ingress,
			Model: c.Model, Upstream: c.Upstream, TraceID: c.TraceID, Request: c.Request, Response: c.Response})
	}
}

// traceDecider unions the legacy `otel` baseline (when configured) with the
// governed trace targets of the request's principal, after the runtime limits.
func traceDecider(set *otelexport.Set, pol *policy.Store, legacy *tracing.Target) tracing.Decider {
	return func(ctx context.Context) []tracing.Target {
		var out []tracing.Target
		if legacy != nil {
			out = append(out, *legacy)
		}
		p, ok := principal.From(ctx)
		if !ok {
			return out
		}
		var selected []otelexport.TraceSelection
		for _, t := range pol.Observability(p.Team, p.AccountSubject()).Traces {
			selected = append(selected, otelexport.TraceSelection{Exporter: t.Exporter, SamplePercent: t.SamplePercent})
		}
		for _, t := range set.TraceTargets(selected) {
			out = append(out, tracing.Target{Exporter: t.Exporter, Ratio: float64(t.SamplePercent) / 100})
		}
		return out
	}
}
```

and in `initTracing` change the return line (114) to `return tracing.InitExporters(ctx, service, exporters, traceDecider(set, pol, legacy))`.

`cmd/mayu/gateway.go` line 249: `sinks = append(sinks, obsSet.AuditSink(auditTargets(obsSet, polStore)))`.

Append to the `internal/CLAUDE.md` section:

```markdown
- `otelexport/` limits: `Limit`/`Limits` only narrow the declared ceiling (pause, signal subset, lower content cap, `max_sample_percent`); `Normalize` refuses anything wider and canonicalizes, `EncodeLimits` bytes are the revision input, `Clamp` narrows a persisted file to a changed bootstrap. Limits apply at decision time (`AuditTargets`, `ContentTarget`, `TraceTargets`, rechecked in `emit`), never through `SetObservabilityCeiling`, so narrowing never rejects a policy document. `Status` reports header names only.
```

- [ ] **Step 4: Run**

```bash
go test -buildvcs=false ./internal/otelexport/ ./internal/policy/ -race -count=1 > $S/t3.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|panic)' $S/t3.log
go test -buildvcs=false ./cmd/mayu/ -run 'Observability|LegacyOTel' -race -count=1 > $S/t3b.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|panic)' $S/t3b.log
gofmt -l internal/otelexport cmd/mayu
```
Expected: both `exit=0`; `ok` for `internal/otelexport`, `internal/policy`, `cmd/mayu` (the base branch's acceptance tests still pass through the new closures); no gofmt output.

- [ ] **Step 5: Commit**

```bash
git -C $W add internal/otelexport/limits.go internal/otelexport/limits_test.go internal/otelexport/status.go internal/otelexport/status_test.go internal/otelexport/set.go internal/otelexport/records.go cmd/mayu/observability.go cmd/mayu/gateway.go internal/CLAUDE.md
git -C $W commit -s -m "feat(otelexport): narrowing-only runtime limits applied at decision time" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 4: Per-exporter span counts from the tracing seam

**Files:**
- Modify: `internal/tracing/route.go:53-63` (add `Observer` after `Decider`), `:65-89` (`InitRouted`), `:109-124` (`InitExporters`), `:126-132` (`routingProcessor`), `:140-162` (`choose`); add `countingExporter` at the end
- Modify: `internal/tracing/tracing.go:58-59` (`Init` passes a nil observer)
- Create: `internal/tracing/observer_test.go`
- Modify: `cmd/mayu/observability.go:93-115` (`initTracing` passes `set.ObserveTrace`)
- Modify: `docs/reference/agent-llm.md:24-25` and `docs/reference/agent-llm.ko.md:29-30` (Tracing and Governed export rows), `internal/CLAUDE.md` (one bullet)

**Interfaces:**
- Consumes: `otelexport.Set.ObserveTrace` (Task 2).
- Produces:
  - `type tracing.Observer func(exporter, outcome string, n int)`
  - `const tracing.OutcomeSent = "sent"`, `OutcomeExportError = "dropped_export_error"`, `OutcomeSampledOut = "sampled_out"`
  - `func InitExporters(ctx context.Context, serviceName string, exporters map[string]ExporterConfig, decide Decider, observe Observer) (func(context.Context) error, error)` (new trailing parameter; `InitRouted` keeps its signature)
  - package-private `initRouted(..., observe Observer)`, `var drawSample = rand.Float64`

- [ ] **Step 1: Write the failing tests**

`internal/tracing/observer_test.go`:

```go
package tracing

import (
	"context"
	"errors"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type observed struct {
	mu     sync.Mutex
	counts map[string]int
}

func (o *observed) observe(exporter, outcome string, n int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.counts == nil {
		o.counts = map[string]int{}
	}
	o.counts[exporter+"/"+outcome] += n
}

func (o *observed) get(key string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.counts[key]
}

// failingExporter returns an error whose text a collector could have chosen.
type failingExporter struct{}

func (failingExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return errors.New("collector said: private response body")
}
func (failingExporter) Shutdown(context.Context) error { return nil }

func TestObserverCountsSentFailedAndSampledOut(t *testing.T) {
	old := drawSample
	drawSample = func() float64 { return 0.9 }
	t.Cleanup(func() { drawSample = old })
	obs := &observed{}
	a := &recExporter{}
	down := initRouted("test", map[string]sdktrace.SpanExporter{"a": a, "bad": failingExporter{}}, ctxDecider, obs.observe)
	for _, targets := range [][]Target{
		{{Exporter: "a", Ratio: 1}},
		{{Exporter: "a", Ratio: 0.5}}, // the draw (0.9) skips it
		{{Exporter: "bad", Ratio: 1}},
	} {
		_, span := Start(withTargets(context.Background(), targets...), "span")
		span.End()
	}
	_ = down(context.Background()) // flushes both batch processors
	Disable()
	otel.SetTracerProvider(nil)
	if got := obs.get("a/" + OutcomeSent); got != 1 {
		t.Fatalf("a sent = %d", got)
	}
	if got := obs.get("a/" + OutcomeSampledOut); got != 1 {
		t.Fatalf("a sampled out = %d", got)
	}
	if got := obs.get("bad/" + OutcomeExportError); got != 1 {
		t.Fatalf("bad failed = %d", got)
	}
	if got := a.names(); len(got) != 1 {
		t.Fatalf("exporter a got %v", got)
	}
}

func TestInitRoutedWithoutObserverIsUnchanged(t *testing.T) {
	a, _, shutdown := routed(t)
	_, span := Start(withTargets(context.Background(), Target{Exporter: "a", Ratio: 1}), "plain")
	span.End()
	shutdown()
	if got := a.names(); len(got) != 1 || got[0] != "plain" {
		t.Fatalf("exporter a got %v", got)
	}
}
```

- [ ] **Step 2: Run — expect compile failure**

```bash
go test -buildvcs=false ./internal/tracing/ -race -count=1 > $S/t4.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|panic)|undefined' $S/t4.log
```
Expected: `exit=1`, `undefined: drawSample`, `initRouted`, `OutcomeSent`.

- [ ] **Step 3: Implement**

In `internal/tracing/route.go`, after the `Decider` type (line 63) add:

```go
// Observer receives per-exporter span counts for the observability console:
// OutcomeSent or OutcomeExportError per exported batch, OutcomeSampledOut per
// span a target's draw skipped. It runs on the request path and on batch
// export goroutines, so it must be cheap and must not block. Only counts cross
// it: the SDK's error text can quote collector responses.
type Observer func(exporter, outcome string, n int)

// Outcome values passed to an Observer (the same strings as the outcome label
// of inferplane_otel_export_records_total).
const (
	OutcomeSent        = "sent"
	OutcomeExportError = "dropped_export_error"
	OutcomeSampledOut  = "sampled_out"
)

// drawSample is the per-target sampling draw; a variable so tests can make it
// deterministic.
var drawSample = rand.Float64
```

Replace the body of `InitRouted` (keep its doc comment, lines 65–72) and add `initRouted` after it:

```go
func InitRouted(serviceName string, exporters map[string]sdktrace.SpanExporter, decide Decider) func(context.Context) error {
	return initRouted(serviceName, exporters, decide, nil)
}

// initRouted is InitRouted with an optional Observer; with one, every
// exporter is wrapped so its batch outcomes are counted.
func initRouted(serviceName string, exporters map[string]sdktrace.SpanExporter, decide Decider, observe Observer) func(context.Context) error {
	if serviceName == "" {
		serviceName = "inferplane"
	}
	r := &routingProcessor{procs: make(map[string]sdktrace.SpanProcessor, len(exporters)), decide: decide, observe: observe}
	for name, exp := range exporters {
		if observe != nil {
			exp = countingExporter{SpanExporter: exp, name: name, observe: observe}
		}
		r.procs[name] = sdktrace.NewBatchSpanProcessor(exp)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(routeSampler{}),
		sdktrace.WithSpanProcessor(r),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", serviceName))),
	)
	SetTracerProvider(tp)
	router = r
	return tp.Shutdown
}
```

Replace `InitExporters` (lines 109–124) with:

```go
// InitExporters builds one OTLP exporter per entry and installs InitRouted
// over them, so callers outside this package never import the SDK. observe,
// when non-nil, receives per-exporter span counts (see Observer).
func InitExporters(ctx context.Context, serviceName string, exporters map[string]ExporterConfig, decide Decider, observe Observer) (func(context.Context) error, error) {
	built := make(map[string]sdktrace.SpanExporter, len(exporters))
	for name, c := range exporters {
		exp, err := NewExporter(ctx, c)
		if err != nil {
			for _, b := range built {
				_ = b.Shutdown(ctx)
			}
			return nil, err
		}
		built[name] = exp
	}
	return initRouted(serviceName, built, decide, observe), nil
}
```

Replace the `routingProcessor` struct (lines 126–132) with:

```go
// routingProcessor forwards each ended span to the batch processors chosen
// for it when it started.
type routingProcessor struct {
	procs   map[string]sdktrace.SpanProcessor
	decide  Decider
	observe Observer // nil = no span counts
	routes  sync.Map // trace.SpanID → []string (exporter names)
}

// note forwards one count to the observer, if any.
func (r *routingProcessor) note(exporter, outcome string, n int) {
	if r.observe != nil {
		r.observe(exporter, outcome, n)
	}
}
```

In `choose`, replace

```go
		if t.Ratio < 1 && rand.Float64() >= t.Ratio {
			continue
		}
```

with

```go
		if t.Ratio < 1 && drawSample() >= t.Ratio {
			r.note(t.Exporter, OutcomeSampledOut, 1)
			continue
		}
```

Append at the end of `route.go`:

```go
// countingExporter reports each batch's outcome to the observer. Only the span
// count leaves: the SDK error can carry collector response text.
type countingExporter struct {
	sdktrace.SpanExporter
	name    string
	observe Observer
}

func (e countingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	err := e.SpanExporter.ExportSpans(ctx, spans)
	outcome := OutcomeSent
	if err != nil {
		outcome = OutcomeExportError
	}
	e.observe(e.name, outcome, len(spans))
	return err
}
```

In `internal/tracing/tracing.go` `Init` (lines 58–59) change the second line of the `InitExporters` call to `		func(context.Context) []Target { return targets }, nil)`.

In `cmd/mayu/observability.go` `initTracing`, replace the final `return tracing.InitExporters(...)` line with:

```go
	var observe tracing.Observer
	if set != nil {
		observe = set.ObserveTrace // span counts for the console's traces rows
	}
	return tracing.InitExporters(ctx, service, exporters, traceDecider(set, pol, legacy), observe)
```

`docs/reference/agent-llm.md`: replace the **Tracing** row (line 24) and the **Governed export** row (line 25) with:

```markdown
| Tracing | `internal/tracing/` | opt-in OTel: OTLP **trace** exporters + GenAI-semconv spans (+ `inferplane.*` cache-tier / cost / partial attributes) + W3C propagation + trace_id in audit; a routing processor sends each span only to the exporters its request's policy selects, with a gateway-side sampling draw; an optional `Observer` receives per-exporter span counts (sent, failed, sampled out) for the observability console, never the SDK's error text; no-op default (ADR-011) |
| Governed export | `internal/otelexport/` | local exporter ceiling (`Allows`) and bounded OTLP log pipelines for audit-record copies and egress content, selected per subject by `observability` policy rules; runtime limits (`Normalize`/`SetLimits`) narrow it at decision time, counted as `withheld_limit` |
```

`docs/reference/agent-llm.ko.md`: replace the **트레이싱** row (line 29) and the **정책 기반 내보내기** row (line 30) with:

```markdown
| 트레이싱 | `internal/tracing/` | 선택적 OTLP 트레이스, GenAI와 캐시·비용·부분 결과, W3C 전파·감사 trace_id. span은 요청 정책이 고른 exporter로만 가고 샘플링은 게이트웨이가 정함. 선택적 `Observer`가 관측성 콘솔에 exporter별 span 수(전송·실패·샘플링 제외)를 넘기며 SDK 오류 문구는 넘기지 않음. 기본 no-op |
| 정책 기반 내보내기 | `internal/otelexport/` | 로컬 exporter 상한(`Allows`)과 감사 기록 사본·송신 본문용 크기 제한 OTLP 로그 파이프라인. `observability` 정책 규칙이 주체별로 선택. 실행 중 한도(`Normalize`/`SetLimits`)가 결정 시점에 좁히고 `withheld_limit`로 셈 |
```

Then refresh the companion hash with the Global Constraints command for `docs/reference/agent-llm.md`.

Append to the `internal/CLAUDE.md` section:

```markdown
- `tracing/`: `InitExporters` takes an optional `Observer` — `sent`/`dropped_export_error` per batch through a counting exporter wrapper, `sampled_out` per skipped draw — so the console's traces rows never see SDK error text; `drawSample` is the sampling seam for tests. `InitRouted`'s signature is unchanged.
```

- [ ] **Step 4: Run**

```bash
go test -buildvcs=false ./internal/tracing/ ./cmd/mayu/ -race -count=1 -run 'Observer|Routed|Traceparent|Legacy|Unselected|Span|Observability|LegacyOTel' > $S/t4.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|panic)' $S/t4.log
go test -buildvcs=false ./internal/tracing/ -race -count=1 > $S/t4b.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL)' $S/t4b.log
python3 scripts/check_docs_translations.py > $S/t4-docs.log 2>&1; echo "docs=$?"
gofmt -l internal/tracing cmd/mayu
```
Expected: `exit=0` twice, `ok` for `internal/tracing` and `cmd/mayu`, `docs=0`, no gofmt output.

- [ ] **Step 5: Commit**

```bash
git -C $W add internal/tracing/route.go internal/tracing/tracing.go internal/tracing/observer_test.go cmd/mayu/observability.go docs/reference/agent-llm.md docs/reference/agent-llm.ko.md internal/CLAUDE.md
git -C $W commit -s -m "feat(tracing): per-exporter span counts for the observability console" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 5: `observability.console` bootstrap block

**Files:**
- Modify: `internal/config/observability.go:3-12` (imports), `:32-39` (`ObservabilityConfig`); add `ObservabilityConsoleConfig` and `validateObservabilityConsole` at the end
- Modify: `internal/config/config.go:865-867` (call after `validateControlPlane`)
- Create: `internal/config/observability_console_test.go`
- Modify: `internal/CLAUDE.md` (one bullet)

**Interfaces:**
- Produces:
  - `type ObservabilityConsoleConfig struct { StateFile string \`json:"state_file,omitempty"\`; PolicyFile string \`json:"policy_file,omitempty"\`; ContentRules bool \`json:"content_rules,omitempty"\` }`
  - `ObservabilityConfig.Console *ObservabilityConsoleConfig` (JSON `console`)
  - package-private `validateObservabilityConsole(cfg *Config) error`

- [ ] **Step 1: Write the failing tests**

`internal/config/observability_console_test.go`:

```go
package config

import (
	"strings"
	"testing"
)

const consoleExporters = `"exporters":{"corp":{"endpoint":"h:1","signals":["traces","audit"]}}`

func TestObservabilityConsoleParses(t *testing.T) {
	cfg, err := loadObservability(t, `{"policies":["/srv/policies","/srv/console/console-observability.yaml"],
		"observability":{`+consoleExporters+`,"console":{"state_file":"/srv/state/observability-limits.json",
		"policy_file":"/srv/console/console-observability.yaml","content_rules":true}}}`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	c := cfg.Observability.Console
	if c == nil || c.StateFile != "/srv/state/observability-limits.json" || c.PolicyFile != "/srv/console/console-observability.yaml" || !c.ContentRules {
		t.Fatalf("console = %+v", c)
	}
}

func TestObservabilityConsoleRejects(t *testing.T) {
	for _, tc := range []struct{ name, top, console, want string }{
		{"relative state file", ``, `{"state_file":"limits.json"}`, "state_file"},
		{"state file not json", ``, `{"state_file":"/srv/limits.yaml"}`, "state_file"},
		{"state file not clean", ``, `{"state_file":"/srv/../srv/limits.json"}`, "state_file"},
		{"state file in a policies directory", `"policies":["/srv/policies"],`, `{"state_file":"/srv/policies/limits.json"}`, "policies"},
		{"policy file not yaml", `"policies":["/srv/console.json"],`, `{"policy_file":"/srv/console.json"}`, "policy_file"},
		{"policy file not listed", `"policies":["/srv/policies"],`, `{"policy_file":"/srv/console.yaml"}`, "listed in policies"},
		{"policy file directory also listed", `"policies":["/srv/console","/srv/console/console.yaml"],`, `{"policy_file":"/srv/console/console.yaml"}`, "twice"},
		{"policy file with control plane", `"control_plane":{"url":"https://cp.internal","dataplane":"dp"},`, `{"policy_file":"/srv/console.yaml"}`, "control plane"},
		{"content rules without policy file", ``, `{"content_rules":true}`, "content_rules"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadObservability(t, `{`+tc.top+`"observability":{`+consoleExporters+`,"console":`+tc.console+`}}`)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run — expect failure**

```bash
go test -buildvcs=false ./internal/config/ -run 'ObservabilityConsole' -race -count=1 > $S/t5.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|panic)|unknown field|Console' $S/t5.log
```
Expected: `exit=1` (`cfg.Observability.Console undefined`).

- [ ] **Step 3: Implement**

`internal/config/observability.go`: add `"path/filepath"` to the imports (after `"net"`). In `ObservabilityConfig` add after `Exporters`:

```go
	// Console enables runtime edits from the observability console; absent
	// keeps the console read-only.
	Console *ObservabilityConsoleConfig `json:"console,omitempty"`
```

Append to the end of the file:

```go
// ObservabilityConsoleConfig lets full administrators narrow export and edit
// console-owned observability rules at runtime (sub-project B). Each key is
// optional; an absent key keeps that part of the console read-only.
type ObservabilityConsoleConfig struct {
	// StateFile persists the runtime export limits (JSON, written by mayu).
	StateFile string `json:"state_file,omitempty"`
	// PolicyFile is the console-owned GovernancePolicy file. It must also be
	// a `policies` entry, so console rules load, validate and hot-reload
	// through the same local file channel as every other local policy.
	PolicyFile string `json:"policy_file,omitempty"`
	// ContentRules lets console rules add or change a content selection. Off
	// by default: copying prompts stays a bootstrap decision.
	ContentRules bool `json:"content_rules,omitempty"`
}

// validateObservabilityConsole checks the console block against the rest of
// the config. It runs after validateControlPlane, which already rejects
// `policies` together with `control_plane`.
func validateObservabilityConsole(cfg *Config) error {
	if cfg.Observability == nil || cfg.Observability.Console == nil {
		return nil
	}
	c := cfg.Observability.Console
	policyDirs := map[string]bool{}
	for _, p := range cfg.Policies {
		policyDirs[filepath.Clean(p)] = true
	}
	if c.StateFile != "" {
		if !filepath.IsAbs(c.StateFile) || filepath.Clean(c.StateFile) != c.StateFile || filepath.Ext(c.StateFile) != ".json" {
			return fmt.Errorf("config: observability.console.state_file must be a clean absolute path ending in .json")
		}
		if policyDirs[filepath.Dir(c.StateFile)] {
			return fmt.Errorf("config: observability.console.state_file must not sit in a directory listed in policies (it would load as a policy file)")
		}
	}
	if c.PolicyFile != "" {
		if cfg.ControlPlane != nil {
			return fmt.Errorf("config: observability.console.policy_file needs the local policy file channel; with control_plane the control plane is the policy source and console rules are read-only")
		}
		ext := filepath.Ext(c.PolicyFile)
		if !filepath.IsAbs(c.PolicyFile) || filepath.Clean(c.PolicyFile) != c.PolicyFile || (ext != ".yaml" && ext != ".yml") {
			return fmt.Errorf("config: observability.console.policy_file must be a clean absolute path ending in .yaml or .yml")
		}
		if !policyDirs[c.PolicyFile] {
			return fmt.Errorf("config: observability.console.policy_file must also be listed in policies, so console rules load through the local policy file channel")
		}
		if policyDirs[filepath.Dir(c.PolicyFile)] {
			return fmt.Errorf("config: observability.console.policy_file's directory is also listed in policies; the file would load twice")
		}
	}
	if c.ContentRules && c.PolicyFile == "" {
		return fmt.Errorf("config: observability.console.content_rules requires observability.console.policy_file")
	}
	return nil
}
```

`internal/config/config.go`: after the `validateControlPlane` call (lines 865–867) insert:

```go
	if err := validateObservabilityConsole(&cfg); err != nil {
		return nil, err
	}
```

Append to the `internal/CLAUDE.md` section:

```markdown
- `config/`: `observability.console` — `state_file` (clean absolute `.json`), `policy_file` (clean absolute `.yaml`/`.yml`, must also be a `policies` entry, never with `control_plane`), `content_rules` (needs `policy_file`); neither file may sit in a directory listed in `policies`.
```

- [ ] **Step 4: Run**

```bash
go test -buildvcs=false ./internal/config/ -race -count=1 > $S/t5.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|panic)' $S/t5.log
INFERPLANE_ADMIN_TOKEN=lint go run -buildvcs=false ./cmd/mayu config validate --config examples/config.json > $S/t5-validate.log 2>&1; echo "validate=$?"
gofmt -l internal/config
```
Expected: `exit=0`, `validate=0`, no gofmt output.

- [ ] **Step 5: Commit**

```bash
git -C $W add internal/config/observability.go internal/config/config.go internal/config/observability_console_test.go internal/CLAUDE.md
git -C $W commit -s -m "feat(config): observability.console block for runtime edits" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 6: Admin change evidence on the audit record

**Files:**
- Modify: `internal/audit/record.go:51-53` (append `Change` after `Routing` in `RequestRef`)
- Create: `internal/audit/change.go`, `internal/audit/change_test.go`, `internal/analytics/observability_change_test.go`
- Modify: `docs/reference/data.md` (new `##` section at the end of the file, after the `## Audit console evidence (ADR-057)` section's last line 310), `docs/reference/data.ko.md` (at the end, after line 237), `internal/CLAUDE.md` (one bullet)

**Audit console interplay (checked at `caf55fe`):** the audit console lists only terminal request records. `analytics.AuditObservable` (`internal/analytics/auditevents.go:37`) accepts `request_completed` and `request_started` with an outcome, the analytics sink drops every other event before indexing (`internal/analytics/sink.go:59`), and `TestAuditObservable` already pins `admin_key_created` as excluded. So `admin_observability_changed` and `request.change` never reach `/admin/audit/*`, the kind chips, the detail panel or `audit.js`, and no `.cjs` check enumerates event types or request fields; no console label or renderer is needed. Step 1 adds one pin so a later change that projects admin events has to label this one first.

**Interfaces:**
- Produces: `type ChangeRef struct { Target, Before, After string; Names []string }` (JSON `target`, `before`, `after`, `names,omitempty`); `RequestRef.Change *ChangeRef` (JSON `change,omitempty`).

- [ ] **Step 1: Write the failing tests**

`internal/audit/change_test.go`:

```go
package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequestChangeIsAppendOnly(t *testing.T) {
	plain := Record{SchemaVersion: 1, Event: "admin_team_upserted", ID: "01J", TS: "t", Instance: "i", Request: RequestRef{Ingress: "admin"}}
	b, err := plain.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"change"`) {
		t.Fatalf("a record without change evidence grew a key: %s", b)
	}
	changed := plain
	changed.Event = "admin_observability_changed"
	changed.Request.Change = &ChangeRef{Target: "observability_limits", Before: "sha256:aa", After: "sha256:bb", Names: []string{"corp"}}
	c, err := changed.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	want := `"request":{"ingress":"admin","model_requested":"","stream":false,"change":{"target":"observability_limits","before":"sha256:aa","after":"sha256:bb","names":["corp"]}}`
	if !strings.Contains(string(c), want) {
		t.Fatalf("change must close the request object:\n%s", c)
	}
}

func TestMixedVersionChainVerifies_Change(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "audit.jsonl")
	if err := os.WriteFile(out, []byte(preAuthMethodFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	fs, err := NewFileSink(out, true)
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter("new-instance", filepath.Join(dir, "wal"), []Sink{fs})
	if err != nil {
		t.Fatal(err)
	}
	sub, method := "admin-sub", "oidc"
	w.Append(Record{SchemaVersion: 1, Event: "admin_observability_changed", ID: "01CHANGE000000000000000000", TS: "2026-10-29T10:00:00Z",
		Principal: PrincipalRef{User: &sub, AuthMethod: &method},
		Request:   RequestRef{Ingress: "admin", Change: &ChangeRef{Target: "observability_rules", Before: "sha256:aa", After: "sha256:bb", Names: []string{"console-eng"}}}})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Verify(bytesReader(raw))
	if err != nil || !res.OK || res.Records != 4 {
		t.Fatalf("mixed chain = %+v, %v; want OK with 4 records", res, err)
	}
	if !containsBytes(raw, []byte(`"change":{"target":"observability_rules","before":"sha256:aa","after":"sha256:bb","names":["console-eng"]}`)) {
		t.Fatalf("change evidence missing:\n%s", raw)
	}
}
```

`internal/analytics/observability_change_test.go`:

```go
package analytics

import (
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
)

// The audit console (ADR-057) and GET /admin/logs index terminal request
// records only. admin_observability_changed, and the request.change evidence it
// carries, stays out of both projections, so the console needs no label or
// renderer for it. If an admin event is ever projected, label this one first.
func TestObservabilityChangeRecordsStayOutOfTheConsole(t *testing.T) {
	rec := audit.Record{SchemaVersion: 1, Event: "admin_observability_changed", ID: "01JC0000000000000000000001", TS: "2026-10-29T10:00:00Z",
		Request: audit.RequestRef{Ingress: "admin", Change: &audit.ChangeRef{Target: "observability_limits", Before: "sha256:aa", After: "sha256:bb", Names: []string{"corp"}}}}
	if AuditObservable(rec) || Observable(rec) {
		t.Fatal("an admin change record reached the audit console or the analytics projection")
	}
}
```

- [ ] **Step 2: Run — expect compile failure**

```bash
go test -buildvcs=false ./internal/audit/ ./internal/analytics/ -run 'RequestChange|Change' -race -count=1 > $S/t6.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|panic)|undefined|unknown field' $S/t6.log
```
Expected: `exit=1`, `undefined: ChangeRef` in both packages.

- [ ] **Step 3: Implement**

`internal/audit/change.go`:

```go
package audit

// ChangeRef is the evidence of one admin configuration change, carried in
// RequestRef.Change on admin_observability_changed records. Target is a fixed
// name ("observability_limits" | "observability_rules"); Before and After are
// "sha256:<hex>" revisions of the persisted state (the exact bytes mayu
// wrote, so an auditor can hash the file and compare); Names lists the
// changed exporters or console rules. The state itself never enters the chain.
type ChangeRef struct {
	Target string   `json:"target"`
	Before string   `json:"before"`
	After  string   `json:"after"`
	Names  []string `json:"names,omitempty"`
}
```

`internal/audit/record.go`, at the end of `RequestRef` (after the `Routing *RoutingRef` line 52), add:

```go
	// Change is admin change evidence (admin_observability_changed): what
	// changed and the revisions before and after. Appended at the end with
	// omitempty like Routing, so every other record keeps its exact bytes.
	Change *ChangeRef `json:"change,omitempty"`
```

`docs/reference/data.md`: append at the end of the file, after the `### Configuration` table that closes `## Audit console evidence (ADR-057)` (line 310), so the two audit-record sections sit together:

~~~~markdown

## Admin change evidence (observability console)

`request.change` is appended at the end of the audit `request` object with
`omitempty`, so records without it keep their exact bytes. The observability
console sets it on `admin_observability_changed`: `target` is
`observability_limits` or `observability_rules`, `before` and `after` are
`sha256:` revisions of the persisted state file bytes, and `names` lists the
changed exporters or console rules. `principal.user` and `principal.auth_method`
name the administrator. The state itself never enters the chain. Admin records
are not projected into the audit console tables above.
~~~~

`docs/reference/data.ko.md`: append at the end of the file, after the `### 설정` table of `## 감사 콘솔 근거 (ADR-057)` (line 237):

~~~~markdown

## 관리 변경 증거 · 관측성 콘솔 {#admin-change-evidence-observability-console}

`request.change`는 감사 `request` 객체 끝에 `omitempty`로 덧붙입니다. 이 필드가 없는 기록은 바이트가 그대로입니다. 관측성 콘솔은 `admin_observability_changed` 기록에 이 필드를 씁니다. `target`은 `observability_limits` 또는 `observability_rules`이고, `before`와 `after`는 저장된 상태 파일 바이트의 `sha256:` 리비전이며, `names`는 바뀐 exporter나 콘솔 규칙 이름입니다. `principal.user`와 `principal.auth_method`가 관리자를 나타냅니다. 상태 내용 자체는 체인에 들어가지 않습니다. 관리 기록은 위의 감사 콘솔 테이블에 투영되지 않습니다.
~~~~

Refresh the companion hash for `docs/reference/data.md` (Global Constraints command). Append to the `internal/CLAUDE.md` section:

```markdown
- `audit/`: `RequestRef.Change` (`ChangeRef`, append-only `omitempty` at the end of `request`) carries admin change evidence — target, before/after revisions, changed names — on `admin_observability_changed`.
```

- [ ] **Step 4: Run**

```bash
go test -buildvcs=false ./internal/audit/ -race -count=1 > $S/t6.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|panic)' $S/t6.log
go test -buildvcs=false ./internal/analytics/ -run 'AuditObservable|ObservabilityChangeRecords' -race -count=1 > $S/t6b.log 2>&1; echo "analytics=$?"; grep -E '^(ok|FAIL|---|panic)' $S/t6b.log
python3 scripts/check_docs_translations.py > $S/t6-docs.log 2>&1; echo "docs=$?"
gofmt -l internal/audit internal/analytics
```
Expected: `exit=0`, `analytics=0`, `docs=0`, no gofmt output.

- [ ] **Step 5: Commit**

```bash
git -C $W add internal/audit/record.go internal/audit/change.go internal/audit/change_test.go internal/analytics/observability_change_test.go docs/reference/data.md docs/reference/data.ko.md internal/CLAUDE.md
git -C $W commit -s -m "feat(audit): append-only admin change evidence on request" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 7: Status API, console rule model and full-admin mounting

**Files:**
- Create: `internal/server/fulladmin.go`, `internal/server/fulladmin_test.go`
- Create: `internal/server/observabilityapi/observabilityapi.go`, `console.go`, `status.go`, `status_test.go`
- Modify: `internal/CLAUDE.md` (one bullet)

**Interfaces:**
- Consumes: `otelexport.Set.Status`, `.Limits`, `.SignalStats`, `.Exporting`, `.SamplePercent`, `otelexport.EncodeLimits`, `Revision` (Tasks 2–3); `policy.ParseWireDocs`, `policy.Store` methods (Task 1); server-internal `AdminAuth`, `requireAdmin`, `adminDenialEmitter`.
- Produces:
  - `func server.WithFullAdminRoutes(admin http.Handler, routes map[string]http.Handler, tokens []string, verifier OIDCVerifier, mapping adminauth.MappingConfig, aud *audit.Writer) http.Handler`
  - `observabilityapi.Options{Set *otelexport.Set; Policies PolicyStore; PolicySource, StateFile, PolicyFile string; ContentRules bool; Legacy *LegacyExporter; Emit func(audit.Record); AuditReady func() (bool, string)}`
  - `type PolicyStore interface { Policies() []*policy.Policy; CheckLocal([]*policy.Policy) error; Reload() error }`
  - `type LegacyExporter struct { Endpoint, Protocol string; TLS bool; SampleRatio float64 }`
  - `const SourceFiles = "files"`, `SourceControlPlane = "control_plane"`, `SourceNone = "none"`
  - `func New(opts Options) *Console`, `func (c *Console) Routes() map[string]http.Handler` (Task 7: `GET /admin/observability`)
  - `type Rule struct { Name string; Subject Subject; Traces *TraceTarget; Audit, Content *SignalTarget }`, `Subject{Team, User}`, `TraceTarget{Exporter string; SamplePercent int}`, `SignalTarget{Exporter string}`
  - Status JSON: `{"configured","policy_source","service_name","counters_since","limits":{"editable","reason","revision"},"rules":{"editable","reason","revision","content_rules","console":[Rule]},"exporters":[ExporterStatus],"legacy_otel":{"endpoint","protocol","tls","sample_ratio","stats"},"decisions":[DecisionStats],"selections":[{"policy","rule","source","subject","targets":[{"signal","exporter","sample_percent","effective_percent","exported"}]}]}`

- [ ] **Step 1: Write the failing tests**

`internal/server/fulladmin_test.go`:

```go
package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/audit"
)

// Review Focus 5: every route, reads included, is full-admin only; a mapped
// member gets 403 and an admin_denied record; anything else falls through.
func TestFullAdminRoutesGateEveryRoute(t *testing.T) {
	var reached []string
	record := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = append(reached, r.Method+" "+r.URL.Path) })
	routes := map[string]http.Handler{
		"GET /admin/observability":        record,
		"PUT /admin/observability/limits": record,
		"PUT /admin/observability/rules":  record,
	}
	adminCalls := 0
	admin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { adminCalls++; http.NotFound(w, r) })
	var lines bytes.Buffer
	aud, err := audit.NewWriter("routes-test", filepath.Join(t.TempDir(), "wal"), []audit.Sink{audit.NewWriterSink("lines", &lines, true)})
	if err != nil {
		t.Fatal(err)
	}
	verifier := &fakeVerifier{claims: adminauth.Claims{Subject: "fixture-member", Groups: []string{"members"}}}
	mapping := adminauth.MappingConfig{GroupMappings: []adminauth.GroupMapping{{Group: "members", Teams: []string{"fixture"}}}}
	mux := WithFullAdminRoutes(admin, routes, []string{"fixture-admin"}, verifier, mapping, aud)
	do := func(method, path, bearer string) int {
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	for pattern := range routes {
		method, path, _ := strings.Cut(pattern, " ")
		if got := do(method, path, ""); got != http.StatusUnauthorized {
			t.Errorf("%s without a credential = %d, want 401", pattern, got)
		}
		if got := do(method, path, jwtShaped); got != http.StatusForbidden {
			t.Errorf("%s as a team-mapped member = %d, want 403", pattern, got)
		}
		if got := do(method, path, "fixture-admin"); got != http.StatusOK {
			t.Errorf("%s as full admin = %d, want 200", pattern, got)
		}
	}
	if len(reached) != len(routes) {
		t.Fatalf("handlers reached = %v, want only the full-admin calls", reached)
	}
	if got := do("GET", "/admin/keys", "fixture-admin"); got != http.StatusNotFound || adminCalls != 1 {
		t.Fatalf("other admin paths must fall through: status %d, admin calls %d", got, adminCalls)
	}
	if got := do("POST", "/admin/observability", "fixture-admin"); got != http.StatusNotFound || adminCalls != 2 {
		t.Fatalf("an unregistered method must fall through: status %d, admin calls %d", got, adminCalls)
	}
	if err := aud.Close(); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(lines.String(), `"event":"admin_denied"`); n != len(routes) {
		t.Fatalf("admin_denied records = %d, want %d", n, len(routes))
	}
}

func TestFullAdminRoutesWithoutRoutesIsTheAdminMux(t *testing.T) {
	calls := 0
	admin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ })
	h := WithFullAdminRoutes(admin, nil, nil, nil, adminauth.MappingConfig{}, nil)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/admin/observability", nil))
	if calls != 1 {
		t.Fatal("no routes must leave the admin handler in place")
	}
}
```

`internal/server/observabilityapi/status_test.go`:

```go
package observabilityapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/metrics"
	"github.com/inferplane/inferplane/internal/otelexport"
	"github.com/inferplane/inferplane/internal/policy"
)

const operatorDoc = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata:
  name: engineering-observability
spec:
  subject:
    team: engineering
  rules:
  - name: export
    failurePolicy: FailOpen
    observability:
      traces: { exporter: corp, samplePercent: 20 }
      audit: { exporter: corp }
`

const consoleDoc = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata:
  name: console-platform
spec:
  subject:
    team: platform
  rules:
  - name: export
    failurePolicy: FailOpen
    observability:
      audit: { exporter: corp }
`

const foreignDoc = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata:
  name: console-money
spec:
  subject:
    team: platform
  rules:
  - name: money
    failurePolicy: FailOpen
    budget:
      limitMilliUSD: 10000
`

func newSet(t *testing.T) *otelexport.Set {
	t.Helper()
	s, err := otelexport.New("console-test", []otelexport.Exporter{
		{Name: "corp", Endpoint: "127.0.0.1:1", Insecure: true, Headers: map[string]string{"authorization": "Bearer console-secret"}, Signals: []string{"traces", "audit"}},
		{Name: "vault", Endpoint: "127.0.0.1:1", Insecure: true, Signals: []string{"content"}, DataBoundary: "internal", MaxContentBytes: 1 << 20, QueueBytes: 4 << 20},
	}, metrics.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	return s
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// consoleStore loads an operator policy file and the console file through a
// real policy.Store with the set's ceiling, the way mayu assembles them.
func consoleStore(t *testing.T, set *otelexport.Set, operator, console string) (store *policy.Store, operatorPath, consolePath string) {
	t.Helper()
	dir := t.TempDir()
	operatorPath, consolePath = filepath.Join(dir, "operator.yaml"), filepath.Join(dir, "console-observability.yaml")
	writeFile(t, operatorPath, operator)
	writeFile(t, consolePath, console)
	store, err := policy.NewStore(operatorPath, consolePath)
	if err != nil {
		t.Fatal(err)
	}
	store.SetObservabilityCeiling(set.Allows)
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	return store, operatorPath, consolePath
}

func getStatus(t *testing.T, c *Console) (map[string]any, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	c.Routes()["GET /admin/observability"].ServeHTTP(rec, httptest.NewRequest("GET", "/admin/observability", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out, rec.Body.String()
}

func TestStatusWithoutExportersOrPolicyStore(t *testing.T) {
	st, _ := getStatus(t, New(Options{}))
	if st["configured"] != false || st["policy_source"] != "none" {
		t.Fatalf("status = %v", st)
	}
	for _, part := range []string{"limits", "rules"} {
		if e := st[part].(map[string]any); e["editable"] != false || e["reason"] != "not_configured" {
			t.Fatalf("%s = %v", part, e)
		}
	}
	for _, k := range []string{"exporters", "decisions", "selections"} {
		if v, ok := st[k].([]any); !ok || len(v) != 0 {
			t.Fatalf("%s = %v, want an empty list", k, st[k])
		}
	}
}

func TestStatusReportsExportersSelectionsAndConsoleRules(t *testing.T) {
	set := newSet(t)
	if _, err := set.SetLimits(otelexport.Limits{"corp": {MaxSamplePercent: 10}}); err != nil {
		t.Fatal(err)
	}
	store, _, consolePath := consoleStore(t, set, operatorDoc, consoleDoc)
	c := New(Options{Set: set, Policies: store, PolicySource: SourceFiles, StateFile: filepath.Join(t.TempDir(), "limits.json"),
		PolicyFile: consolePath, Legacy: &LegacyExporter{Endpoint: "localhost:4318", Protocol: "http", SampleRatio: 0.25}})
	st, raw := getStatus(t, c)
	if strings.Contains(raw, "console-secret") {
		t.Fatalf("status leaked a header value: %s", raw)
	}
	if st["configured"] != true || st["policy_source"] != "files" || st["service_name"] != "console-test" {
		t.Fatalf("status = %v", st)
	}
	exporters := st["exporters"].([]any)
	corp := exporters[0].(map[string]any)
	if len(exporters) != 2 || corp["name"] != "corp" || corp["header_names"].([]any)[0] != "authorization" {
		t.Fatalf("exporters = %v", exporters)
	}
	limits := st["limits"].(map[string]any)
	if limits["editable"] != true || limits["revision"] != otelexport.Revision(otelexport.EncodeLimits(set.Limits())) {
		t.Fatalf("limits = %v", limits)
	}
	rules := st["rules"].(map[string]any)
	fileBytes, _ := os.ReadFile(consolePath)
	console := rules["console"].([]any)
	if rules["editable"] != true || rules["revision"] != otelexport.Revision(fileBytes) || len(console) != 1 ||
		console[0].(map[string]any)["name"] != "console-platform" {
		t.Fatalf("rules = %v", rules)
	}
	sources := map[string]string{}
	var engineering map[string]any
	for _, s := range st["selections"].([]any) {
		sel := s.(map[string]any)
		sources[sel["policy"].(string)] = sel["source"].(string)
		if sel["policy"] == "engineering-observability" {
			engineering = sel
		}
	}
	if sources["console-platform"] != "console" || sources["engineering-observability"] != "files" {
		t.Fatalf("selection sources = %v", sources)
	}
	traces := engineering["targets"].([]any)[0].(map[string]any)
	if traces["signal"] != "traces" || traces["sample_percent"] != float64(20) || traces["effective_percent"] != float64(10) || traces["exported"] != true {
		t.Fatalf("engineering traces target = %v", traces)
	}
	legacy := st["legacy_otel"].(map[string]any)
	if legacy["endpoint"] != "localhost:4318" || legacy["sample_ratio"] != 0.25 || legacy["stats"].(map[string]any)["signal"] != "traces" {
		t.Fatalf("legacy = %v", legacy)
	}
}

func TestStatusRuleReadOnlyReasons(t *testing.T) {
	set := newSet(t)
	store, _, consolePath := consoleStore(t, set, operatorDoc, "")
	for _, tc := range []struct {
		name string
		opts Options
		want string
	}{
		{"control plane", Options{Set: set, Policies: store, PolicySource: SourceControlPlane}, "control_plane"},
		{"no policy file", Options{Set: set, Policies: store, PolicySource: SourceFiles}, "no_policy_file"},
		{"no state file for limits", Options{Set: set, Policies: store, PolicySource: SourceFiles, PolicyFile: consolePath}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := getStatus(t, New(tc.opts))
			rules := st["rules"].(map[string]any)
			if tc.want == "" {
				if rules["editable"] != true || st["limits"].(map[string]any)["reason"] != "no_state_file" {
					t.Fatalf("rules = %v, limits = %v", rules, st["limits"])
				}
				return
			}
			if rules["editable"] != false || rules["reason"] != tc.want {
				t.Fatalf("rules = %v, want reason %s", rules, tc.want)
			}
		})
	}
	foreignStore, _, foreignPath := consoleStore(t, set, operatorDoc, foreignDoc)
	st, _ := getStatus(t, New(Options{Set: set, Policies: foreignStore, PolicySource: SourceFiles, PolicyFile: foreignPath}))
	if rules := st["rules"].(map[string]any); rules["editable"] != false || rules["reason"] != "foreign_documents" || len(rules["console"].([]any)) != 0 {
		t.Fatalf("foreign console file: rules = %v", rules)
	}
}
```

- [ ] **Step 2: Run — expect compile failure**

```bash
go test -buildvcs=false ./internal/server/ ./internal/server/observabilityapi/ -run 'FullAdmin|Status' -race -count=1 > $S/t7.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|panic)|undefined|no Go files|cannot find' $S/t7.log
```
Expected: `exit=1`, `undefined: WithFullAdminRoutes` and the missing `observabilityapi` package.

- [ ] **Step 3: Implement**

`internal/server/fulladmin.go`:

```go
package server

import (
	"net/http"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/audit"
)

// WithFullAdminRoutes layers extra routes in front of mayu's AdminMux without
// growing its positional signature — the WithEvaluations pattern, used by the
// observability console (sub-project B). Every route runs behind AdminAuth and
// requireAdmin, reads included, so a team-mapped identity gets 403 and an
// admin_denied record. Anything else, including an unregistered method on a
// registered path, falls through to admin. No routes leaves admin unchanged.
func WithFullAdminRoutes(admin http.Handler, routes map[string]http.Handler, tokens []string, verifier OIDCVerifier, mapping adminauth.MappingConfig, aud *audit.Writer) http.Handler {
	if len(routes) == 0 {
		return admin
	}
	var emit func(audit.Record)
	if aud != nil {
		emit = aud.Append
	}
	denied := adminDenialEmitter(emit)
	mux := http.NewServeMux()
	for pattern, h := range routes {
		mux.Handle(pattern, AdminAuth(tokens, verifier, mapping, denied, requireAdmin(h, emit)))
	}
	mux.Handle("/", admin)
	return mux
}
```

`internal/server/observabilityapi/observabilityapi.go`:

```go
// Package observabilityapi is the observability console API (sub-project B of
// the console work): GET /admin/observability reports governed OTLP export
// status, and the edit routes narrow export or replace console-owned rules,
// dry run first. Console.Routes is mounted by server.WithFullAdminRoutes, so
// every route, reads included, needs a full administrator.
//
// The bootstrap config bounds every edit. Runtime limits only narrow the
// declared exporter ceiling (otelexport.Limit); console rules are
// GovernancePolicy documents in a console-owned file that the local policy
// file channel loads, so the existing ceiling check applies and, with a
// control plane, the control plane stays the only policy source. No route
// names, changes or reveals an endpoint credential.
package observabilityapi

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/otelexport"
	"github.com/inferplane/inferplane/internal/policy"
)

// Policy sources the status reports.
const (
	SourceFiles        = "files"
	SourceControlPlane = "control_plane"
	SourceNone         = "none"
	sourceConsole      = "console"
)

// legacyExporter is tracing.LegacyExporter, the legacy `otel` block's name.
const legacyExporter = "otel"

// PolicyStore is the part of *policy.Store the console uses. Reload is called
// only after the console wrote its own policy file.
type PolicyStore interface {
	Policies() []*policy.Policy
	CheckLocal(policies []*policy.Policy) error
	Reload() error
}

// LegacyExporter describes the legacy `otel` block for the status view.
type LegacyExporter struct {
	Endpoint    string  `json:"endpoint"`
	Protocol    string  `json:"protocol"`
	TLS         bool    `json:"tls"`
	SampleRatio float64 `json:"sample_ratio"`
}

// Options wires one console. A nil Set reports configured=false, an empty
// StateFile keeps limits read-only and an empty PolicyFile keeps rules
// read-only. Policies must be a nil interface — not a nil *policy.Store —
// when the gateway has no policy store.
type Options struct {
	Set          *otelexport.Set
	Policies     PolicyStore
	PolicySource string // SourceFiles | SourceControlPlane | SourceNone
	StateFile    string
	PolicyFile   string
	ContentRules bool
	Legacy       *LegacyExporter
	Emit         func(audit.Record)
	AuditReady   func() (bool, string) // nil = ready
}

// Console serves the observability console routes. Edits are serialized.
type Console struct {
	opts Options
	mu   sync.Mutex
}

// New builds a console over opts.
func New(opts Options) *Console { return &Console{opts: opts} }

// Routes maps each console route pattern to its handler, for
// server.WithFullAdminRoutes.
func (c *Console) Routes() map[string]http.Handler {
	return map[string]http.Handler{
		"GET /admin/observability": http.HandlerFunc(c.status),
	}
}

func (c *Console) source() string {
	if c.opts.PolicySource == "" {
		return SourceNone
	}
	return c.opts.PolicySource
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
```

`internal/server/observabilityapi/console.go`:

```go
package observabilityapi

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/policy"
)

// consoleRuleName is the single rule of every console document.
const consoleRuleName = "export"

// consoleNameRE is a console document name: "console-" and a DNS-label tail,
// at most 63 characters in total.
var consoleNameRE = regexp.MustCompile(`^console-[a-z0-9]([-a-z0-9]{0,53}[a-z0-9])?$`)

// errForeign marks a console policy file that holds something other than
// console documents (a hand edit, or another tool): the console keeps it
// read-only rather than overwrite it.
var errForeign = errors.New("the console policy file holds documents the console does not manage")

// Subject is a console rule's policy subject.
type Subject struct {
	Team string `json:"team,omitempty"`
	User string `json:"user,omitempty"`
}

// TraceTarget exports the subject's spans to Exporter at SamplePercent (1..100).
type TraceTarget struct {
	Exporter      string `json:"exporter"`
	SamplePercent int    `json:"sample_percent"`
}

// SignalTarget exports one log signal to Exporter.
type SignalTarget struct {
	Exporter string `json:"exporter"`
}

// Rule is one console-owned selection, stored as a GovernancePolicy document
// with a single FailOpen observability rule named "export".
type Rule struct {
	Name    string        `json:"name"`
	Subject Subject       `json:"subject"`
	Traces  *TraceTarget  `json:"traces,omitempty"`
	Audit   *SignalTarget `json:"audit,omitempty"`
	Content *SignalTarget `json:"content,omitempty"`
}

// ruleFromDoc reads a console document back; any other shape is errForeign.
func ruleFromDoc(d v1alpha1.GovernancePolicy) (Rule, error) {
	if !consoleNameRE.MatchString(d.Metadata.Name) || len(d.Spec.Rules) != 1 {
		return Rule{}, errForeign
	}
	wr := d.Spec.Rules[0]
	if wr.Name != consoleRuleName || wr.FailurePolicy != v1alpha1.FailOpen || wr.Observability == nil ||
		wr.Budget != nil || wr.Routing != nil || wr.ModelAccess != nil || wr.Rate != nil || wr.SensitiveData != nil || wr.TokenQuota != nil {
		return Rule{}, errForeign
	}
	r := Rule{Name: d.Metadata.Name, Subject: Subject{Team: d.Spec.Subject.Team, User: d.Spec.Subject.User}}
	o := wr.Observability
	if o.Traces != nil {
		pct := 100
		if o.Traces.SamplePercent != nil {
			pct = *o.Traces.SamplePercent
		}
		r.Traces = &TraceTarget{Exporter: o.Traces.Exporter, SamplePercent: pct}
	}
	if o.Audit != nil {
		r.Audit = &SignalTarget{Exporter: o.Audit.Exporter}
	}
	if o.Content != nil {
		r.Content = &SignalTarget{Exporter: o.Content.Exporter}
	}
	return r, nil
}

// readConsole parses the console policy file into rules sorted by name and
// returns its bytes, the rules revision input. A missing file is no rules.
func (c *Console) readConsole() ([]Rule, []byte, error) {
	b, err := os.ReadFile(c.opts.PolicyFile)
	if errors.Is(err, fs.ErrNotExist) {
		return []Rule{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	docs, err := policy.ParseWireDocs(b)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errForeign, err)
	}
	rules := make([]Rule, 0, len(docs))
	for _, d := range docs {
		r, err := ruleFromDoc(d)
		if err != nil {
			return nil, nil, err
		}
		rules = append(rules, r)
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Name < rules[j].Name })
	return rules, b, nil
}
```

`internal/server/observabilityapi/status.go`:

```go
package observabilityapi

import (
	"errors"
	"net/http"
	"sort"

	"github.com/inferplane/inferplane/internal/otelexport"
	"github.com/inferplane/inferplane/internal/policy"
)

// editState says whether one editor is usable and, when it is, which
// revision an apply must name. Reason is why not: not_configured,
// no_state_file, no_policy_file, control_plane, foreign_documents,
// read_failed.
type editState struct {
	Editable bool   `json:"editable"`
	Reason   string `json:"reason,omitempty"`
	Revision string `json:"revision,omitempty"`
}

type rulesState struct {
	editState
	ContentRules bool   `json:"content_rules"`
	Console      []Rule `json:"console"`
}

type legacyStatus struct {
	LegacyExporter
	Stats otelexport.SignalStats `json:"stats"`
}

// Selection is one observability rule the gateway enforces, from any source.
type Selection struct {
	Policy  string   `json:"policy"`
	Rule    string   `json:"rule"`
	Source  string   `json:"source"` // console | files | control_plane
	Subject Subject  `json:"subject"`
	Targets []Target `json:"targets"`
}

// Target is one signal → exporter selection and what the limits do with it.
type Target struct {
	Signal           string `json:"signal"`
	Exporter         string `json:"exporter"`
	SamplePercent    int    `json:"sample_percent,omitempty"`
	EffectivePercent int    `json:"effective_percent,omitempty"`
	Exported         bool   `json:"exported"`
}

type statusResponse struct {
	Configured    bool                        `json:"configured"`
	PolicySource  string                      `json:"policy_source"`
	ServiceName   string                      `json:"service_name,omitempty"`
	CountersSince string                      `json:"counters_since,omitempty"`
	Limits        editState                   `json:"limits"`
	Rules         rulesState                  `json:"rules"`
	Exporters     []otelexport.ExporterStatus `json:"exporters"`
	LegacyOTel    *legacyStatus               `json:"legacy_otel,omitempty"`
	Decisions     []otelexport.DecisionStats  `json:"decisions"`
	Selections    []Selection                 `json:"selections"`
}

func (c *Console) status(w http.ResponseWriter, _ *http.Request) {
	st := c.opts.Set.Status()
	resp := statusResponse{
		Configured: c.opts.Set != nil, PolicySource: c.source(),
		ServiceName: st.ServiceName, CountersSince: st.CountersSince,
		Limits: c.limitsState(), Rules: c.rulesState(),
		Exporters: st.Exporters, Decisions: st.Decisions,
	}
	if l := c.opts.Legacy; l != nil {
		resp.LegacyOTel = &legacyStatus{LegacyExporter: *l, Stats: c.opts.Set.SignalStats(legacyExporter, otelexport.SignalTraces)}
	}
	console := map[string]bool{}
	for _, r := range resp.Rules.Console {
		console[r.Name] = true
	}
	resp.Selections = c.selections(c.opts.Set.Limits(), console)
	writeJSON(w, http.StatusOK, resp)
}

func (c *Console) limitsState() editState {
	switch {
	case c.opts.Set == nil:
		return editState{Reason: "not_configured"}
	case c.opts.StateFile == "":
		return editState{Reason: "no_state_file", Revision: otelexport.Revision(otelexport.EncodeLimits(c.opts.Set.Limits()))}
	}
	return editState{Editable: true, Revision: otelexport.Revision(otelexport.EncodeLimits(c.opts.Set.Limits()))}
}

// rulesReadOnly is why console rules cannot be edited here, or "".
func (c *Console) rulesReadOnly() string {
	switch {
	case c.opts.Set == nil:
		return "not_configured"
	case c.source() == SourceControlPlane:
		return "control_plane"
	case c.opts.PolicyFile == "" || c.opts.Policies == nil:
		return "no_policy_file"
	}
	return ""
}

func (c *Console) rulesState() rulesState {
	out := rulesState{ContentRules: c.opts.ContentRules, Console: []Rule{}}
	if reason := c.rulesReadOnly(); reason != "" {
		out.Reason = reason
		return out
	}
	rules, b, err := c.readConsole()
	switch {
	case errors.Is(err, errForeign):
		out.Reason = "foreign_documents"
	case err != nil:
		out.Reason = "read_failed"
	default:
		out.Editable, out.Revision, out.Console = true, otelexport.Revision(b), rules
	}
	return out
}

// selections lists every observability rule in the policy store under lim.
// Documents named in console come from the console file.
func (c *Console) selections(lim otelexport.Limits, console map[string]bool) []Selection {
	out := []Selection{}
	if c.opts.Policies == nil {
		return out
	}
	for _, p := range c.opts.Policies.Policies() {
		source := c.source()
		if console[p.Name] {
			source = sourceConsole
		}
		for _, r := range p.Rules {
			if r.Observability == nil {
				continue
			}
			out = append(out, Selection{Policy: p.Name, Rule: r.Name, Source: source,
				Subject: Subject{Team: p.Subject.Team, User: p.Subject.User}, Targets: c.targets(lim, r.Observability)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Policy != out[j].Policy {
			return out[i].Policy < out[j].Policy
		}
		return out[i].Rule < out[j].Rule
	})
	return out
}

// targets reports each selection of one rule under lim, in signal order.
func (c *Console) targets(lim otelexport.Limits, o *policy.Observability) []Target {
	set := c.opts.Set
	var out []Target
	if o.Traces != nil {
		out = append(out, Target{Signal: policy.SignalTraces, Exporter: o.Traces.Exporter, SamplePercent: o.Traces.SamplePercent,
			EffectivePercent: set.SamplePercent(lim, o.Traces.Exporter, o.Traces.SamplePercent),
			Exported:         set.Exporting(lim, o.Traces.Exporter, policy.SignalTraces)})
	}
	if o.Audit != "" {
		out = append(out, Target{Signal: policy.SignalAudit, Exporter: o.Audit, Exported: set.Exporting(lim, o.Audit, policy.SignalAudit)})
	}
	if o.Content != "" {
		out = append(out, Target{Signal: policy.SignalContent, Exporter: o.Content, Exported: set.Exporting(lim, o.Content, policy.SignalContent)})
	}
	return out
}
```

Append to the `internal/CLAUDE.md` section:

```markdown
- `server/fulladmin.go` + `server/observabilityapi/`: `WithFullAdminRoutes` layers extra routes behind AdminAuth + requireAdmin (reads included) in front of AdminMux without touching its signature; `observabilityapi.Console.Routes` is the console API. The status carries header names, never values; `Options.Policies` must stay a nil interface when there is no store; console documents are read back with `ruleFromDoc` and anything else marks the file `foreign_documents`.
```

- [ ] **Step 4: Run**

```bash
go test -buildvcs=false ./internal/server/ ./internal/server/observabilityapi/ -race -count=1 > $S/t7.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|panic)' $S/t7.log
go vet -buildvcs=false ./internal/server/... > $S/t7-vet.log 2>&1; echo "vet=$?"
gofmt -l internal/server
```
Expected: `exit=0`, `vet=0`, `ok` for `internal/server` and `internal/server/observabilityapi`, no gofmt output.

- [ ] **Step 5: Commit**

```bash
git -C $W add internal/server/fulladmin.go internal/server/fulladmin_test.go internal/server/observabilityapi internal/CLAUDE.md
git -C $W commit -s -m "feat(server): observability console status API behind full-admin routes" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 8: Runtime limits API, state file and boot load

**Files:**
- Create: `internal/server/observabilityapi/edit.go`, `internal/server/observabilityapi/limits.go`, `internal/server/observabilityapi/limits_test.go`
- Modify: `internal/server/observabilityapi/observabilityapi.go` (`Routes` gains the limits route)
- Modify: `docs/reference/data.md` / `docs/reference/data.ko.md` (append a paragraph to the Task 6 section), `internal/CLAUDE.md` (one bullet)

**Interfaces:**
- Consumes: `otelexport.Set.Normalize`, `SetLimits`, `Limits`, `Clamp`, `Status`, `Exporting`, `SamplePercent`; `EncodeLimits`, `DecodeLimits`, `Revision` (Task 3); `audit.ChangeRef` (Task 6); `principal.AdminFrom`; `ulid.New`.
- Produces:
  - `PUT /admin/observability/limits` — request `{"dry_run": bool, "expected_revision": "sha256:…", "exporters": {"<name>": {"paused": bool, "signals": [..], "max_content_bytes": n, "max_sample_percent": n}}}`; 200 `{"dry_run", "current_revision", "result_revision", "changes": [{"exporter","field","before","after"}], "effects": [{"policy","rule","signal","exporter","before":{"exported","sample_percent"},"after":{…}}]}`; errors 400 `invalid_request`/`invalid_limits`, 409 `read_only`/`revision_conflict`/`limits_file_changed`, 413 `invalid_request`, 500 `persist_failed`/`read_failed`, 503 `audit_unavailable`
  - `func LoadLimits(path string, set *otelexport.Set) ([]string, error)`
  - package-private `apiError`, `writeError`, `decode`, `checkRevision`, `(*Console).auditReady`, `(*Console).emitChange`, `writeFileAtomic`

- [ ] **Step 1: Write the failing tests**

`internal/server/observabilityapi/limits_test.go`:

```go
package observabilityapi

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/otelexport"
	"github.com/inferplane/inferplane/internal/principal"
)

// put calls one console route as a full administrator and decodes the reply.
func put(t *testing.T, c *Console, pattern string, body any) (int, map[string]any) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	h, ok := c.Routes()[pattern]
	if !ok {
		t.Fatalf("no route %s", pattern)
	}
	method, path, _ := strings.Cut(pattern, " ")
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req = req.WithContext(principal.WithAdmin(req.Context(), principal.AdminIdentity{Subject: "admin-sub", IsAdmin: true, AuthMethod: "oidc"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: status %d, body %q", pattern, rec.Code, rec.Body)
	}
	return rec.Code, out
}

type limitsHarness struct {
	set     *otelexport.Set
	console *Console
	state   string
	records *[]audit.Record
}

func newLimitsHarness(t *testing.T, edit func(*Options)) limitsHarness {
	t.Helper()
	records := &[]audit.Record{}
	opts := Options{Set: newSet(t), PolicySource: SourceNone, StateFile: filepath.Join(t.TempDir(), "observability-limits.json"),
		Emit: func(r audit.Record) { *records = append(*records, r) }}
	if edit != nil {
		edit(&opts)
	}
	return limitsHarness{set: opts.Set, console: New(opts), state: opts.StateFile, records: records}
}

func (h limitsHarness) revision(t *testing.T) string {
	t.Helper()
	st, _ := getStatus(t, h.console)
	return st["limits"].(map[string]any)["revision"].(string)
}

const limitsRoute = "PUT /admin/observability/limits"

func pauseCorp(dryRun bool, revision string) map[string]any {
	return map[string]any{"dry_run": dryRun, "expected_revision": revision, "exporters": map[string]any{"corp": map[string]any{"paused": true}}}
}

func TestLimitsDryRunChangesNothing(t *testing.T) {
	h := newLimitsHarness(t, nil)
	code, res := put(t, h.console, limitsRoute, pauseCorp(true, h.revision(t)))
	if code != 200 || res["dry_run"] != true {
		t.Fatalf("dry run = %d %v", code, res)
	}
	changes := res["changes"].([]any)
	if len(changes) != 1 {
		t.Fatalf("changes = %v", changes)
	}
	if first := changes[0].(map[string]any); first["exporter"] != "corp" || first["field"] != "paused" || first["before"] != false || first["after"] != true {
		t.Fatalf("change = %v", first)
	}
	if _, err := os.Stat(h.state); !os.IsNotExist(err) {
		t.Fatalf("a dry run wrote the state file: %v", err)
	}
	if len(h.set.Limits()) != 0 || len(*h.records) != 0 {
		t.Fatal("a dry run installed or audited something")
	}
}

func TestLimitsApplyPersistsInstallsAndAudits(t *testing.T) {
	h := newLimitsHarness(t, nil)
	before := h.revision(t)
	code, res := put(t, h.console, limitsRoute, pauseCorp(false, before))
	if code != 200 || res["dry_run"] != false {
		t.Fatalf("apply = %d %v", code, res)
	}
	want := otelexport.EncodeLimits(otelexport.Limits{"corp": {Paused: true}})
	got, err := os.ReadFile(h.state)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("state file = %q (%v), want %q", got, err, want)
	}
	if info, err := os.Stat(h.state); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode = %v (%v)", info.Mode().Perm(), err)
	}
	if !h.set.Limits()["corp"].Paused || res["result_revision"] != otelexport.Revision(want) {
		t.Fatalf("installed = %+v, result = %v", h.set.Limits(), res)
	}
	if len(*h.records) != 1 {
		t.Fatalf("audit records = %d", len(*h.records))
	}
	rec := (*h.records)[0]
	ch := rec.Request.Change
	if rec.Event != "admin_observability_changed" || ch == nil || ch.Target != "observability_limits" || ch.Before != before ||
		ch.After != res["result_revision"] || strings.Join(ch.Names, ",") != "corp" ||
		rec.Principal.User == nil || *rec.Principal.User != "admin-sub" || *rec.Principal.AuthMethod != "oidc" {
		t.Fatalf("audit record = %+v (change %+v)", rec, ch)
	}
	code, res = put(t, h.console, limitsRoute, pauseCorp(false, res["result_revision"].(string)))
	if code != 200 || len(res["changes"].([]any)) != 0 || len(*h.records) != 1 {
		t.Fatalf("a no-op apply = %d %v, records %d", code, res, len(*h.records))
	}
}

func TestLimitsApplyNeedsTheCurrentRevision(t *testing.T) {
	h := newLimitsHarness(t, nil)
	if code, res := put(t, h.console, limitsRoute, pauseCorp(false, "")); code != 400 || res["code"] != "invalid_request" {
		t.Fatalf("apply without a revision = %d %v", code, res)
	}
	code, res := put(t, h.console, limitsRoute, pauseCorp(false, "sha256:stale"))
	if code != 409 || res["code"] != "revision_conflict" || res["current_revision"] != h.revision(t) {
		t.Fatalf("stale apply = %d %v", code, res)
	}
	if len(h.set.Limits()) != 0 || len(*h.records) != 0 {
		t.Fatal("a refused apply changed something")
	}
}

func TestLimitsRefuseWidening(t *testing.T) {
	h := newLimitsHarness(t, nil)
	rev := h.revision(t)
	for name, exporters := range map[string]map[string]any{
		"undeclared exporter": {"other": map[string]any{"paused": true}},
		"undeclared signal":   {"corp": map[string]any{"signals": []string{"content"}}},
		"raised content cap":  {"vault": map[string]any{"max_content_bytes": 2 << 20}},
	} {
		code, res := put(t, h.console, limitsRoute, map[string]any{"dry_run": true, "expected_revision": rev, "exporters": exporters})
		if code != 400 || res["code"] != "invalid_limits" {
			t.Errorf("%s: %d %v", name, code, res)
		}
	}
	if code, res := put(t, h.console, limitsRoute, map[string]any{"dry_run": true, "endpoint": "evil.example:4318"}); code != 400 || res["code"] != "invalid_request" {
		t.Fatalf("an unknown field (an endpoint) = %d %v", code, res)
	}
}

func TestLimitsReadOnlyWithoutStateFile(t *testing.T) {
	h := newLimitsHarness(t, func(o *Options) { o.StateFile = "" })
	if code, res := put(t, h.console, limitsRoute, pauseCorp(true, "")); code != 409 || res["code"] != "read_only" || res["reason"] != "no_state_file" {
		t.Fatalf("read-only = %d %v", code, res)
	}
}

func TestLimitsRefuseAfterAHandEdit(t *testing.T) {
	h := newLimitsHarness(t, nil)
	code, res := put(t, h.console, limitsRoute, pauseCorp(false, h.revision(t)))
	if code != 200 {
		t.Fatalf("apply = %d %v", code, res)
	}
	writeFile(t, h.state, `{"version":1,"exporters":{"vault":{"paused":true}}}`)
	code, res = put(t, h.console, limitsRoute, map[string]any{"dry_run": false, "expected_revision": res["result_revision"], "exporters": map[string]any{}})
	if code != 409 || res["code"] != "limits_file_changed" {
		t.Fatalf("apply over a hand edit = %d %v", code, res)
	}
	if !h.set.Limits()["corp"].Paused {
		t.Fatal("the refused apply changed the installed limits")
	}
}

func TestLimitsRefusedWhileAuditNotReady(t *testing.T) {
	h := newLimitsHarness(t, func(o *Options) { o.AuditReady = func() (bool, string) { return false, "wal" } })
	rev := h.revision(t)
	if code, _ := put(t, h.console, limitsRoute, pauseCorp(true, rev)); code != 200 {
		t.Fatalf("a dry run must still work: %d", code)
	}
	if code, res := put(t, h.console, limitsRoute, pauseCorp(false, rev)); code != 503 || res["code"] != "audit_unavailable" {
		t.Fatalf("apply = %d %v", code, res)
	}
	if _, err := os.Stat(h.state); !os.IsNotExist(err) || len(h.set.Limits()) != 0 {
		t.Fatal("an unrecordable change was made")
	}
}

func TestLimitsEffectsNameWithheldSelections(t *testing.T) {
	h := newLimitsHarness(t, func(o *Options) {
		store, _, _ := consoleStore(t, o.Set, operatorDoc, "")
		o.Policies, o.PolicySource = store, SourceFiles
	})
	code, res := put(t, h.console, limitsRoute, pauseCorp(true, h.revision(t)))
	if code != 200 {
		t.Fatalf("dry run = %d %v", code, res)
	}
	effects := res["effects"].([]any)
	if len(effects) != 2 {
		t.Fatalf("effects = %v", effects)
	}
	auditEffect, tracesEffect := effects[0].(map[string]any), effects[1].(map[string]any)
	if auditEffect["signal"] != "audit" || auditEffect["before"].(map[string]any)["exported"] != true || auditEffect["after"].(map[string]any)["exported"] != false {
		t.Fatalf("audit effect = %v", auditEffect)
	}
	if tracesEffect["signal"] != "traces" || tracesEffect["before"].(map[string]any)["sample_percent"] != float64(20) || tracesEffect["after"].(map[string]any)["exported"] != false {
		t.Fatalf("traces effect = %v", tracesEffect)
	}
}

// Review Focus 2, boot half: a damaged state file refuses boot; a missing one
// means no limits; entries the bootstrap no longer supports are clamped.
func TestLoadLimitsRefusesADamagedFile(t *testing.T) {
	set := newSet(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "observability-limits.json")
	if notes, err := LoadLimits(path, set); err != nil || notes != nil || len(set.Limits()) != 0 {
		t.Fatalf("missing file = %v, %v", notes, err)
	}
	writeFile(t, path, `{"version":1,"exporters":{"corp":{"pased":true}}}`)
	if _, err := LoadLimits(path, set); err == nil || !strings.Contains(err.Error(), "state_file") {
		t.Fatalf("damaged file = %v, want a boot error naming the state file", err)
	}
	writeFile(t, path, `{"version":1,"exporters":{"corp":{"paused":true},"gone":{"paused":true}}}`)
	notes, err := LoadLimits(path, set)
	if err != nil || len(notes) != 1 || !strings.Contains(notes[0], "gone") || !set.Limits()["corp"].Paused {
		t.Fatalf("clamped load = %v, %v, limits %+v", notes, err, set.Limits())
	}
	if _, err := LoadLimits(filepath.Join(dir, "missing-dir", "limits.json"), set); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("missing directory = %v", err)
	}
}
```

- [ ] **Step 2: Run — expect failure**

```bash
go test -buildvcs=false ./internal/server/observabilityapi/ -race -count=1 > $S/t8.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|panic)|undefined' $S/t8.log
```
Expected: `exit=1` (`undefined: LoadLimits`; the limits route is missing).

- [ ] **Step 3: Implement**

`internal/server/observabilityapi/edit.go`:

```go
package observabilityapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/pkg/ulid"
)

// apiError is the JSON error body: a human message (what the console's api()
// helper shows) and a stable code the console maps to translated text.
type apiError struct {
	Error           string `json:"error"`
	Code            string `json:"code"`
	Reason          string `json:"reason,omitempty"`
	CurrentRevision string `json:"current_revision,omitempty"`
}

func writeError(w http.ResponseWriter, status int, e apiError) { writeJSON(w, status, e) }

// decode reads one strict JSON object of at most max bytes into v: an unknown
// field (an endpoint, a header) is refused, never ignored.
func decode(w http.ResponseWriter, r *http.Request, max int64, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, max))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, apiError{Error: "request body too large", Code: "invalid_request"})
			return false
		}
		writeError(w, http.StatusBadRequest, apiError{Error: "the body must be one JSON object with known fields: " + err.Error(), Code: "invalid_request"})
		return false
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, apiError{Error: "the body has data after the JSON object", Code: "invalid_request"})
		return false
	}
	return true
}

// checkRevision enforces the optimistic precondition: an apply must name the
// revision its preview saw; a dry run may omit it.
func checkRevision(w http.ResponseWriter, dryRun bool, expected, current string) bool {
	if expected == "" && !dryRun {
		writeError(w, http.StatusBadRequest, apiError{Error: "expected_revision is required to apply; run a dry run first", Code: "invalid_request"})
		return false
	}
	if expected != "" && expected != current {
		writeError(w, http.StatusConflict, apiError{Error: "the state changed since it was read; reload and preview again", Code: "revision_conflict", CurrentRevision: current})
		return false
	}
	return true
}

// auditReady refuses an apply while the audit chain is not accepting records:
// a change that cannot be recorded is not made.
func (c *Console) auditReady(w http.ResponseWriter) bool {
	if c.opts.AuditReady == nil {
		return true
	}
	if ok, _ := c.opts.AuditReady(); ok {
		return true
	}
	writeError(w, http.StatusServiceUnavailable, apiError{Error: "the audit chain is not accepting records, so nothing was changed", Code: "audit_unavailable"})
	return false
}

// emitChange appends the admin_observability_changed record of one applied
// edit: the administrator from the request context and the revisions before
// and after. No state content enters the chain.
func (c *Console) emitChange(r *http.Request, target, before, after string, names []string) {
	if c.opts.Emit == nil {
		return
	}
	rec := audit.Record{
		SchemaVersion: 1,
		Event:         "admin_observability_changed",
		ID:            ulid.New(),
		TS:            time.Now().UTC().Format(time.RFC3339Nano),
		Request:       audit.RequestRef{Ingress: "admin", Change: &audit.ChangeRef{Target: target, Before: before, After: after, Names: names}},
	}
	if id, ok := principal.AdminFrom(r.Context()); ok {
		sub, method := id.Subject, id.AuthMethod
		rec.Principal = audit.PrincipalRef{User: &sub, AuthMethod: &method}
	}
	c.opts.Emit(rec)
}

// writeFileAtomic replaces path with data: a 0600 temp file in the same
// directory (extension .tmp, so a policy directory never loads it), fsync,
// rename, then a best-effort directory fsync. Readers see the old bytes or the
// new ones, never a torn file.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op after the rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
```

`internal/server/observabilityapi/limits.go`:

```go
package observabilityapi

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/inferplane/inferplane/internal/otelexport"
	"github.com/inferplane/inferplane/internal/policy"
)

const maxLimitsBody = 64 << 10

type limitsRequest struct {
	DryRun           bool              `json:"dry_run"`
	ExpectedRevision string            `json:"expected_revision"`
	Exporters        otelexport.Limits `json:"exporters"`
}

// limitChange is one changed field of one exporter in effective terms: a
// bootstrap default is shown as its value, never as an empty field.
type limitChange struct {
	Exporter string `json:"exporter"`
	Field    string `json:"field"` // paused | signals | max_content_bytes | max_sample_percent
	Before   any    `json:"before"`
	After    any    `json:"after"`
}

type targetState struct {
	Exported      bool `json:"exported"`
	SamplePercent int  `json:"sample_percent,omitempty"`
}

// limitEffect is one policy selection whose export the edit changes.
type limitEffect struct {
	Policy   string      `json:"policy"`
	Rule     string      `json:"rule"`
	Signal   string      `json:"signal"`
	Exporter string      `json:"exporter"`
	Before   targetState `json:"before"`
	After    targetState `json:"after"`
}

type limitsResult struct {
	DryRun          bool          `json:"dry_run"`
	CurrentRevision string        `json:"current_revision"`
	ResultRevision  string        `json:"result_revision"`
	Changes         []limitChange `json:"changes"`
	Effects         []limitEffect `json:"effects"`
}

// putLimits is PUT /admin/observability/limits: it replaces the runtime
// limits — a dry run reports the change, an apply persists, installs and
// audits it.
func (c *Console) putLimits(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if state := c.limitsState(); !state.Editable {
		writeError(w, http.StatusConflict, apiError{Error: "runtime limits are read-only on this gateway", Code: "read_only", Reason: state.Reason})
		return
	}
	var req limitsRequest
	if !decode(w, r, maxLimitsBody, &req) {
		return
	}
	set := c.opts.Set
	before := set.Limits()
	beforeBytes := otelexport.EncodeLimits(before)
	current := otelexport.Revision(beforeBytes)
	if !checkRevision(w, req.DryRun, req.ExpectedRevision, current) || !c.stateFileMatches(w, beforeBytes) {
		return
	}
	after, err := set.Normalize(req.Exporters)
	if err != nil {
		writeError(w, http.StatusBadRequest, apiError{Error: err.Error(), Code: "invalid_limits"})
		return
	}
	afterBytes := otelexport.EncodeLimits(after)
	res := limitsResult{DryRun: req.DryRun, CurrentRevision: current, ResultRevision: otelexport.Revision(afterBytes),
		Changes: c.limitChanges(before, after), Effects: c.limitEffects(before, after)}
	if req.DryRun || res.ResultRevision == current {
		writeJSON(w, http.StatusOK, res)
		return
	}
	if !c.auditReady(w) {
		return
	}
	if err := writeFileAtomic(c.opts.StateFile, afterBytes); err != nil {
		fmt.Fprintln(os.Stderr, "inferplane: observability console: persist limits:", err)
		writeError(w, http.StatusInternalServerError, apiError{Error: "the limits could not be saved; nothing changed", Code: "persist_failed"})
		return
	}
	if _, err := set.SetLimits(after); err != nil { // unreachable: Normalize accepted it against the same ceiling
		writeError(w, http.StatusInternalServerError, apiError{Error: "the saved limits could not be installed; restart mayu", Code: "persist_failed"})
		return
	}
	c.emitChange(r, "observability_limits", current, res.ResultRevision, changedExporters(res.Changes))
	writeJSON(w, http.StatusOK, res)
}

// stateFileMatches refuses an edit when the state file no longer holds the
// installed limits (edited by hand while mayu runs): overwriting it would
// discard that edit silently. A restart loads the file.
func (c *Console) stateFileMatches(w http.ResponseWriter, installed []byte) bool {
	b, err := os.ReadFile(c.opts.StateFile)
	var disk []byte
	switch {
	case errors.Is(err, fs.ErrNotExist):
		disk = otelexport.EncodeLimits(nil)
	case err != nil:
		writeError(w, http.StatusInternalServerError, apiError{Error: "the state file could not be read", Code: "read_failed"})
		return false
	default:
		if l, derr := otelexport.DecodeLimits(b); derr == nil {
			clamped, _ := c.opts.Set.Clamp(l)
			if n, nerr := c.opts.Set.Normalize(clamped); nerr == nil {
				disk = otelexport.EncodeLimits(n)
			}
		}
	}
	if !bytes.Equal(disk, installed) {
		writeError(w, http.StatusConflict, apiError{Error: "the state file changed on disk after mayu loaded it; restart mayu to load it, or restore it", Code: "limits_file_changed"})
		return false
	}
	return true
}

// limitChanges lists every effective field that differs, per declared
// exporter in name order.
func (c *Console) limitChanges(before, after otelexport.Limits) []limitChange {
	out := []limitChange{}
	for _, e := range c.opts.Set.Status().Exporters {
		b, a := before[e.Name], after[e.Name]
		if b.Paused != a.Paused {
			out = append(out, limitChange{Exporter: e.Name, Field: "paused", Before: b.Paused, After: a.Paused})
		}
		if bs, as := effectiveSignals(e.Signals, b), effectiveSignals(e.Signals, a); strings.Join(bs, ",") != strings.Join(as, ",") {
			out = append(out, limitChange{Exporter: e.Name, Field: "signals", Before: bs, After: as})
		}
		if e.MaxContentBytes > 0 {
			if bc, ac := orDefault(b.MaxContentBytes, e.MaxContentBytes), orDefault(a.MaxContentBytes, e.MaxContentBytes); bc != ac {
				out = append(out, limitChange{Exporter: e.Name, Field: "max_content_bytes", Before: bc, After: ac})
			}
		}
		if hasSignal(e.Signals, policy.SignalTraces) {
			if bp, ap := orDefault(int64(b.MaxSamplePercent), 100), orDefault(int64(a.MaxSamplePercent), 100); bp != ap {
				out = append(out, limitChange{Exporter: e.Name, Field: "max_sample_percent", Before: bp, After: ap})
			}
		}
	}
	return out
}

// limitEffects lists every policy selection whose export or sampling the edit
// changes.
func (c *Console) limitEffects(before, after otelexport.Limits) []limitEffect {
	out := []limitEffect{}
	if c.opts.Policies == nil {
		return out
	}
	for _, p := range c.opts.Policies.Policies() {
		for _, r := range p.Rules {
			if r.Observability == nil {
				continue
			}
			bt, at := c.targets(before, r.Observability), c.targets(after, r.Observability)
			for i := range bt {
				bs, as := stateOf(bt[i]), stateOf(at[i])
				if bs != as {
					out = append(out, limitEffect{Policy: p.Name, Rule: r.Name, Signal: bt[i].Signal, Exporter: bt[i].Exporter, Before: bs, After: as})
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Policy != out[j].Policy {
			return out[i].Policy < out[j].Policy
		}
		if out[i].Rule != out[j].Rule {
			return out[i].Rule < out[j].Rule
		}
		return out[i].Signal < out[j].Signal
	})
	return out
}

func stateOf(t Target) targetState {
	s := targetState{Exported: t.Exported}
	if t.Exported && t.Signal == policy.SignalTraces {
		s.SamplePercent = t.EffectivePercent
	}
	return s
}

func effectiveSignals(declared []string, l otelexport.Limit) []string {
	if l.Signals != nil {
		return append([]string(nil), l.Signals...)
	}
	return append([]string(nil), declared...)
}

func hasSignal(signals []string, signal string) bool {
	for _, s := range signals {
		if s == signal {
			return true
		}
	}
	return false
}

func orDefault(v, def int64) int64 {
	if v == 0 {
		return def
	}
	return v
}

func changedExporters(changes []limitChange) []string {
	seen := map[string]bool{}
	var out []string
	for _, ch := range changes {
		if !seen[ch.Exporter] {
			seen[ch.Exporter] = true
			out = append(out, ch.Exporter)
		}
	}
	sort.Strings(out)
	return out
}

// LoadLimits reads observability.console.state_file at boot and installs it
// on set. A missing file means no limits. A file that does not parse refuses
// boot: silently dropping a pause would resume export after a restart.
// Entries the current bootstrap no longer supports are narrowed (Set.Clamp)
// and returned as notes for the boot log.
func LoadLimits(path string, set *otelexport.Set) ([]string, error) {
	if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("observability.console.state_file: directory %s must exist", filepath.Dir(path))
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("observability.console.state_file: %w", err)
	}
	l, err := otelexport.DecodeLimits(b)
	if err != nil {
		return nil, fmt.Errorf("observability.console.state_file %s: %w (fix or remove the file; mayu does not start with a limit it cannot read)", path, err)
	}
	clamped, notes := set.Clamp(l)
	if _, err := set.SetLimits(clamped); err != nil {
		return nil, fmt.Errorf("observability.console.state_file %s: %w", path, err)
	}
	return notes, nil
}
```

In `observabilityapi.go` replace `Routes` with:

```go
// Routes maps each console route pattern to its handler, for
// server.WithFullAdminRoutes.
func (c *Console) Routes() map[string]http.Handler {
	return map[string]http.Handler{
		"GET /admin/observability":        http.HandlerFunc(c.status),
		"PUT /admin/observability/limits": http.HandlerFunc(c.putLimits),
	}
}
```

`docs/reference/data.md`: after the paragraph that ends `Admin records are not projected into the audit console tables above.` (the Task 6 section, at the end of the file) add:

~~~~markdown

`observability.console.state_file` holds the runtime limits as
`{"version": 1, "exporters": {...}}` (per exporter `paused`, `signals`,
`max_content_bytes`, `max_sample_percent`), written by mayu with mode 0600
through a temp file, fsync and rename. The limits revision is the SHA-256 of the
canonical bytes mayu writes, so for a file mayu wrote it equals the digest in the
audit record. A file that does not parse refuses boot.
~~~~

`docs/reference/data.ko.md`: after the paragraph that ends `관리 기록은 위의 감사 콘솔 테이블에 투영되지 않습니다.` (the Task 6 section, at the end of the file) add:

~~~~markdown

`observability.console.state_file`은 실행 중 한도를 `{"version": 1, "exporters": {...}}` 형식(exporter마다 `paused`, `signals`, `max_content_bytes`, `max_sample_percent`)으로 담습니다. mayu가 임시 파일·fsync·rename을 거쳐 0600 권한으로 씁니다. 한도 리비전은 mayu가 쓰는 정규 바이트의 SHA-256이므로, mayu가 쓴 파일이라면 감사 기록의 다이제스트와 같습니다. 읽을 수 없는 파일이면 부팅을 거부합니다.
~~~~

Refresh the companion hash for `docs/reference/data.md`. Append to the `internal/CLAUDE.md` section:

```markdown
- `observabilityapi` limits: dry run first; an apply needs `expected_revision`, the on-disk state file must still match the installed limits (`limits_file_changed` otherwise), the audit writer must be ready, then `writeFileAtomic` (0600, temp + fsync + rename), `SetLimits` and one `admin_observability_changed`. `LoadLimits` refuses boot on a file that does not parse and clamps entries the bootstrap no longer supports.
```

- [ ] **Step 4: Run**

```bash
go test -buildvcs=false ./internal/server/observabilityapi/ ./internal/server/ -race -count=1 > $S/t8.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|panic)' $S/t8.log
python3 scripts/check_docs_translations.py > $S/t8-docs.log 2>&1; echo "docs=$?"
gofmt -l internal/server
```
Expected: `exit=0`, `docs=0`, no gofmt output.

- [ ] **Step 5: Commit**

```bash
git -C $W add internal/server/observabilityapi docs/reference/data.md docs/reference/data.ko.md internal/CLAUDE.md
git -C $W commit -s -m "feat(observabilityapi): dry-run-first runtime limits with a persisted state file" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 9: Console rules API, with API and security references

**Files:**
- Create: `internal/server/observabilityapi/rules.go`, `internal/server/observabilityapi/rules_test.go`
- Modify: `internal/server/observabilityapi/observabilityapi.go` (`Routes` gains the rules route)
- Modify: `docs/reference/api.md` (after the "Observability rule schema" section, before line 76 `### Policy-routing schema and evidence (ADR-043)`), `docs/reference/api.ko.md` (before line 91 `### 정책 라우팅 스키마와 증거 · ADR-043`)
- Modify: `docs/api-reference.md` (after the `### Audit console (ADR-057)` section, before line 286 `## Error Codes`), `docs/api-reference.ko.md` (after `### 감사 콘솔 (ADR-057)`, before line 248 `## 오류 코드 {#error-codes}`)
- Modify: `docs/reference/security.md` (new `##` section at the end, after `## Audit console (ADR-057)`, last line 196), `docs/reference/security.ko.md` (at the end, after `## 감사 콘솔 (ADR-057)`, last line 175)
- Modify: `docs/reference/data.md` / `data.ko.md` (one more paragraph in the Task 6 section), `internal/CLAUDE.md` (one bullet)

**Interfaces:**
- Consumes: `readConsole`, `ruleFromDoc`, `consoleNameRE`, `errForeign` (Task 7); `decode`, `checkRevision`, `auditReady`, `emitChange`, `writeFileAtomic`, `apiError` (Task 8); `policy.FromV1Alpha1`, `policy.MergeObservability`, `policy.ValidExporterName`, `PolicyStore.CheckLocal`/`Reload` (Task 1); `sigs.k8s.io/yaml.Marshal`.
- Produces:
  - `PUT /admin/observability/rules` — request `{"dry_run": bool, "expected_revision": "sha256:…", "rules": [Rule]}`; 200 `{"dry_run","current_revision","result_revision","changes":[{"name","action"}],"summary":{"added","changed","removed","unchanged"},"decisions":[{"subject","before":{"traces","audit","content","content_conflict"},"after":{…}}]}`; errors 400 `invalid_request`/`invalid_rules`/`content_not_allowed`, 409 `read_only`/`revision_conflict`/`console_file_foreign`/`policy_reload_failed`, 500 `persist_failed`/`read_failed`, 503 `audit_unavailable`
  - package-private `normalizeRules`, `encodeRules`, `(Rule).doc`, `(Rule).exporters`, `(*Console).contentGate`, `ruleChanges`, `decisionChanges`

- [ ] **Step 1: Write the failing tests**

`internal/server/observabilityapi/rules_test.go`:

```go
package observabilityapi

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/otelexport"
	"github.com/inferplane/inferplane/internal/policy"
)

const rulesRoute = "PUT /admin/observability/rules"

const consoleContentDoc = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata:
  name: console-vault
spec:
  subject:
    team: engineering
  rules:
  - name: export
    failurePolicy: FailOpen
    observability:
      content: { exporter: vault }
`

type rulesHarness struct {
	store        *policy.Store
	console      *Console
	operatorPath string
	consolePath  string
	records      *[]audit.Record
}

func newRulesHarness(t *testing.T, console string, edit func(*Options)) rulesHarness {
	t.Helper()
	set := newSet(t)
	store, operatorPath, consolePath := consoleStore(t, set, operatorDoc, console)
	records := &[]audit.Record{}
	opts := Options{Set: set, Policies: store, PolicySource: SourceFiles, PolicyFile: consolePath,
		Emit: func(r audit.Record) { *records = append(*records, r) }}
	if edit != nil {
		edit(&opts)
	}
	return rulesHarness{store: store, console: New(opts), operatorPath: operatorPath, consolePath: consolePath, records: records}
}

func (h rulesHarness) revision(t *testing.T) string {
	t.Helper()
	st, _ := getStatus(t, h.console)
	rev, _ := st["rules"].(map[string]any)["revision"].(string)
	return rev
}

func financeRule() map[string]any {
	return map[string]any{"name": "console-finance", "subject": map[string]any{"team": "finance"}, "audit": map[string]any{"exporter": "corp"}}
}

func rulesBody(dryRun bool, revision string, rules ...map[string]any) map[string]any {
	if rules == nil {
		rules = []map[string]any{}
	}
	return map[string]any{"dry_run": dryRun, "expected_revision": revision, "rules": rules}
}

func policyNames(s *policy.Store) string {
	var names []string
	for _, p := range s.Policies() {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func TestRulesDryRunShowsTheAddedRuleAndDecision(t *testing.T) {
	h := newRulesHarness(t, "", nil)
	code, res := put(t, h.console, rulesRoute, rulesBody(true, h.revision(t), financeRule()))
	if code != 200 || res["dry_run"] != true {
		t.Fatalf("dry run = %d %v", code, res)
	}
	changes := res["changes"].([]any)
	if len(changes) != 1 || changes[0].(map[string]any)["name"] != "console-finance" || changes[0].(map[string]any)["action"] != "added" {
		t.Fatalf("changes = %v", changes)
	}
	decisions := res["decisions"].([]any)
	if len(decisions) != 1 {
		t.Fatalf("decisions = %v", decisions)
	}
	d := decisions[0].(map[string]any)
	if d["subject"].(map[string]any)["team"] != "finance" || len(d["before"].(map[string]any)["audit"].([]any)) != 0 ||
		d["after"].(map[string]any)["audit"].([]any)[0] != "corp" {
		t.Fatalf("decision = %v", d)
	}
	if b, _ := os.ReadFile(h.consolePath); len(b) != 0 || policyNames(h.store) != "engineering-observability" {
		t.Fatal("a dry run wrote the console file or changed the policy set")
	}
}

func TestRulesApplyWritesTheFileReloadsAndAudits(t *testing.T) {
	h := newRulesHarness(t, "", nil)
	before := h.revision(t)
	code, res := put(t, h.console, rulesRoute, rulesBody(false, before, financeRule()))
	if code != 200 {
		t.Fatalf("apply = %d %v", code, res)
	}
	b, err := os.ReadFile(h.consolePath)
	if err != nil {
		t.Fatal(err)
	}
	if res["result_revision"] != otelexport.Revision(b) {
		t.Fatalf("result revision %v is not the file digest", res["result_revision"])
	}
	docs, err := policy.ParseWireDocs(b)
	if err != nil || len(docs) != 1 || docs[0].Metadata.Name != "console-finance" {
		t.Fatalf("console file = %s (%v)", b, err)
	}
	if got := policyNames(h.store); got != "console-finance,engineering-observability" {
		t.Fatalf("live policy set = %s", got)
	}
	if d := h.store.Observability("finance", ""); strings.Join(d.Audit, ",") != "corp" {
		t.Fatalf("finance decision after reload = %+v", d)
	}
	if len(*h.records) != 1 {
		t.Fatalf("audit records = %d", len(*h.records))
	}
	ch := (*h.records)[0].Request.Change
	if ch == nil || ch.Target != "observability_rules" || ch.Before != before || ch.Before != otelexport.Revision(nil) ||
		ch.After != res["result_revision"] || strings.Join(ch.Names, ",") != "console-finance" {
		t.Fatalf("change = %+v", ch)
	}
	st, _ := getStatus(t, h.console)
	found := false
	for _, s := range st["selections"].([]any) {
		sel := s.(map[string]any)
		found = found || (sel["policy"] == "console-finance" && sel["source"] == "console")
	}
	if !found {
		t.Fatalf("selections = %v", st["selections"])
	}
}

func TestRulesValidation(t *testing.T) {
	h := newRulesHarness(t, "", nil)
	rev := h.revision(t)
	for name, rule := range map[string]map[string]any{
		"name without prefix": {"name": "finance", "subject": map[string]any{"team": "finance"}, "audit": map[string]any{"exporter": "corp"}},
		"no subject":          {"name": "console-x", "subject": map[string]any{}, "audit": map[string]any{"exporter": "corp"}},
		"no signal":           {"name": "console-x", "subject": map[string]any{"team": "finance"}},
		"percent too high":    {"name": "console-x", "subject": map[string]any{"team": "finance"}, "traces": map[string]any{"exporter": "corp", "sample_percent": 101}},
		"undeclared exporter": {"name": "console-x", "subject": map[string]any{"team": "finance"}, "audit": map[string]any{"exporter": "missing"}},
		"signal not allowed":  {"name": "console-x", "subject": map[string]any{"team": "finance"}, "audit": map[string]any{"exporter": "vault"}},
		"team with a slash":   {"name": "console-x", "subject": map[string]any{"team": "fin/ance"}, "audit": map[string]any{"exporter": "corp"}},
	} {
		if code, res := put(t, h.console, rulesRoute, rulesBody(true, rev, rule)); code != 400 || res["code"] != "invalid_rules" {
			t.Errorf("%s: %d %v", name, code, res)
		}
	}
	if code, res := put(t, h.console, rulesRoute, rulesBody(true, rev, financeRule(), financeRule())); code != 400 || !strings.Contains(res["error"].(string), "duplicate") {
		t.Fatalf("duplicate names = %d %v", code, res)
	}
	if code, res := put(t, h.console, rulesRoute, rulesBody(false, "sha256:stale", financeRule())); code != 409 || res["code"] != "revision_conflict" {
		t.Fatalf("stale apply = %d %v", code, res)
	}
}

func TestRulesNameMayNotShadowAnotherFile(t *testing.T) {
	set := newSet(t)
	store, _, consolePath := consoleStore(t, set, strings.Replace(operatorDoc, "name: engineering-observability", "name: console-shared", 1), "")
	c := New(Options{Set: set, Policies: store, PolicySource: SourceFiles, PolicyFile: consolePath})
	rule := map[string]any{"name": "console-shared", "subject": map[string]any{"team": "finance"}, "audit": map[string]any{"exporter": "corp"}}
	if code, res := put(t, c, rulesRoute, rulesBody(true, "", rule)); code != 400 || !strings.Contains(res["error"].(string), "another policy file") {
		t.Fatalf("shadowing an operator document = %d %v", code, res)
	}
}

func TestRulesContentNeedsTheBootstrapOptIn(t *testing.T) {
	content := map[string]any{"name": "console-vault", "subject": map[string]any{"team": "engineering"}, "content": map[string]any{"exporter": "vault"}}
	h := newRulesHarness(t, "", nil)
	if code, res := put(t, h.console, rulesRoute, rulesBody(true, h.revision(t), content)); code != 400 || res["code"] != "content_not_allowed" {
		t.Fatalf("new content selection without opt-in = %d %v", code, res)
	}
	kept := newRulesHarness(t, consoleContentDoc, nil)
	rev := kept.revision(t)
	if code, res := put(t, kept.console, rulesRoute, rulesBody(true, rev, content, financeRule())); code != 200 {
		t.Fatalf("keeping an unchanged content selection = %d %v", code, res)
	}
	moved := map[string]any{"name": "console-vault", "subject": map[string]any{"team": "finance"}, "content": map[string]any{"exporter": "vault"}}
	if code, res := put(t, kept.console, rulesRoute, rulesBody(true, rev, moved)); code != 400 || res["code"] != "content_not_allowed" {
		t.Fatalf("moving a content selection to another subject = %d %v", code, res)
	}
	if code, res := put(t, kept.console, rulesRoute, rulesBody(true, rev)); code != 200 || res["summary"].(map[string]any)["removed"] != float64(1) {
		t.Fatalf("removing a content selection = %d %v", code, res)
	}
	opted := newRulesHarness(t, "", func(o *Options) { o.ContentRules = true })
	if code, res := put(t, opted.console, rulesRoute, rulesBody(true, opted.revision(t), content)); code != 200 {
		t.Fatalf("content with content_rules = %d %v", code, res)
	}
}

func TestRulesReadOnlyAndForeign(t *testing.T) {
	cp := newRulesHarness(t, "", func(o *Options) { o.PolicySource = SourceControlPlane })
	if code, res := put(t, cp.console, rulesRoute, rulesBody(true, "", financeRule())); code != 409 || res["code"] != "read_only" || res["reason"] != "control_plane" {
		t.Fatalf("control plane = %d %v", code, res)
	}
	foreign := newRulesHarness(t, foreignDoc, nil)
	before, _ := os.ReadFile(foreign.consolePath)
	if code, res := put(t, foreign.console, rulesRoute, rulesBody(false, otelexport.Revision(before), financeRule())); code != 409 || res["code"] != "console_file_foreign" {
		t.Fatalf("foreign console file = %d %v", code, res)
	}
	if after, _ := os.ReadFile(foreign.consolePath); string(after) != string(before) {
		t.Fatal("the console overwrote a file it does not own")
	}
}

// Review Focus 4: when Reload fails after the write, the console file is
// restored, so disk matches the running set.
func TestRulesApplyRestoresTheFileWhenReloadFails(t *testing.T) {
	h := newRulesHarness(t, consoleDoc, nil)
	before, _ := os.ReadFile(h.consolePath)
	rev := h.revision(t)
	writeFile(t, h.operatorPath, "spec: [") // an operator's broken edit, not yet reloaded
	code, res := put(t, h.console, rulesRoute, rulesBody(false, rev, financeRule()))
	if code != 409 || res["code"] != "policy_reload_failed" || !strings.Contains(res["error"].(string), "operator.yaml") {
		t.Fatalf("apply over a broken operator file = %d %v", code, res)
	}
	if after, _ := os.ReadFile(h.consolePath); string(after) != string(before) {
		t.Fatalf("console file not restored:\n%s", after)
	}
	if got := policyNames(h.store); got != "console-platform,engineering-observability" {
		t.Fatalf("live policy set changed: %s", got)
	}
	if len(*h.records) != 0 {
		t.Fatal("a failed apply was audited as a change")
	}
}

func TestEncodeRulesRoundTrips(t *testing.T) {
	rules, err := normalizeRules([]Rule{
		{Name: "console-b", Subject: Subject{User: "alice"}, Traces: &TraceTarget{Exporter: "corp"}},
		{Name: "console-a", Subject: Subject{Team: "eng"}, Audit: &SignalTarget{Exporter: "corp"}, Content: &SignalTarget{Exporter: "vault"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, policies, err := encodeRules(rules)
	if err != nil || len(policies) != 2 {
		t.Fatalf("encode = %v (%d policies)", err, len(policies))
	}
	docs, err := policy.ParseWireDocs(b)
	if err != nil || len(docs) != 2 || docs[0].Metadata.Name != "console-a" {
		t.Fatalf("file = %s (%v)", b, err)
	}
	for i, d := range docs {
		if back, err := ruleFromDoc(d); err != nil || back.Name != rules[i].Name {
			t.Fatalf("round trip %d = %+v (%v)", i, back, err)
		}
	}
	if p := docs[1].Spec.Rules[0].Observability.Traces.SamplePercent; p == nil || *p != 100 {
		t.Fatal("an omitted sample percent must be written explicitly as 100")
	}
	if empty, _, _ := encodeRules(nil); len(empty) != 0 {
		t.Fatalf("no rules = %q, want an empty file", empty)
	}
}
```

- [ ] **Step 2: Run — expect failure**

```bash
go test -buildvcs=false ./internal/server/observabilityapi/ -run 'Rules|EncodeRules' -race -count=1 > $S/t9.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|panic)|undefined' $S/t9.log
```
Expected: `exit=1` (`undefined: normalizeRules`, `encodeRules`; no rules route).

- [ ] **Step 3: Implement**

`internal/server/observabilityapi/rules.go`:

```go
package observabilityapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"unicode"

	sigyaml "sigs.k8s.io/yaml"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/otelexport"
	"github.com/inferplane/inferplane/internal/policy"
)

const (
	maxRulesBody    = 1 << 20
	maxConsoleRules = 256
)

type rulesRequest struct {
	DryRun           bool   `json:"dry_run"`
	ExpectedRevision string `json:"expected_revision"`
	Rules            []Rule `json:"rules"`
}

type ruleChange struct {
	Name   string `json:"name"`
	Action string `json:"action"` // added | changed | removed
}

type ruleSummary struct {
	Added     int `json:"added"`
	Changed   int `json:"changed"`
	Removed   int `json:"removed"`
	Unchanged int `json:"unchanged"`
}

// decision is the policy-level observability decision for one subject,
// before runtime limits (policy.MergeObservability).
type decision struct {
	Traces          []TraceTarget `json:"traces"`
	Audit           []string      `json:"audit"`
	Content         string        `json:"content,omitempty"`
	ContentConflict bool          `json:"content_conflict,omitempty"`
}

type decisionChange struct {
	Subject Subject  `json:"subject"`
	Before  decision `json:"before"`
	After   decision `json:"after"`
}

type rulesResult struct {
	DryRun          bool             `json:"dry_run"`
	CurrentRevision string           `json:"current_revision"`
	ResultRevision  string           `json:"result_revision"`
	Changes         []ruleChange     `json:"changes"`
	Summary         ruleSummary      `json:"summary"`
	Decisions       []decisionChange `json:"decisions"`
}

// putRules is PUT /admin/observability/rules: it replaces the console-owned
// rules. An apply writes the console policy file and reloads the local file
// channel; when the reload fails the previous bytes are restored, so the file
// on disk matches the running policy set.
func (c *Console) putRules(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if reason := c.rulesReadOnly(); reason != "" {
		writeError(w, http.StatusConflict, apiError{Error: "console rules are read-only on this gateway", Code: "read_only", Reason: reason})
		return
	}
	var req rulesRequest
	if !decode(w, r, maxRulesBody, &req) {
		return
	}
	current, currentBytes, err := c.readConsole()
	if errors.Is(err, errForeign) {
		writeError(w, http.StatusConflict, apiError{Error: err.Error() + "; edit it by hand", Code: "console_file_foreign"})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, apiError{Error: "the console policy file could not be read", Code: "read_failed"})
		return
	}
	currentRev := otelexport.Revision(currentBytes)
	if !checkRevision(w, req.DryRun, req.ExpectedRevision, currentRev) {
		return
	}
	next, err := normalizeRules(req.Rules)
	if err != nil {
		writeError(w, http.StatusBadRequest, apiError{Error: err.Error(), Code: "invalid_rules"})
		return
	}
	if err := c.contentGate(current, next); err != nil {
		writeError(w, http.StatusBadRequest, apiError{Error: err.Error(), Code: "content_not_allowed"})
		return
	}
	nextBytes, candidate, err := encodeRules(next)
	if err != nil {
		writeError(w, http.StatusBadRequest, apiError{Error: err.Error(), Code: "invalid_rules"})
		return
	}
	live := c.opts.Policies.Policies()
	owned := map[string]bool{}
	for _, rule := range current {
		owned[rule.Name] = true
	}
	after := make([]*policy.Policy, 0, len(live)+len(candidate))
	taken := map[string]bool{}
	for _, p := range live {
		if !owned[p.Name] {
			after = append(after, p)
			taken[p.Name] = true
		}
	}
	for _, p := range candidate {
		if taken[p.Name] {
			writeError(w, http.StatusBadRequest, apiError{Error: fmt.Sprintf("rule %q: a policy with this name already exists in another policy file", p.Name), Code: "invalid_rules"})
			return
		}
	}
	after = append(after, candidate...)
	if err := c.opts.Policies.CheckLocal(after); err != nil {
		writeError(w, http.StatusBadRequest, apiError{Error: err.Error(), Code: "invalid_rules"})
		return
	}
	changes, summary := ruleChanges(current, next)
	res := rulesResult{DryRun: req.DryRun, CurrentRevision: currentRev, ResultRevision: otelexport.Revision(nextBytes),
		Changes: changes, Summary: summary, Decisions: decisionChanges(live, after)}
	if req.DryRun || res.ResultRevision == currentRev {
		writeJSON(w, http.StatusOK, res)
		return
	}
	if !c.auditReady(w) {
		return
	}
	if err := writeFileAtomic(c.opts.PolicyFile, nextBytes); err != nil {
		fmt.Fprintln(os.Stderr, "inferplane: observability console: persist rules:", err)
		writeError(w, http.StatusInternalServerError, apiError{Error: "the console rules could not be saved; nothing changed", Code: "persist_failed"})
		return
	}
	if err := c.opts.Policies.Reload(); err != nil {
		msg := "the policy set did not reload, so the console file was restored: " + err.Error()
		if rerr := writeFileAtomic(c.opts.PolicyFile, currentBytes); rerr != nil {
			fmt.Fprintln(os.Stderr, "inferplane: observability console: restore rules:", rerr)
			msg = "the policy set did not reload and the console file could not be restored; fix the policy files before the next reload: " + err.Error()
		}
		writeError(w, http.StatusConflict, apiError{Error: msg, Code: "policy_reload_failed"})
		return
	}
	c.emitChange(r, "observability_rules", currentRev, res.ResultRevision, changedRules(changes))
	writeJSON(w, http.StatusOK, res)
}

// normalizeRules validates console rules and returns them sorted by name, with
// trace sample percents written explicitly (0 means 100).
func normalizeRules(in []Rule) ([]Rule, error) {
	if len(in) > maxConsoleRules {
		return nil, fmt.Errorf("at most %d console rules", maxConsoleRules)
	}
	out := make([]Rule, 0, len(in))
	seen := map[string]bool{}
	for _, r := range in {
		if !consoleNameRE.MatchString(r.Name) {
			return nil, fmt.Errorf("rule name %q must be console- followed by lowercase letters, digits and '-' (at most 63 characters)", r.Name)
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("duplicate rule name %q", r.Name)
		}
		seen[r.Name] = true
		if r.Subject.Team == "" && r.Subject.User == "" {
			return nil, fmt.Errorf("rule %q: the subject must name a team and/or a user", r.Name)
		}
		for _, v := range []string{r.Subject.Team, r.Subject.User} {
			if len(v) > 256 || strings.ContainsFunc(v, unicode.IsControl) {
				return nil, fmt.Errorf("rule %q: subject values must be at most 256 bytes without control characters", r.Name)
			}
		}
		if strings.Contains(r.Subject.Team, "/") {
			return nil, fmt.Errorf("rule %q: the team must not contain '/'", r.Name)
		}
		if r.Traces == nil && r.Audit == nil && r.Content == nil {
			return nil, fmt.Errorf("rule %q: select at least one of traces, audit or content", r.Name)
		}
		n := Rule{Name: r.Name, Subject: r.Subject}
		if r.Traces != nil {
			t := *r.Traces
			if t.SamplePercent == 0 {
				t.SamplePercent = 100
			}
			if t.SamplePercent < 1 || t.SamplePercent > 100 {
				return nil, fmt.Errorf("rule %q: traces.sample_percent must be in [1, 100]", r.Name)
			}
			n.Traces = &t
		}
		if r.Audit != nil {
			a := *r.Audit
			n.Audit = &a
		}
		if r.Content != nil {
			ct := *r.Content
			n.Content = &ct
		}
		for _, name := range n.exporters() {
			if !policy.ValidExporterName(name) {
				return nil, fmt.Errorf("rule %q: exporter %q must be a declared exporter name (lowercase letters, digits, '-')", r.Name, name)
			}
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// exporters lists the exporter names r selects.
func (r Rule) exporters() []string {
	var out []string
	if r.Traces != nil {
		out = append(out, r.Traces.Exporter)
	}
	if r.Audit != nil {
		out = append(out, r.Audit.Exporter)
	}
	if r.Content != nil {
		out = append(out, r.Content.Exporter)
	}
	return out
}

// contentGate keeps content export a bootstrap decision: without
// observability.console.content_rules an edit may keep or remove an existing
// content selection, never add one or move it to another subject or exporter.
func (c *Console) contentGate(current, next []Rule) error {
	if c.opts.ContentRules {
		return nil
	}
	had := map[string]Rule{}
	for _, r := range current {
		if r.Content != nil {
			had[r.Name] = r
		}
	}
	for _, r := range next {
		if r.Content == nil {
			continue
		}
		prev, ok := had[r.Name]
		if !ok || prev.Subject != r.Subject || prev.Content.Exporter != r.Content.Exporter {
			return fmt.Errorf("rule %q: console rules cannot add or change content export unless observability.console.content_rules is true", r.Name)
		}
	}
	return nil
}

// doc is the GovernancePolicy document the console writes for r.
func (r Rule) doc() v1alpha1.GovernancePolicy {
	o := &v1alpha1.ObservabilityRule{}
	if r.Traces != nil {
		pct := r.Traces.SamplePercent
		o.Traces = &v1alpha1.TraceExport{Exporter: r.Traces.Exporter, SamplePercent: &pct}
	}
	if r.Audit != nil {
		o.Audit = &v1alpha1.SignalExport{Exporter: r.Audit.Exporter}
	}
	if r.Content != nil {
		o.Content = &v1alpha1.SignalExport{Exporter: r.Content.Exporter}
	}
	return v1alpha1.GovernancePolicy{
		TypeMeta: v1alpha1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.KindGovernancePolicy},
		Metadata: v1alpha1.ObjectMeta{Name: r.Name},
		Spec: v1alpha1.GovernancePolicySpec{
			Subject: v1alpha1.Subject{Team: r.Subject.Team, User: r.Subject.User},
			Rules:   []v1alpha1.Rule{{Name: consoleRuleName, FailurePolicy: v1alpha1.FailOpen, Observability: o}},
		},
	}
}

// encodeRules renders normalized rules as the console policy file: one
// document per rule in name order, "---" between documents, and zero bytes for
// no rules (an empty file is a valid policy file). Every document goes through
// policy.FromV1Alpha1, the loader's own validation.
func encodeRules(rules []Rule) ([]byte, []*policy.Policy, error) {
	var buf bytes.Buffer
	policies := make([]*policy.Policy, 0, len(rules))
	for i, r := range rules {
		doc := r.doc()
		p, err := policy.FromV1Alpha1(&doc)
		if err != nil {
			return nil, nil, fmt.Errorf("rule %q: %w", r.Name, err)
		}
		y, err := sigyaml.Marshal(doc)
		if err != nil {
			return nil, nil, fmt.Errorf("rule %q: %w", r.Name, err)
		}
		if i > 0 {
			buf.WriteString("---\n")
		}
		buf.Write(y)
		policies = append(policies, p)
	}
	return buf.Bytes(), policies, nil
}

// ruleChanges diffs console rules by name.
func ruleChanges(current, next []Rule) ([]ruleChange, ruleSummary) {
	prev := map[string]string{}
	for _, r := range current {
		b, _ := json.Marshal(r)
		prev[r.Name] = string(b)
	}
	out := []ruleChange{}
	var sum ruleSummary
	seen := map[string]bool{}
	for _, r := range next {
		seen[r.Name] = true
		b, _ := json.Marshal(r)
		old, ok := prev[r.Name]
		switch {
		case !ok:
			out = append(out, ruleChange{Name: r.Name, Action: "added"})
			sum.Added++
		case old != string(b):
			out = append(out, ruleChange{Name: r.Name, Action: "changed"})
			sum.Changed++
		default:
			sum.Unchanged++
		}
	}
	for _, r := range current {
		if !seen[r.Name] {
			out = append(out, ruleChange{Name: r.Name, Action: "removed"})
			sum.Removed++
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, sum
}

// decisionChanges lists, for every subject an observability rule names before
// or after the edit, the policy decision when it differs.
func decisionChanges(before, after []*policy.Policy) []decisionChange {
	subjects := map[Subject]bool{}
	for _, set := range [][]*policy.Policy{before, after} {
		for _, p := range set {
			for _, r := range p.Rules {
				if r.Observability != nil {
					subjects[Subject{Team: p.Subject.Team, User: p.Subject.User}] = true
				}
			}
		}
	}
	keys := make([]Subject, 0, len(subjects))
	for s := range subjects {
		keys = append(keys, s)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Team != keys[j].Team {
			return keys[i].Team < keys[j].Team
		}
		return keys[i].User < keys[j].User
	})
	out := []decisionChange{}
	for _, s := range keys {
		b := decisionOf(policy.MergeObservability(before, s.Team, s.User))
		a := decisionOf(policy.MergeObservability(after, s.Team, s.User))
		if !reflect.DeepEqual(b, a) {
			out = append(out, decisionChange{Subject: s, Before: b, After: a})
		}
	}
	return out
}

func decisionOf(d policy.ObservabilityDecision) decision {
	out := decision{Traces: []TraceTarget{}, Audit: append([]string{}, d.Audit...), Content: d.Content, ContentConflict: d.ContentConflict}
	for _, t := range d.Traces {
		out.Traces = append(out.Traces, TraceTarget{Exporter: t.Exporter, SamplePercent: t.SamplePercent})
	}
	return out
}

func changedRules(changes []ruleChange) []string {
	out := make([]string, 0, len(changes))
	for _, c := range changes {
		out = append(out, c.Name)
	}
	return out
}
```

In `observabilityapi.go` replace `Routes` with:

```go
// Routes maps each console route pattern to its handler, for
// server.WithFullAdminRoutes.
func (c *Console) Routes() map[string]http.Handler {
	return map[string]http.Handler{
		"GET /admin/observability":        http.HandlerFunc(c.status),
		"PUT /admin/observability/limits": http.HandlerFunc(c.putLimits),
		"PUT /admin/observability/rules":  http.HandlerFunc(c.putRules),
	}
}
```

`docs/reference/api.md`: insert before `### Policy-routing schema and evidence (ADR-043)` (line 76):

~~~~markdown
### Observability console API (`mayu` only)

`GET /admin/observability`, `PUT /admin/observability/limits` and
`PUT /admin/observability/rules` (`internal/server/observabilityapi`) are mounted
by `server.WithFullAdminRoutes`, so every route, reads included, needs a full
administrator. Both PUTs take `dry_run` and `expected_revision`: a dry run changes
nothing; an apply must name the revision its preview saw (409 `revision_conflict`
otherwise), then persists, installs and appends `admin_observability_changed`.
Errors are `{"error": message, "code": code}`. `/admin/capabilities` adds
`observability_export`. Request and response shapes are in the
[API reference](../api-reference.md#observability-console-mayu-only).

~~~~

`docs/reference/api.ko.md`: insert before `### 정책 라우팅 스키마와 증거 · ADR-043 {#policy-routing-schema-and-evidence-adr-043}` (line 91):

~~~~markdown
### 관측성 콘솔 API · mayu 전용 {#observability-console-api-mayu-only}

`GET /admin/observability`, `PUT /admin/observability/limits`, `PUT /admin/observability/rules`(`internal/server/observabilityapi`)는 `server.WithFullAdminRoutes`로 붙으므로 읽기를 포함한 모든 경로에 전체 관리자 권한이 필요합니다. 두 PUT은 `dry_run`과 `expected_revision`을 받습니다. dry run은 아무것도 바꾸지 않고, 적용은 미리보기 때 본 리비전을 지정해야 하며(다르면 409 `revision_conflict`), 저장·설치한 뒤 `admin_observability_changed`를 기록합니다. 오류 본문은 `{"error": message, "code": code}`입니다. `/admin/capabilities`에 `observability_export`가 추가됩니다. 요청·응답 형식은 [API 레퍼런스](../api-reference.md#observability-console-mayu-only)에 있습니다.

~~~~

`docs/api-reference.md`: insert after the `### Audit console (ADR-057)` section, before `## Error Codes` (line 286), so the two console APIs sit together:

~~~~markdown
### Observability console (`mayu` only)

Full administrators only, reads included (team-mapped identities get 403 and an
`admin_denied` record). Counters are per process since `counters_since`.

`GET /admin/observability`:

```json
{
  "configured": true,
  "policy_source": "files",
  "service_name": "inferplane",
  "counters_since": "2026-10-29T10:00:00Z",
  "limits": {"editable": true, "revision": "sha256:4f8b…"},
  "rules": {"editable": true, "revision": "sha256:e3b0…", "content_rules": false, "console": [
    {"name": "console-eng", "subject": {"team": "engineering"}, "audit": {"exporter": "corp"}}
  ]},
  "exporters": [{
    "name": "corp", "endpoint": "otel-collector.internal:4318", "protocol": "http", "tls": true,
    "header_names": ["authorization"], "signals": ["audit", "traces"], "effective_signals": ["audit", "traces"],
    "queue_limit_bytes": 67108864, "queued_bytes": 0, "limit": {},
    "stats": [{"signal": "audit", "outcomes": {"sent": 12, "dropped_queue_full": 0, "dropped_oversize": 0,
      "dropped_export_error": 0, "withheld_limit": 0, "sampled_out": 0}, "last_success_at": "2026-10-29T10:05:00Z"}]
  }],
  "decisions": [{"signal": "audit", "decisions": {"selected": 12, "no_rule": 30, "content_conflict": 0, "withheld_limit": 0}}],
  "selections": [{"policy": "console-eng", "rule": "export", "source": "console", "subject": {"team": "engineering"},
    "targets": [{"signal": "audit", "exporter": "corp", "exported": true}]}]
}
```

`PUT /admin/observability/limits` replaces the runtime limits. An exporter left
out has no limit; `signals` lists the declared signals to keep; `max_content_bytes`
and `max_sample_percent` only lower the bootstrap values.

```json
{"dry_run": true, "expected_revision": "sha256:4f8b…", "exporters": {"corp": {"paused": true}}}
```

```json
{
  "dry_run": true,
  "current_revision": "sha256:4f8b…",
  "result_revision": "sha256:9a1c…",
  "changes": [{"exporter": "corp", "field": "paused", "before": false, "after": true}],
  "effects": [{"policy": "engineering-observability", "rule": "export", "signal": "audit", "exporter": "corp",
    "before": {"exported": true}, "after": {"exported": false}}]
}
```

`PUT /admin/observability/rules` replaces the console-owned rules, the
`console-…` documents in `observability.console.policy_file`:

```json
{"dry_run": true, "expected_revision": "sha256:e3b0…", "rules": [
  {"name": "console-eng", "subject": {"team": "engineering"},
   "traces": {"exporter": "corp", "sample_percent": 20}, "audit": {"exporter": "corp"}}]}
```

```json
{
  "dry_run": true,
  "current_revision": "sha256:e3b0…",
  "result_revision": "sha256:7c2d…",
  "changes": [{"name": "console-eng", "action": "added"}],
  "summary": {"added": 1, "changed": 0, "removed": 0, "unchanged": 0},
  "decisions": [{"subject": {"team": "engineering"},
    "before": {"traces": [], "audit": []},
    "after": {"traces": [{"exporter": "corp", "sample_percent": 20}], "audit": ["corp"]}}]
}
```

Send the same body with `"dry_run": false` to apply. Errors:

| Status | `code` | Meaning |
| --- | --- | --- |
| 400 | `invalid_request` | The body is not one JSON object with known fields, or an apply has no `expected_revision` |
| 400 | `invalid_limits` / `invalid_rules` | Outside the bootstrap ceiling, or a rule the policy loader refuses |
| 400 | `content_not_allowed` | A console rule adds or changes content export without `observability.console.content_rules` |
| 409 | `read_only` | Not editable here (`reason`: `not_configured`, `no_state_file`, `no_policy_file`, `control_plane`) |
| 409 | `revision_conflict` | The state changed since it was read (`current_revision` is returned) |
| 409 | `limits_file_changed` | The state file was edited on disk; restart mayu to load it |
| 409 | `console_file_foreign` | The console policy file holds documents the console does not manage |
| 409 | `policy_reload_failed` | The policy set did not reload; the console file was restored |
| 503 | `audit_unavailable` | The audit chain is not accepting records; nothing changed |
| 500 | `persist_failed` / `read_failed` | A state file could not be written or read; nothing changed |

~~~~

`docs/api-reference.ko.md`: insert after `### 감사 콘솔 (ADR-057) {#audit-console-adr-057}`, before `## 오류 코드 {#error-codes}` (line 248), the same section translated; every fenced JSON block is byte-identical to the English one above:

~~~~markdown
### 관측성 콘솔 · mayu 전용 {#observability-console-mayu-only}

읽기를 포함해 전체 관리자만 쓸 수 있습니다(팀에 매핑된 신원은 403과 `admin_denied` 기록을 받습니다). 카운터는 `counters_since` 이후의 프로세스별 값입니다.

`GET /admin/observability`:

```json
{
  "configured": true,
  "policy_source": "files",
  "service_name": "inferplane",
  "counters_since": "2026-10-29T10:00:00Z",
  "limits": {"editable": true, "revision": "sha256:4f8b…"},
  "rules": {"editable": true, "revision": "sha256:e3b0…", "content_rules": false, "console": [
    {"name": "console-eng", "subject": {"team": "engineering"}, "audit": {"exporter": "corp"}}
  ]},
  "exporters": [{
    "name": "corp", "endpoint": "otel-collector.internal:4318", "protocol": "http", "tls": true,
    "header_names": ["authorization"], "signals": ["audit", "traces"], "effective_signals": ["audit", "traces"],
    "queue_limit_bytes": 67108864, "queued_bytes": 0, "limit": {},
    "stats": [{"signal": "audit", "outcomes": {"sent": 12, "dropped_queue_full": 0, "dropped_oversize": 0,
      "dropped_export_error": 0, "withheld_limit": 0, "sampled_out": 0}, "last_success_at": "2026-10-29T10:05:00Z"}]
  }],
  "decisions": [{"signal": "audit", "decisions": {"selected": 12, "no_rule": 30, "content_conflict": 0, "withheld_limit": 0}}],
  "selections": [{"policy": "console-eng", "rule": "export", "source": "console", "subject": {"team": "engineering"},
    "targets": [{"signal": "audit", "exporter": "corp", "exported": true}]}]
}
```

`PUT /admin/observability/limits`는 실행 중 한도 전체를 바꿉니다. 빠진 exporter에는 한도가 없고, `signals`는 남길 선언 신호 목록이며, `max_content_bytes`와 `max_sample_percent`는 부트스트랩 값보다 낮추기만 합니다.

```json
{"dry_run": true, "expected_revision": "sha256:4f8b…", "exporters": {"corp": {"paused": true}}}
```

```json
{
  "dry_run": true,
  "current_revision": "sha256:4f8b…",
  "result_revision": "sha256:9a1c…",
  "changes": [{"exporter": "corp", "field": "paused", "before": false, "after": true}],
  "effects": [{"policy": "engineering-observability", "rule": "export", "signal": "audit", "exporter": "corp",
    "before": {"exported": true}, "after": {"exported": false}}]
}
```

`PUT /admin/observability/rules`는 콘솔이 관리하는 규칙, 곧 `observability.console.policy_file`의 `console-…` 문서 전체를 바꿉니다.

```json
{"dry_run": true, "expected_revision": "sha256:e3b0…", "rules": [
  {"name": "console-eng", "subject": {"team": "engineering"},
   "traces": {"exporter": "corp", "sample_percent": 20}, "audit": {"exporter": "corp"}}]}
```

```json
{
  "dry_run": true,
  "current_revision": "sha256:e3b0…",
  "result_revision": "sha256:7c2d…",
  "changes": [{"name": "console-eng", "action": "added"}],
  "summary": {"added": 1, "changed": 0, "removed": 0, "unchanged": 0},
  "decisions": [{"subject": {"team": "engineering"},
    "before": {"traces": [], "audit": []},
    "after": {"traces": [{"exporter": "corp", "sample_percent": 20}], "audit": ["corp"]}}]
}
```

적용하려면 같은 본문을 `"dry_run": false`로 보냅니다. 오류:

| 상태 | `code` | 의미 |
| --- | --- | --- |
| 400 | `invalid_request` | 본문이 알려진 필드만 가진 JSON 객체 하나가 아니거나, 적용 요청에 `expected_revision`이 없음 |
| 400 | `invalid_limits` / `invalid_rules` | 부트스트랩 상한을 벗어나거나 정책 로더가 거부하는 규칙 |
| 400 | `content_not_allowed` | `observability.console.content_rules` 없이 콘솔 규칙이 본문 내보내기를 추가하거나 바꿈 |
| 409 | `read_only` | 이 게이트웨이에서 편집할 수 없음(`reason`: `not_configured`, `no_state_file`, `no_policy_file`, `control_plane`) |
| 409 | `revision_conflict` | 읽은 뒤 상태가 바뀜(`current_revision`을 함께 반환) |
| 409 | `limits_file_changed` | 디스크의 상태 파일을 직접 고침. mayu를 재시작해 불러오세요 |
| 409 | `console_file_foreign` | 콘솔 정책 파일에 콘솔이 관리하지 않는 문서가 있음 |
| 409 | `policy_reload_failed` | 정책 세트를 다시 읽지 못해 콘솔 파일을 되돌림 |
| 503 | `audit_unavailable` | 감사 체인이 기록을 받지 않아 아무것도 바꾸지 않음 |
| 500 | `persist_failed` / `read_failed` | 상태 파일을 쓰거나 읽지 못해 아무것도 바꾸지 않음 |

~~~~

`docs/reference/security.md`: append at the end of the file, after `## Audit console (ADR-057)` (its last line, 196, ends `not verification.`), so the two console sections sit together:

~~~~markdown

## Observability console edits

- Every `/admin/observability*` route is full-admin only, reads included. The
  status shows header names, never header values, and errors carry status codes or
  fixed text, never collector responses.
- Runtime limits only narrow the bootstrap exporter ceiling. No console or
  control-plane input names an endpoint, protocol, TLS setting or credential, so no
  edit can redirect prompts. Console rules may select content only with
  `observability.console.content_rules: true`.
- Console rules load through the local policy file channel and its ceiling check;
  with a control plane the console cannot become a second policy source.
- An edit is refused while the audit chain is not accepting records, and each
  applied edit records the actor and the before/after revisions.
- Whoever administers mayu's host can still edit the config and both files; export
  that users cannot turn off still needs a host they do not administer.
~~~~

`docs/reference/security.ko.md`: append at the end of the file, after `## 감사 콘솔 (ADR-057) {#audit-console-adr-057}` (last line 175):

~~~~markdown

## 관측성 콘솔 편집 {#observability-console-edits}

- `/admin/observability*`의 모든 경로는 읽기를 포함해 전체 관리자 전용입니다. 상태에는 헤더 이름만 나오고 값은 나오지 않습니다. 오류는 상태 코드나 고정 문구만 담고 수집기 응답은 담지 않습니다.
- 실행 중 한도는 부트스트랩 exporter 상한을 좁히기만 합니다. 콘솔이나 제어 영역 입력으로 엔드포인트·프로토콜·TLS 설정·자격 증명을 지정할 수 없으므로 어떤 편집도 프롬프트를 다른 곳으로 보내지 못합니다. 콘솔 규칙으로 본문을 고르려면 `observability.console.content_rules: true`가 필요합니다.
- 콘솔 규칙은 로컬 정책 파일 채널과 그 상한 검사를 거쳐 읽힙니다. 제어 영역이 있으면 콘솔이 두 번째 정책 출처가 될 수 없습니다.
- 감사 체인이 기록을 받지 않는 동안에는 편집을 거부하고, 적용한 편집마다 수행자와 전후 리비전을 기록합니다.
- mayu 호스트를 관리하는 사람은 여전히 설정과 두 파일을 고칠 수 있습니다. 사용자가 끌 수 없는 내보내기가 필요하면 사용자가 관리하지 않는 호스트가 필요합니다.
~~~~

`docs/reference/data.md`: after the paragraph added in Task 8 (ending `A file that does not parse refuses boot.`) add:

~~~~markdown

`observability.console.policy_file` holds the console-owned GovernancePolicy
documents: names start with `console-`, each has one FailOpen observability rule
named `export`, and documents are sorted by name and separated by `---`. The
local policy file channel loads it like any other policy file. Its revision is the
SHA-256 of the file bytes.
~~~~

`docs/reference/data.ko.md`: after the paragraph added in Task 8 (ending `읽을 수 없는 파일이면 부팅을 거부합니다.`) add:

~~~~markdown

`observability.console.policy_file`에는 콘솔이 관리하는 GovernancePolicy 문서가 들어갑니다. 이름은 `console-`로 시작하고, 문서마다 `export`라는 FailOpen observability 규칙이 하나 있으며, 문서는 이름순으로 `---`로 구분합니다. 로컬 정책 파일 채널이 다른 정책 파일과 똑같이 읽습니다. 리비전은 파일 바이트의 SHA-256입니다.
~~~~

Refresh the companion hashes for `docs/reference/api.md`, `docs/api-reference.md`, `docs/reference/security.md` and `docs/reference/data.md` (Global Constraints command, once per file). Append to the `internal/CLAUDE.md` section:

```markdown
- `observabilityapi` rules: console documents are `console-…` with one FailOpen observability rule `export`; a console file holding anything else is read-only. An apply validates with `CheckLocal` (names, identity, ceiling) against the live set minus the console's own documents, writes the file, `Reload`s and restores the previous bytes when Reload fails. Content selections need `observability.console.content_rules` unless they stay unchanged.
```

- [ ] **Step 4: Run**

```bash
go test -buildvcs=false ./internal/server/... ./internal/policy/ -race -count=1 > $S/t9.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|panic)' $S/t9.log
python3 scripts/check_docs_translations.py > $S/t9-docs.log 2>&1; echo "docs=$?"
gofmt -l internal/server
```
Expected: `exit=0`, every package `ok`, `docs=0`, no gofmt output.

- [ ] **Step 5: Commit**

```bash
git -C $W add internal/server/observabilityapi docs/reference/api.md docs/reference/api.ko.md docs/api-reference.md docs/api-reference.ko.md docs/reference/security.md docs/reference/security.ko.md docs/reference/data.md docs/reference/data.ko.md internal/CLAUDE.md
git -C $W commit -s -m "feat(observabilityapi): console-owned observability rules through the local file channel" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 10: Gateway assembly, capability and acceptance

**Files:**
- Modify: `cmd/mayu/observability.go:3-16` (imports), `:26-55` (`buildObservability` loads the state file); add `ensureConsolePolicyFile` and `observabilityConsole`
- Modify: `cmd/mayu/gateway.go:107` (after the `LoadRaw` error check), `:1005` (capabilities literal, after `ProviderAutoHealth`), `:1055` (after the `WithEvaluations` line)
- Modify: `internal/server/configapi/capabilities.go:32` (commented field at the end of `Capabilities`, after the ADR-057 fields)
- Create: `internal/server/configapi/observability_caps_test.go`, `cmd/mayu/observability_console_test.go`
- Modify: `docs/operations/observability.md` and `docs/operations/observability.ko.md` (one paragraph after the signal table, EN line 223 / KO line 209, and a section at the end), `cmd/CLAUDE.md` (append to "Governed observability assembly")

**Interfaces:**
- Consumes: `observabilityapi.New`, `Options`, `LoadLimits`, `Console.Routes`, `SourceFiles`/`SourceControlPlane`/`SourceNone` (Tasks 7–9); `server.WithFullAdminRoutes` (Task 7); `tracing.LegacyTarget`; `audit.Writer.Append`/`Ready`; config `ObservabilityConsoleConfig` (Task 5).
- Produces:
  - `configapi.Capabilities.ObservabilityExport bool` (JSON `observability_export`)
  - cmd/mayu: `func ensureConsolePolicyFile(cfg *config.Config) error`, `func observabilityConsole(cfg *config.Config, set *otelexport.Set, pol *policy.Store, aud *audit.Writer) *observabilityapi.Console`

- [ ] **Step 1: Write the failing tests**

`internal/server/configapi/observability_caps_test.go`:

```go
package configapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCapabilitiesCarryObservabilityExport(t *testing.T) {
	h := CapabilitiesHandler(func() Capabilities { return Capabilities{ObservabilityExport: true} })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/capabilities", nil))
	if !strings.Contains(rec.Body.String(), `"observability_export":true`) {
		t.Fatalf("capabilities = %s", rec.Body)
	}
}
```

`cmd/mayu/observability_console_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/api/v1alpha1"
)

const consoleAdmin = "console-admin-token"

// withConsoleAdmin adds a break-glass admin token; the test sets
// OBS_CONSOLE_ADMIN to consoleAdmin.
func withConsoleAdmin(cfg map[string]any) {
	cfg["server"].(map[string]any)["admin_auth"] = map[string]any{"token_refs": []any{map[string]any{"env": "OBS_CONSOLE_ADMIN"}}}
}

func consoleCall(t *testing.T, g *gateway, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+consoleAdmin)
	rec := httptest.NewRecorder()
	g.adminSrv.Handler.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: status %d, body %q", method, path, rec.Code, rec.Body)
	}
	return rec.Code, out
}

// Review Focus 2: a pause applied from the console is persisted, enforced on
// the request path, audited, and still in force after a restart.
func TestGatewayConsoleLimitSurvivesRestart(t *testing.T) {
	t.Setenv("OBS_ACCEPTANCE_TOKEN", "Bearer acceptance-collector")
	t.Setenv("OBS_CONSOLE_ADMIN", consoleAdmin)
	corp, vault := newFakeOTLP(t), newFakeOTLP(t)
	doc := observabilityDoc(&v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}})
	var dir string
	path := routingAcceptanceConfig(t, &doc, func(cfg map[string]any, d string) {
		dir = d
		withExporters(corp, vault)(cfg, d)
		cfg["observability"].(map[string]any)["console"] = map[string]any{"state_file": filepath.Join(d, "observability-limits.json")}
		withConsoleAdmin(cfg)
	})
	g, key := bootObservability(t, path)
	code, st := consoleCall(t, g, "GET", "/admin/observability", nil)
	limits := st["limits"].(map[string]any)
	if code != 200 || limits["editable"] != true {
		t.Fatalf("status %d, limits %v", code, limits)
	}
	body := map[string]any{"dry_run": true, "expected_revision": limits["revision"], "exporters": map[string]any{"corp": map[string]any{"paused": true}}}
	if code, res := consoleCall(t, g, "PUT", "/admin/observability/limits", body); code != 200 || res["dry_run"] != true {
		t.Fatalf("dry run %d %v", code, res)
	}
	state := filepath.Join(dir, "observability-limits.json")
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("a dry run wrote the state file: %v", err)
	}
	body["dry_run"] = false
	code, applied := consoleCall(t, g, "PUT", "/admin/observability/limits", body)
	if code != 200 {
		t.Fatalf("apply %d %v", code, applied)
	}
	if rec := routingCall(t, g, key, "after the pause"); rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	flushObservability(t, g)
	if n := len(corp.events("inferplane.audit")); n != 0 {
		t.Fatalf("a paused exporter received %d audit copies", n)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"event":"admin_observability_changed"`) || !strings.Contains(string(raw), `"after":"`+applied["result_revision"].(string)+`"`) {
		t.Fatal("the apply left no admin_observability_changed record naming the new revision")
	}

	g2, err := newGateway(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		g2.dataLn.Close()
		g2.adminLn.Close()
		g2.store.Close()
	})
	defer flushObservability(t, g2)
	if !g2.otelSet.Limits()["corp"].Paused {
		t.Fatal("the pause did not survive a restart")
	}
}

func TestGatewayConsoleDamagedStateFileRefusesBoot(t *testing.T) {
	t.Setenv("OBS_ACCEPTANCE_TOKEN", "Bearer acceptance-collector")
	corp, vault := newFakeOTLP(t), newFakeOTLP(t)
	doc := observabilityDoc(&v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}})
	path := routingAcceptanceConfig(t, &doc, func(cfg map[string]any, d string) {
		withExporters(corp, vault)(cfg, d)
		state := filepath.Join(d, "observability-limits.json")
		if err := os.WriteFile(state, []byte(`{"version":1,"exporters":{"corp":{"pased":true}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg["observability"].(map[string]any)["console"] = map[string]any{"state_file": state}
		cfg["server"].(map[string]any)["listen"] = "invalid-listen-address"
	})
	g, err := newGateway(path)
	if g != nil {
		t.Fatal("a gateway with a damaged state file reached serving")
	}
	if err == nil || !strings.Contains(err.Error(), "state_file") {
		t.Fatalf("want a boot error naming the state file, got %v", err)
	}
}

func TestGatewayConsoleRulesLoadThroughPolicyFiles(t *testing.T) {
	t.Setenv("OBS_ACCEPTANCE_TOKEN", "Bearer acceptance-collector")
	t.Setenv("OBS_CONSOLE_ADMIN", consoleAdmin)
	corp, vault := newFakeOTLP(t), newFakeOTLP(t)
	doc := observabilityDoc(&v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}})
	doc.Spec.Subject.Team = "finance" // engineering exports nothing until the console rule
	var consoleFile string
	path := routingAcceptanceConfig(t, &doc, func(cfg map[string]any, d string) {
		withExporters(corp, vault)(cfg, d)
		consoleFile = filepath.Join(d, "console-observability.yaml")
		cfg["policies"] = append(cfg["policies"].([]string), consoleFile)
		cfg["observability"].(map[string]any)["console"] = map[string]any{"policy_file": consoleFile}
		withConsoleAdmin(cfg)
	})
	g, key := bootObservability(t, path)
	if info, err := os.Stat(consoleFile); err != nil || info.Size() != 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("console file at boot = %v, %v", info, err)
	}
	_, st := consoleCall(t, g, "GET", "/admin/observability", nil)
	rules := st["rules"].(map[string]any)
	if rules["editable"] != true || st["policy_source"] != "files" {
		t.Fatalf("rules = %v", rules)
	}
	content := map[string]any{"name": "console-eng-content", "subject": map[string]any{"team": "engineering"}, "content": map[string]any{"exporter": "vault"}}
	if code, res := consoleCall(t, g, "PUT", "/admin/observability/rules", map[string]any{"dry_run": true, "expected_revision": rules["revision"], "rules": []any{content}}); code != 400 || res["code"] != "content_not_allowed" {
		t.Fatalf("content without the opt-in = %d %v", code, res)
	}
	rule := map[string]any{"name": "console-eng", "subject": map[string]any{"team": "engineering"}, "audit": map[string]any{"exporter": "corp"}}
	code, res := consoleCall(t, g, "PUT", "/admin/observability/rules", map[string]any{"dry_run": false, "expected_revision": rules["revision"], "rules": []any{rule}})
	if code != 200 {
		t.Fatalf("apply = %d %v", code, res)
	}
	if b, _ := os.ReadFile(consoleFile); !strings.Contains(string(b), "name: console-eng") {
		t.Fatalf("console file = %s", b)
	}
	_, st = consoleCall(t, g, "GET", "/admin/observability", nil)
	found := false
	for _, s := range st["selections"].([]any) {
		sel := s.(map[string]any)
		found = found || (sel["policy"] == "console-eng" && sel["source"] == "console")
	}
	if !found {
		t.Fatalf("selections = %v", st["selections"])
	}
	if rec := routingCall(t, g, key, "console rule"); rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	flushObservability(t, g)
	completed := 0
	for _, l := range corp.events("inferplane.audit") {
		if strings.Contains(l.Body.GetStringValue(), `"event":"request_completed"`) {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("engineering audit copies after the console rule = %d", completed)
	}
}

// Review Focus 3: with a control plane, rules are read-only, a limit is still
// editable, and the narrowed signal never rejects a distributed document.
func TestGatewayConsoleReadOnlyUnderControlPlane(t *testing.T) {
	t.Setenv("OBS_ACCEPTANCE_TOKEN", "Bearer acceptance-collector")
	t.Setenv("OBS_CONSOLE_ADMIN", consoleAdmin)
	corp, vault := newFakeOTLP(t), newFakeOTLP(t)
	path := routingAcceptanceConfig(t, nil, func(cfg map[string]any, d string) {
		withExporters(corp, vault)(cfg, d)
		cfg["observability"].(map[string]any)["console"] = map[string]any{"state_file": filepath.Join(d, "observability-limits.json")}
		withConsoleAdmin(cfg)
	})
	g, _ := bootObservability(t, path)
	t.Cleanup(func() { flushObservability(t, g) })
	_, st := consoleCall(t, g, "GET", "/admin/observability", nil)
	if st["policy_source"] != "control_plane" || st["rules"].(map[string]any)["reason"] != "control_plane" || st["limits"].(map[string]any)["editable"] != true {
		t.Fatalf("status = %v", st)
	}
	if code, res := consoleCall(t, g, "PUT", "/admin/observability/rules", map[string]any{"dry_run": true, "rules": []any{}}); code != 409 || res["code"] != "read_only" {
		t.Fatalf("rules under a control plane = %d %v", code, res)
	}
	rev := st["limits"].(map[string]any)["revision"]
	if code, res := consoleCall(t, g, "PUT", "/admin/observability/limits", map[string]any{"dry_run": false, "expected_revision": rev,
		"exporters": map[string]any{"corp": map[string]any{"paused": true}}}); code != 200 {
		t.Fatalf("pause = %d %v", code, res)
	}
	mixed := observabilityDoc(&v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}})
	mixed.Spec.Rules = append(mixed.Spec.Rules, v1alpha1.Rule{Name: "money", FailurePolicy: v1alpha1.FailOpen, Budget: &v1alpha1.BudgetRule{LimitMilliUSD: 10_000}})
	if rej := g.polStore.ApplyWire([]v1alpha1.GovernancePolicy{mixed}); len(rej) != 0 {
		t.Fatalf("a runtime limit made a control-plane document fail: %+v", rej)
	}
	if _, ok := g.polStore.TeamLimits("engineering"); !ok {
		t.Fatal("the budget rule in the same document did not apply")
	}
	if got := g.otelSet.AuditTargets(g.polStore.Observability("engineering", "").Audit); got != nil {
		t.Fatalf("a paused exporter was selected: %v", got)
	}
}

func TestGatewayConsoleWithoutPolicyStore(t *testing.T) {
	t.Setenv("OBS_ACCEPTANCE_TOKEN", "Bearer acceptance-collector")
	t.Setenv("OBS_CONSOLE_ADMIN", consoleAdmin)
	corp, vault := newFakeOTLP(t), newFakeOTLP(t)
	path := routingAcceptanceConfig(t, nil, func(cfg map[string]any, d string) {
		delete(cfg, "control_plane") // neither policies nor a control plane: no policy store
		withExporters(corp, vault)(cfg, d)
		withConsoleAdmin(cfg)
	})
	g, _ := bootObservability(t, path)
	t.Cleanup(func() { flushObservability(t, g) })
	if g.polStore != nil {
		t.Fatal("expected no policy store")
	}
	code, st := consoleCall(t, g, "GET", "/admin/observability", nil)
	if code != 200 || st["configured"] != true || st["policy_source"] != "none" || len(st["selections"].([]any)) != 0 {
		t.Fatalf("status %d %v", code, st)
	}
	code, caps := consoleCall(t, g, "GET", "/admin/capabilities", nil)
	if code != 200 || caps["observability_export"] != true {
		t.Fatalf("capabilities %d %v", code, caps)
	}
}
```

- [ ] **Step 2: Run — expect failure**

```bash
go test -buildvcs=false ./internal/server/configapi/ ./cmd/mayu/ -run 'ObservabilityExport|GatewayConsole' -race -count=1 > $S/t10.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|panic)|unknown field|undefined' $S/t10.log
```
Expected: `exit=1` (`unknown field ObservabilityExport`; the console routes return 404 so `consoleCall` fails to decode).

- [ ] **Step 3: Implement**

`internal/server/configapi/capabilities.go`: at the end of `Capabilities`, after the ADR-057 `TraceLinkTemplate` field (line 32), add a commented field in the same style as `AuditConsole`:

```go
	// ObservabilityExport: governed OTLP export is configured
	// (observability.exporters); the console shows the Observability view.
	ObservabilityExport bool `json:"observability_export"`
```

(The comment line separates it from the aligned block above, so gofmt changes no other line.)

`cmd/mayu/observability.go`: replace the import block with

```go
import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/metrics"
	"github.com/inferplane/inferplane/internal/otelexport"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/server"
	"github.com/inferplane/inferplane/internal/server/observabilityapi"
	"github.com/inferplane/inferplane/internal/server/requestpolicy"
	"github.com/inferplane/inferplane/internal/tracing"
)
```

change the doc comment of `buildObservability` to

```go
// buildObservability resolves header refs, starts the log pipelines and
// installs the runtime limits persisted in observability.console.state_file.
// A nil Set (no observability block) is valid. A state file that does not
// parse fails boot: a pause must not silently lapse on restart.
```

and replace its last line (`return otelexport.New(o.ServiceName, exporters, m)`) with

```go
	set, err := otelexport.New(o.ServiceName, exporters, m)
	if err != nil {
		return nil, err
	}
	if c := o.Console; c != nil && c.StateFile != "" {
		notes, err := observabilityapi.LoadLimits(c.StateFile, set)
		if err != nil {
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // nothing is queued yet: stop the pipelines without waiting
			_ = set.Shutdown(ctx)
			return nil, err
		}
		for _, n := range notes {
			fmt.Fprintln(os.Stderr, "inferplane: observability state file:", n)
		}
	}
	return set, nil
```

Append to `cmd/mayu/observability.go`:

```go
// ensureConsolePolicyFile creates observability.console.policy_file empty
// (0600) when it does not exist yet, so the local file channel, which lists it
// in `policies`, can load it on first boot. An existing file is untouched.
func ensureConsolePolicyFile(cfg *config.Config) error {
	if cfg.Observability == nil || cfg.Observability.Console == nil || cfg.Observability.Console.PolicyFile == "" {
		return nil
	}
	f, err := os.OpenFile(cfg.Observability.Console.PolicyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create console policy file: %w", err)
	}
	return f.Close()
}

// observabilityConsole builds the full-admin observability console API. It is
// mounted even without an observability block, so the view can say what is
// missing instead of failing with 404.
func observabilityConsole(cfg *config.Config, set *otelexport.Set, pol *policy.Store, aud *audit.Writer) *observabilityapi.Console {
	opts := observabilityapi.Options{Set: set, PolicySource: observabilityapi.SourceNone, Emit: aud.Append, AuditReady: aud.Ready}
	switch {
	case cfg.ControlPlane != nil:
		opts.PolicySource = observabilityapi.SourceControlPlane
	case len(cfg.Policies) > 0:
		opts.PolicySource = observabilityapi.SourceFiles
	}
	if pol != nil {
		opts.Policies = pol // a nil *policy.Store must stay a nil interface
	}
	if o := cfg.Observability; o != nil && o.Console != nil {
		opts.StateFile, opts.PolicyFile, opts.ContentRules = o.Console.StateFile, o.Console.PolicyFile, o.Console.ContentRules
	}
	if o := cfg.OTel; o != nil {
		protocol := o.Protocol
		if protocol == "" {
			protocol = "http"
		}
		opts.Legacy = &observabilityapi.LegacyExporter{Endpoint: o.Endpoint, Protocol: protocol, TLS: !o.Insecure,
			SampleRatio: tracing.LegacyTarget(o.SampleRatio).Ratio}
	}
	return observabilityapi.New(opts)
}
```

`cmd/mayu/gateway.go`: after the `LoadRaw` error check (lines 104–107) insert

```go

	// The console-owned observability policy file (sub-project B) is listed in
	// `policies`; create it empty on first boot so the file channel can load it.
	if err := ensureConsolePolicyFile(raw); err != nil {
		return nil, fmt.Errorf("observability: %w", err)
	}
```

in the capabilities literal, after its last entry `ProviderAutoHealth:  healthStore != nil,` (line 1005) add

```go
			ObservabilityExport: obsSet != nil,
```

and after the `g.adminSrv.Handler = server.WithEvaluations(...)` line (1055) add

```go
	// Observability console (sub-project B): full-admin routes, reads included.
	g.adminSrv.Handler = server.WithFullAdminRoutes(g.adminSrv.Handler, observabilityConsole(cfg, obsSet, polStore, aud).Routes(),
		cfg.Server.AdminAuth.Tokens, oidcVerifier(cfg), oidcMapping(cfg), aud)
```

In `docs/operations/observability.md`, `## Governed OTLP export`, insert this paragraph between the signal table (its last row, the `content` row, line 223) and `Rules a data plane enforces:` (line 225), with one blank line on each side. It states what the base branch's `audit` row leaves implicit now that ADR-057 is in the base:

~~~~markdown
An exported audit record is the same canonical line every audit sink receives,
so it also carries the ADR-057 evidence fields a record has: `attempts`,
`client` (including the key-scoped `client.session_ref` digest), `auto_mode` and
`governance`. A collector that receives audit copies can therefore group one
key's requests by session, as the audit file can. Export adds no metric label:
`inferplane_otel_export_records_total` keeps `exporter`, `signal` and `outcome`.
~~~~

In `docs/operations/observability.ko.md`, insert the Korean paragraph between the signal table (line 209) and `데이터 영역이 지키는 규칙:` (line 211), with one blank line on each side:

~~~~markdown
내보낸 감사 기록은 모든 감사 저장 대상이 받는 것과 같은 정규 줄이므로, 기록에 있는 ADR-057 근거 필드인 `attempts`, `client`(키 범위 `client.session_ref` 다이제스트 포함), `auto_mode`, `governance`도 함께 담습니다. 따라서 감사 사본을 받는 수집기도 감사 파일처럼 한 키의 요청을 세션별로 묶을 수 있습니다. 내보내기로 메트릭 레이블이 늘지 않습니다. `inferplane_otel_export_records_total`의 레이블은 그대로 `exporter`, `signal`, `outcome`입니다.
~~~~

Append to `docs/operations/observability.md` (after the last paragraph, `Limits: metrics stay Prometheus exposition; …`):

~~~~markdown

### Observability console

Full administrators can open **Observability** in the gateway console
(`/admin/ui/`). It shows each declared exporter (endpoint, protocol, TLS, header
names, declared and current signals, queue bytes), records per signal since the
process started (sent, dropped, withheld by a limit, sampled out, last success,
last error as a status code), governed decisions per signal (exported, no rule,
rules disagree on content, withheld by a limit) and every observability rule with
its sampling after limits. Counters reset when mayu restarts; use
`inferplane_otel_export_records_total` and `inferplane_otel_export_decisions_total`
for history. The legacy `otel` exporter is listed read-only.

Edits are opt-in in the bootstrap config:

```json
"policies": ["/etc/inferplane/policies", "/var/lib/inferplane/console-observability.yaml"],
"observability": {
  "exporters": {
    "corp": { "endpoint": "otel-collector.internal:4318", "signals": ["traces", "audit"] }
  },
  "console": {
    "state_file": "/var/lib/inferplane/observability-limits.json",
    "policy_file": "/var/lib/inferplane/console-observability.yaml",
    "content_rules": false
  }
}
```

- **Runtime limits** (`state_file`): per declared exporter, pause it, keep a
  subset of its declared signals, lower `max_content_bytes`, or cap trace sampling
  with `max_sample_percent`. A limit only narrows: it cannot add a signal, raise a
  size, or change an endpoint, protocol, TLS setting or header. Limits withhold
  export when the decision is made; they never reject a policy document, so a
  control-plane document keeps its budget rules. They stay editable with a control
  plane because they narrow the local ceiling. mayu writes the file (0600,
  atomically) and loads it at boot; a file that does not parse refuses boot
  instead of resuming paused export. Entries the bootstrap no longer supports are
  narrowed and logged. Edit the file by hand only while mayu is stopped.
- **Console rules** (`policy_file`): GovernancePolicy documents named
  `console-…`, one FailOpen observability rule each, written by the console and
  loaded by the local policy file channel like every other policy file. The file
  must also be listed in `policies`, so it cannot be combined with
  `control_plane`; with a control plane the console shows rules read-only. Rules
  in other policy files are read-only too. Selecting content needs
  `content_rules: true`; without it the console can keep or remove a content
  selection, never add or redirect one. mayu creates an empty file on first boot.
- **Edit flow**: every edit is previewed first (`dry_run`), showing the changed
  fields, the rules whose export changes, or the decisions per subject. Apply sends
  the same body with the revision the preview saw (409 if anything changed in
  between), persists, applies (rules through a policy reload; a failed reload
  restores the file) and appends `admin_observability_changed` with the
  administrator and the SHA-256 of the persisted state before and after. Edits are
  refused while the audit chain is not accepting records.

Runtime limits and console rules apply without a restart; changes to
`observability.exporters` and `observability.console` still need one. The
[API reference](../api-reference.md#observability-console-mayu-only) lists the
request and response shapes.
~~~~

Append to `docs/operations/observability.ko.md` (after the last paragraph, `한계: 메트릭은 …`); the JSON block is byte-identical to the English one:

~~~~markdown

### 관측성 콘솔 {#observability-console}

전체 관리자는 게이트웨이 콘솔(`/admin/ui/`)의 **관측성**에서 다음을 볼 수 있습니다. 선언된 exporter마다 엔드포인트·프로토콜·TLS·헤더 이름·선언 신호와 현재 신호·큐 바이트, 프로세스 시작 이후 신호별 기록(전송·버림·한도로 보류·샘플링 제외·마지막 성공·상태 코드로 표시한 마지막 오류), 신호별 정책 결정(내보냄·규칙 없음·본문 규칙 불일치·한도로 보류), 한도 적용 뒤의 샘플링을 포함한 모든 observability 규칙입니다. 카운터는 mayu를 재시작하면 초기화되므로 이력은 `inferplane_otel_export_records_total`과 `inferplane_otel_export_decisions_total`로 보세요. 기존 `otel` exporter는 읽기 전용으로 표시합니다.

편집은 부트스트랩 설정에서 켜야 합니다.

```json
"policies": ["/etc/inferplane/policies", "/var/lib/inferplane/console-observability.yaml"],
"observability": {
  "exporters": {
    "corp": { "endpoint": "otel-collector.internal:4318", "signals": ["traces", "audit"] }
  },
  "console": {
    "state_file": "/var/lib/inferplane/observability-limits.json",
    "policy_file": "/var/lib/inferplane/console-observability.yaml",
    "content_rules": false
  }
}
```

- **실행 중 한도**(`state_file`): 선언된 exporter마다 일시 중지하거나, 선언 신호 중 일부만 남기거나, `max_content_bytes`를 낮추거나, `max_sample_percent`로 트레이스 샘플링 상한을 둘 수 있습니다. 한도는 좁히기만 합니다. 신호를 추가하거나 크기를 키우거나 엔드포인트·프로토콜·TLS 설정·헤더를 바꿀 수 없습니다. 한도는 결정 시점에 내보내기를 보류할 뿐 정책 문서를 거부하지 않으므로, 제어 영역 문서의 예산 규칙은 그대로 적용됩니다. 로컬 상한을 좁히는 것이므로 제어 영역이 있어도 편집할 수 있습니다. mayu가 파일을 0600 권한으로 원자적으로 쓰고 부팅 때 읽습니다. 읽을 수 없는 파일이면 일시 중지된 내보내기를 다시 켜는 대신 부팅을 거부합니다. 부트스트랩이 더는 지원하지 않는 항목은 더 좁은 쪽으로 맞추고 로그에 남깁니다. 파일은 mayu가 멈춰 있을 때만 직접 고치세요.
- **콘솔 규칙**(`policy_file`): `console-…` 이름의 GovernancePolicy 문서이며, 문서마다 FailOpen observability 규칙이 하나 있습니다. 콘솔이 쓰고 로컬 정책 파일 채널이 다른 정책 파일과 똑같이 읽습니다. 이 파일은 `policies`에도 넣어야 하므로 `control_plane`과 함께 쓸 수 없습니다. 제어 영역이 있으면 콘솔은 규칙을 읽기 전용으로 보여 줍니다. 다른 정책 파일의 규칙도 읽기 전용입니다. 본문을 고르려면 `content_rules: true`가 필요하며, 없으면 기존 본문 선택을 유지하거나 지울 수만 있고 새로 추가하거나 다른 곳으로 옮길 수는 없습니다. 첫 부팅 때 mayu가 빈 파일을 만듭니다.
- **편집 흐름**: 모든 편집은 먼저 미리보기(`dry_run`)로 바뀌는 필드, 내보내기가 달라지는 규칙, 주체별 결정을 보여 줍니다. 적용은 미리보기가 본 리비전과 함께 같은 본문을 보내며(그 사이 바뀌었으면 409), 저장하고 반영한 뒤(규칙은 정책 리로드로 반영하고, 리로드에 실패하면 파일을 되돌림) 관리자와 저장된 상태의 전후 SHA-256을 담은 `admin_observability_changed`를 기록합니다. 감사 체인이 기록을 받지 않는 동안에는 편집을 거부합니다.

실행 중 한도와 콘솔 규칙은 재시작 없이 반영됩니다. `observability.exporters`와 `observability.console` 변경은 여전히 재시작해야 합니다. 요청·응답 형식은 [API 레퍼런스](../api-reference.md#observability-console-mayu-only)에 있습니다.
~~~~

Refresh the companion hash for `docs/operations/observability.md`. Append to `cmd/CLAUDE.md` (end of the "Governed observability assembly" section):

```markdown

Console (sub-project B): `ensureConsolePolicyFile` creates an empty
`observability.console.policy_file` before `policy.NewStore`; `buildObservability`
loads `state_file` through `observabilityapi.LoadLimits` (boot fails on a file that
does not parse); the request-path closures go through `Set.AuditTargets`,
`ContentTarget` and `TraceTargets` so limits apply; `observabilityConsole` wires the
API (a nil `*policy.Store` stays a nil interface) and `gateway.go` mounts its routes
with `server.WithFullAdminRoutes` after `WithEvaluations`.
`observability_console_test.go` covers restart persistence, a damaged state file,
the file channel, control-plane read-only and the no-store case.
```

- [ ] **Step 4: Run**

```bash
go test -buildvcs=false ./internal/server/configapi/ ./cmd/mayu/ -race -count=1 > $S/t10.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|panic)' $S/t10.log
python3 scripts/check_docs_translations.py > $S/t10-docs.log 2>&1; echo "docs=$?"
gofmt -l cmd internal/server/configapi
git -C $W diff -U0 internal/server/configapi/capabilities.go | grep -c '^-[^-]'
```
Expected: `exit=0`, `docs=0`, no gofmt output, and `0` removed lines in `capabilities.go` (only the added field).

- [ ] **Step 5: Commit**

```bash
git -C $W add cmd/mayu/observability.go cmd/mayu/gateway.go cmd/mayu/observability_console_test.go internal/server/configapi/capabilities.go internal/server/configapi/observability_caps_test.go docs/operations/observability.md docs/operations/observability.ko.md cmd/CLAUDE.md
git -C $W commit -s -m "feat(mayu): assemble the observability console API" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 11: Console Observability view

**Files:**
- Create: `internal/server/adminui/static/observability.js`, `internal/server/adminui/testdata/observability_console.cjs`, `internal/server/adminui/observability_console_test.go`
- Modify: `internal/server/adminui/static/index.html:43` (nav button after **Audit**, before **Compare models**), `:272` (section after the audit `</section>`), `:630` (script tag after `audit.js`)
- Modify: `internal/server/adminui/static/app.js:91` (`VIEWS` entry after `audit`), `:127` (`showView`'s refreshers literal gains `observability: refreshObservabilityView`)
- Modify: `internal/server/adminui/static/i18n.js:557` (block after the last audit key, before the `};` that closes `MSG` at 558)
- Modify: `internal/server/adminui/static/style.css:159` (rules after the audit block, at the end of the file)
- Modify: `internal/server/adminui/adminui.go:23` (content type after `audit.js`)
- Modify: `internal/server/adminui/testdata/routing_forms.cjs:103`, `testdata/analytics_evidence.cjs:48`, `testdata/browser_qa.py:54,58` (load or serve `observability.js` next to `audit.js`)
- Modify: `internal/CLAUDE.md` (one bullet)

**Audit console interplay (checked at `caf55fe`):** `audit.js` is wired by name: `VIEWS.audit` and `audit: refreshAuditView` in `showView`'s refreshers literal (`app.js:91`, `:126`), pinned by `TestAuditViewWired`. This task follows that pattern instead of a registry. Because the literal is built on every `showView` call, every harness that loads `app.js` and switches views must also load `observability.js`, or it throws `ReferenceError: refreshObservabilityView is not defined`: `routing_forms.cjs` (`--views` calls `showView("teams")` and `showView("logs")`), `analytics_evidence.cjs` (`showView("logs")`) and `browser_qa.py` (serves only the files it lists; `audit_qa.py` subclasses its `Handler`). Step 2 updates all three. No audit console renderer, kind list or i18n key is affected: admin records never reach the console (Task 6).

**Interfaces:**
- Consumes: the status, limits and rules JSON of Tasks 7–9; `caps.observability_export` (Task 10); app.js globals `$`, `api`, `msg`, `MSG`, `td`, `emptyRow`, `exactJSON`, `parseExactJSON`, `adminToken`, `whoamiIsAdmin`, `showWorkspaceError`.
- Produces: observability.js globals `refreshObservabilityView()` (named in app.js's refreshers literal, like `refreshAuditView`) and pure helpers `obsTime`, `obsShortRev`, `obsBytes`, `obsDropped`, `obsSubjectText`, `obsTargetText`, `obsValue`, `obsChangeText`, `obsStateText`, `obsEffectText`, `obsRuleChangeText`, `obsDecisionSummary`, `obsDecisionText`, `obsErrorText`, `obsInt`, `obsLimitsBody`, `obsRuleError`, `obsRulesBody`.

- [ ] **Step 1: Write the failing checks**

`internal/server/adminui/observability_console_test.go`:

```go
package adminui

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func TestObservabilityViewWired(t *testing.T) {
	_, html := get(t, "/index.html")
	_, app := get(t, "/app.js")
	resp, js := get(t, "/observability.js")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/javascript") ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("observability.js served as %d %v", resp.StatusCode, resp.Header)
	}
	for _, want := range []string{`data-view="observability"`, `id="view-observability"`, `data-cap="observability_export"`, `<script src="observability.js"></script>`} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing %s", want)
		}
	}
	// The audit console mockup places Observability between Audit and Compare models.
	if a, o, e := strings.Index(html, `data-view="audit"`), strings.Index(html, `data-view="observability"`), strings.Index(html, `data-view="evaluation"`); !(a < o && o < e) {
		t.Error("the Observability nav button must sit between Audit and Compare models")
	}
	if !strings.Contains(app, `observability: "nav.observability"`) || !strings.Contains(app, "observability: refreshObservabilityView") {
		t.Error("app.js VIEWS/refreshers missing observability")
	}
	if !strings.Contains(js, "async function refreshObservabilityView(") {
		t.Error("observability.js does not define refreshObservabilityView")
	}
	for _, banned := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "localStorage", "sessionStorage", "document.cookie", "eval(", "ik_"} {
		if strings.Contains(js, banned) {
			t.Errorf("observability.js must not use %s", banned)
		}
	}
	for _, want := range []string{`"/admin/observability"`, `"/admin/observability/limits"`, `"/admin/observability/rules"`} {
		if !strings.Contains(js, want) {
			t.Errorf("observability.js does not call %s", want)
		}
	}
}

func TestObservabilityI18nKeysHaveFourLanguages(t *testing.T) {
	js, err := os.ReadFile("static/observability.js")
	if err != nil {
		t.Fatal(err)
	}
	html, _ := os.ReadFile("static/index.html")
	dict, _ := os.ReadFile("static/i18n.js")
	keys := map[string]bool{"nav.observability": true}
	for _, m := range regexp.MustCompile(`"(obs\.[A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)*)"`).FindAllStringSubmatch(string(js), -1) {
		keys[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`data-i18n(?:-placeholder)?="(obs\.[A-Za-z0-9_.]+)"`).FindAllStringSubmatch(string(html), -1) {
		keys[m[1]] = true
	}
	for prefix, names := range map[string][]string{
		"obs.source.":   {"files", "control_plane", "none", "console"},
		"obs.reason.":   {"not_configured", "no_state_file", "no_policy_file", "control_plane", "foreign_documents", "read_failed"},
		"obs.field.":    {"paused", "signals", "max_content_bytes", "max_sample_percent"},
		"obs.action.":   {"added", "changed", "removed"},
		"obs.decision.": {"selected", "no_rule", "content_conflict", "withheld_limit"},
		"obs.err.": {"revision_conflict", "limits_file_changed", "read_only", "policy_reload_failed", "console_file_foreign",
			"content_not_allowed", "audit_unavailable", "invalid_limits", "invalid_rules", "invalid_request", "persist_failed", "read_failed"},
	} {
		for _, n := range names {
			keys[prefix+n] = true
		}
	}
	for k := range keys {
		line := regexp.MustCompile(`(?m)^\s*"` + regexp.QuoteMeta(k) + `": \{.*\},?$`).FindString(string(dict))
		for _, lang := range []string{"en:", "ko:", "zh:", "ja:"} {
			if !strings.Contains(line, lang) {
				t.Errorf("i18n key %s missing %s", k, lang)
			}
		}
	}
}

func TestObservabilityConsoleNodeChecks(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("observability console helper checks require Node (standard library only)")
	}
	out, err := exec.Command(node, "testdata/observability_console.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("observability_console.cjs failed: %v\n%s", err, out)
	}
}
```

`internal/server/adminui/testdata/observability_console.cjs`:

```js
// Pure helper checks for observability.js (no DOM). Run: node testdata/observability_console.cjs
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const ctx = vm.createContext({ navigator: { language: "en" }, document: { getElementById: () => null, documentElement: {} }, console });
for (const f of ["i18n.js", "observability.js"]) vm.runInContext(fs.readFileSync(path.join(__dirname, "..", "static", f), "utf8"), ctx, { filename: f });
const run = src => vm.runInContext(src, ctx);
const json = src => JSON.parse(run("JSON.stringify(" + src + ")"));

assert.equal(run("typeof refreshObservabilityView"), "function");
assert.equal(run('obsBytes(2097152)'), "2 MiB");
assert.equal(run('obsBytes(1536)'), "1.5 KiB");
assert.equal(run('obsBytes(0)'), "0 B");
assert.equal(run('obsTime("2026-10-29T10:05:00Z")'), "2026-10-29 10:05:00");
assert.equal(run('obsTime("")'), "—");
assert.equal(run('obsShortRev("sha256:0123456789abcdef0123")'), "0123456789ab");
assert.equal(run('obsDropped({ dropped_queue_full: 1, dropped_oversize: 2, dropped_export_error: 9007199254740993n })'), "9007199254740996");
assert.equal(run('obsSubjectText({ team: "eng", user: "alice" })'), "team eng · user alice");
assert.equal(run('obsSubjectText({})'), "—");
assert.equal(run('obsTargetText({ signal: "traces", exporter: "corp", sample_percent: 50, effective_percent: 10 })'), "traces → corp · 50% → 10%");
assert.equal(run('obsTargetText({ signal: "audit", exporter: "corp" })'), "audit → corp");

// Limits body: bootstrap-equal rows send nothing; narrowing rows send only what narrows.
const rows = JSON.stringify([
  { exporter: "corp", declared: ["audit", "traces"], paused: false, signals: ["audit", "traces"], max_content_bytes: "", max_sample_percent: "" },
  { exporter: "vault", declared: ["content"], paused: true, signals: ["content"], max_content_bytes: "1024", max_sample_percent: "" },
  { exporter: "siem", declared: ["audit", "traces"], paused: false, signals: ["traces"], max_content_bytes: "", max_sample_percent: "20" },
]);
assert.deepEqual(json(`obsLimitsBody(${rows}, true, "sha256:a")`), { dry_run: true, expected_revision: "sha256:a", exporters: {
  vault: { paused: true, max_content_bytes: 1024 }, siem: { signals: ["traces"], max_sample_percent: 20 } } });
assert.throws(() => run('obsLimitsBody([{ exporter: "corp", declared: ["audit"], paused: false, signals: [], max_content_bytes: "", max_sample_percent: "" }], true, "r")'), /keep at least one signal/);
assert.throws(() => run('obsLimitsBody([{ exporter: "corp", declared: ["audit"], paused: false, signals: ["audit"], max_content_bytes: "1.5", max_sample_percent: "" }], true, "r")'), /whole number/);
assert.throws(() => run('obsLimitsBody([{ exporter: "corp", declared: ["traces"], paused: false, signals: ["traces"], max_content_bytes: "", max_sample_percent: "101" }], true, "r")'), /1 to 100/);

// Rules body and client-side validation.
assert.equal(run('obsRuleError({ name: "finance", team: "fin", user: "", traces: "", audit: "corp", content: "" })'), "obs.err.ruleName");
assert.equal(run('obsRuleError({ name: "console-fin", team: "", user: "", traces: "", audit: "corp", content: "" })'), "obs.err.ruleSubject");
assert.equal(run('obsRuleError({ name: "console-fin", team: "fin", user: "", traces: "", audit: "", content: "" })'), "obs.err.ruleSignal");
assert.equal(run('obsRuleError({ name: "console-fin", team: "fin", user: "", traces: "corp", sample_percent: "0", audit: "", content: "" })'), "obs.err.percent");
assert.equal(run('obsRuleError({ name: "console-" + "a".repeat(55), team: "fin", user: "", traces: "", audit: "corp", content: "" })'), "");
assert.equal(run('obsRuleError({ name: "console-" + "a".repeat(56), team: "fin", user: "", traces: "", audit: "corp", content: "" })'), "obs.err.ruleName");
assert.deepEqual(json('obsRulesBody([{ name: " console-fin ", team: "fin", user: "", traces: "corp", sample_percent: "20", audit: "corp", content: "" }], false, "sha256:b")'),
  { dry_run: false, expected_revision: "sha256:b", rules: [{ name: "console-fin", subject: { team: "fin" }, traces: { exporter: "corp", sample_percent: 20 }, audit: { exporter: "corp" } }] });
assert.throws(() => run('obsRulesBody([{ name: "finance", team: "fin", user: "", traces: "", sample_percent: "", audit: "corp", content: "" }], true, "r")'), /console-/);

// Diff and error text.
assert.equal(run('obsChangeText({ exporter: "corp", field: "signals", before: ["audit", "traces"], after: ["traces"] })'), "corp · signals: audit, traces → traces");
assert.equal(run('obsChangeText({ exporter: "corp", field: "paused", before: false, after: true })'), "corp · paused: no → yes");
assert.equal(run('obsChangeText({ exporter: "vault", field: "max_content_bytes", before: 2097152, after: 1024 })'), "vault · max content bytes: 2 MiB → 1 KiB");
assert.equal(run('obsEffectText({ policy: "p", rule: "export", signal: "traces", exporter: "corp", before: { exported: true, sample_percent: 20 }, after: { exported: false } })'),
  "p · export · traces → corp: 20% → withheld by limit");
assert.equal(run('obsRuleChangeText({ name: "console-fin", action: "added" })'), "console-fin · added");
assert.equal(run('obsDecisionText({ subject: { team: "fin" }, before: { traces: [], audit: [] }, after: { traces: [{ exporter: "corp", sample_percent: 20 }], audit: ["corp"], content_conflict: true } })'),
  "team fin: none → traces corp 20%, audit corp, content off: rules disagree");
assert.equal(run('obsErrorText({ code: "revision_conflict", error: "x" }, 409)'), "Someone changed this state since it was loaded. Refresh and preview again. — x");
assert.equal(run('obsErrorText(null, 502)'), "Request failed (HTTP 502).");
console.log("observability_console.cjs: ok");
```

Run: `go test -buildvcs=false ./internal/server/adminui/ -run 'Observability' -count=1 > $S/t11.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|    )' $S/t11.log`
Expected: `exit=1` (`/observability.js` 404, markup and keys missing).

- [ ] **Step 2: Serve the asset and add the hooks**

`internal/server/adminui/adminui.go`: in `contentTypes`, after the `"audit.js":` entry (line 23) add the new entry; run `gofmt -w internal/server/adminui/adminui.go`, which realigns the map because the new key is the longest:

```go
var contentTypes = map[string]string{
	"index.html":       "text/html; charset=utf-8",
	"app.js":           "text/javascript; charset=utf-8",
	"evaluation.js":    "text/javascript; charset=utf-8",
	"audit.js":         "text/javascript; charset=utf-8",
	"observability.js": "text/javascript; charset=utf-8",
	"i18n.js":          "text/javascript; charset=utf-8",
	"style.css":        "text/css; charset=utf-8",
	"favicon.svg":      "image/svg+xml",
}
```

`internal/server/adminui/static/app.js`: in `VIEWS`, after `  audit: "nav.audit",` (line 91) add `  observability: "nav.observability",`; in `showView`, replace the second line of the refreshers literal (line 127)

```js
    teams: refreshTeamsView, providers: refreshProviders, governance: refreshGovernance, evaluation: refreshEvaluation };
```

with

```js
    teams: refreshTeamsView, providers: refreshProviders, governance: refreshGovernance, evaluation: refreshEvaluation,
    observability: refreshObservabilityView };
```

The `if (refreshers[name]) …` dispatch line stays as it is.

Load or serve `observability.js` wherever `audit.js` is loaded with `app.js`:

- `internal/server/adminui/testdata/routing_forms.cjs`: after the `audit.js` line (103) add
  `vm.runInContext(fs.readFileSync(path.join(__dirname, "../static/observability.js"), "utf8"), context);`
- `internal/server/adminui/testdata/analytics_evidence.cjs` line 48: change the list `["i18n.js", "app.js", "evaluation.js", "audit.js"]` to `["i18n.js", "app.js", "evaluation.js", "audit.js", "observability.js"]`.
- `internal/server/adminui/testdata/browser_qa.py` lines 54 and 58: add `'observability.js'` after `'audit.js'` to the served-name tuple, and `'observability.js': 'text/javascript'` after the `'audit.js'` entry of the content-type dict.

`internal/server/adminui/static/index.html`: after the Audit nav button (line 43), before Compare models, add

```html
        <button class="nav" data-view="observability">◎&ensp;<span data-i18n="nav.observability">Observability</span></button>
```

after `<script src="audit.js"></script>` (line 630) add `  <script src="observability.js"></script>`, and after the audit section's closing `</section>` (line 272, before the `<!-- ===== keys ===== -->` comment) insert:

```html

      <!-- ===== observability (sub-project B) ===== -->
      <section id="view-observability" class="view" hidden>
        <div class="card affordance" data-cap="observability_export">
          <div class="microlabel" data-i18n="obs.enableTitle">governed OTLP export</div>
          <p class="hint" data-i18n="obs.enableHint">Declare collectors under observability.exporters in mayu's config to export traces, audit copies and egress content per policy. This view then shows exporter health and lets full administrators narrow export at runtime.</p>
        </div>
        <p id="obs-admin-only" class="hint" hidden data-i18n="obs.adminOnly">Only full administrators can open the observability console.</p>
        <div id="obs-content" hidden>
          <div class="obs-head">
            <p id="obs-since" class="hint"></p>
            <p id="obs-source" class="hint"></p>
            <button id="obs-refresh" type="button" class="ghost" data-i18n="obs.refresh">Refresh</button>
          </div>
          <div class="card">
            <h2 data-i18n="obs.exportersTitle">Exporters</h2>
            <p class="hint" data-i18n="obs.exportersHint">Destinations come only from mayu's config. Header values are never shown.</p>
            <table class="data" id="obs-exporters"><thead><tr>
              <th data-i18n="obs.colExporter">exporter</th><th data-i18n="obs.colEndpoint">endpoint</th><th data-i18n="obs.colSignals">signals (declared → now)</th>
              <th data-i18n="obs.colQueue">queue (queued / limit)</th><th data-i18n="obs.colContentCap">content cap (now)</th>
            </tr></thead><tbody></tbody></table>
          </div>
          <div class="card">
            <h2 data-i18n="obs.signalsTitle">Records by signal</h2>
            <p class="hint" data-i18n="obs.signalsHint">Counted since this gateway process started; Prometheus keeps the history. Errors show a status code or a fixed message, never collector responses.</p>
            <table class="data" id="obs-signals"><thead><tr>
              <th data-i18n="obs.colExporter">exporter</th><th data-i18n="obs.colSignal">signal</th><th class="num" data-i18n="obs.colSent">sent</th>
              <th class="num" data-i18n="obs.colDropped">dropped</th><th class="num" data-i18n="obs.colWithheld">withheld by limit</th><th class="num" data-i18n="obs.colSampledOut">sampled out</th>
              <th data-i18n="obs.colLastSuccess">last success (UTC)</th><th data-i18n="obs.colLastError">last error (UTC)</th>
            </tr></thead><tbody></tbody></table>
          </div>
          <div class="obs-two">
            <div class="card">
              <h2 data-i18n="obs.decisionsTitle">Governed decisions</h2>
              <p class="hint" data-i18n="obs.decisionsHint">Audit is counted per record, content per request that reached a provider, traces per span.</p>
              <table class="data" id="obs-decisions"><thead><tr>
                <th data-i18n="obs.colSignal">signal</th><th class="num" data-i18n="obs.decision.selected">exported</th><th class="num" data-i18n="obs.decision.no_rule">no rule</th>
                <th class="num" data-i18n="obs.decision.content_conflict">rules disagree</th><th class="num" data-i18n="obs.decision.withheld_limit">withheld by limit</th>
              </tr></thead><tbody></tbody></table>
            </div>
            <div class="card">
              <h2 data-i18n="obs.selectionsTitle">Policy selections</h2>
              <p class="hint" data-i18n="obs.selectionsHint">Every observability rule this gateway enforces, with sampling after limits.</p>
              <table class="data" id="obs-selections"><thead><tr>
                <th data-i18n="obs.colPolicy">policy · rule</th><th data-i18n="obs.colSource">source</th><th data-i18n="obs.colSubject">subject</th>
                <th data-i18n="obs.colTarget">signal → exporter</th><th data-i18n="obs.colState">state</th>
              </tr></thead><tbody></tbody></table>
            </div>
          </div>
          <div class="card" id="obs-limits-card">
            <h2 data-i18n="obs.limitsTitle">Runtime limits</h2>
            <p class="hint" data-i18n="obs.limitsHint">A limit can only narrow what mayu's config allows: pause an exporter, drop signals, lower the content cap or the trace sample rate. Endpoints and credentials cannot be changed here.</p>
            <p id="obs-limits-readonly" class="hint" hidden></p>
            <div id="obs-limits-rows"></div>
            <div class="row"><button id="obs-limits-preview" type="button" class="ghost" data-i18n="obs.preview">Preview changes</button><button id="obs-limits-apply" type="button" disabled data-i18n="obs.apply">Apply</button></div>
            <p id="obs-limits-status" class="hint" role="status" aria-live="polite"></p>
            <ul id="obs-limits-diff" class="obs-diff"></ul>
          </div>
          <div class="card" id="obs-rules-card">
            <h2 data-i18n="obs.rulesTitle">Console rules</h2>
            <p class="hint" data-i18n="obs.rulesHint">Rules saved here are GovernancePolicy documents in the console policy file, loaded by the local policy file channel. Rules from other policy files or the control plane are read-only.</p>
            <p id="obs-rules-readonly" class="hint" hidden></p>
            <table class="data" id="obs-rules-table"><thead><tr>
              <th data-i18n="obs.colName">name</th><th data-i18n="obs.colTeam">team</th><th data-i18n="obs.colUser">user</th><th data-i18n="obs.colTraces">traces · %</th>
              <th data-i18n="obs.colAudit">audit</th><th data-i18n="obs.colContent">content</th><th></th>
            </tr></thead><tbody></tbody></table>
            <div class="row"><button id="obs-rules-add" type="button" class="ghost" data-i18n="obs.addRule">Add rule</button><button id="obs-rules-preview" type="button" class="ghost" data-i18n="obs.preview">Preview changes</button><button id="obs-rules-apply" type="button" disabled data-i18n="obs.apply">Apply</button></div>
            <p id="obs-rules-status" class="hint" role="status" aria-live="polite"></p>
            <ul id="obs-rules-diff" class="obs-diff"></ul>
          </div>
        </div>
      </section>
```

`internal/server/adminui/static/style.css`: append at the end of the file, after the audit block (last line 159, its `@media(max-width:480px)` rule), in the same style as `/* Audit view (ADR-057): … */`:

```css
/* Observability console (sub-project B). */
.obs-head { display:flex; flex-wrap:wrap; align-items:center; justify-content:space-between; gap:8px 16px; margin-bottom:16px; }
.obs-head .hint { margin:0; overflow-wrap:anywhere; }
.obs-two { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:20px; }
.obs-limit { min-width:0; border:1px solid var(--line); border-radius:8px; padding:14px 16px; margin:0 0 14px; display:flex; flex-wrap:wrap; gap:10px 20px; align-items:end; }
.obs-limit legend { font-weight:600; padding:0 6px; }
.obs-limit .form-field { flex:0 1 200px; }
.obs-check { display:inline-flex; align-items:center; gap:8px; font-size:12px; }
.obs-check input { width:15px; margin:0; accent-color:#6753d8; }
.obs-inline { display:flex; gap:8px; } .obs-inline select { flex:1 1 110px; } .obs-inline input { flex:0 0 72px; margin:0; }
#obs-rules-table input,#obs-rules-table select { margin:0; min-width:96px; }
.obs-diff { margin:8px 0 0; padding-left:18px; font-size:12px; } .obs-diff li { margin:4px 0; overflow-wrap:anywhere; }
#view-observability table.data td { overflow-wrap:break-word; } /* words stay whole; the .table-scroll wrapper scrolls */
#obs-rules-table button { white-space:nowrap; }
@media(max-width:1600px) { .obs-two { grid-template-columns:1fr; } }
```

`internal/server/adminui/static/i18n.js`: after the last audit key, `"audit.showingFirst": …` (line 557), and before the `};` that closes `MSG` (line 558), insert this block (`nav.observability` opens it, as `nav.audit` opens the audit block):

```js

  // observability console (sub-project B)
  "nav.observability": { en: "Observability", ko: "관측성", zh: "可观测性", ja: "オブザーバビリティ" },
  "obs.enableTitle": { en: "governed OTLP export", ko: "정책 기반 OTLP 내보내기", zh: "受治理的 OTLP 导出", ja: "ポリシー制御の OTLP エクスポート" },
  "obs.enableHint": { en: "Declare collectors under observability.exporters in mayu's config to export traces, audit copies and egress content per policy. This view then shows exporter health and lets full administrators narrow export at runtime.", ko: "mayu 설정의 observability.exporters에 수집기를 선언하면 정책에 따라 트레이스·감사 사본·송신 본문을 내보냅니다. 그러면 이 화면에서 exporter 상태를 보고, 전체 관리자가 실행 중에 내보내기를 좁힐 수 있습니다.", zh: "在 mayu 配置的 observability.exporters 中声明收集器后，即可按策略导出追踪、审计副本和出站内容。此页面随后显示导出器状态，完整管理员可在运行时收窄导出。", ja: "mayu の設定の observability.exporters にコレクターを宣言すると、ポリシーに従ってトレース・監査コピー・送信内容をエクスポートします。この画面でエクスポーターの状態を確認でき、全体管理者は実行中にエクスポートを絞り込めます。" },
  "obs.adminOnly": { en: "Only full administrators can open the observability console.", ko: "관측성 콘솔은 전체 관리자만 열 수 있습니다.", zh: "只有完整管理员可以打开可观测性控制台。", ja: "オブザーバビリティコンソールは全体管理者のみ開けます。" },
  "obs.refresh": { en: "Refresh", ko: "새로고침", zh: "刷新", ja: "更新" },
  "obs.exportersTitle": { en: "Exporters", ko: "Exporter", zh: "导出器", ja: "エクスポーター" },
  "obs.exportersHint": { en: "Destinations come only from mayu's config. Header values are never shown.", ko: "목적지는 mayu 설정에서만 정해집니다. 헤더 값은 표시하지 않습니다.", zh: "目的地只来自 mayu 配置。不会显示请求头的值。", ja: "送信先は mayu の設定でのみ決まります。ヘッダーの値は表示しません。" },
  "obs.colExporter": { en: "exporter", ko: "exporter", zh: "导出器", ja: "エクスポーター" },
  "obs.colEndpoint": { en: "endpoint", ko: "엔드포인트", zh: "端点", ja: "エンドポイント" },
  "obs.colSignals": { en: "signals (declared → now)", ko: "신호 (선언 → 현재)", zh: "信号（声明 → 当前）", ja: "シグナル（宣言 → 現在）" },
  "obs.colQueue": { en: "queue (queued / limit)", ko: "큐 (대기 / 한도)", zh: "队列（排队 / 上限）", ja: "キュー（待機 / 上限）" },
  "obs.colContentCap": { en: "content cap (now)", ko: "본문 상한 (현재)", zh: "内容上限（当前）", ja: "本文上限（現在）" },
  "obs.signalsTitle": { en: "Records by signal", ko: "신호별 기록", zh: "按信号统计的记录", ja: "シグナル別レコード" },
  "obs.signalsHint": { en: "Counted since this gateway process started; Prometheus keeps the history. Errors show a status code or a fixed message, never collector responses.", ko: "이 게이트웨이 프로세스가 시작된 뒤의 개수이며 이력은 Prometheus에 남습니다. 오류는 상태 코드나 고정 문구로만 보이고 수집기 응답은 표시하지 않습니다.", zh: "自本网关进程启动以来的计数；历史数据保存在 Prometheus 中。错误只显示状态码或固定消息，不显示收集器响应。", ja: "このゲートウェイプロセスの起動以降の件数です。履歴は Prometheus に残ります。エラーはステータスコードか固定メッセージのみで、コレクターの応答は表示しません。" },
  "obs.colSignal": { en: "signal", ko: "신호", zh: "信号", ja: "シグナル" },
  "obs.colSent": { en: "sent", ko: "전송", zh: "已发送", ja: "送信済み" },
  "obs.colDropped": { en: "dropped", ko: "버림", zh: "已丢弃", ja: "破棄" },
  "obs.colWithheld": { en: "withheld by limit", ko: "한도로 보류", zh: "因限制而保留", ja: "制限で保留" },
  "obs.colSampledOut": { en: "sampled out", ko: "샘플링 제외", zh: "未被采样", ja: "サンプリング対象外" },
  "obs.colLastSuccess": { en: "last success (UTC)", ko: "마지막 성공 (UTC)", zh: "最近成功（UTC）", ja: "最終成功（UTC）" },
  "obs.colLastError": { en: "last error (UTC)", ko: "마지막 오류 (UTC)", zh: "最近错误（UTC）", ja: "最終エラー（UTC）" },
  "obs.decisionsTitle": { en: "Governed decisions", ko: "정책 결정", zh: "治理决策", ja: "ポリシー判定" },
  "obs.decisionsHint": { en: "Audit is counted per record, content per request that reached a provider, traces per span.", ko: "감사는 기록마다, 본문은 공급자에 도달한 요청마다, 트레이스는 span마다 셉니다.", zh: "审计按记录计数，内容按到达提供方的请求计数，追踪按 span 计数。", ja: "監査はレコードごと、本文はプロバイダーに届いたリクエストごと、トレースは span ごとに数えます。" },
  "obs.decision.selected": { en: "exported", ko: "내보냄", zh: "已导出", ja: "エクスポート" },
  "obs.decision.no_rule": { en: "no rule", ko: "규칙 없음", zh: "无规则", ja: "ルールなし" },
  "obs.decision.content_conflict": { en: "rules disagree", ko: "규칙 불일치", zh: "规则不一致", ja: "ルール不一致" },
  "obs.decision.withheld_limit": { en: "withheld by limit", ko: "한도로 보류", zh: "因限制而保留", ja: "制限で保留" },
  "obs.selectionsTitle": { en: "Policy selections", ko: "정책 선택", zh: "策略选择", ja: "ポリシー選択" },
  "obs.selectionsHint": { en: "Every observability rule this gateway enforces, with sampling after limits.", ko: "이 게이트웨이가 적용하는 모든 observability 규칙과 한도 적용 뒤의 샘플링입니다.", zh: "本网关执行的所有 observability 规则，以及应用限制后的采样率。", ja: "このゲートウェイが適用するすべての observability ルールと、制限適用後のサンプリングです。" },
  "obs.colPolicy": { en: "policy · rule", ko: "정책 · 규칙", zh: "策略 · 规则", ja: "ポリシー · ルール" },
  "obs.colSource": { en: "source", ko: "출처", zh: "来源", ja: "出所" },
  "obs.colSubject": { en: "subject", ko: "주체", zh: "主体", ja: "対象" },
  "obs.colTarget": { en: "signal → exporter", ko: "신호 → exporter", zh: "信号 → 导出器", ja: "シグナル → エクスポーター" },
  "obs.colState": { en: "state", ko: "상태", zh: "状态", ja: "状態" },
  "obs.limitsTitle": { en: "Runtime limits", ko: "실행 중 한도", zh: "运行时限制", ja: "実行時の制限" },
  "obs.limitsHint": { en: "A limit can only narrow what mayu's config allows: pause an exporter, drop signals, lower the content cap or the trace sample rate. Endpoints and credentials cannot be changed here.", ko: "한도는 mayu 설정이 허용한 범위를 좁히기만 합니다. exporter 일시 중지, 신호 제외, 본문 상한이나 트레이스 샘플링 비율 낮추기만 할 수 있으며 엔드포인트와 자격 증명은 여기서 바꿀 수 없습니다.", zh: "限制只能收窄 mayu 配置允许的范围：暂停导出器、去掉信号、降低内容上限或追踪采样率。此处不能更改端点和凭据。", ja: "制限は mayu の設定が許す範囲を狭めるだけです。エクスポーターの一時停止、シグナルの除外、本文上限やトレースのサンプリング率の引き下げのみ可能で、エンドポイントと認証情報はここでは変更できません。" },
  "obs.preview": { en: "Preview changes", ko: "변경 미리보기", zh: "预览更改", ja: "変更をプレビュー" },
  "obs.apply": { en: "Apply", ko: "적용", zh: "应用", ja: "適用" },
  "obs.rulesTitle": { en: "Console rules", ko: "콘솔 규칙", zh: "控制台规则", ja: "コンソールルール" },
  "obs.rulesHint": { en: "Rules saved here are GovernancePolicy documents in the console policy file, loaded by the local policy file channel. Rules from other policy files or the control plane are read-only.", ko: "여기서 저장한 규칙은 콘솔 정책 파일의 GovernancePolicy 문서이며 로컬 정책 파일 채널이 읽습니다. 다른 정책 파일이나 제어 영역의 규칙은 읽기 전용입니다.", zh: "此处保存的规则是控制台策略文件中的 GovernancePolicy 文档，由本地策略文件通道加载。来自其他策略文件或控制平面的规则为只读。", ja: "ここで保存したルールはコンソールポリシーファイルの GovernancePolicy 文書で、ローカルのポリシーファイルチャネルが読み込みます。ほかのポリシーファイルやコントロールプレーンのルールは読み取り専用です。" },
  "obs.colName": { en: "name", ko: "이름", zh: "名称", ja: "名前" },
  "obs.colTeam": { en: "team", ko: "팀", zh: "团队", ja: "チーム" },
  "obs.colUser": { en: "user", ko: "사용자", zh: "用户", ja: "ユーザー" },
  "obs.colTraces": { en: "traces · %", ko: "트레이스 · %", zh: "追踪 · %", ja: "トレース · %" },
  "obs.colAudit": { en: "audit", ko: "감사", zh: "审计", ja: "監査" },
  "obs.colContent": { en: "content", ko: "본문", zh: "内容", ja: "本文" },
  "obs.addRule": { en: "Add rule", ko: "규칙 추가", zh: "添加规则", ja: "ルールを追加" },
  "obs.since": { en: "Counters since {t} UTC (this gateway process)", ko: "{t} UTC 이후 집계 (이 게이트웨이 프로세스)", zh: "自 {t} UTC 起的计数（本网关进程）", ja: "{t} UTC 以降の集計（このゲートウェイプロセス）" },
  "obs.notConfigured": { en: "Governed export is not configured on this gateway.", ko: "이 게이트웨이에는 정책 기반 내보내기가 설정되지 않았습니다.", zh: "此网关未配置受治理的导出。", ja: "このゲートウェイにはポリシー制御のエクスポートが設定されていません。" },
  "obs.sourceLabel": { en: "Policy source", ko: "정책 출처", zh: "策略来源", ja: "ポリシーの出所" },
  "obs.source.files": { en: "policy files", ko: "정책 파일", zh: "策略文件", ja: "ポリシーファイル" },
  "obs.source.control_plane": { en: "control plane", ko: "제어 영역", zh: "控制平面", ja: "コントロールプレーン" },
  "obs.source.none": { en: "none", ko: "없음", zh: "无", ja: "なし" },
  "obs.source.console": { en: "console", ko: "콘솔", zh: "控制台", ja: "コンソール" },
  "obs.paused": { en: "paused", ko: "일시 중지", zh: "已暂停", ja: "一時停止" },
  "obs.plaintext": { en: "plaintext", ko: "평문", zh: "明文", ja: "平文" },
  "obs.headers": { en: "headers", ko: "헤더", zh: "请求头", ja: "ヘッダー" },
  "obs.legacy": { en: "legacy otel block, read-only", ko: "기존 otel 블록, 읽기 전용", zh: "旧版 otel 配置，只读", ja: "従来の otel ブロック、読み取り専用" },
  "obs.exported": { en: "exported", ko: "내보냄", zh: "已导出", ja: "エクスポート" },
  "obs.withheld": { en: "withheld by limit", ko: "한도로 보류", zh: "因限制而保留", ja: "制限で保留" },
  "obs.noExporters": { en: "no exporters declared", ko: "선언된 exporter 없음", zh: "未声明导出器", ja: "宣言されたエクスポーターなし" },
  "obs.noDecisions": { en: "no decisions yet", ko: "아직 결정 없음", zh: "尚无决策", ja: "まだ判定なし" },
  "obs.noSelections": { en: "no observability rules", ko: "observability 규칙 없음", zh: "无 observability 规则", ja: "observability ルールなし" },
  "obs.noRules": { en: "no console rules", ko: "콘솔 규칙 없음", zh: "无控制台规则", ja: "コンソールルールなし" },
  "obs.team": { en: "team", ko: "팀", zh: "团队", ja: "チーム" },
  "obs.user": { en: "user", ko: "사용자", zh: "用户", ja: "ユーザー" },
  "obs.maxContent": { en: "Max content bytes", ko: "본문 최대 바이트", zh: "内容最大字节数", ja: "本文の最大バイト数" },
  "obs.maxSample": { en: "Max trace sample %", ko: "트레이스 최대 샘플링 %", zh: "追踪最大采样 %", ja: "トレースの最大サンプリング %" },
  "obs.previewFirst": { en: "Preview the changes before applying.", ko: "적용하기 전에 변경을 미리 보세요.", zh: "应用前请先预览更改。", ja: "適用する前に変更をプレビューしてください。" },
  "obs.formChanged": { en: "The form changed after the preview. Preview again.", ko: "미리보기 뒤에 입력이 바뀌었습니다. 다시 미리 보세요.", zh: "预览后表单已更改，请重新预览。", ja: "プレビュー後に入力が変わりました。もう一度プレビューしてください。" },
  "obs.noChanges": { en: "No changes.", ko: "변경 사항이 없습니다.", zh: "没有更改。", ja: "変更はありません。" },
  "obs.previewed": { en: "Preview: {n} change(s). Nothing is saved until you apply.", ko: "미리보기: 변경 {n}건. 적용하기 전에는 저장되지 않습니다.", zh: "预览：{n} 项更改。应用前不会保存。", ja: "プレビュー: 変更 {n} 件。適用するまで保存されません。" },
  "obs.applied": { en: "Applied. Revision {r}.", ko: "적용했습니다. 리비전 {r}.", zh: "已应用。修订版 {r}。", ja: "適用しました。リビジョン {r}。" },
  "obs.reason.not_configured": { en: "Read-only: no observability exporters are declared.", ko: "읽기 전용: 선언된 observability exporter가 없습니다.", zh: "只读：未声明 observability 导出器。", ja: "読み取り専用: observability エクスポーターが宣言されていません。" },
  "obs.reason.no_state_file": { en: "Read-only: set observability.console.state_file to edit limits.", ko: "읽기 전용: 한도를 편집하려면 observability.console.state_file을 설정하세요.", zh: "只读：设置 observability.console.state_file 后才能编辑限制。", ja: "読み取り専用: 制限を編集するには observability.console.state_file を設定してください。" },
  "obs.reason.no_policy_file": { en: "Read-only: set observability.console.policy_file and list it in policies to edit rules here.", ko: "읽기 전용: 여기서 규칙을 편집하려면 observability.console.policy_file을 설정하고 policies에도 넣으세요.", zh: "只读：设置 observability.console.policy_file 并将其列入 policies 后，才能在此编辑规则。", ja: "読み取り専用: ここでルールを編集するには observability.console.policy_file を設定し、policies にも追加してください。" },
  "obs.reason.control_plane": { en: "Read-only: the control plane is the policy source. Edit observability rules there.", ko: "읽기 전용: 제어 영역이 정책 출처입니다. observability 규칙은 제어 영역에서 편집하세요.", zh: "只读：控制平面是策略来源，请在控制平面编辑 observability 规则。", ja: "読み取り専用: コントロールプレーンがポリシーの出所です。observability ルールはそちらで編集してください。" },
  "obs.reason.foreign_documents": { en: "Read-only: the console policy file holds documents the console does not manage. Edit it by hand.", ko: "읽기 전용: 콘솔 정책 파일에 콘솔이 관리하지 않는 문서가 있습니다. 직접 편집하세요.", zh: "只读：控制台策略文件包含控制台不管理的文档，请手动编辑。", ja: "読み取り専用: コンソールポリシーファイルにコンソールが管理しない文書があります。手動で編集してください。" },
  "obs.reason.read_failed": { en: "Read-only: the console policy file could not be read.", ko: "읽기 전용: 콘솔 정책 파일을 읽지 못했습니다.", zh: "只读：无法读取控制台策略文件。", ja: "読み取り専用: コンソールポリシーファイルを読めませんでした。" },
  "obs.field.paused": { en: "paused", ko: "일시 중지", zh: "暂停", ja: "一時停止" },
  "obs.field.signals": { en: "signals", ko: "신호", zh: "信号", ja: "シグナル" },
  "obs.field.max_content_bytes": { en: "max content bytes", ko: "본문 최대 바이트", zh: "内容最大字节数", ja: "本文の最大バイト数" },
  "obs.field.max_sample_percent": { en: "max trace sample %", ko: "트레이스 최대 샘플링 %", zh: "追踪最大采样 %", ja: "トレースの最大サンプリング %" },
  "obs.action.added": { en: "added", ko: "추가", zh: "新增", ja: "追加" },
  "obs.action.changed": { en: "changed", ko: "변경", zh: "修改", ja: "変更" },
  "obs.action.removed": { en: "removed", ko: "삭제", zh: "删除", ja: "削除" },
  "obs.yes": { en: "yes", ko: "예", zh: "是", ja: "はい" },
  "obs.no": { en: "no", ko: "아니요", zh: "否", ja: "いいえ" },
  "obs.none": { en: "none", ko: "없음", zh: "无", ja: "なし" },
  "obs.conflict": { en: "content off: rules disagree", ko: "본문 꺼짐: 규칙 불일치", zh: "内容关闭：规则不一致", ja: "本文オフ: ルール不一致" },
  "obs.remove": { en: "Remove", ko: "삭제", zh: "删除", ja: "削除" },
  "obs.err.revision_conflict": { en: "Someone changed this state since it was loaded. Refresh and preview again.", ko: "불러온 뒤에 다른 사람이 상태를 바꿨습니다. 새로고침한 뒤 다시 미리 보세요.", zh: "加载后状态已被他人更改。请刷新后重新预览。", ja: "読み込んだ後に状態が変更されました。更新してもう一度プレビューしてください。" },
  "obs.err.limits_file_changed": { en: "The state file changed on disk. Restart mayu to load it before editing.", ko: "디스크의 상태 파일이 바뀌었습니다. 편집하기 전에 mayu를 재시작해 불러오세요.", zh: "磁盘上的状态文件已更改。编辑前请重启 mayu 以加载它。", ja: "ディスク上の状態ファイルが変わりました。編集する前に mayu を再起動して読み込んでください。" },
  "obs.err.read_only": { en: "This part is read-only on this gateway.", ko: "이 게이트웨이에서는 읽기 전용입니다.", zh: "此部分在本网关上为只读。", ja: "このゲートウェイでは読み取り専用です。" },
  "obs.err.policy_reload_failed": { en: "The policy set did not reload; the console file was restored.", ko: "정책 세트를 다시 읽지 못해 콘솔 파일을 되돌렸습니다.", zh: "策略集未能重新加载，已恢复控制台文件。", ja: "ポリシーセットを再読み込みできなかったため、コンソールファイルを元に戻しました。" },
  "obs.err.console_file_foreign": { en: "The console policy file holds documents the console does not manage.", ko: "콘솔 정책 파일에 콘솔이 관리하지 않는 문서가 있습니다.", zh: "控制台策略文件包含控制台不管理的文档。", ja: "コンソールポリシーファイルにコンソールが管理しない文書があります。" },
  "obs.err.content_not_allowed": { en: "Console rules cannot add or change content export unless observability.console.content_rules is true.", ko: "observability.console.content_rules가 true가 아니면 콘솔 규칙으로 본문 내보내기를 추가하거나 바꿀 수 없습니다.", zh: "除非 observability.console.content_rules 为 true，否则控制台规则不能新增或更改内容导出。", ja: "observability.console.content_rules が true でない限り、コンソールルールで本文のエクスポートを追加・変更できません。" },
  "obs.err.audit_unavailable": { en: "The audit chain is not accepting records, so nothing was changed.", ko: "감사 체인이 기록을 받지 않는 상태라 아무것도 바꾸지 않았습니다.", zh: "审计链当前不接受记录，因此未做任何更改。", ja: "監査チェーンがレコードを受け付けていないため、何も変更していません。" },
  "obs.err.invalid_limits": { en: "The limits are outside what mayu's config allows.", ko: "한도가 mayu 설정이 허용하는 범위를 벗어났습니다.", zh: "限制超出了 mayu 配置允许的范围。", ja: "制限が mayu の設定で許される範囲を超えています。" },
  "obs.err.invalid_rules": { en: "The rules are not valid.", ko: "규칙이 올바르지 않습니다.", zh: "规则无效。", ja: "ルールが正しくありません。" },
  "obs.err.invalid_request": { en: "The request was not accepted.", ko: "요청을 받아들이지 않았습니다.", zh: "请求未被接受。", ja: "リクエストが受け付けられませんでした。" },
  "obs.err.persist_failed": { en: "The change could not be saved; nothing changed.", ko: "변경을 저장하지 못해 아무것도 바뀌지 않았습니다.", zh: "无法保存更改，未做任何更改。", ja: "変更を保存できなかったため、何も変わっていません。" },
  "obs.err.read_failed": { en: "A state file could not be read.", ko: "상태 파일을 읽지 못했습니다.", zh: "无法读取状态文件。", ja: "状態ファイルを読めませんでした。" },
  "obs.err.generic": { en: "Request failed (HTTP {status}).", ko: "요청 실패 (HTTP {status}).", zh: "请求失败（HTTP {status}）。", ja: "リクエスト失敗（HTTP {status}）。" },
  "obs.err.noSignals": { en: "{exporter}: keep at least one signal, or pause the exporter.", ko: "{exporter}: 신호를 하나 이상 남기거나 exporter를 일시 중지하세요.", zh: "{exporter}：至少保留一个信号，或暂停该导出器。", ja: "{exporter}: シグナルを1つ以上残すか、エクスポーターを一時停止してください。" },
  "obs.err.number": { en: "{exporter}: enter a whole number of at least 1, or leave the field empty.", ko: "{exporter}: 1 이상의 정수를 입력하거나 비워 두세요.", zh: "{exporter}：请输入不小于 1 的整数，或留空。", ja: "{exporter}: 1 以上の整数を入力するか、空欄のままにしてください。" },
  "obs.err.ruleName": { en: "Rule names start with console- and use lowercase letters, digits and '-' (at most 63 characters).", ko: "규칙 이름은 console-로 시작하고 소문자·숫자·'-'만 씁니다(최대 63자).", zh: "规则名称以 console- 开头，只能使用小写字母、数字和 '-'（最多 63 个字符）。", ja: "ルール名は console- で始め、小文字・数字・'-' のみ使います（最大 63 文字）。" },
  "obs.err.ruleSubject": { en: "Each rule needs a team, a user, or both.", ko: "규칙마다 팀이나 사용자, 또는 둘 다 필요합니다.", zh: "每条规则需要团队、用户或两者。", ja: "各ルールにはチーム、ユーザー、またはその両方が必要です。" },
  "obs.err.ruleSignal": { en: "Select at least one exporter for traces, audit or content.", ko: "트레이스·감사·본문 중 하나 이상에 exporter를 고르세요.", zh: "请至少为追踪、审计或内容选择一个导出器。", ja: "トレース・監査・本文のいずれか1つ以上にエクスポーターを選んでください。" },
  "obs.err.percent": { en: "Trace sample % must be a whole number from 1 to 100.", ko: "트레이스 샘플링 %는 1~100의 정수여야 합니다.", zh: "追踪采样 % 必须是 1 到 100 之间的整数。", ja: "トレースのサンプリング % は 1〜100 の整数で指定してください。" },
```

- [ ] **Step 3: Write the view**

`internal/server/adminui/static/observability.js`:

```js
"use strict";

// Observability view (sub-project B): governed OTLP export status and runtime
// edits. Full administrators only — the server enforces that on every route,
// reads included. Rendering is textContent-only and nothing is kept in browser
// storage (ADR-001): the last status lives in page memory and is re-fetched on
// every visit. Edits are dry run first; Apply sends exactly the body the last
// preview validated, with the revision that preview saw.
let obsState = null;
let obsPreviews = { limits: null, rules: null };
let obsBound = false;

const OBS_DROPPED = ["dropped_queue_full", "dropped_oversize", "dropped_export_error"];
const OBS_DECISIONS = ["selected", "no_rule", "content_conflict", "withheld_limit"];
const OBS_RULE_NAME = /^console-[a-z0-9]([-a-z0-9]{0,53}[a-z0-9])?$/;

// app.js's showView names this function in its refreshers literal, as it
// names refreshAuditView (audit.js).
async function refreshObservabilityView() {
  obsBind();
  $("obs-admin-only").hidden = whoamiIsAdmin;
  $("obs-content").hidden = true;
  if (!whoamiIsAdmin) return;
  obsState = await api("GET", "/admin/observability");
  obsPreviews = { limits: null, rules: null };
  renderObservability();
  $("obs-content").hidden = false;
}

/* ---------- pure helpers (testdata/observability_console.cjs) ---------- */

function obsTime(iso) { return iso ? String(iso).replace("T", " ").replace(/\.\d+/, "").replace("Z", "") : "—"; }
function obsShortRev(rev) { return String(rev || "").replace(/^sha256:/, "").slice(0, 12); }
function obsBytes(n) {
  const v = Number(n || 0), mib = 1024 * 1024;
  if (v >= mib) return (v % mib === 0 ? String(v / mib) : (v / mib).toFixed(1)) + " MiB";
  if (v >= 1024) return (v % 1024 === 0 ? String(v / 1024) : (v / 1024).toFixed(1)) + " KiB";
  return v + " B";
}
function obsDropped(outcomes) {
  let n = 0n;
  for (const k of OBS_DROPPED) n += BigInt((outcomes || {})[k] || 0);
  return n.toString();
}
function obsSubjectText(subject) {
  const parts = [];
  if (subject && subject.team) parts.push(msg("obs.team") + " " + subject.team);
  if (subject && subject.user) parts.push(msg("obs.user") + " " + subject.user);
  return parts.join(" · ") || "—";
}
function obsTargetText(t) {
  if (t.signal !== "traces") return t.signal + " → " + t.exporter;
  const pct = t.effective_percent !== t.sample_percent ? t.sample_percent + "% → " + t.effective_percent + "%" : t.sample_percent + "%";
  return t.signal + " → " + t.exporter + " · " + pct;
}
function obsValue(field, v) {
  if (Array.isArray(v)) return v.length ? v.join(", ") : msg("obs.none");
  if (typeof v === "boolean") return msg(v ? "obs.yes" : "obs.no");
  if (field === "max_content_bytes") return obsBytes(v);
  if (field === "max_sample_percent") return String(v) + "%";
  return String(v);
}
function obsChangeText(c) { return c.exporter + " · " + msg("obs.field." + c.field) + ": " + obsValue(c.field, c.before) + " → " + obsValue(c.field, c.after); }
function obsStateText(s) { return s.exported ? (s.sample_percent ? s.sample_percent + "%" : msg("obs.exported")) : msg("obs.withheld"); }
function obsEffectText(e) { return e.policy + " · " + e.rule + " · " + e.signal + " → " + e.exporter + ": " + obsStateText(e.before) + " → " + obsStateText(e.after); }
function obsRuleChangeText(c) { return c.name + " · " + msg("obs.action." + c.action); }
function obsDecisionSummary(d) {
  const parts = [];
  for (const t of d.traces || []) parts.push("traces " + t.exporter + " " + t.sample_percent + "%");
  for (const a of d.audit || []) parts.push("audit " + a);
  if (d.content) parts.push("content " + d.content);
  if (d.content_conflict) parts.push(msg("obs.conflict"));
  return parts.join(", ") || msg("obs.none");
}
function obsDecisionText(d) { return obsSubjectText(d.subject) + ": " + obsDecisionSummary(d.before) + " → " + obsDecisionSummary(d.after); }
function obsErrorText(data, status) {
  const key = "obs.err." + ((data && data.code) || "");
  const base = Object.hasOwn(MSG, key) ? msg(key) : msg("obs.err.generic").replace("{status}", String(status));
  return data && data.error ? base + " — " + data.error : base;
}
function obsInt(text, exporter) {
  const t = String(text == null ? "" : text).trim();
  if (!t) return 0;
  if (!/^\d+$/.test(t) || !Number.isSafeInteger(Number(t)) || Number(t) < 1) throw Error(msg("obs.err.number").replace("{exporter}", exporter));
  return Number(t);
}
// obsLimitsBody turns editor rows into the PUT body. A row equal to the
// bootstrap (every declared signal, no caps, not paused) sends nothing for its
// exporter; the server canonicalizes the same way, so the revisions agree.
function obsLimitsBody(rows, dryRun, revision) {
  const exporters = {};
  for (const r of rows) {
    const limit = {};
    if (r.paused) limit.paused = true;
    if (r.signals.length !== r.declared.length) {
      if (!r.signals.length && !r.paused) throw Error(msg("obs.err.noSignals").replace("{exporter}", r.exporter));
      if (r.signals.length) limit.signals = [...r.signals].sort();
    }
    const bytes = obsInt(r.max_content_bytes, r.exporter), percent = obsInt(r.max_sample_percent, r.exporter);
    if (percent > 100) throw Error(msg("obs.err.percent"));
    if (bytes) limit.max_content_bytes = bytes;
    if (percent) limit.max_sample_percent = percent;
    if (Object.keys(limit).length) exporters[r.exporter] = limit;
  }
  return { dry_run: dryRun, expected_revision: revision, exporters };
}
// obsRuleError is the client-side check of one editor row (the server checks
// again); it returns an i18n key, or "" when the row is acceptable.
function obsRuleError(row) {
  if (!OBS_RULE_NAME.test(row.name)) return "obs.err.ruleName";
  if (!row.team && !row.user) return "obs.err.ruleSubject";
  if (!row.traces && !row.audit && !row.content) return "obs.err.ruleSignal";
  if (row.traces && !/^(100|[1-9][0-9]?)$/.test(String(row.sample_percent).trim())) return "obs.err.percent";
  return "";
}
function obsRulesBody(rows, dryRun, revision) {
  const rules = rows.map(row => {
    const r = { ...row, name: row.name.trim(), team: row.team.trim(), user: row.user.trim() };
    const err = obsRuleError(r);
    if (err) throw Error(msg(err) + " (" + (r.name || "—") + ")");
    const rule = { name: r.name, subject: {} };
    if (r.team) rule.subject.team = r.team;
    if (r.user) rule.subject.user = r.user;
    if (r.traces) rule.traces = { exporter: r.traces, sample_percent: Number(String(r.sample_percent).trim()) };
    if (r.audit) rule.audit = { exporter: r.audit };
    if (r.content) rule.content = { exporter: r.content };
    return rule;
  });
  return { dry_run: dryRun, expected_revision: revision, rules };
}

/* ---------- rendering ---------- */

function obsNum(v) { const e = td(v == null ? "0" : String(v)); e.className = "num"; return e; }

function obsEndpointText(endpoint, protocol, tls, headers) {
  let text = endpoint + " · " + protocol + " · " + (tls ? "TLS" : msg("obs.plaintext"));
  if (headers && headers.length) text += " · " + msg("obs.headers") + ": " + headers.join(", ");
  return text;
}

function renderObservability() {
  const s = obsState;
  $("obs-since").textContent = s.configured ? msg("obs.since").replace("{t}", obsTime(s.counters_since)) : msg("obs.notConfigured");
  $("obs-source").textContent = msg("obs.sourceLabel") + ": " + msg("obs.source." + s.policy_source);
  renderObsExporters(s);
  renderObsSignals(s);
  renderObsDecisions(s);
  renderObsSelections(s);
  renderObsLimits(s);
  renderObsRules(s);
}

function renderObsExporters(s) {
  const tbody = $("obs-exporters").querySelector("tbody");
  tbody.replaceChildren();
  for (const e of s.exporters || []) {
    const now = e.limit && e.limit.paused ? msg("obs.paused") : (e.effective_signals.length ? e.effective_signals.join(", ") : "—");
    const cap = e.max_content_bytes ? obsBytes(e.effective_max_content_bytes) +
      (e.effective_max_content_bytes !== e.max_content_bytes ? " (" + obsBytes(e.max_content_bytes) + ")" : "") : "—";
    const tr = document.createElement("tr");
    tr.append(td(e.name), td(obsEndpointText(e.endpoint, e.protocol, e.tls, e.header_names)), td(e.signals.join(", ") + " → " + now),
      td(e.queue_limit_bytes ? obsBytes(e.queued_bytes) + " / " + obsBytes(e.queue_limit_bytes) : "—"), td(cap));
    tbody.appendChild(tr);
  }
  if (s.legacy_otel) {
    const l = s.legacy_otel, tr = document.createElement("tr");
    tr.append(td("otel · " + msg("obs.legacy")), td(obsEndpointText(l.endpoint, l.protocol, l.tls, [])),
      td("traces · " + Math.round(l.sample_ratio * 100) + "%"), td("—"), td("—"));
    tbody.appendChild(tr);
  }
  if (!tbody.children.length) tbody.appendChild(emptyRow(5, msg("obs.noExporters")));
}

function renderObsSignals(s) {
  const tbody = $("obs-signals").querySelector("tbody");
  tbody.replaceChildren();
  const rows = [];
  for (const e of s.exporters || []) for (const st of e.stats || []) rows.push([e.name, st]);
  if (s.legacy_otel && s.legacy_otel.stats) rows.push(["otel", s.legacy_otel.stats]);
  for (const [name, st] of rows) {
    const o = st.outcomes || {}, tr = document.createElement("tr");
    tr.append(td(name), td(st.signal), obsNum(o.sent), obsNum(obsDropped(o)), obsNum(o.withheld_limit), obsNum(o.sampled_out),
      td(st.last_success_at ? obsTime(st.last_success_at) : "—"), td(st.last_error ? obsTime(st.last_error_at) + " · " + st.last_error : "—"));
    tbody.appendChild(tr);
  }
  if (!rows.length) tbody.appendChild(emptyRow(8, msg("obs.noExporters")));
}

function renderObsDecisions(s) {
  const tbody = $("obs-decisions").querySelector("tbody");
  tbody.replaceChildren();
  for (const d of s.decisions || []) {
    const tr = document.createElement("tr");
    tr.append(td(d.signal), ...OBS_DECISIONS.map(k => obsNum((d.decisions || {})[k])));
    tbody.appendChild(tr);
  }
  if (!tbody.children.length) tbody.appendChild(emptyRow(5, msg("obs.noDecisions")));
}

function renderObsSelections(s) {
  const tbody = $("obs-selections").querySelector("tbody");
  tbody.replaceChildren();
  for (const sel of s.selections || []) {
    for (const t of sel.targets || []) {
      const tr = document.createElement("tr");
      tr.append(td(sel.policy + " · " + sel.rule), td(msg("obs.source." + sel.source)), td(obsSubjectText(sel.subject)),
        td(obsTargetText(t)), td(t.exported ? msg("obs.exported") : msg("obs.withheld")));
      tbody.appendChild(tr);
    }
  }
  if (!tbody.children.length) tbody.appendChild(emptyRow(5, msg("obs.noSelections")));
}

function obsCheck(field, label, checked, enabled) {
  const wrap = document.createElement("label"); wrap.className = "obs-check";
  const input = document.createElement("input"); input.type = "checkbox"; input.dataset.field = field;
  input.checked = checked; input.disabled = !enabled;
  const span = document.createElement("span"); span.textContent = label;
  wrap.append(input, span);
  return wrap;
}

function obsNumberField(field, label, value, placeholder, max, enabled) {
  const wrap = document.createElement("label"); wrap.className = "form-field";
  const span = document.createElement("span"); span.textContent = label;
  const input = document.createElement("input"); input.type = "number"; input.dataset.field = field;
  input.min = "1"; input.max = String(max); input.placeholder = String(placeholder);
  input.value = value ? String(value) : ""; input.disabled = !enabled;
  wrap.append(span, input);
  return wrap;
}

function renderObsLimits(s) {
  const box = $("obs-limits-rows"), note = $("obs-limits-readonly");
  const editable = !!(s.limits && s.limits.editable);
  box.replaceChildren();
  note.hidden = editable;
  note.textContent = editable ? "" : msg("obs.reason." + ((s.limits && s.limits.reason) || "not_configured"));
  for (const e of s.exporters || []) {
    const set = document.createElement("fieldset"); set.className = "obs-limit"; set.dataset.exporter = e.name;
    const legend = document.createElement("legend"); legend.textContent = e.name; set.appendChild(legend);
    const limit = e.limit || {};
    set.appendChild(obsCheck("paused", msg("obs.paused"), !!limit.paused, editable));
    const kept = new Set(limit.signals || e.signals);
    for (const sig of e.signals) set.appendChild(obsCheck("signal:" + sig, sig, kept.has(sig), editable));
    if (e.signals.includes("content")) set.appendChild(obsNumberField("max_content_bytes", msg("obs.maxContent"), limit.max_content_bytes, e.max_content_bytes, e.max_content_bytes, editable));
    if (e.signals.includes("traces")) set.appendChild(obsNumberField("max_sample_percent", msg("obs.maxSample"), limit.max_sample_percent, 100, 100, editable));
    box.appendChild(set);
  }
  $("obs-limits-preview").disabled = !editable;
  $("obs-limits-apply").disabled = true;
  $("obs-limits-status").textContent = "";
  $("obs-limits-diff").replaceChildren();
}

function obsReadLimitRows() {
  return [...$("obs-limits-rows").querySelectorAll("fieldset.obs-limit")].map(set => {
    const e = obsState.exporters.find(x => x.name === set.dataset.exporter);
    const field = name => set.querySelector('[data-field="' + name + '"]');
    return {
      exporter: e.name, declared: e.signals, paused: field("paused").checked,
      signals: e.signals.filter(sig => field("signal:" + sig).checked),
      max_content_bytes: field("max_content_bytes") ? field("max_content_bytes").value : "",
      max_sample_percent: field("max_sample_percent") ? field("max_sample_percent").value : "",
    };
  });
}

function obsExportersFor(signal) { return (obsState.exporters || []).filter(e => e.signals.includes(signal)).map(e => e.name); }

function obsRuleRow(rule, editable) {
  const tr = document.createElement("tr"); tr.className = "obs-rule";
  const cell = node => { const c = document.createElement("td"); c.appendChild(node); return c; };
  const text = (field, value, label) => {
    const i = document.createElement("input"); i.type = "text"; i.dataset.field = field; i.value = value || "";
    i.autocomplete = "off"; i.spellcheck = false; i.disabled = !editable; i.setAttribute("aria-label", msg(label));
    return i;
  };
  const select = (field, signal, value, enabled, label) => {
    const sel = document.createElement("select"); sel.dataset.field = field; sel.disabled = !enabled; sel.setAttribute("aria-label", msg(label));
    for (const name of ["", ...obsExportersFor(signal)]) {
      const o = document.createElement("option"); o.value = name; o.textContent = name || "—"; sel.appendChild(o);
    }
    sel.value = value || "";
    return sel;
  };
  const pct = document.createElement("input"); pct.type = "number"; pct.min = "1"; pct.max = "100"; pct.dataset.field = "sample_percent";
  pct.value = rule.traces ? String(rule.traces.sample_percent) : "100"; pct.disabled = !editable; pct.setAttribute("aria-label", msg("obs.maxSample"));
  const traces = document.createElement("div"); traces.className = "obs-inline";
  traces.append(select("traces", "traces", rule.traces && rule.traces.exporter, editable, "obs.colTraces"), pct);
  const remove = document.createElement("button"); remove.type = "button"; remove.className = "ghost";
  remove.textContent = msg("obs.remove"); remove.disabled = !editable;
  remove.addEventListener("click", () => { tr.remove(); obsInvalidate("rules"); });
  const subject = rule.subject || {};
  const contentEditable = editable && (obsState.rules.content_rules || !!rule.content);
  tr.append(cell(text("name", rule.name, "obs.colName")), cell(text("team", subject.team, "obs.colTeam")), cell(text("user", subject.user, "obs.colUser")),
    cell(traces), cell(select("audit", "audit", rule.audit && rule.audit.exporter, editable, "obs.colAudit")),
    cell(select("content", "content", rule.content && rule.content.exporter, contentEditable, "obs.colContent")), cell(remove));
  return tr;
}

function renderObsRules(s) {
  const r = s.rules || {}, editable = !!r.editable;
  $("obs-rules-readonly").hidden = editable;
  $("obs-rules-readonly").textContent = editable ? "" : msg("obs.reason." + (r.reason || "not_configured"));
  const tbody = $("obs-rules-table").querySelector("tbody");
  tbody.replaceChildren();
  for (const rule of r.console || []) tbody.appendChild(obsRuleRow(rule, editable));
  if (!tbody.children.length) tbody.appendChild(emptyRow(7, msg("obs.noRules")));
  for (const id of ["obs-rules-add", "obs-rules-preview"]) $(id).disabled = !editable;
  $("obs-rules-apply").disabled = true;
  $("obs-rules-status").textContent = "";
  $("obs-rules-diff").replaceChildren();
}

function obsReadRuleRows() {
  return [...$("obs-rules-table").querySelectorAll("tr.obs-rule")].map(tr => {
    const v = field => { const e = tr.querySelector('[data-field="' + field + '"]'); return e ? e.value : ""; };
    return { name: v("name"), team: v("team"), user: v("user"), traces: v("traces"), sample_percent: v("sample_percent"), audit: v("audit"), content: v("content") };
  });
}

/* ---------- edits ---------- */

// obsRequest is the edit-route fetch: unlike api() it returns the status and
// the error body, whose stable code becomes translated text.
async function obsRequest(method, path, body) {
  const resp = await fetch(path, {
    method,
    headers: { "Authorization": "Bearer " + adminToken, "Content-Type": "application/json" },
    body: exactJSON(body), cache: "no-store", credentials: "omit", redirect: "error",
  });
  if (resp.status === 401) throw new Error("unauthorized — check the admin token");
  let data = null;
  try { data = parseExactJSON(await resp.text()); } catch { data = null; }
  return { status: resp.status, data };
}

function obsInvalidate(kind) {
  obsPreviews[kind] = null;
  $("obs-" + kind + "-apply").disabled = true;
}

function obsShowResult(kind, text, lines) {
  $("obs-" + kind + "-status").textContent = text;
  $("obs-" + kind + "-diff").replaceChildren(...lines.map(line => { const li = document.createElement("li"); li.textContent = line; return li; }));
}

// obsSubmit runs one dry run or apply for an editor. Apply refuses a body that
// differs from the last previewed one.
async function obsSubmit(kind, dryRun, build, path, describe) {
  const status = $("obs-" + kind + "-status");
  let body;
  try {
    body = build(dryRun);
  } catch (err) {
    status.textContent = String(err.message || err);
    return;
  }
  const preview = obsPreviews[kind];
  if (!dryRun && (!preview || preview.body !== exactJSON({ ...body, dry_run: true }))) {
    status.textContent = msg(preview ? "obs.formChanged" : "obs.previewFirst");
    return;
  }
  const { status: code, data } = await obsRequest("PUT", path, body);
  if (code !== 200) {
    status.textContent = obsErrorText(data, code);
    obsInvalidate(kind);
    return;
  }
  const lines = describe(data);
  if (dryRun) {
    obsPreviews[kind] = { body: exactJSON(body) };
    obsShowResult(kind, data.changes.length ? msg("obs.previewed").replace("{n}", String(data.changes.length)) : msg("obs.noChanges"), lines);
    $("obs-" + kind + "-apply").disabled = !data.changes.length;
    return;
  }
  await refreshObservabilityView();
  obsShowResult(kind, msg("obs.applied").replace("{r}", obsShortRev(data.result_revision)), lines);
}

function obsSubmitLimits(dryRun) {
  return obsSubmit("limits", dryRun, d => obsLimitsBody(obsReadLimitRows(), d, obsState.limits.revision), "/admin/observability/limits",
    data => (data.changes || []).map(obsChangeText).concat((data.effects || []).map(obsEffectText)));
}

function obsSubmitRules(dryRun) {
  return obsSubmit("rules", dryRun, d => obsRulesBody(obsReadRuleRows(), d, obsState.rules.revision), "/admin/observability/rules",
    data => (data.changes || []).map(obsRuleChangeText).concat((data.decisions || []).map(obsDecisionText)));
}

function obsBind() {
  if (obsBound) return;
  obsBound = true;
  const report = kind => err => { $("obs-" + kind + "-status").textContent = String(err.message || err); };
  $("obs-refresh").addEventListener("click", () => refreshObservabilityView().catch(showWorkspaceError));
  $("obs-limits-preview").addEventListener("click", () => obsSubmitLimits(true).catch(report("limits")));
  $("obs-limits-apply").addEventListener("click", () => obsSubmitLimits(false).catch(report("limits")));
  $("obs-rules-preview").addEventListener("click", () => obsSubmitRules(true).catch(report("rules")));
  $("obs-rules-apply").addEventListener("click", () => obsSubmitRules(false).catch(report("rules")));
  $("obs-rules-add").addEventListener("click", () => {
    const tbody = $("obs-rules-table").querySelector("tbody");
    tbody.querySelectorAll("td.empty").forEach(cell => cell.parentNode.remove());
    tbody.appendChild(obsRuleRow({ name: "console-", subject: {} }, true));
    obsInvalidate("rules");
  });
  for (const [kind, id] of [["limits", "obs-limits-rows"], ["rules", "obs-rules-table"]]) {
    $(id).addEventListener("input", () => obsInvalidate(kind));
    $(id).addEventListener("change", () => obsInvalidate(kind));
  }
}
```

Append to the `internal/CLAUDE.md` section:

```markdown
- `server/adminui/static/observability.js`: the Observability view, wired like `audit.js` (`VIEWS.observability` plus `observability: refreshObservabilityView` in `showView`'s refreshers literal; nav button between Audit and Compare models). Any harness that loads `app.js` and switches views must load `observability.js` too (`routing_forms.cjs`, `analytics_evidence.cjs`, `browser_qa.py` do). Renders with textContent only, stores nothing in the browser, and Apply sends exactly the last previewed body with its revision.
```

- [ ] **Step 4: Run**

```bash
go test -buildvcs=false ./internal/server/adminui/ -race -count=1 > $S/t11.log 2>&1; echo "exit=$?"; grep -E '^(ok|FAIL|---|    )' $S/t11.log
node internal/server/adminui/testdata/observability_console.cjs > $S/t11-node.log 2>&1; echo "node=$?"; cat $S/t11-node.log
node internal/server/adminui/testdata/routing_forms.cjs --views > $S/t11-views.log 2>&1; echo "views=$?"
node internal/server/adminui/testdata/analytics_evidence.cjs > $S/t11-evidence.log 2>&1; echo "evidence=$?"
gofmt -l internal/server/adminui
git -C $W diff -w -U0 internal/server/adminui/adminui.go | grep -c '^+[^+]'
```
Expected: `exit=0` (it also runs `TestAuditViewWired`, the three `routing_forms.cjs` modes and `analytics_evidence.cjs` through their Go tests), `node=0` with `observability_console.cjs: ok`, `views=0`, `evidence=0`, no gofmt output, and `1` added line in `adminui.go` once whitespace is ignored (gofmt realigned the other entries). If `views` or `evidence` fails with `refreshObservabilityView is not defined`, the harness edit from Step 2 is missing.

- [ ] **Step 5: Commit**

```bash
git -C $W add internal/server/adminui/static/observability.js internal/server/adminui/static/index.html internal/server/adminui/static/app.js internal/server/adminui/static/i18n.js internal/server/adminui/static/style.css internal/server/adminui/adminui.go internal/server/adminui/observability_console_test.go internal/server/adminui/testdata/observability_console.cjs internal/server/adminui/testdata/routing_forms.cjs internal/server/adminui/testdata/analytics_evidence.cjs internal/server/adminui/testdata/browser_qa.py internal/CLAUDE.md
git -C $W commit -s -m "feat(console): Observability view for governed export (sub-project B)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 12: Console browser QA (Playwright, 1280 and 390 px)

**Files:**
- Create: `internal/server/adminui/testdata/observability_qa.py`
- Modify (only if QA finds layout overflow): `internal/server/adminui/static/style.css` (the Task 11 block)

**Interfaces:**
- Consumes: `testdata/browser_qa.py` (`Handler`, `STATIC`, `STATE`, `respond`; since Task 11 its `Handler` also serves `observability.js`, which `audit_qa.py` and this script inherit), the Task 11 markup and ids.
- Produces: screenshots `observability-1280-light.png`, `observability-1280-light-applied.png`, `observability-390-dark.png`, `observability-390-dark-applied.png` under the `--screenshots` directory.

- [ ] **Step 1: Write the QA script**

`internal/server/adminui/testdata/observability_qa.py`:

```python
"""Exercise the Observability view with synthetic /admin/observability responses.
Run: python3 internal/server/adminui/testdata/observability_qa.py --screenshots <local-scratch>/obs-qa
Requires existing Playwright + Chromium. No service, collector or credentials.
"""
import argparse
import copy
import json
import re
import sys
import threading
from http.server import ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlparse

sys.path.insert(0, str(Path(__file__).resolve().parent))
import browser_qa  # noqa: E402
from playwright.sync_api import sync_playwright  # noqa: E402

I18N = (browser_qa.STATIC / 'i18n.js').read_text(encoding='utf-8')


def en(key):
    """English string for an i18n key, read from i18n.js so copy lives in one place."""
    m = re.search(r'"' + re.escape(key) + r'": \{ en: "((?:[^"\\]|\\.)*)"', I18N)
    assert m, f'missing i18n key {key}'
    return json.loads('"' + m.group(1) + '"')


REV_L0, REV_L1 = 'sha256:' + '1' * 64, 'sha256:' + '2' * 64
REV_R0, REV_R1 = 'sha256:' + '3' * 64, 'sha256:' + '4' * 64
LONG_ENDPOINT = 'otel-collector.observability.svc.cluster.local:4318'
OBS = {}


def reset(cp=False, admin=True):
    OBS.clear()
    OBS.update(cp=cp, paused=False, limits_rev=REV_L0, rules_rev=REV_R0, rules=[], conflict=False, puts=[], gets=0)
    browser_qa.STATE['admin'] = admin


def stat(signal, sent, dropped, sampled, error=''):
    row = {'signal': signal, 'outcomes': {'sent': sent, 'dropped_queue_full': dropped, 'dropped_oversize': 0, 'dropped_export_error': 0,
                                          'withheld_limit': 0, 'sampled_out': sampled}, 'last_success_at': '2026-10-29T10:05:00Z'}
    if error:
        row.update(last_error_at='2026-10-29T10:06:00Z', last_error=error)
    return row


def status():
    paused = OBS['paused']
    rules = ({'editable': False, 'reason': 'control_plane', 'content_rules': False, 'console': []} if OBS['cp'] else
             {'editable': True, 'revision': OBS['rules_rev'], 'content_rules': False, 'console': copy.deepcopy(OBS['rules'])})
    return {
        'configured': True, 'policy_source': 'control_plane' if OBS['cp'] else 'files', 'service_name': 'inferplane',
        'counters_since': '2026-10-29T10:00:00Z', 'limits': {'editable': True, 'revision': OBS['limits_rev']}, 'rules': rules,
        'exporters': [
            {'name': 'corp', 'endpoint': LONG_ENDPOINT, 'protocol': 'http', 'tls': True, 'header_names': ['authorization'],
             'signals': ['audit', 'traces'], 'effective_signals': [] if paused else ['audit', 'traces'], 'queue_limit_bytes': 67108864,
             'queued_bytes': 4096, 'limit': {'paused': True} if paused else {}, 'stats': [stat('audit', 12, 1, 0), stat('traces', 30, 0, 9)]},
            {'name': 'vault', 'endpoint': 'secure-collector.internal:4317', 'protocol': 'grpc', 'tls': True, 'header_names': [],
             'data_boundary': 'internal', 'signals': ['content'], 'effective_signals': ['content'], 'max_content_bytes': 2097152,
             'effective_max_content_bytes': 2097152, 'queue_limit_bytes': 67108864, 'queued_bytes': 0, 'limit': {},
             'stats': [stat('content', 3, 2, 0, error='collector code Unavailable')]}],
        'legacy_otel': {'endpoint': 'localhost:4318', 'protocol': 'http', 'tls': False, 'sample_ratio': 0.25, 'stats': stat('traces', 5, 0, 15)},
        'decisions': [{'signal': s, 'decisions': {'selected': 4, 'no_rule': 20, 'content_conflict': 1 if s == 'content' else 0, 'withheld_limit': 0}}
                      for s in ('traces', 'audit', 'content')],
        'selections': [{'policy': 'engineering-observability', 'rule': 'export', 'source': 'files', 'subject': {'team': 'engineering'},
                        'targets': [{'signal': 'traces', 'exporter': 'corp', 'sample_percent': 20, 'effective_percent': 20, 'exported': not paused},
                                    {'signal': 'audit', 'exporter': 'corp', 'exported': not paused}]}],
    }


class Handler(browser_qa.Handler):
    # The base handler serves observability.js with the other static files (Task 11).
    def do_GET(self):
        path = urlparse(self.path).path
        if path.startswith('/admin/') and self.headers.get('Authorization') == 'Bearer fixture-admin':
            if path == '/admin/capabilities':
                return self.respond(200, {'analytics_index': 'A', 'teams_records': True, 'observability_export': True})
            if path == '/admin/observability':
                OBS['gets'] += 1
                return self.respond(200, status())
        return super().do_GET()

    def do_PUT(self):
        path = urlparse(self.path).path
        if path not in ('/admin/observability/limits', '/admin/observability/rules'):
            return super().do_PUT()
        if self.headers.get('Authorization') != 'Bearer fixture-admin':
            return self.respond(401, {})
        body = json.loads(self.rfile.read(int(self.headers.get('Content-Length', 0))))
        OBS['puts'].append((path, body))
        if OBS['conflict']:
            return self.respond(409, {'error': 'the state changed since it was read; reload and preview again',
                                      'code': 'revision_conflict', 'current_revision': REV_L1})
        if path.endswith('/limits'):
            assert body['expected_revision'] == OBS['limits_rev'], body
            res = {'dry_run': body['dry_run'], 'current_revision': OBS['limits_rev'], 'result_revision': REV_L1,
                   'changes': [{'exporter': 'corp', 'field': 'paused', 'before': False, 'after': True}],
                   'effects': [{'policy': 'engineering-observability', 'rule': 'export', 'signal': 'audit', 'exporter': 'corp',
                                'before': {'exported': True}, 'after': {'exported': False}}]}
            if not body['dry_run']:
                OBS.update(paused=True, limits_rev=REV_L1)
            return self.respond(200, res)
        assert body['expected_revision'] == OBS['rules_rev'], body
        res = {'dry_run': body['dry_run'], 'current_revision': OBS['rules_rev'], 'result_revision': REV_R1,
               'changes': [{'name': r['name'], 'action': 'added'} for r in body['rules']],
               'summary': {'added': len(body['rules']), 'changed': 0, 'removed': 0, 'unchanged': 0},
               'decisions': [{'subject': {'team': 'finance'}, 'before': {'traces': [], 'audit': []}, 'after': {'traces': [], 'audit': ['corp']}}]}
        if not body['dry_run']:
            OBS.update(rules=copy.deepcopy(body['rules']), rules_rev=REV_R1)
        return self.respond(200, res)


def watch_errors(page):
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))

    def on_console(m):
        url = m.location.get('url', '')
        # The shared harness serves /admin/auth/config as 404 on purpose, and the
        # conflict step provokes the 409 it asserts on; nothing else may fail.
        if m.type != 'error' or url.endswith('/admin/auth/config'):
            return
        if OBS.get('conflict') and urlparse(url).path in ('/admin/observability/limits', '/admin/observability/rules'):
            return
        errors.append(m.text + ' @ ' + url)
    page.on('console', on_console)
    return errors


def login(page, base):
    page.goto(base + '/admin/ui/')
    page.locator('#token').fill('fixture-admin')
    page.locator('#token-form button').click()
    page.locator('#shell').wait_for(state='visible')
    page.locator('button.nav[data-view="observability"]').click()


def starts(sel, text):
    return f"() => document.querySelector({json.dumps(sel)}).textContent.startsWith({json.dumps(text)})"


def run_scenario(page, base, width, scheme, shots):
    reset()
    errors = watch_errors(page)
    text = lambda sel: page.locator(sel).inner_text()  # noqa: E731
    login(page, base)
    page.wait_for_function("() => document.querySelectorAll('#obs-exporters tbody tr').length === 3")
    assert page.locator('#obs-signals tbody tr').count() == 4
    assert 'authorization' in text('#obs-exporters') and LONG_ENDPOINT in text('#obs-exporters')
    assert 'collector code Unavailable' in text('#obs-signals')
    assert page.locator('#obs-decisions tbody tr').count() == 3
    assert page.locator('#obs-selections tbody tr').count() == 2
    assert text('#obs-source').endswith(en('obs.source.files'))

    # Limits: preview; a change after the preview disables Apply; preview again; apply.
    corp = '#obs-limits-rows fieldset[data-exporter="corp"]'
    vault_cap = '#obs-limits-rows fieldset[data-exporter="vault"] input[data-field="max_content_bytes"]'
    page.locator(corp + ' input[data-field="paused"]').check()
    page.locator('#obs-limits-preview').click()
    page.wait_for_function("() => document.querySelectorAll('#obs-limits-diff li').length === 2")
    assert OBS['puts'][-1][1] == {'dry_run': True, 'expected_revision': REV_L0, 'exporters': {'corp': {'paused': True}}}, OBS['puts'][-1]
    assert page.locator('#obs-limits-apply').is_enabled()
    page.locator(vault_cap).fill('1024')
    assert page.locator('#obs-limits-apply').is_disabled(), 'an edit after the preview must disable Apply'
    page.locator(vault_cap).fill('')
    page.locator('#obs-limits-preview').click()
    page.wait_for_function("() => !document.getElementById('obs-limits-apply').disabled")
    if shots:
        page.screenshot(path=str(shots / f'observability-{width}-{scheme}.png'), full_page=True)
    puts = len(OBS['puts'])
    page.locator('#obs-limits-apply').click()
    page.wait_for_function(starts('#obs-limits-status', en('obs.applied').split('{r}')[0]))
    assert OBS['puts'][puts][1]['dry_run'] is False and OBS['puts'][puts][1]['expected_revision'] == REV_L0, OBS['puts'][puts]
    assert en('obs.paused') in text('#obs-exporters')
    assert text('#obs-selections').count(en('obs.withheld')) == 2

    # Rules: client-side validation sends nothing; a valid rule previews and applies.
    page.locator('#obs-rules-add').click()
    row = page.locator('#obs-rules-table tbody tr.obs-rule').last
    row.locator('[data-field="name"]').fill('finance')
    puts = len(OBS['puts'])
    page.locator('#obs-rules-preview').click()
    page.wait_for_function(f"() => document.getElementById('obs-rules-status').textContent.includes({json.dumps(en('obs.err.ruleName'))})")
    assert len(OBS['puts']) == puts, 'an invalid row reached the server'
    row.locator('[data-field="name"]').fill('console-finance')
    row.locator('[data-field="team"]').fill('finance')
    row.locator('[data-field="audit"]').select_option('corp')
    assert row.locator('[data-field="content"]').is_disabled(), 'content selection needs content_rules'
    # Cells keep words whole and the table scrolls instead: Remove never wraps per letter.
    remove_box, add_box = row.locator('button').bounding_box(), page.locator('#obs-rules-add').bounding_box()
    assert remove_box['height'] <= add_box['height'] + 1, f'Remove wraps: {remove_box} vs {add_box} at {width}px'
    page.locator('#obs-rules-preview').click()
    page.wait_for_function("() => document.querySelectorAll('#obs-rules-diff li').length === 2")
    assert OBS['puts'][-1][1]['rules'] == [{'name': 'console-finance', 'subject': {'team': 'finance'}, 'audit': {'exporter': 'corp'}}], OBS['puts'][-1]
    page.locator('#obs-rules-apply').click()
    page.wait_for_function(starts('#obs-rules-status', en('obs.applied').split('{r}')[0]))
    assert page.locator('#obs-rules-table tbody tr.obs-rule').count() == 1

    # A revision conflict shows the translated message and keeps Apply off.
    OBS['conflict'] = True
    page.locator(corp + ' input[data-field="paused"]').uncheck()
    page.locator('#obs-limits-preview').click()
    page.wait_for_function(starts('#obs-limits-status', en('obs.err.revision_conflict')))
    assert page.locator('#obs-limits-apply').is_disabled()

    overflow = page.evaluate('document.documentElement.scrollWidth - window.innerWidth')
    assert overflow <= 0, f'horizontal overflow {overflow}px at {width}px'
    if shots:
        page.screenshot(path=str(shots / f'observability-{width}-{scheme}-applied.png'), full_page=True)
    assert not errors, errors


def run_readonly(page, base):
    reset(cp=True)
    errors = watch_errors(page)
    login(page, base)
    page.locator('#obs-rules-readonly').wait_for(state='visible')
    assert page.locator('#obs-rules-readonly').inner_text() == en('obs.reason.control_plane')
    assert page.locator('#obs-rules-add').is_disabled() and page.locator('#obs-rules-preview').is_disabled()
    assert page.locator('#obs-source').inner_text().endswith(en('obs.source.control_plane'))
    assert page.locator('#obs-limits-preview').is_enabled(), 'limits narrow the local ceiling, so they stay editable'
    assert not errors, errors


def run_member(page, base):
    reset(admin=False)
    errors = watch_errors(page)
    login(page, base)
    page.locator('#obs-admin-only').wait_for(state='visible')
    assert OBS['gets'] == 0, 'a non-admin view fetched the status'
    assert page.locator('#obs-content').is_hidden()
    assert not errors, errors


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--screenshots', type=Path)
    args = parser.parse_args()
    if args.screenshots:
        args.screenshots.mkdir(parents=True, exist_ok=True)
    server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    base = f'http://127.0.0.1:{server.server_port}'
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        for width, scheme in [(1280, 'light'), (390, 'dark')]:
            page = browser.new_page(viewport={'width': width, 'height': 1000}, color_scheme=scheme, locale='en-US')
            try:
                run_scenario(page, base, width, scheme, args.screenshots)
            finally:
                page.close()
        for check in (run_readonly, run_member):
            page = browser.new_page(viewport={'width': 1280, 'height': 1000}, color_scheme='light', locale='en-US')
            try:
                check(page, base)
            finally:
                page.close()
        browser.close()
    server.shutdown()
    print('observability_qa.py: ok')


if __name__ == '__main__':
    main()
```

- [ ] **Step 2: Run the QA script and the existing harness**

```bash
python3 internal/server/adminui/testdata/observability_qa.py --screenshots $S/obs-qa > $S/t12.log 2>&1; echo "exit=$?"; grep -E 'ok|Error|assert' $S/t12.log
python3 internal/server/adminui/testdata/browser_qa.py > $S/t12b.log 2>&1; echo "browser_qa=$?"
python3 internal/server/adminui/testdata/audit_qa.py > $S/t12c.log 2>&1; echo "audit_qa=$?"
ls $S/obs-qa
```
Expected: `exit=0` with `observability_qa.py: ok`, `browser_qa=0`, `audit_qa=0` (the audit view still works with the Observability button between Audit and Compare models), and the four PNG files. If an overflow assertion fails at 390 px, fix the CSS block from Task 11 (not the assertion), rerun, and record the fix in the ledger. Open the four screenshots and confirm: tables scroll inside their cards, the limits fieldsets wrap, the diff lists are readable, dark mode keeps contrast.

- [ ] **Step 3: Commit**

```bash
git -C $W add internal/server/adminui/testdata/observability_qa.py internal/server/adminui/static/style.css
git -C $W commit -s -m "test(console): Playwright QA for the Observability view at 1280 and 390 px" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 13: Changelog, repository gates, merge check and live end-to-end

**Files:**
- Modify: `CHANGELOG.md` (one `Unreleased` → `### Added` entry), root `CLAUDE.md` (two Project Structure lines). Never edit `AGENTS.md`: it is generated from `CLAUDE.md`, and the controller regenerates it after this plan lands.
- Scratch only otherwise: `/home/atomoh/.tmp/inferplane-observability-console-20261029/verify/` (`$V`), logs under `$S/`.

- [ ] **Step 1: Changelog entry and Project Structure lines**

The base branch adds governed OTLP export without a CHANGELOG entry; one entry covers it and the Observability console. In `CHANGELOG.md`, under `## [Unreleased]` → `### Added` (line 13), insert this as the first bullet, before the `Native Bedrock Converse and local evaluation (ADR-052)` entry (line 14), in the file's bilingual style:

```markdown
- **Governed OTLP export and the Observability console.** `observability.exporters` declares the only OTLP collectors mayu may send to (endpoint, protocol, TLS, `env`/`file` header refs, allowed signals); a FailOpen GovernancePolicy `observability` rule selects, per team or user, which declared exporter receives traces, audit-record copies or egress content, with a gateway-side sampling draw and bounded per-exporter queues that never block the audit writer (`inferplane_otel_export_records_total`). Full administrators get an **Observability** console view backed by `GET /admin/observability` (per-exporter records, drops, last error, and governed decisions in `inferplane_otel_export_decisions_total`), can narrow export at runtime (pause, signal subset, lower content cap or trace sampling) and can edit console-owned rules, configured under `observability.console`: dry run first, persisted, audited as `admin_observability_changed`, and never able to add a destination or credential. / **정책 기반 OTLP 내보내기·관측성 콘솔:** 선언된 수집기로만 트레이스·감사 사본·송신 본문을 정책별로 내보내고, 전체 관리자가 콘솔에서 상태를 보며 dry run 뒤 내보내기를 좁히거나 콘솔 규칙을 편집(저장·감사 기록)할 수 있음.
```

In the root `CLAUDE.md` `## Project Structure` block (line 184), keep the column style of the `internal/` entries:

- after `  server/          - HTTP data plane + admin plane, ingress handlers` add
  `  server/observabilityapi/ - Observability console API (full-admin): export status, dry-run-first runtime limits (state file) and console-owned observability rules`
- after `  tracing/         - Opt-in OpenTelemetry seam, no-op default (ADR-011)` add
  `  otelexport/      - Governed OTLP log export: local exporter ceiling, bounded audit-copy/content pipelines, runtime limits and console status`

```bash
git -C $W diff --stat -- CHANGELOG.md CLAUDE.md AGENTS.md
git -C $W add CHANGELOG.md CLAUDE.md
git -C $W commit -s -m "docs: changelog and project structure for governed export and the observability console" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
Expected: the stat lists `CHANGELOG.md` (1 insertion) and `CLAUDE.md` (2 insertions) only, never `AGENTS.md`. Tell the controller that `AGENTS.md` needs regenerating.

- [ ] **Step 2: Repository gates**

```bash
go test -buildvcs=false ./... -race > $S/gate-test.log 2>&1; echo "test=$?"; grep -E '^(FAIL|--- FAIL|panic)' $S/gate-test.log
go vet -buildvcs=false ./... > $S/gate-vet.log 2>&1; echo "vet=$?"
gofmt -l . > $S/gate-fmt.log; echo "fmt-files=$(wc -l < $S/gate-fmt.log)"
bash tests/run-all.sh > $S/gate-harness.log 2>&1; echo "harness=$?"
python3 scripts/check_docs_translations.py > $S/gate-docs.log 2>&1; echo "translations=$?"
CGO_ENABLED=0 go build -buildvcs=false -trimpath -o $S/bin/mayu ./cmd/mayu > $S/gate-build.log 2>&1; echo "mayu=$?"
CGO_ENABLED=0 go build -buildvcs=false -trimpath -o $S/bin/inferplaned ./cmd/inferplaned >> $S/gate-build.log 2>&1; echo "inferplaned=$?"
```
Expected: `test=0` with no FAIL lines, `vet=0`, `fmt-files=0`, `harness=0`, `translations=0`, `mayu=0`, `inferplaned=0`.

- [ ] **Step 3: Docs build with the shared docs venv**

```bash
D=/home/atomoh/.tmp/inferplane-audit-console-20261029/docs-venv/bin/python
$D -B -m unittest discover -s tests/docs -v > $S/gate-docs-unit.log 2>&1; echo "docs_unit=$?"
$D scripts/check_docs_translations.py > $S/gate-docs-tr.log 2>&1; echo "docs_tr=$?"
$D scripts/build_performance_docs.py --check > $S/gate-perf.log 2>&1; echo "perf=$?"
$D scripts/build_client_compat_docs.py --check > $S/gate-compat.log 2>&1; echo "compat=$?"
$D -m mkdocs build --strict -d $S/site > $S/gate-mkdocs.log 2>&1; echo "mkdocs=$?"; grep -E 'WARNING|ERROR' $S/gate-mkdocs.log
$D scripts/check_docs_site.py $S/site > $S/gate-site.log 2>&1; echo "site=$?"
```
Expected: every exit `0`; no `WARNING`/`ERROR` lines (the anchors `#observability-console-mayu-only`, `#observability-console-api-mayu-only`, `#observability-console`, `#observability-console-edits` and `#admin-change-evidence-observability-console` resolve in both languages).

- [ ] **Step 4: Merge check against main**

The audit console is in the base (`caf55fe` merges origin/main `a8a71f6`), so the branch must merge into the current `origin/main` with no conflict.

```bash
git -C $W fetch origin main > $S/merge-fetch.log 2>&1; echo "fetch=$?"
git -C $W merge-tree --write-tree --name-only --no-messages origin/main HEAD > $S/merge-main.txt; echo "merge=$?"
wc -l < $S/merge-main.txt
git -C $W diff --name-only caf55fe HEAD -- AGENTS.md
```
Expected: `fetch=0`, `merge=0`, `1` line in `$S/merge-main.txt` (the merged tree id, no conflicted file), and no `AGENTS.md` in the diff. If `merge=1`, origin/main moved after `caf55fe`: record the files listed after the first line in the ledger as a merge note for the controller; do not resolve them in this branch. (Re-verification at `caf55fe` with the plan commit as `HEAD`: `merge=0`.)

- [ ] **Step 5: Write the live check driver**

```bash
V=$S/verify; mkdir -p $V
cp $S/bin/mayu $V/mayu
ss -ltn > $V/ports.txt; grep -cE ':(18381|19391|18390|18399) ' $V/ports.txt
```
Expected: `0` (all four ports free).

`$V/live_check.py` (scratch, not committed):

```python
"""Live end-to-end check of the observability console on a throwaway mayu.
Run: python3 live_check.py <verify-dir> [--config-only]
Starts an OTLP/HTTP receiver stub (127.0.0.1:18390) and an Anthropic-format
upstream stub (127.0.0.1:18399) in this process, runs <verify-dir>/mayu on
127.0.0.1:18381/19391 twice, drives the console API and a browser, and prints
one JSON summary. The admin token, the virtual key and the collector header
value are generated here and never printed or written to a file."""
import json
import os
import pathlib
import re
import secrets
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

V = pathlib.Path(sys.argv[1]).resolve()
MAYU = V / 'mayu'
ADMIN = secrets.token_hex(24)
SECRET = 'Bearer live-' + secrets.token_hex(12)
DATA, ADMIN_URL = 'http://127.0.0.1:18381', 'http://127.0.0.1:19391'
RECEIVED, LOCK = [], threading.Lock()
ENV = dict(os.environ, INFERPLANE_ADMIN_TOKEN=ADMIN, STUB_KEY='stub', OTEL_LIVE_AUTH=SECRET)


class Collector(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get('Content-Length') or 0))
        with LOCK:
            RECEIVED.append((self.path, body, self.headers.get('Authorization') == SECRET))
        self.send_response(200)
        self.send_header('Content-Type', 'application/x-protobuf')
        self.send_header('Content-Length', '0')
        self.end_headers()


class Upstream(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        self.rfile.read(int(self.headers.get('Content-Length') or 0))
        body = json.dumps({'id': 'msg_live', 'type': 'message', 'role': 'assistant', 'model': 'stub-upstream',
                           'content': [{'type': 'text', 'text': 'ok'}], 'stop_reason': 'end_turn', 'stop_sequence': None,
                           'usage': {'input_tokens': 5, 'output_tokens': 1}}).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def serve(port, handler):
    srv = ThreadingHTTPServer(('127.0.0.1', port), handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv


def write_config():
    (V / 'policies').mkdir(exist_ok=True)
    (V / 'policies' / 'live.yaml').write_text('''apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata:
  name: live-observability
spec:
  subject:
    team: obs-live
  rules:
  - name: export
    failurePolicy: FailOpen
    observability:
      traces: { exporter: corp, samplePercent: 100 }
      audit: { exporter: corp }
''')
    header = {'authorization': {'env': 'OTEL_LIVE_AUTH'}}
    cfg = {
        'server': {'listen': '127.0.0.1:18381', 'admin_listen': '127.0.0.1:19391', 'drain_grace': '2s',
                   'admin_auth': {'token_refs': [{'env': 'INFERPLANE_ADMIN_TOKEN'}]}},
        'key_store': {'type': 'sqlite', 'path': str(V / 'keys.db')},
        'audit': {'buffer': {'path': str(V / 'audit.wal')}, 'sinks': [{'type': 'file', 'path': str(V / 'audit.jsonl')}]},
        'analytics': {'disabled': True},
        'providers': {'stub': {'type': 'anthropic', 'base_url': 'http://127.0.0.1:18399', 'api_key_ref': {'env': 'STUB_KEY'}}},
        'models': {'stub-model': {'targets': [{'provider': 'stub', 'model': 'stub-upstream'}], 'context_window': 200000}},
        'pricing': {'on_missing': 'allow', 'overrides': {'stub': {'stub-upstream': {'input_per_mtok': 1, 'output_per_mtok': 2}}}},
        'policies': [str(V / 'policies'), str(V / 'console-observability.yaml')],
        'observability': {'service_name': 'mayu-live', 'exporters': {
            'corp': {'endpoint': '127.0.0.1:18390', 'insecure': True, 'signals': ['traces', 'audit'], 'headers': header},
            'vault': {'endpoint': '127.0.0.1:18390', 'insecure': True, 'signals': ['content'], 'data_boundary': 'internal', 'headers': header}},
            'console': {'state_file': str(V / 'observability-limits.json'), 'policy_file': str(V / 'console-observability.yaml'),
                        'content_rules': True}},
    }
    (V / 'config.json').write_text(json.dumps(cfg, indent=2))


def wait(pred, timeout):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if pred():
            return True
        time.sleep(0.25)
    return pred()


def http(method, url, body=None, headers=None):
    data = json.dumps(body).encode() if body is not None else None
    hdrs = dict(headers or {})
    if data is not None:
        hdrs['Content-Type'] = 'application/json'
    req = urllib.request.Request(url, data=data, method=method, headers=hdrs)
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()
    except OSError:
        return 0, b''


def admin(method, path, body=None):
    code, raw = http(method, ADMIN_URL + path, body, {'Authorization': 'Bearer ' + ADMIN})
    try:
        return code, json.loads(raw or b'{}')
    except ValueError:
        return code, {}


def start():
    log = open(V / 'mayu.log', 'ab')
    p = subprocess.Popen([str(MAYU), 'serve', '--config', str(V / 'config.json')], env=ENV, stdout=log, stderr=subprocess.STDOUT, cwd=V)
    if not wait(lambda: http('GET', ADMIN_URL + '/healthz')[0] == 200, 30):
        p.terminate()
        raise SystemExit('mayu did not become healthy; see mayu.log')
    return p


def stop(p):
    p.terminate()
    p.wait(timeout=30)


def logs(marker=None, event=None):
    with LOCK:
        items = list(RECEIVED)
    return [b for path, b, _ in items if path == '/v1/logs' and (marker is None or marker.encode() in b) and (event is None or event.encode() in b)]


def ask(key, text):
    body = {'model': 'stub-model', 'max_tokens': 16, 'messages': [{'role': 'user', 'content': text}]}
    return http('POST', DATA + '/v1/messages', body, {'x-api-key': key, 'anthropic-version': '2023-06-01'})[0]


def screenshots(out):
    from playwright.sync_api import sync_playwright
    with sync_playwright() as pw:
        browser = pw.chromium.launch(headless=True)
        for width, scheme in [(1280, 'light'), (390, 'dark')]:
            page = browser.new_page(viewport={'width': width, 'height': 1000}, color_scheme=scheme, locale='en-US')
            page.goto(ADMIN_URL + '/admin/ui/')
            page.locator('#token').fill(ADMIN)
            page.locator('#token-form button').click()
            page.locator('#shell').wait_for(state='visible')
            page.locator('button.nav[data-view="observability"]').click()
            page.wait_for_function("() => document.querySelectorAll('#obs-exporters tbody tr').length >= 2")
            page.screenshot(path=str(out / f'live-observability-{width}-{scheme}.png'), full_page=True)
            page.close()
        browser.close()


def main():
    write_config()
    if '--config-only' in sys.argv:
        return
    collector, upstream = serve(18390, Collector), serve(18399, Upstream)
    made = subprocess.run([str(MAYU), 'keys', 'create', '--team', 'obs-live', '--models', '*', '--store', str(V / 'keys.db')],
                          env=ENV, capture_output=True, text=True, cwd=V)
    key = (re.findall(r'ik_[A-Za-z0-9_-]+', made.stdout + made.stderr) or [''])[0]
    s = {'key_created': made.returncode == 0 and bool(key)}
    p = start()
    code, st = admin('GET', '/admin/observability')
    s['status'] = code == 200 and st.get('configured') is True and st['limits']['editable'] and st['rules']['editable']
    s['header_value_hidden'] = SECRET not in json.dumps(st) and 'authorization' in json.dumps(st)
    s['request_1'] = ask(key, 'obs-live-marker-1')
    s['audit_copy'] = wait(lambda: bool(logs(event='inferplane.audit')), 15)
    s['trace_exported'] = wait(lambda: any(path == '/v1/traces' for path, _, _ in list(RECEIVED)), 15)
    s['collector_auth_header'] = all(ok for _, _, ok in list(RECEIVED))
    s['content_before_rule'] = bool(logs(marker='obs-live-marker-1', event='inferplane.content'))

    rev = st['rules']['revision']
    rule = {'name': 'console-live-content', 'subject': {'team': 'obs-live'}, 'content': {'exporter': 'vault'}}
    console_file = V / 'console-observability.yaml'
    code, dry = admin('PUT', '/admin/observability/rules', {'dry_run': True, 'expected_revision': rev, 'rules': [rule]})
    s['rules_dry_run'] = code == 200 and dry.get('changes') == [{'name': 'console-live-content', 'action': 'added'}] and 'console-live-content' not in console_file.read_text()
    code, applied_rules = admin('PUT', '/admin/observability/rules', {'dry_run': False, 'expected_revision': rev, 'rules': [rule]})
    s['rules_applied'] = code == 200 and 'console-live-content' in console_file.read_text()
    s['request_2'] = ask(key, 'obs-live-marker-2')
    s['content_after_rule'] = wait(lambda: bool(logs(marker='obs-live-marker-2', event='inferplane.content')), 15)

    code, st = admin('GET', '/admin/observability')
    lrev = st['limits']['revision']
    pause = {'exporters': {'vault': {'paused': True}}}
    state_file = V / 'observability-limits.json'
    code, ldry = admin('PUT', '/admin/observability/limits', dict(pause, dry_run=True, expected_revision=lrev))
    s['limits_dry_run'] = code == 200 and len(ldry.get('changes', [])) == 1 and not state_file.exists()
    code, applied_limits = admin('PUT', '/admin/observability/limits', dict(pause, dry_run=False, expected_revision=lrev))
    s['limits_applied'] = code == 200 and state_file.exists() and json.loads(state_file.read_text())['exporters']['vault']['paused'] is True
    code, stale = admin('PUT', '/admin/observability/limits', {'dry_run': False, 'expected_revision': lrev, 'exporters': {}})
    s['stale_revision_rejected'] = code == 409 and stale.get('code') == 'revision_conflict'
    s['request_3'] = ask(key, 'obs-live-marker-3')
    time.sleep(3)  # longer than the 1 s log batch interval
    s['content_while_paused'] = bool(logs(marker='obs-live-marker-3', event='inferplane.content'))
    code, st = admin('GET', '/admin/observability')
    content = next(d for d in st['decisions'] if d['signal'] == 'content')
    s['withheld_counted'] = content['decisions']['withheld_limit'] >= 1
    metrics = http('GET', ADMIN_URL + '/metrics')[1].decode()
    s['decision_metric'] = 'inferplane_otel_export_decisions_total' in metrics
    s['metrics_clean'] = 'key_id="' not in metrics and SECRET not in metrics
    stop(p)

    p = start()
    code, st = admin('GET', '/admin/observability')
    vault = next(e for e in st['exporters'] if e['name'] == 'vault')
    s['pause_survived_restart'] = vault['limit'].get('paused') is True and vault['effective_signals'] == []
    s['rule_survived_restart'] = any(x['policy'] == 'console-live-content' and x['source'] == 'console' for x in st['selections'])
    try:
        screenshots(V.parent)
        s['screenshots'] = True
    except Exception as e:  # noqa: BLE001 — report it, do not hide it
        s['screenshots'] = 'failed: ' + type(e).__name__
    stop(p)

    lines = (V / 'audit.jsonl').read_text().splitlines()
    changes = [json.loads(line) for line in lines if '"event":"admin_observability_changed"' in line]
    s['audit_changes'] = [[c['request']['change']['target'], c['principal'].get('auth_method')] for c in changes]
    s['audit_digests_match'] = (len(changes) == 2 and changes[0]['request']['change']['after'] == applied_rules.get('result_revision')
                                and changes[1]['request']['change']['after'] == applied_limits.get('result_revision'))
    verify = subprocess.run([str(MAYU), 'audit', 'verify', '--file', str(V / 'audit.jsonl')], env=ENV, capture_output=True, text=True)
    s['audit_verify'] = verify.returncode
    log_text = (V / 'mayu.log').read_text(errors='replace')
    s['secrets_in_log'] = SECRET in log_text or ADMIN in log_text or (bool(key) and key in log_text)
    collector.shutdown()
    upstream.shutdown()
    print(json.dumps(s, indent=2))


if __name__ == '__main__':
    main()
```

- [ ] **Step 6: Validate the config, then run the live check**

```bash
V=$S/verify
python3 $V/live_check.py $V --config-only; echo "config=$?"
INFERPLANE_ADMIN_TOKEN=lint STUB_KEY=stub OTEL_LIVE_AUTH=x $V/mayu config validate --config $V/config.json > $V/validate.log 2>&1; echo "validate=$?"
python3 $V/live_check.py $V > $V/summary.json 2> $V/live.err; echo "live=$?"; cat $V/summary.json
```
Expected: `config=0`, `validate=0`, `live=0`, and the summary

```json
{
  "key_created": true,
  "status": true,
  "header_value_hidden": true,
  "request_1": 200,
  "audit_copy": true,
  "trace_exported": true,
  "collector_auth_header": true,
  "content_before_rule": false,
  "rules_dry_run": true,
  "rules_applied": true,
  "request_2": 200,
  "content_after_rule": true,
  "limits_dry_run": true,
  "limits_applied": true,
  "stale_revision_rejected": true,
  "request_3": 200,
  "content_while_paused": false,
  "withheld_counted": true,
  "decision_metric": true,
  "metrics_clean": true,
  "pause_survived_restart": true,
  "rule_survived_restart": true,
  "screenshots": true,
  "audit_changes": [["observability_rules", "break_glass"], ["observability_limits", "break_glass"]],
  "audit_digests_match": true,
  "audit_verify": 0,
  "secrets_in_log": false
}
```
If `mayu config validate` rejects the stub provider shape (field names of the `anthropic` provider), adjust `write_config` to the schema `config validate` accepts and record the ruling. If any value differs, keep the logs (`$V/mayu.log`, `$V/live.err`), find the cause, fix the product code in the owning task's files with a new commit, and rerun Steps 2 and 6. Open `$S/live-observability-1280-light.png` and `$S/live-observability-390-dark.png` and confirm the exporters table shows `corp` and `vault`, `vault` shows `paused`, and the selections list includes `console-live-content` from the console.

- [ ] **Step 7: Clean up**

```bash
pkill -f "$S/verify/mayu serve" ; ss -ltn > $V/ports-after.txt; grep -cE ':(18381|19391|18390|18399) ' $V/ports-after.txt
ls $V
```
Expected: `0` listeners left. The admin token, virtual key and collector secret existed only in the driver's memory; nothing to delete. Logs, configs, the key store (hashed keys only) and the audit file stay in `$V` for the report. Record Steps 1–6 results in the ledger. Step 1 is this task's only commit; nothing else in the repository changes here.

---

## Self-review notes (plan author)

- **Coverage of the program's Plan 4 items:** dashboard (Tasks 2–3 data, 7 API, 11 view), exporter status/queue/drops/last error (2–3), governed decisions incl. withheld and why (2–3: `no_rule`, `content_conflict`, `withheld_limit`, `sampled_out`), sampling (3–4, selections with effective percent), runtime editing of rules and ceilings with validation, dry-run diff, apply, persistence, audit (5–10), one-policy-source and control-plane read-only (5, 7, 9, 10), blast radius (Decisions 4, 5, 10; tests in 3, 8, 9), console conventions and nav entry (11), Node and Playwright checks with 1280/390 screenshots (11–12), changelog, gates, docs build, merge check against main and live check with a local OTLP stub (13).
- **Interfaces are consistent across tasks:** `Set.AuditTargets/ContentTarget/TraceTargets`, `Set.Normalize/SetLimits/Limits/Clamp/Exporting/SamplePercent/Status/SignalStats/ObserveTrace`, `EncodeLimits/DecodeLimits/Revision`, `policy.MergeObservability`, `Store.CheckLocal`, `tracing.InitExporters(..., observe)`, `server.WithFullAdminRoutes`, `observabilityapi.New/Options/Routes/LoadLimits`, `audit.ChangeRef` keep one name and signature everywhere they appear.
- **Known judgment calls an executor may need (record as rulings):** exact line numbers drift after earlier tasks in the same file (anchor on the quoted code, not the number); whether Playwright needs the `--chromium` path on another host (reuse `browser_qa.py`'s convention if launch fails).
- **Plan dry run (author, 2026-10-29, against the earlier base `0e602a9`):** every task's code, tests and doc edits were applied in order to a scratch export of the base branch. `go test ./... -race`, gofmt, `go vet ./...`, `tests/run-all.sh`, the docs gates (translations, unit, perf, compat, `mkdocs build --strict`, site check) pass. So do the Node checks, `routing_forms.cjs --views`, `browser_qa.py` and `observability_qa.py`. The Task 13 live driver also passed: `config validate` accepted its config and the summary matched the expected JSON exactly. Mutation probes confirmed the pinning tests fail without the reload mutex, with stat-after-read, without the boot state-file load, and with the old table CSS.
- **Re-verification against `caf55fe` (2026-11-04):** `internal/otelexport`, `internal/tracing`, `cmd/mayu/observability.go`, `internal/config/observability.go`, `internal/policy/observability.go` and `api/` are byte-identical to `0e602a9`, so Tasks 2–4's code there stands. Every anchor in files the merge changed was re-read and renumbered. Checked by `-overlay` (no repository change): Task 1's code and tests (new tests plus the whole `internal/policy` package), Task 2's metrics changes and the structural non-blocking test (RED at 30 s against a blocking `pipeline.go`, GREEN in 3.8 s), Task 5's config block, Task 6's record change and the analytics pin, Task 10's capability field and test. The Task 11 wiring ran in a scratch copy of `internal/server/adminui`: `observability_console.cjs`, the three `routing_forms.cjs` modes, `analytics_evidence.cjs` and `audit_console.cjs` pass, and without the harness edits `routing_forms.cjs --views` and `analytics_evidence.cjs` fail with `refreshObservabilityView is not defined`. Not re-run at `caf55fe`: the full race suite, the Go adminui tests (embedded files), the Playwright scripts, the docs build and the live driver; Task 13 runs them.
- **Out of scope (unchanged):** OTLP metrics export; editing endpoints, headers, TLS, `queue_bytes` or the legacy `otel` block at runtime; reporting local limits to the control plane; team-scoped observability views; persisting console counters across restarts.
