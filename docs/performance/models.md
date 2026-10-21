---
title: Model performance
description: Context occupancy, generation throughput, response failures and measured request costs for 49 Bedrock models.
---

# Model performance

**Measurement date: October 19, 2026 · 49 models · direct Bedrock access.** These model measurements inform inferplane configuration; they do not measure gateway overhead or establish native Converse compatibility for every model. The recorded transport is model-dependent: Bedrock Runtime or Mantle. See [gateway and administrator results](index.md) for inferplane measurements.

See [repeated calls and Claude Code/Codex compatibility](clients.md) for repeated task outcomes, actual coding tasks and client context settings.

## Context capacity and request cost

Each row selects that model's **largest successfully retrieved input at or below 100% of its declared window**. The reported input includes cache tokens once. A success must retrieve all three planted values at approximately 10%, 50% and 90% of the input. It does not require strict output ordering or JSON formatting.

**26 models reached 95–100% occupancy.** A lower percentage means this dataset does not demonstrate near-full success for that model. A dash means no qualifying successful retrieval was observed; it does not mean the model has no context support. Window declarations are the measured inventory, not a live catalog.

<div class="ip-performance-table" markdown>

<!-- performance:context -->
| Model | Declared window (tok) | Largest retrieved input (tok) | Occupancy | Input | TTFT (s) | Request cost |
| --- | --- | --- | --- | --- | --- | --- |
| Claude Fable 5.1 | 1,000,000 | — | — | — | — | — |
| Claude Fable 5 | 1,000,000 | — | — | — | — | — |
| Claude Haiku 4.5 | 200,000 | 197,667 | 98.83% | varied | 4.925 | $0.197847 |
| Claude Haiku 5.5 | 1,000,000 | 989,512 | 98.95% | varied | 46.336 | $0.494836 |
| Claude Mythos 5.1 | 1,000,000 | — | — | — | — | — |
| Claude Mythos 5 | 1,000,000 | — | — | — | — | — |
| Claude Opus 4.5 | 200,000 | 197,677 | 98.84% | varied | 9.334 | $0.989110 |
| Claude Opus 4.6 | 1,000,000 | 989,681 | 98.97% | varied | 49.812 | $4.949105 |
| Claude Opus 4.7 | 1,000,000 | 990,229 | 99.02% | varied | 25.796 | $4.951970 |
| Claude Opus 4.8 | 1,000,000 | 990,876 | 99.09% | varied | 22.645 | $4.955130 |
| Claude Opus 5.5 | 1,000,000 | 750,757 | 75.08% | varied | 37.534 | $3.003708 |
| Claude Opus 5 | 1,000,000 | 990,019 | 99.00% | varied | 38.100 | $4.950845 |
| Claude Sonnet 4.5 | 200,000 | 197,674 | 98.84% | varied | 9.167 | $0.593562 |
| Claude Sonnet 4.6 | 1,000,000 | 989,669 | 98.97% | varied | 34.545 | $2.969397 |
| Claude Sonnet 5.5 | 1,000,000 | 991,079 | 99.11% | varied | 34.553 | $1.982548 |
| Claude Sonnet 5 | 1,000,000 | — | — | — | — | — |
| DeepSeek-R1 | 128,000 | 126,419 | 98.76% | varied | 19.302 | $0.172367 |
| DeepSeek-V3.1 | 128,000 | 126,414 | 98.76% | varied | 15.784 | $0.073372 |
| DeepSeek V3.2 | 164,000 | 162,063 | 98.82% | varied | 31.521 | $0.100536 |
| MiniMax M2.1 | 196,000 | 49,130 | 25.07% | varied | 4.255 | $0.015053 |
| MiniMax M2.5 | 196,000 | 193,663 | 98.81% | varied | 15.011 | $0.058406 |
| MiniMax M2 | 1,000,000 | 2,160 | 0.22% | repeat | 4.556 | $0.001306 |
| Kimi K2.5 | 256,000 | 253,110 | 98.87% | varied | 24.959 | $0.151965 |
| Kimi K2 Thinking | 256,000 | 253,111 | 98.87% | varied | 37.385 | $0.152397 |
| Kimi K3 | 1,000,000 | 989,489 | 98.95% | varied | 99.084 | $2.971317 |
| GPT-5.5 | 1,050,000 | 262,596 | 25.01% | repeat | 5.286 | $1.447776 |
| Daybreak Red: GPT-5.6 Cyber | 1,050,000 | — | — | — | — | — |
| GPT-5.6 Luna | 1,050,000 | 787,301 | 74.98% | varied | 8.761 | $0.393722 |
| GPT-5.6 Sol | 1,050,000 | 787,594 | 75.01% | varied | 18.902 | $7.876746 |
| GPT-5.6 Terra | 1,050,000 | 787,307 | 74.98% | varied | 10.717 | $3.937883 |
| GPT-6.1 Sol | 1,000,000 | 749,797 | 74.98% | varied | 14.339 | $3.749388 |
| GPT-6 Astra | 1,050,000 | 787,308 | 74.98% | varied | 27.883 | $19.684865 |
| GPT-6 Luna | 1,050,000 | 787,293 | 74.98% | varied | 6.198 | $0.196882 |
| GPT-6 Sol | 1,050,000 | 787,303 | 74.98% | varied | 17.544 | $3.936903 |
| Daybreak Blue: GPT-5.6 Sol | 1,050,000 | — | — | — | — | — |
| Qwen3 235B A22B 2507 | 256,000 | 253,128 | 98.88% | varied | 54.312 | $0.055721 |
| Qwen3 32B | 32,000 | 23,782 | 74.32% | varied | 1.798 | $0.003592 |
| Qwen3-Coder-30B-A3B-Instruct | 256,000 | 253,128 | 98.88% | varied | 50.616 | $0.037994 |
| Qwen3 Coder 480B A35B Instruct | 256,000 | 253,126 | 98.88% | varied | 42.758 | $0.113977 |
| Qwen3 Coder Next | 256,000 | 253,122 | 98.88% | varied | 7.399 | $0.126609 |
| Qwen3 Next 80B A3B | 256,000 | 253,118 | 98.87% | varied | 8.641 | $0.035476 |
| Qwen3 VL 235B A22B | 256,000 | 253,136 | 98.88% | varied | 30.493 | $0.134263 |
| Grok 4.3 | 1,000,000 | 749,755 | 74.98% | varied | 42.881 | $0.937784 |
| Grok 4.6 | 500,000 | 494,671 | 98.93% | varied | 106.742 | $0.990818 |
| Grok 4.7 | 524,288 | 518,723 | 98.94% | varied | 67.955 | $1.038262 |
| GLM 4.7 Flash | 203,000 | 50,848 | 25.05% | varied | 3.059 | $0.003575 |
| GLM 4.7 | 203,000 | 65,639 | 32.33% | varied | 8.833 | $0.039480 |
| GLM 5.3 | 1,000,000 | 2,161 | 0.22% | repeat | 1.475 | $0.004201 |
| GLM 5 | 200,000 | 197,696 | 98.85% | varied | 23.627 | $0.197782 |
<!-- /performance:context -->

