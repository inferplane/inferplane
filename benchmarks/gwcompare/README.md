# Gateway head-to-head: mayu vs LiteLLM vs Portkey (measured)

Reproducible loopback benchmark behind `docs/comparison.md` §5: the SAME
OpenAI-compatible mock upstream, the SAME client, measured through each
gateway and directly. The mock answers instantly, so the numbers are almost
entirely GATEWAY overhead; loopback excludes network distance, which only
widens the gap for any centrally-hosted gateway.

## Results (2026-09-02)

Environment: 4-vCPU Intel Xeon @ 2.10GHz container, Linux, all processes on
loopback. inferplane mayu (this repo, `CGO_ENABLED=0 go build ./cmd/mayu`),
LiteLLM proxy **1.99.0** (pip, venv, Python 3.11), Portkey open-source
gateway **1.15.2** (npm, Node 22). 400 requests per row after 20 warmup, no
errors in any completed row.

### Non-streaming: full request latency (ms)

| target | c=1 mean | c=1 p50 | c=1 p99 | c=8 mean | c=8 p50 | c=8 p99 |
|---|---|---|---|---|---|---|
| direct (baseline) | 0.15 | 0.14 | 0.25 | 0.45 | 0.29 | 2.89 |
| **inferplane mayu** | **0.97** | **0.94** | **1.64** | **4.23** | **4.02** | **8.53** |
| Portkey gateway | 2.32 | 2.14 | 5.22 | 13.25 | 12.36 | 20.46 |
| LiteLLM proxy | 7.68 | 7.67 | 8.63 | 48.33 | 47.62 | 69.35 |

Gateway overhead over direct at c=1 p50: **mayu +0.80ms**, Portkey +2.00ms
(2.5× mayu's), LiteLLM +7.53ms (9.4× mayu's). At c=8 the spread widens:
mayu p50 4.02ms vs Portkey 12.36ms vs LiteLLM 47.62ms.

### Streaming: time to first content chunk (ms)

| target | c=1 mean | c=1 p50 | c=1 p99 | c=8 mean | c=8 p50 | c=8 p99 |
|---|---|---|---|---|---|---|
| direct (baseline) | 0.15 | 0.14 | 0.31 | 0.28 | 0.19 | 1.54 |
| **inferplane mayu** | **0.85** | **0.81** | **1.59** | **2.85** | **2.62** | **6.24** |
| LiteLLM proxy | 11.56 | 11.49 | 13.71 | 81.97 | 81.09 | 109.21 |
| Portkey gateway | — (500) | — | — | — | — | — |

mayu adds **+0.67ms** to first-token time at c=1 p50 where LiteLLM adds
+11.35ms (17× mayu's). Portkey OSS 1.15.2 running on plain Node 22 returned
`{"status":"failure"}` (internal `TypeError: immutable` in
`tryTargetsRecursively`) on every streaming request in this environment —
its streaming row is honestly unmeasurable here, not silently omitted; its
non-streaming rows above are valid.

Every mayu request in these runs passed the full pipeline: key auth,
RBAC, governance PreCheck with budget reservation, settle with integer-µUSD
cost, and the hash-chained audit write (verified after the run with
`mayu audit verify`).

## Reproduce

```bash
go build -o /tmp/gwcompare ./benchmarks/gwcompare
CGO_ENABLED=0 go build -o /tmp/mayu ./cmd/mayu

/tmp/gwcompare upstream -addr 127.0.0.1:9101 &        # the shared mock

# mayu: openai_compatible provider -> http://127.0.0.1:9101, model bench-model,
# team bench, mint a key via POST /admin/keys (see examples/config.selfhosted.json)
/tmp/mayu serve --config mayu.json &

# LiteLLM: model_list openai/bench-model api_base http://127.0.0.1:9101/v1
litellm --config litellm.yaml --port 9104 &

# Portkey OSS
node node_modules/@portkey-ai/gateway/build/start-server.js &   # :8787

/tmp/gwcompare bench -label mayu -url http://127.0.0.1:9102/v1/chat/completions \
  -header "Authorization: Bearer $KEY" -n 400 -c 1            # + -stream, -c 8
/tmp/gwcompare bench -label litellm -url http://127.0.0.1:9104/v1/chat/completions \
  -header "Authorization: Bearer sk-bench-master" -n 400 -c 1
/tmp/gwcompare bench -label portkey -url http://127.0.0.1:8787/v1/chat/completions \
  -header "Authorization: Bearer dummy" -header "x-portkey-provider: openai" \
  -header "x-portkey-custom-host: http://127.0.0.1:9101/v1" -n 400 -c 1
```

## Caveats (stated, not hidden)

- Loopback, mock upstream: measures the proxy hop, not end-to-end serving.
  Against a real model the ABSOLUTE difference stays (it is per-request CPU
  and buffering cost) while the RELATIVE difference shrinks into model time.
- Single container, 4 vCPU: all four processes shared the host; the c=8
  rows partially measure scheduler contention, identically for every target.
- LiteLLM ran with its default settings plus a master key; no Redis. Portkey
  ran the open-source gateway, not their hosted edge deployment (which adds
  network distance instead).
