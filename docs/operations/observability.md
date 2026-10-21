# Monitor and troubleshoot

Use readiness for admission, metrics for trends, and audit/authority records for accounting evidence. A green process health check alone does not mean requests can be admitted.

## Health and signal channels

| Signal | Endpoint / transport | Use |
| --- | --- | --- |
| Process liveness | `GET :9090/healthz` | Is the gateway process serving? |
| Readiness | `GET :9090/readyz` | Can this profile admit traffic under its current dependencies/gates? |
| Metrics | `GET :9090/metrics`, Prometheus | Latency, requests, cost estimates, refusals, audit health |
| Traces | Opt-in `otel`, OTLP HTTP/gRPC | Request/attempt timing and bounded metadata |
| Usage windows | Gateway to CP `/v1alpha1/usage` | Control-plane analytics; not OTLP |
| Audit | Configured per-instance sinks/WAL | Request evidence and exact-byte chain verification |

Health and metrics are unauthenticated by design. Restrict the admin network path. The static console is public/data-free; its data APIs require authentication.

Start with the [Grafana dashboard](../../deploy/grafana/inferplane.json). Configure your Prometheus scrape or collector receiver separately from the OTLP trace receiver. The chart does not install a ServiceMonitor.

## Signals worth alerting on

| Signal | Investigate |
| --- | --- |
| Sustained readiness failures / 503s | Policy sync, identity fingerprint, DB availability, authority grant/journal |
| `inferplane_pricing_miss_total` | Missing route rates or incorrect pricing metadata |
| `inferplane_audit_write_failures_total` | Sink permissions, capacity, downstream availability |
| `inferplane_audit_buffer_utilization_ratio` | WAL filling; investigate sink failures (not proof of automatic admission blocking) |
| `inferplane_audit_anchor_failures_total` | Object storage/IAM/retention configuration |
| `gen_ai_server_time_to_first_token_seconds` | Upstream or request-path latency |
| `inferplane_fallback_total` / circuit state | Provider failure or incompatibility |
| `inferplane_usage_windows_dropped_total` | Analytics delivery loss; distinguish from authority accounting |

Choose thresholds from measured traffic and the selected profile. Prometheus spend is observational; integer accounting records and authority commitments determine available balance.

## Troubleshooting by symptom

| Symptom | First checks |
| --- | --- |
| 401 | Virtual key vs provider/admin token; selected database; revocation and identity bindings |
| 403 | Model/team access, privacy/region restriction, policy compatibility |
| 402 in authority profiles | Money exhausted or conservative bound exceeds available balance |
| 429 | Rate/token quota exhaustion; inspect the enforcing scope |
| 503 before first request | Required sync, shared namespace/binding, initial grant, DB/journal availability |
| Retryable grant wait | Respect `Retry-After`; do not lower accounting bounds to force admission |
| HTTP 200 stream ends early | Inspect terminal protocol error, observed usage and retained liability |
| Count returns zero while generation refuses | Counts remain local/200; check readiness and key-store availability |
| Missing model in Codex | Explicit tool/Responses capability, native catalog mapping, installed client version |
| Lower cache hit rate | Protocol conversion, masking, model switching, changed request bytes |

## Read outcome and cost evidence

The gateway Overview, Usage and Logs use observed completion records (ADR-053).
They include costless failures previously absent from analytics. The denominator
is `request_completed`, not all received traffic or unique requests/sessions.
`request_started` refusals, unauthenticated 401s, admin events and client-local
failures are excluded; retries/fallbacks may have different recording granularity
across ingresses. A succeeded record describes transport, not model answer quality
or completion of the user's task.

Investigate `(failed + partial) / completed` with its numerator, denominator and
unknown count. Partial takes precedence even with HTTP 200; missing/invalid
outcomes remain unknown. With no completions the ratio is `—`. Unknown outcomes
make the observed proportion incomplete, rather than successful. Metrics traffic
and this denominator are separate observations.

Read cost state before amount: `estimated` is a usage/rate-based estimate (zero
is valid), `uncertain` is a recorded amount with incomplete evidence,
`legacy_unknown` lacks usage evidence, `unpriced` lacks pricing and `unavailable`
has no Cost record. Unpriced/unavailable numeric zeros are not known dollar
amounts. Estimated totals and uncertain/legacy recorded totals are separate;
the existing Cost-present financial totals remain unchanged. No state is a
supplier invoice or permission to refund retained liability.