</div>

Each selected row is **n=1**. `varied` uses a varied English-word filler; `repeat` uses repeated ASCII text. Tokenizers and tasks differ across models. For example, Qwen3 Coder Next completed retrieval at **253,122 input tokens (98.88%)**, with **7.399 s TTFT** and **$0.126609** estimated cost. GPT-6.1 Sol's largest qualifying retrieval was **749,797 tokens (74.98%)**, which does not qualify its full declared window.

Full-window costs compare different amounts of work. A roughly 1M-token request and a roughly 256K-token request are not equal workloads. The recorded rates, cache quantities, input/output token counts and selected request IDs are available in the [data download](../assets/data/performance.json).

## Generation: short versus near-full input

The short generation task uses varied input; the near-full task targets approximately 98% occupancy with repeated input to reduce body size. The output cap is **1,024 tokens**. Provider reasoning defaults remain enabled where applicable. Input type and length both change, so ratios cannot isolate a pure context-length effect.

`tok/s` is **reported billed output tokens ÷ whole-request E2E seconds**, including prefill and waiting. It can include hidden reasoning. It is not steady-state decode speed or visible-text throughput. Failed tasks show their failure class instead of a successful throughput score; “Not invoked” is excluded from failure-rate denominators.

<div class="ip-performance-table" markdown>

