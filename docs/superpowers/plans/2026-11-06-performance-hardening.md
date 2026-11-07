# Performance hardening Implementation Plan

> For agentic workers: use superpowers:executing-plans inline.

**Goal:** Implement the five accepted performance-review fixes.
**Architecture:** Request-local parsing, explicit masking administration,
acknowledged OTLP audit batches, and incremental durable journal persistence.
**Tech Stack:** Go, SQLite, OTLP HTTP/gRPC, embedded JavaScript console.
**Spec:** ../specs/2026-11-06-performance-hardening-design.md

## Global Constraints

Preserve existing uncommitted security fixes and benchmark artifacts. Work on
the existing fix branch. No production service, network collector or credentials
in tests. No commits, deployment or remote changes are required.
Required policy, accounting and audit evidence cannot be weakened for speed.
No subagents: the session lacks the close-agent lifecycle tool required by the
repository instructions. Record validation here so work survives compaction.

## Review Focus

Default-off semantics; admin-only writes; stale-state refusal; duplicate-key and
opaque-state refusal; exact raw bytes; response-header-only timeout; no
acknowledgment before disk sync; collector partial success is not full delivery;
previous failed fan-out is never discarded; owner fencing survives migration.

## Tasks

- [x] 1. Add `providers/http_client.go` and transport tests, then use the default
  in the existing HTTP provider factories. Test timeout and continued body reads.
- [x] 2. Add reusable `internal/responses` parsing and tests, reuse it in
  `internal/server/responsesapi/handler.go`; benchmark native/adapter paths.
- [x] 3. Add explicit optional masking configuration, persisted admin control
  and console setting; verify default-off, authorization and required policy
  independence with offline tests.
- [x] 4. Add audit group commit and required OTLP sink, wire config and assembly;
  test full/partial/failed collector acceptance and retained WAL invariants.
- [x] 5. Add entity journal storage with atomic version migration, incremental
  updates and database-fenced snapshot reuse. Extend restart, corruption,
  rollback and bounded-retention tests; repeat disk benchmark.
- [x] 6. Update reference/operations docs and ADR; run both static builds,
  full race tests, vet, tracked/new-file formatting and bash harness.

## Execution ledger

- Baseline: branch `fix/security-review-hardening-20261105`, HEAD `49a4c2a`;
  performance evidence `/tmp/inferplane-perf-20261106/review.ko.md`.
- Ruling: continue in the existing fix branch and preserve its dirty changes;
  moving only part of the work into another checkout would omit necessary
  prior audit fixes.
- Ruling: existing best-effort governed OTLP copies cannot authorize deletion.
  Add an explicit required destination; retain file archives when still selected.

- Implemented: response-header transport deadline; immutable Responses document;
  persisted full-admin masking UI/API; complete structural optional redaction;
  audit group commit and acknowledged OTLP; version-2 incremental journal.
- Regression evidence: duplicate/opaque JSON, continued stream reads, admin
  authorization and restart, required-policy independence, HTTP/gRPC partial
  acceptance retention, journal rollback/corruption/migration/fencing all pass.
- TDD refinement: system-message masking regression initially failed on Anthropic
  and InvokeModel; both now use the complete redactor under the new control.
- Static builds, vet and 70 bash harness checks pass. Full race suite is running;
  final formatting and performance artifact review remain.

- Final verification: both CGO-disabled binaries build; full `go test ./... -race`
  passes (68 tested packages, including authority/local and sensitivity).
  Final review changes in cmd/mayu and responsesapi also pass their full race
  suites. Vet passes, 847 tracked/new authored Go files are formatted, and the
  bash harness passes 70 checks. Translation validation passes 48 document pairs
  and both ADR indexes. No production calls, deployment, commit or subagents.
- Final disk benchmarks passed with original audit 1s and authority 32x settings,
  after the race process exited. Results, source/binary hashes and raw output
  are preserved in docs/benchmarks/2026-11-06-performance-hardening.ko.md and its
  companion results directory. All planned work is complete.
