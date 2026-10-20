---
title: Performance results
description: Measured inferplane latency, throughput, cache cost and native Converse compatibility.
---

# Performance results

**As of October 20, 2026.** This page presents the latest saved measurements. Native Converse/UI results below belong to an unmerged development implementation; they do not describe functionality available in the published main branch. Gateway and broad model measurements are from October 19; native Converse and administrator UI measurements are from October 20. These are bounded observations, not a production SLA.

<div class="grid cards" markdown>

-   **49 models covered**

    42 models completed at least one task through direct Bedrock access. Coverage includes Anthropic, OpenAI, xAI/Grok, Qwen, DeepSeek, Moonshot/Kimi, MiniMax and GLM.

-   **26 models at 95–100% input occupancy**

    Successfully retrieved all three planted values near the declared context limit. This measures synthetic retrieval, not coding or document understanding.

-   **48/48 native Invoke requests completed**

    Through inferplane, at concurrency 1 and 4. Per-cell median E2E latency was 795.96–814.56 ms for a short Sonnet 4.6 task.

-   **4/4 native Converse requests completed**

    Haiku 4.5 and Qwen3 32B on the development build, each through its administrator UI and a streaming SDK client. One request per model/API.

</div>

[Explore all model results](models.md){ .md-button .md-button--primary }
[Download measurement data](../assets/data/performance.json){ .md-button download="inferplane-performance.json" }

## Gateway latency

The client called inferplane's native Bedrock Invoke endpoints using Claude Sonnet 4.6 (`us.anthropic.claude-sonnet-4-6`). Each cell contains 12 requests with a 64-token output cap and a short fixed-response task. The SDK made one attempt per request. E2E includes model execution and network time; it does **not** isolate gateway CPU overhead.

<!-- performance:gateway -->
| API | Concurrency | Success/calls | E2E p50 (ms) | E2E p95 (ms) | TTFT p50 (ms) | Success req/s |
| --- | --- | --- | --- | --- | --- | --- |
| InvokeModel | 1 | 12/12 | 814.56 | 913.70 | — | 1.23 |
| InvokeModelWithResponseStream | 1 | 12/12 | 797.03 | 852.62 | 792.05 | 1.26 |
| InvokeModel | 4 | 12/12 | 795.96 | 908.80 | — | 4.67 |
| InvokeModelWithResponseStream | 4 | 12/12 | 796.06 | 1,012.43 | 790.73 | 4.52 |
<!-- /performance:gateway -->

The observed response failure rate is **0/48** for this task. With 12 observations per cell, nearest-rank p95 is the largest observation; it is a description of this run, not a stable tail-latency estimate. The test does not establish throughput limits, multi-node scaling or shared-Postgres overhead.

## Generation throughput under concurrency

The following batches used inferplane's **Messages adapter**, three models, eight requests per cell and a 512-token output cap. Successful output tokens are divided by the **whole batch wall time**, including time spent on failed requests. This is delivered batch throughput, not per-request decode speed.

<!-- performance:concurrency -->
| Model | Concurrency | Success/calls | Response failure rate | Batch time (s) | Successful output tok/s |
| --- | --- | --- | --- | --- | --- |
| Claude Opus 4.8 | 1 | 5/8 | 37.5% | 41.88 | 57.50 |
| Claude Opus 4.8 | 4 | 6/8 | 25.0% | 16.35 | 166.95 |
| GPT-6.1 Sol | 1 | 8/8 | 0.0% | 71.77 | 57.07 |
| GPT-6.1 Sol | 4 | 8/8 | 0.0% | 18.46 | 221.87 |
| Qwen3 Next 80B A3B | 1 | 8/8 | 0.0% | 45.87 | 89.29 |
| Qwen3 Next 80B A3B | 4 | 8/8 | 0.0% | 13.59 | 301.38 |
<!-- /performance:concurrency -->

GPT-6.1 Sol increased from **57.07 to 221.87 tok/s** with concurrency 1 → 4; Qwen3 Next 80B A3B increased from **89.29 to 301.38 tok/s**. Both completed 8/8 requests in each cell. Opus 4.8's five failed responses were classified as empty output; its successful-throughput numbers include the wall-time penalty. These small batches do not establish saturation or rate-limit capacity.

## Observed prompt-cache cost

Each model reused its own identical prefix sequentially through inferplane's Messages adapter: one observed cache write, followed by three observed reads. The table uses reported cache token counts, not an assumption that the first request was cold.

