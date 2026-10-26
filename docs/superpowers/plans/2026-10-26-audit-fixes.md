# Verified audit fixes implementation plan

> **For agentic workers:** Use superpowers:subagent-driven-development or superpowers:executing-plans task by task. Every runtime change starts with an observable failing regression.

**Goal:** Repair the confirmed policy, accounting, client compatibility, configuration and deployment defects from the cross-comparison.

**Architecture:** Preserve independent hard caps and existing protocol boundaries. Local monetary admission gains per-attempt uncertainty ownership; observed actual cost remains distinct from retained liability. Durable/shared authority must retain its existing fail-closed guarantees. Configuration and chart changes make unsupported or unsafe settings explicit.

**Tech stack:** Go, local httptest providers, shell/Python launcher tests, Helm, paired English/Korean Markdown.

**Spec:** `/tmp/inferplane-review/cross-comparison.ko.md`; existing AGENTS.md policy, authority, privacy and protocol contracts remain binding.

## Global constraints

- Integer microUSD accounting; no invented actual usage or cost.
- Preserve provider/core extension boundaries and leaf package dependency rules.
- No credentials or live upstreams in tests; installed CLI tests use isolated config and loopback fakes.
- Preserve untracked benchmark work. The subsequent user request authorizes a signed commit and pull request; deployment is outside this task.
- New audit fields must be append-only/omitempty; metrics labels remain bounded.
- Counts keep their authenticated local/200 contract.
- Local enforcement remains process-local; no fleet-global claim.

## Review focus

- A smaller soft threshold must not erase or replace an independent hard cap.
- Concurrent or canceled requests must not release uncertain money or charge undispatched TPM.
- Provider retries must acquire their own liability and settle it exactly once.
- Typed context/rate errors must reach clients without exposing arbitrary sensitive upstream messages.
- ConfigMap changes, fresh PVC ownership and slow audit replay must yield usable chart deployments.

## Task 1 — Policy layering

Owner: budget worker. Files: `internal/policy/store.go`, policy regressions, `cmd/mayu/gateway.go` policy lookup/clamp sections and gateway policy tests.

- [x] Reproduce soft/hard month/day/user combinations and omitted default block.
- [x] Preserve independent hard admission caps while retaining soft routing/accounting semantics; hard lease flags remain binding even if allowance is larger.
- [x] Verify focused policy/gateway tests and update affected paired references.

## Task 2 — Local liability, concurrency and undispatched token reservations

Owner: root. Files: `internal/governance`, `internal/budget`, `internal/limiter`, `internal/server/requestpolicy`, ingress settlement and accounting tests.

- [x] Reproduce disconnect/missing usage, overlapping local monetary attempts and denied TPM.
- [x] Add a per-attempt local monetary permit at the existing requestpolicy attempt seam; atomically consult blocking scopes and keep uncertain liability separately from observed spend.
- [x] Exact complete usage releases the permit after actual settlement; incomplete/malformed usage retains a conservative verified bound. No fabricated actual cost.
- [x] Failed PreCheck returns its undispatched TPM reservation; dispatched unknown outcomes retain their estimate.
- [x] Verify all ingress accounting, independent key day budgets, shared/durable uncertainty preservation and race tests.

## Task 3 — Client error and native Responses compatibility

Owner: client worker. Files: `internal/responses`, `internal/server/responsesapi`, provider error/Responses packages, launcher/probe scripts and tests, Messages attempt selection/retry sections.

- [x] Reproduce dropped context codes, incorrect rate-quota type, incomplete response rejection, alias probe and inherited reasoning effort.
- [x] Preserve allowlisted typed context errors and native incomplete responses; avoid retrying validation errors into smaller models.
- [x] Keep the launcher catalog compatible with inherited effort, align default port and qualify native aliases.
- [x] Verify isolated CLI/script and protocol tests; update paired client references.

## Task 4 — Configuration, packaging and documentation

Owner: docs/ops worker; root owns CLI parser, monthly quota integration and audit gating.

- [x] Reject unknown typed config keys and invalid team budgets without rejecting documented annotation/free-form fields.
- [x] Add usable default admin token refs, PVC group ownership, startup probes, config checksum and correct config-aware NOTES commands; repair CRD adminContact.
- [x] Correct confirmed stale classifier/auth/Mask/SQLite/CI/status documentation, preserving English/Korean synchronization.
- [x] Implement or explicitly reject unsupported monthly quota/audit options; add `config validate` and strict serve flags.
- [x] Verify config, chart, CRD and docs checks.

