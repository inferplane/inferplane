# Benchmarks

Reproducible harnesses backing the performance claims in
[docs/comparison.md](../docs/comparison.md) §5. Run manually from the
repository root; results are hardware-dependent and quoted numbers must
always carry the machine they were measured on. These are deliberately not
CI jobs — timing assertions in shared CI runners flake.

## streaming — data-plane overhead on a streamed Messages request

```bash
go run ./benchmarks/streaming
go run ./benchmarks/streaming -requests 100 -chunks 60 -chunk-interval 20ms -hop 8ms -json
```

Measures TTFT (p50/p99), mean inter-chunk gap, and total duration for the
same mock Anthropic Messages SSE response across three topologies:

| Scenario | Path | What it represents |
|---|---|---|
| `direct` | client → upstream | no gateway (baseline) |
| `mayu` | client → mayu (127.0.0.1) → upstream | inferplane's node-local data plane, **real binary, full hot path** (key auth, routing, governance PreCheck/Settle, hash-chained audit) |
| `central-sim` | client → delay proxy → upstream | a central gateway's *network position only*: request and each response chunk shifted by `-hop` one-way transit latency, pipelined |

### Methodology and honesty notes

- **mayu's cost is measured, the central hop's is simulated — asymmetrically
  in the central gateway's favor.** The simulation models lossless network
  transit only: no gateway processing, no translation, no queueing, no TLS,
  no shared-tenant contention. A real central gateway adds its processing
  *on top of* the transit latency modeled here (Portkey self-reports
  10–20ms P50 total overhead; the default `-hop 8ms` ≈ 16ms round trip is
  chosen to sit at the low end of that). mayu's number, by contrast,
  includes all of its real work.
- The mock upstream emits `-chunks` deltas at a fixed `-chunk-interval`
  cadence, modeling token streaming. mayu forwards this verbatim
  (ingress protocol == provider protocol → `RawBody` passthrough), so the
  measured overhead is the true hot-path cost, not a re-serialization
  shortcut.
- Transit pipelining is modeled correctly: a network hop shifts every
  chunk by a constant and does **not** stretch inter-chunk gaps, and the
  harness reproduces that (compare the inter-chunk column across
  scenarios).
- Everything runs on loopback in one process group; there is no real
  network. The point is the *relative* cost of mayu's position vs a
  central gateway's position, not absolute latencies.

### Illustrative result

Linux container, 2026-09-01, defaults except
`-requests 20 -chunks 30 -chunk-interval 15ms`:

```
scenario         TTFT p50     TTFT p99    inter-chunk    total p50    total p99
direct             0.37ms       0.46ms        15.36ms     461.13ms     462.11ms
mayu               1.19ms       1.44ms        15.63ms     470.35ms     471.57ms
central-sim       17.39ms      17.73ms        15.79ms     490.05ms     494.14ms

mayu         adds +0.82ms TTFT p50, +9.22ms total p50 vs direct
central-sim  adds +17.01ms TTFT p50, +28.92ms total p50 vs direct
```

Reading it: the fully governed node-local hop costs under a millisecond of
TTFT; a central gateway pays double-digit milliseconds before it has done
any work at all. Re-run on your own hardware before quoting numbers.
