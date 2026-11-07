# ADR-058: Request-local parsing, optional masking and bounded durable work

Date: 2026-11-06
Status: Accepted

## Context

The local review found repeated Responses JSON traversal, expensive opt-in
privacy work, per-record audit fsync, unbounded default upstream header waits
and whole-snapshot monetary journal rewrites. Optimization must preserve
privacy, raw forwarding bytes, exact accounting and audit evidence.

## Decision

* Default HTTP transports wait at most 120 seconds for response headers.
  Established streams have no new total-duration timeout. Explicitly injected
  clients retain their transport and timeout settings.
* `responses.ParsedRequest` shares validated root fields among observation,
  stored-reference refusal, conversion and strict-tool checks. Conversion checks
  are cached within the request. Masking invalidates the observation.
  Duplicate-key/depth checks, opaque-state refusal and separate policy inspection
  remain. Native forwarding still uses raw bytes.
* `masking.enabled` defaults to false. A dedicated `masking.state_file` enables
  full-admin console/API edits. Persisted choices survive restart and revisions
  prevent stale writes. Team scope is a bootstrap setting; required
  GovernancePolicy privacy stays independent. Legacy `plugins` cannot coexist
  with this block. The new switch supports structural OpenAI Chat masking;
  existing plugin-only Chat refusal remains unchanged.
* Audit drains up to 64 queued records per group without an intentional delay.
  WAL and required file outputs each sync once per group. Durable issuance
  records keep their separate acknowledgement path. Chain bytes, order and
  publication after sync remain unchanged.
* `audit.sinks` with `type: otlp` and `exporter` selects a required destination
  from the local observability ceiling. It sends all canonical audit records,
  independently of subject-based best-effort copies. Only complete HTTP/protobuf
  or gRPC acknowledgement permits reclamation. Partial acceptance, malformed
  responses, pause/narrowing and transport errors retain the WAL. No automatic
  transport retries are added. Successful groups truncate and sync the WAL only
  when no earlier undelivered history remains. Existing files and issuance
  archives are never erased by enabling this mode. Collector acceptance transfers
  responsibility to that collector; it does not prove permanent backend storage.
* ADR-045 local journal version 2 stores budgets, grants, requests, permits,
  meters and receipts in separate SQLite rows. A root holds metadata and entity
  counts. Changes and collection commit atomically with FULL durability.
  A decoded snapshot is copied before mutation and published after commit.
  Every transaction checks the durable owner, root bytes and revision; entry
  triggers invalidate snapshots after other writes. Missing entities refuse.
  There is no memory fallback for admission.

## Migration and consequences

Version 1 migrates atomically at startup. Startup still burns old authority,
retires pending permits and fences the previous owner. Older binaries refuse
the version-2 journal. Do not downgrade by deleting the journal or restoring a
stale copy; coordinate accounting recovery with the central authority.

Choose required OTLP instead of a file sink when permanent local JSONL retention
is unwanted. Keeping a file sink explicitly keeps its archive. The analytics
database and captured-body store have separate retention. Avoid selecting the
same collector for best-effort copies unless duplicates are intended.

Ambiguous fan-out still requires operator reconciliation. Restart neither
blindly replays nor discards it. Group commit improves sustained drain throughput,
not the latency of a solitary durable operation. Masking remains finite detector
coverage. Local tests do not qualify production model quality or fleet HA.