For Responses streams the bounded reasons are `context_length_exceeded`,
`invalid_encrypted_content`, `request_canceled`, `upstream_timeout`,
`stream_incomplete`, `response_incompatible`, `usage_missing`,
`client_write_failed` and `upstream_error`. Known provider codes require typed
provider evidence. Cancellation/deadline and gateway reasons require explicit
runtime evidence; raw error text and native provider-owned frames are not parsed
to guess a code. Unknown nonempty analytics codes become `upstream_error`.

Before streaming commits, existing HTTP/retry rules apply. After commit, the
response retains HTTP 200 with partial=true and the same bounded code in audit
and a gateway-generated SSE error when writable; it is not retried. Client-write
failure records `client_write_failed` without forcing another SSE write. Native
success bytes remain intact. Observed usage still follows the existing exactly-once
finish/settlement path; missing usage is not fabricated as zero.

Check `legacy_projection_records`, `/admin/analytics/health`, queue diagnostics
and retained source segments together. Version 0 means source evidence has not
been replayed; version 1 can still be `legacy_unknown` if the original lacked
usage evidence. Zero legacy rows does not prove full historical collection.
Retention gaps, the local nonblocking 4096-entry queue's drops, collection lag,
truncated JSONL tails and requests with only `request_started` limit coverage.
Missing fields from old APIs require an evidence-unavailable notice.

Use the [API examples](../api-reference.md#analytics-outcome-and-cost-evidence-adr-053)
and [recovery procedure](recovery.md#recover-analytics-evidence). This projection
adds no metric labels, raw prompts/errors, key/session identifiers or financial
authority. Default/local, node-local authority and shared-governance dependency
contracts retain their existing profile-specific limits.

## Verify records

```bash
bin/mayu audit verify --file /path/to/instance-audit.jsonl
bin/mayu report --file /path/to/instance-audit.jsonl --by team,model
```

Keep instance/restart segments distinct. A valid local chain does not prove resistance to a host rewriting its history; configure and verify [external anchoring](../runbooks/audit-anchoring.md) when that evidence is required.

For an issue report, include the commit/image, profile, client version, status/error class, sanitized configuration, and whether the failure occurs before egress or during streaming. Exclude keys, credentials, raw prompts, identity declarations and DSNs. Send security reports through the [private security policy](../../SECURITY.md).

## Audit durability limits

The default chart writes audit records to container stdout (a best-effort sink)
with a WAL on the data volume and the local analytics index disabled, so nothing
grows on the pod volume; see [Collect audit records](#collect-audit-records). Any
other deployment must configure its sinks explicitly. A
required-sink failure increments diagnostics but does not currently provide a
guaranteed fail-closed admission gate. Automatic WAL replay/recovery is incomplete;
do not treat process readiness or a local chain check as proof that all prior
records survived. Preserve evidence and stop affected traffic during sink failures.

## Collect audit records

[`examples/otel-collector/audit-filelog.yaml`](https://github.com/inferplane/inferplane/blob/main/examples/otel-collector/audit-filelog.yaml)
is an OpenTelemetry Collector (contrib) configuration for a node DaemonSet. It tails
`/var/log/pods/*/inferplane/*.log`, keeps only lines that start with
`{"schema_version"` (mayu also prints startup lines), and exports each body
verbatim to S3 as JSONL. Checkpoints and the send queue persist in `file_storage`.

- Do not parse and re-serialize record bodies: each `prev_hash` covers the previous
  line's exact bytes, and a rewritten line fails `mayu audit verify`.
- stdout is not a required sink, so the container log is the gateway's remaining
  local copy until the collector durably enqueues the record. The collector's
  persistent queue holds a separate local copy pending export. Kubelet rotation
  is size-based (`containerLogMaxSize` × `containerLogMaxFiles`); size it for the
  longest period when the collector cannot ingest (about 1.3 KB per request).
- Verify each instance's collected records at the destination, together with
  [external anchoring](../runbooks/audit-anchoring.md): a truncated or restarted
  chain still verifies without an anchor.

Verified on 2026-09-25 with otelcol-contrib 0.119.0 against a CRI-format log that
included a split (partial) line: 41 records exported byte-identically and the
collected file verified with `mayu audit verify`. The S3 upload itself was not
exercised.