<!-- performance:generation -->
| Model | Short TTFT (s) | Short tok/s | Short cost/result | Long TTFT (s) | Long tok/s | Long cost/result |
| --- | --- | --- | --- | --- | --- | --- |
| Claude Fable 5.1 | — | — | Not invoked | — | — | Not invoked |
| Claude Fable 5 | — | — | Not invoked | — | — | Not invoked |
| Claude Haiku 4.5 | 1.063 | 110.95 | $0.009327 | 4.455 | 80.96 | $0.200814 |
| Claude Haiku 5.5 | 2.667 | 163.84 | $0.001160 | 48.702 | 20.00 | $0.492348 |
| Claude Mythos 5.1 | — | — | Not invoked | — | — | Not invoked |
| Claude Mythos 5 | — | — | Not invoked | — | — | Not invoked |
| Claude Opus 4.5 | 2.759 | 44.64 | $0.046650 | 8.358 | 43.86 | $1.003975 |
| Claude Opus 4.6 | 2.370 | 46.73 | $0.046660 | 89.241 | 11.11 | $4.923980 |
| Claude Opus 4.7 | — | — | Filtered | 29.495 | 32.45 | $4.923355 |
| Claude Opus 4.8 | — | — | Filtered | 28.478 | 32.85 | $4.923410 |
| Claude Opus 5.5 | 2.610 | 87.79 | $0.046544 | — | — | Upstream error |
| Claude Opus 5 | 2.465 | 63.86 | $0.058050 | — | — | Upstream error |
| Claude Sonnet 4.5 | — | — | Filtered | 8.063 | 42.81 | $0.602445 |
| Claude Sonnet 4.6 | — | — | Filtered | 34.609 | 26.41 | $2.954361 |
| Claude Sonnet 5.5 | 2.434 | 115.36 | $0.023242 | 39.319 | 25.28 | $1.969366 |
| Claude Sonnet 5 | — | — | Not invoked | — | — | Not invoked |
| DeepSeek-R1 | — | — | Output cap | 29.457 | 30.46 | $0.174465 |
| DeepSeek-V3.1 | 6.069 | 22.40 | $0.004162 | 16.645 | 26.63 | $0.074310 |
| DeepSeek V3.2 | 1.011 | 56.12 | $0.004501 | 18.835 | 23.92 | $0.100668 |
| MiniMax M2.1 | 2.499 | 121.80 | $0.002499 | — | — | Output cap |
| MiniMax M2.5 | — | — | Output cap | — | — | Output cap |
| MiniMax M2 | — | — | Output cap | — | — | Context rejected |
| Kimi K2.5 | 0.921 | 74.74 | $0.005602 | 23.958 | 29.27 | $0.153397 |
| Kimi K2 Thinking | — | — | Output cap | — | — | Output cap |
| Kimi K3 | 32.291 | 27.62 | $0.028179 | — | — | Upstream error |
| GPT-5.5 | 1.573 | 83.96 | $0.056881 | — | — | Context rejected |
| Daybreak Red: GPT-5.6 Cyber | — | — | Not invoked | — | — | Not invoked |
| GPT-5.6 Luna | 2.559 | 121.92 | $0.002279 | — | — | Context rejected |
| GPT-5.6 Sol | 1.037 | 80.19 | $0.041478 | — | — | Context rejected |
| GPT-5.6 Terra | 0.875 | 106.37 | $0.022784 | — | — | Context rejected |
| GPT-6.1 Sol | 1.535 | 53.57 | $0.020744 | — | — | Context rejected |
| GPT-6 Astra | 1.663 | 45.59 | $0.103708 | — | — | Context rejected |
| GPT-6 Luna | 0.859 | 172.38 | $0.001037 | — | — | Context rejected |
| GPT-6 Sol | 1.227 | 107.17 | $0.020729 | — | — | Context rejected |
| Daybreak Blue: GPT-5.6 Sol | — | — | Not invoked | — | — | Not invoked |
| Qwen3 235B A22B 2507 | 0.824 | 59.55 | $0.001828 | 51.757 | 12.99 | $0.056030 |
| Qwen3 32B | 0.828 | 112.67 | $0.001247 | 2.363 | 88.68 | $0.005273 |
| Qwen3-Coder-30B-A3B-Instruct | 0.643 | 252.42 | $0.001246 | — | — | Empty output |
| Qwen3 Coder 480B A35B Instruct | 0.743 | 93.52 | $0.003739 | 41.790 | 16.71 | $0.114607 |
| Qwen3 Coder Next | 1.016 | 180.13 | $0.003334 | 6.693 | 85.13 | $0.126524 |
| Qwen3 Next 80B A3B | 1.003 | 79.62 | $0.001819 | 7.539 | 53.19 | $0.036309 |
| Qwen3 VL 235B A22B | 0.872 | 51.07 | $0.004957 | 29.484 | 1.17 | $0.132906 |
| Grok 4.3 | 2.920 | 202.30 | $0.007834 | 72.174 | 13.77 | $1.227128 |
| Grok 4.6 | — | — | Output cap | — | — | Output cap |
| Grok 4.7 | — | — | Output cap | — | — | Interrupted |
| GLM 4.7 Flash | 0.997 | 126.10 | $0.000704 | 19.221 | 23.36 | $0.014317 |
| GLM 4.7 | 0.855 | 68.12 | $0.004775 | 51.429 | 9.30 | $0.121454 |
| GLM 5.3 | — | — | Output cap | — | — | Upstream error |
| GLM 5 | 0.776 | 90.12 | $0.007479 | 25.525 | 0.78 | $0.195793 |
<!-- /performance:generation -->