<!-- performance:cache -->
| Model | Cached tokens | Write/read calls | Write request cost | Median read request cost | Write TTFT (s) | Median read TTFT (s) |
| --- | --- | --- | --- | --- | --- | --- |
| Claude Opus 4.8 | 65,693 | 1 / 3 | $0.411541 | $0.033806 | 2.391 | 1.645 |
| Claude Sonnet 4.6 | 65,642 | 1 / 3 | $0.246616 | $0.020152 | 2.475 | 1.610 |
| GPT-6.1 Sol | 65,634 | 1 / 3 | $0.164409 | $0.006887 | 2.082 | 1.377 |
<!-- /performance:cache -->

The read requests were approximately **91.8% cheaper** for Sonnet 4.6 and Opus 4.8, and **95.8% cheaper** for GPT-6.1 Sol than their respective cache-creation requests. Those percentages compare complete requests under the recorded prices; they are not a discount on every workload. Initial writes, expiry, misses, changed prefixes and retries remain billable. Only the default TTL was exercised.

For context-sized request costs and generation latency, see the [model tables](models.md). Their different token counts, input types and cache states matter when comparing rows.

## Native Converse and administrator UI

The evaluated **development build** accepted `/model/{configured-model}/converse` and `/converse-stream` for eligible Bedrock Runtime targets. Its **Model comparison** administrator page executed paired requests through virtual-key admission and settlement. This implementation is not merged into main; publishing these measurements does not enable those endpoints or that UI. The [API reference](../api-reference.md) describes the published implementation.

<!-- performance:native -->
| Model | API | Completed/calls | E2E (s) | TTFT (s) | Request cost |
| --- | --- | --- | --- | --- | --- |
| Claude Haiku 4.5 | Converse · UI | 1/1 | 1.035 | — | $0.000041 |
| Qwen3 32B | Converse · UI | 1/1 | 0.344 | — | $0.000004 |
| Claude Haiku 4.5 | ConverseStream · SDK | 1/1 | 0.900 | 0.890 | $0.000041 |
| Qwen3 32B | ConverseStream · SDK | 1/1 | 0.289 | 0.289 | $0.000004 |
<!-- /performance:native -->

These four short `READY` requests completed with usage and settlement evidence. The UI is non-streaming: TTFT is unavailable, and its E2E measures the internal inference handler, excluding the browser round-trip. SDK E2E and TTFT are measured from the client. **n=1 per model/API** does not support model ranking or an operational error-rate claim. All four costs total **$0.000090** under the installed price snapshot.

The evaluated UI supports sequential A/B then B/A order, response failure counts, E2E output tok/s and JSON export. It accepts text prompts and an output cap up to 4,096 tokens. It is a paired comparison tool; the broad context and concurrency results above come from API clients.

## Cost visibility and evidence

| Visible field | Interpretation |
| --- | --- |
| Usage and cache classes | Provider-reported quantities; evidence flags describe completeness |
| Cost in integer microUSD | Installed price snapshot × interpreted usage, with half-even settlement |
| Accounting known | Complete settlement evidence and a configured price; not a reconciled invoice |
| Unknown cost | Missing price/usage, cancellation, transport loss or incomplete attempt evidence; never silently zero |
| Response success | Complete response; synthetic retrieval additionally requires all three planted values |

Prices in this dataset are the **October 19 measurement snapshot**, not a live price feed. They exclude infrastructure, taxes, private discounts and unconfigured charges. Recorded estimates are preserved rather than retroactively repriced. For request-level tiers, cache semantics and telemetry, read [context pricing](../reference/pricing.md). A failed request may still incur cost.

## Scope and reproducibility

The downloadable JSON includes model IDs, regions, declared windows, recorded rates, sanitized per-request metrics, batch denominators and source SHA-256 digests. Source paths identify local recordings; the raw recording files are not published. It excludes prompts, answer text, upstream error bodies, credentials and personal budget details. Native Invoke, Messages adaptation, native Converse and direct model measurements are identified separately.

Most model/context/generation cells have **one observation**. Timing depends on region, transport, cache and provider reasoning defaults. No quality ranking, exact maximum context, production error rate, HA guarantee or availability SLA follows from these measurements. Interpret results with the [deployment profile](../getting-started/deployment-profiles.md) and [production readiness](../operations/production-readiness.md) requirements.