## Task 5 — Integration and verification

Owner: root.

- [x] Review worker changes and integration contracts; obtain independent code review.
- [x] Build both binaries with CGO disabled.
- [x] Run `go test ./... -race`, `go vet ./...`, tracked-source gofmt and `bash tests/run-all.sh`.
- [x] Run documentation/deployment/launcher checks and database-backed tests where locally available.
- [x] Record exact successful checks, any environment skips and remaining measurement/qualification limits.


## Verification recorded before PR integration — 2026-10-26

All scoped tasks above were complete in the working tree at this checkpoint. No commit, push or deployment had been performed.

- Final `go test ./... -race -json -timeout 15m`: 62 test packages passed; 2,270 top-level tests and 2,630 subtests passed; no test skips. Four packages have no test files. The final command reused valid current-tree cache results for 61 packages after the full integration run; the PostgreSQL key package was additionally executed with its exact required environment variable.
- Database sentinel gate: 7/7 passed against disposable loopback PostgreSQL; all three opt-in Codex CLI Go cases passed. Launcher 48 tests and context-probe 10 tests passed with isolated configuration and fake upstreams.
- Both binaries built with `CGO_ENABLED=0`; whole-repository vet and tracked/new project-source gofmt passed. The original ignored benchmark dependency tree was excluded from project formatting.
- Latest shell harness: 70/70, no skips. Documentation: 31 unit tests, 47 translation pairs/ADR indexes, strict MkDocs, 201-page link/assets check; committed performance/client data validators passed.
- Helm default/shared lint and NOTES rendering, native Kubernetes CRD schema/CEL checks passed. No live deployment or PVC qualification was attempted.
- Independent reviews completed for policy/local settlement/config/chart, client compatibility, audit admission and final large-record verifier. The actual vLLM v0.8.5 top-level HTTP error envelope was checked separately from nested OpenAI errors; all 42 conversion cases and native byte preservation pass.

Financial limits: local counters/liability remain process-local and restart-volatile; unknown usage retains authority rather than inventing spend; one-attempt overshoot and conservative context bounds remain. Provider surcharges, SQL/body/PII/fsync performance, response-header deadlines and live client/deployment qualification remain follow-up work.

Evidence: `/tmp/inferplane-review/fix-results.ko.md`, `fixes-complete-race.jsonl`, `fixes-latest-static-build.log`, `fixes-latest-vet.log`, `fixes-latest-gofmt.log`, `fixes-latest-harness.log`, `fixes-final-docs-build.log` and independent review reports in that directory.

## PR integration verification — 2026-10-26

The signed change was rebased on main `b83101b`, preserving native Converse and atomic control-plane policy import. Native Converse now receives the same audit readiness and monetary admission safeguards. Invalid accounting evidence cannot make the new evaluation observer report complete settlement; independent local/shared regression cases verify this without changing shared financial settlement semantics.

- Whole-repository race run: 62 test packages, 2,332 top-level tests and 2,674 subtests passed. No test skips; four packages have no test files.
- Disposable loopback PostgreSQL: all 7 required database sentinel tests passed. All three opt-in Codex CLI Go tests passed against isolated fake upstreams.
- Both static binaries and all packages built with `CGO_ENABLED=0`; whole-repository vet and tracked-source gofmt passed.
- Shell harness: 70/70 passed without skips. Documentation: 31 unit tests, 48 translation pairs and both ADR indexes, strict MkDocs and links/assets in 205 HTML pages passed. Performance/client data validators passed.
- Default and shared Helm lint passed. Earlier native CRD schema/CEL and launcher checks remain applicable; those files did not change during rebase.
- Independent integration reviews found no remaining important or critical issue within their validation scope.

Evidence: `pr-all-race.jsonl`, `pr-static-build.log`, `pr-build-all.log`, `pr-vet.log`, `pr-harness.log`, `pr-docs-build.log`, `pr-site-links.log`, `pr-translation-pairs.log` and `rebase-budget-evaluation-independent-review.md` under `/tmp/inferplane-review/`. No deployment or live upstream qualification was performed.