</div>

For Qwen3 Coder Next, the successful short and long requests delivered **180.13** and **85.13 tok/s**, respectively; long-input TTFT was **6.693 s**. Haiku 5.5 delivered **163.84** and **20.00 tok/s**, with **48.702 s** long-input TTFT. These paired observations show why request completion time, input size and total cost belong beside throughput.

## Task failures by input condition

The denominator is **invoked direct requests in each named condition**, with one request per included model. Non-invoked records are excluded. These intentionally varied workloads must not be pooled into a production gateway error rate. Some failures are provider refusals or expected capacity checks rather than transport failures.

<!-- performance:failure -->
| Task | Calls | Successes | Task failure rate | Failure classes |
| --- | --- | --- | --- | --- |
| 25% varied retrieval | 41 | 38 | 7.3% | Filtered 1; Context rejected 1; Upstream error 1 |
| 75% varied retrieval | 42 | 30 | 28.6% | Body limit 2; Context rejected 3; Empty output 1; Output cap 3; Retrieval mismatch 1; Upstream error 2 |
| 99% varied retrieval | 42 | 26 | 38.1% | Body limit 2; Filtered 1; Context rejected 8; Output cap 3; Retrieval mismatch 2 |
| Short generation | 42 | 31 | 26.2% | Filtered 4; Output cap 7 |
| 98% generation | 42 | 23 | 45.2% | Context rejected 9; Empty output 1; Interrupted 1; Output cap 4; Upstream error 4 |
| 102% limit probe | 38 | 10 | 73.7% | Context rejected 23; Empty output 1; Output cap 1; Retrieval mismatch 1; Timeout 1; Upstream error 1 |
<!-- /performance:failure -->

The 102% condition has **38 calls**; four additional records were not invoked. A request accepted above a shorthand window declaration does not demonstrate a bypass or an unlimited window. K/M declarations, server limits and tokenizer conventions may differ.

HTTP 200 alone is insufficient: content filtering, empty output, output truncation, incomplete streams and incorrect retrieval can still fail the task. Missing usage does not prove a failed request was free. Regional/access failures describe the tested account and region, not global model availability.

## Reading the evidence

| Measure | Definition |
| --- | --- |
| Declared window | Recorded model-card number, converting shorthand K/M with 1,000/1,000,000; original text retained in JSON |
| Occupancy | Observed total input tokens / declared context window; output is not in this numerator |
| TTFT | Time until first user-visible text; absent if none was received |
| E2E | Time until the response stream ends |
| Request cost | Recorded configured-price estimate, including observed cache classes and output |
| Success | Completed generation, or completed retrieval containing all three planted values, under the task's output rules |

The model inventory includes all 49 recorded targets, including those with no successful invocation. The JSON preserves failure classifications and measured rates while omitting raw prompts and responses. Prices and catalog entries are dated observations; model IDs do not attest price, privacy or task quality.

[Back to gateway results](index.md) · [Request-level pricing](../reference/pricing.md) · [Feature support](../getting-started/support-matrix.md)
