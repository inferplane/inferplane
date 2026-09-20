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

## Verify records

```bash
bin/mayu audit verify --file /path/to/instance-audit.jsonl
bin/mayu report --file /path/to/instance-audit.jsonl --by team,model
```

Keep instance/restart segments distinct. A valid local chain does not prove resistance to a host rewriting its history; configure and verify [external anchoring](../runbooks/audit-anchoring.md) when that evidence is required.

For an issue report, include the commit/image, profile, client version, status/error class, sanitized configuration, and whether the failure occurs before egress or during streaming. Exclude keys, credentials, raw prompts, identity declarations and DSNs. Send security reports through the [private security policy](../../SECURITY.md).

## Audit durability limits

Configure audit sinks explicitly; the default chart does not supply one. A
required-sink failure increments diagnostics but does not currently provide a
guaranteed fail-closed admission gate. Automatic WAL replay/recovery is incomplete;
do not treat process readiness or a local chain check as proof that all prior
records survived. Preserve evidence and stop affected traffic during sink failures.
