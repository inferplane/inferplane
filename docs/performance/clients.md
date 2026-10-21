---
title: Repeated calls and CLI compatibility
description: Repeated model requests, real Claude Code and Codex tasks, and measured client context-window settings.
---

# Repeated calls and CLI compatibility

**Measured October 20–21, 2026 · Claude Code 2.1.304 · Codex 0.163.0.** This page adds repeated observations to the [model performance results](models.md). The [downloadable evidence](../assets/data/client-compatibility.json) separates direct Bedrock requests, live CLI tasks through inferplane, and local protocol fixtures. Search each table by model name.

A **98% input occupancy** means reported input tokens divided by the declared context window is 98%. It is not a 98% success rate. Success/calls counts invoked attempts only; budget-skipped and unsupported-transport records are excluded. The task failure rate uses that denominator. HTTP/stream error rate counts non-200 status, an error event or an incomplete stream. Task failures additionally include incorrect or unfinished retrieval. HTTP 200 alone does not establish task success. A terminal response that reaches its output cap still counts as retrieval success if it returned all three values.

This summary covers published observations only. API rows count direct calls; CLI rows count attempts and task successes, including rejection before a model call. CLI error-request denominators count gateway requests including retries. Known estimated costs are not complete spending totals when any request cost is unknown.

<!-- compatibility:overview -->
| Campaign | Calls / CLI attempts | Task successes | Error requests | Known estimated cost | Unknown-cost requests |
| --- | --- | --- | --- | --- | --- |
| Short input | 245 | 194 | 20 | $1.113606 | 20 |
| Near-full input | 90 | 43 | 26 | $61.009357 | 26 |
| CLI smoke | 270 | 252 | 33/288 requests | $2.159496 | 33 |
| CLI coding | 90 | 79 | 14/367 requests | $2.978807 | 14 |
| CLI long input | 94 | 33 | 32/85 requests | $52.846899 | 32 |
<!-- /compatibility:overview -->

## Repeated short-input retrieval

Each model received the same model-specific synthetic prompt five times, with three planted values near 10%, 50% and 90% of the input. The short prompt uses 2,048 units of varied English filler and a 1,024-token output cap. Units are not tokens. A successful task must finish the stream and return all three values; strict JSON order is not required.

<div class="ip-performance-table" markdown>

<!-- compatibility:repeat-short -->
| Model | Success/calls | HTTP/stream error rate | Task failure rate | TTFT p50 (s) | E2E p50 (s) | p50 billed tok/s | All-call cost | Failures |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Claude Fable 5.1 | 0/5 | 0.0% | 100.0% | — | — | — | $0.169450 | Filtered ×5 |
| Claude Fable 5 | 0/5 | 0.0% | 100.0% | — | — | — | $0.000000 | Filtered ×5 |
| Claude Haiku 4.5 | 5/5 | 0.0% | 0.0% | 0.867 | 1.325 | 27.164 | $0.011690 | — |
| Claude Haiku 5.5 | 5/5 | 0.0% | 0.0% | 2.086 | 2.102 | 63.125 | $0.002034 | — |
| Claude Mythos 5.1 | 0/5 | 100.0% | 100.0% | — | — | — | $0.000000 + ? (5) | Access unavailable ×5 |
| Claude Mythos 5 | 0/5 | 100.0% | 100.0% | — | — | — | $0.000000 + ? (5) | Access unavailable ×5 |
| Claude Opus 4.5 | 5/5 | 0.0% | 0.0% | 2.864 | 3.199 | 10.314 | $0.058125 | — |
| Claude Opus 4.6 | 5/5 | 0.0% | 0.0% | 2.874 | 4.019 | 8.958 | $0.058450 | — |
| Claude Opus 4.7 | 5/5 | 0.0% | 0.0% | 1.543 | 1.556 | 23.143 | $0.089300 | — |
| Claude Opus 4.8 | 0/5 | 0.0% | 100.0% | — | — | — | $0.084280 | Filtered ×5 |
| Claude Opus 5.5 | 5/5 | 0.0% | 0.0% | 1.672 | 1.679 | 20.135 | $0.068900 | — |
| Claude Opus 5 | 5/5 | 0.0% | 0.0% | 1.958 | 2.172 | 15.655 | $0.086425 | — |
| Claude Sonnet 4.5 | 0/5 | 0.0% | 100.0% | — | — | — | $0.032460 | Filtered ×5 |
| Claude Sonnet 4.6 | 0/5 | 0.0% | 100.0% | — | — | — | $0.032355 | Filtered ×5 |
| Claude Sonnet 5.5 | 5/5 | 0.0% | 0.0% | 1.364 | 1.377 | 21.053 | $0.034750 | — |
| Claude Sonnet 5 | 0/5 | 0.0% | 100.0% | — | — | — | $0.033770 | Filtered ×5 |
| DeepSeek-R1 | 5/5 | 0.0% | 0.0% | 2.431 | 2.531 | 138.691 | $0.025080 | — |
| DeepSeek-V3.1 | 5/5 | 0.0% | 0.0% | 0.582 | 0.582 | 48.124 | $0.006495 | — |
| DeepSeek V3.2 | 5/5 | 0.0% | 0.0% | 0.818 | 1.101 | 30.105 | $0.006970 | — |
| MiniMax M2.1 | 5/5 | 0.0% | 0.0% | 1.498 | 1.661 | 99.877 | $0.004322 | — |
| MiniMax M2.5 | 5/5 | 0.0% | 0.0% | 1.650 | 1.808 | 71.922 | $0.004045 | — |
| MiniMax M2 | 5/5 | 0.0% | 0.0% | 3.333 | 3.417 | 116.077 | $0.006095 | — |
| Kimi K2.5 | 5/5 | 0.0% | 0.0% | 0.797 | 0.842 | 39.199 | $0.006995 | — |
| Kimi K2 Thinking | 4/5 | 0.0% | 20.0% | 3.933 | 4.156 | 75.911 | $0.012743 | Output cap ×1 |
| Kimi K3 | 5/5 | 0.0% | 0.0% | 2.670 | 2.835 | 44.093 | $0.043005 | — |
| GPT-5.5 | 5/5 | 0.0% | 0.0% | 1.215 | 1.242 | 62.088 | $0.043426 | — |
| Daybreak Red: GPT-5.6 Cyber | 0/5 | 100.0% | 100.0% | — | — | — | $0.000000 + ? (5) | Access unavailable ×5 |
| GPT-5.6 Luna | 5/5 | 0.0% | 0.0% | 0.676 | 0.769 | 56.348 | $0.000955 | — |
| GPT-5.6 Sol | 5/5 | 0.0% | 0.0% | 0.752 | 0.887 | 32.683 | $0.017083 | — |
| GPT-5.6 Terra | 5/5 | 0.0% | 0.0% | 0.672 | 0.781 | 37.153 | $0.008834 | — |
| GPT-6.1 Sol | 5/5 | 0.0% | 0.0% | 1.141 | 1.415 | 21.910 | $0.007786 | — |
| GPT-6 Astra | 5/5 | 0.0% | 0.0% | 1.115 | 1.397 | 22.192 | $0.043242 | — |
| GPT-6 Luna | 5/5 | 0.0% | 0.0% | 0.603 | 0.691 | 68.715 | $0.000469 | — |
| GPT-6 Sol | 5/5 | 0.0% | 0.0% | 0.757 | 0.863 | 32.446 | $0.008487 | — |
| Daybreak Blue: GPT-5.6 Sol | 0/5 | 100.0% | 100.0% | — | — | — | $0.000000 + ? (5) | Access unavailable ×5 |
| Qwen3 235B A22B 2507 | 5/5 | 0.0% | 0.0% | 0.838 | 1.278 | 32.077 | $0.002550 | — |
| Qwen3 32B | 5/5 | 0.0% | 0.0% | 0.660 | 0.685 | 71.503 | $0.001765 | — |
| Qwen3-Coder-30B-A3B-Instruct | 5/5 | 0.0% | 0.0% | 0.553 | 0.553 | 75.915 | $0.001745 | — |
| Qwen3 Coder 480B A35B Instruct | 5/5 | 0.0% | 0.0% | 0.665 | 0.854 | 46.839 | $0.005210 | — |
| Qwen3 Coder Next | 5/5 | 0.0% | 0.0% | 0.975 | 0.975 | 38.955 | $0.005610 | — |
| Qwen3 Next 80B A3B | 5/5 | 0.0% | 0.0% | 0.765 | 1.002 | 38.939 | $0.001740 | — |
| Qwen3 VL 235B A22B | 5/5 | 0.0% | 0.0% | 0.703 | 1.014 | 36.480 | $0.006190 | — |
| Grok 4.3 | 5/5 | 0.0% | 0.0% | 1.516 | 15.554 | 19.159 | $0.014539 | — |
| Grok 4.6 | 5/5 | 0.0% | 0.0% | 3.235 | 3.337 | 91.330 | $0.018704 | — |
| Grok 4.7 | 5/5 | 0.0% | 0.0% | 3.919 | 4.004 | 73.670 | $0.018412 | — |
| GLM 4.7 Flash | 5/5 | 0.0% | 0.0% | 0.630 | 0.630 | 56.258 | $0.000814 | — |
| GLM 4.7 | 5/5 | 0.0% | 0.0% | 0.809 | 1.013 | 31.577 | $0.006796 | — |
| GLM 5.3 | 5/5 | 0.0% | 0.0% | 0.748 | 0.841 | 155.029 | $0.010215 | — |
| GLM 5 | 5/5 | 0.0% | 0.0% | 0.646 | 0.669 | 52.297 | $0.011295 | — |
<!-- /compatibility:repeat-short -->

</div>

## Repeated near-full input

The long campaign targets 98% of the declared model window using repeated ASCII filler and the same retrieval task. Two rounds cover the 45 models with configured prices. Four unpriced models are not invoked in this campaign. Actual occupancy is shown only when the provider returned usage; a rejection with no usage does not establish its exact token count or maximum supported window.

<div class="ip-performance-table" markdown>

<!-- compatibility:repeat-near-full -->
| Model | Success/calls | HTTP/stream error rate | Task failure rate | Observed input occupancy | TTFT p50 (s) | E2E p50 (s) | p50 billed tok/s | All-call cost | Failures |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Claude Fable 5.1 | 0/2 | 100.0% | 100.0% | — | — | — | — | $0.000000 + ? (2) | Incomplete stream ×1, Upstream error ×1 |
| Claude Fable 5 | 0/2 | 100.0% | 100.0% | — | — | — | — | $0.000000 + ? (2) | Upstream error ×2 |
| Claude Haiku 4.5 | 2/2 | 0.0% | 0.0% | 97.95%–97.95% | 8.265 | 8.847 | 4.394 | $0.392180 | — |
| Claude Haiku 5.5 | 2/2 | 0.0% | 0.0% | 98.00%–98.00% | 44.255 | 44.291 | 0.728 | $0.980116 | — |
| Claude Mythos 5.1 | Not invoked | — | — | — | — | — | — | — | — |
| Claude Mythos 5 | Not invoked | — | — | — | — | — | — | — | — |
| Claude Opus 4.5 | 2/2 | 0.0% | 0.0% | 97.96%–97.96% | 11.556 | 12.414 | 2.670 | $1.960770 | — |
| Claude Opus 4.6 | 2/2 | 0.0% | 0.0% | 97.99%–97.99% | 50.077 | 50.281 | 0.721 | $9.800900 | — |
| Claude Opus 4.7 | 2/2 | 0.0% | 0.0% | 98.00%–98.00% | 29.987 | 30.256 | 3.485 | $9.804885 | — |
| Claude Opus 4.8 | 2/2 | 0.0% | 0.0% | 98.00%–98.00% | 28.124 | 28.237 | 1.168 | $9.801200 | — |
| Claude Opus 5.5 | 0/2 | 100.0% | 100.0% | — | — | — | — | $0.000000 + ? (2) | Upstream error ×2 |
| Claude Opus 5 | 0/2 | 100.0% | 100.0% | — | — | — | — | $0.000000 + ? (2) | Upstream error ×2 |
| Claude Sonnet 4.5 | 2/2 | 0.0% | 0.0% | 97.95%–97.95% | 9.658 | 9.990 | 7.876 | $1.177560 | — |
| Claude Sonnet 4.6 | 2/2 | 0.0% | 0.0% | 97.99%–97.99% | 33.987 | 34.234 | 0.876 | $5.880354 | — |
| Claude Sonnet 5.5 | 2/2 | 0.0% | 0.0% | 98.00%–98.00% | 38.559 | 38.577 | 0.752 | $3.920396 | — |
| Claude Sonnet 5 | 0/2 | 0.0% | 100.0% | 98.00%–98.00% | — | — | — | $3.919816 | Filtered ×2 |
| DeepSeek-R1 | 1/2 | 0.0% | 50.0% | 97.92%–97.92% | 15.496 | 15.777 | 18.128 | $0.345497 | Output cap ×1 |
| DeepSeek-V3.1 | 2/2 | 0.0% | 0.0% | 97.92%–97.92% | 14.521 | 14.721 | 2.105 | $0.145504 | — |
| DeepSeek V3.2 | 0/2 | 0.0% | 100.0% | 97.94%–97.94% | — | — | — | $0.202966 | Output cap ×2 |
| MiniMax M2.1 | 0/2 | 0.0% | 100.0% | 97.96%–97.96% | — | — | — | $0.117664 | Output cap ×2 |
| MiniMax M2.5 | 0/2 | 0.0% | 100.0% | 97.96%–97.96% | — | — | — | $0.117662 | Output cap ×2 |
| MiniMax M2 | 0/2 | 100.0% | 100.0% | — | — | — | — | $0.000000 + ? (2) | Context limit ×2 |
| Kimi K2.5 | 2/2 | 0.0% | 0.0% | 97.97%–97.97% | 27.868 | 29.217 | 1.752 | $0.301257 | — |
| Kimi K2 Thinking | 2/2 | 0.0% | 0.0% | 97.97%–97.97% | 38.478 | 38.893 | 9.459 | $0.302800 | — |
| Kimi K3 | 2/2 | 0.0% | 0.0% | 98.00%–98.00% | 40.095 | 40.855 | 4.592 | $3.245840 | — |
| GPT-5.5 | 0/2 | 100.0% | 100.0% | — | — | — | — | $0.000000 + ? (2) | Context limit ×2 |
| Daybreak Red: GPT-5.6 Cyber | Not invoked | — | — | — | — | — | — | — | — |
| GPT-5.6 Luna | 0/2 | 100.0% | 100.0% | — | — | — | — | $0.000000 + ? (2) | Context limit ×2 |
| GPT-5.6 Sol | 0/2 | 100.0% | 100.0% | — | — | — | — | $0.000000 + ? (2) | Context limit ×2 |
| GPT-5.6 Terra | 0/2 | 100.0% | 100.0% | — | — | — | — | $0.000000 + ? (2) | Context limit ×2 |
| GPT-6.1 Sol | 0/2 | 100.0% | 100.0% | — | — | — | — | $0.000000 + ? (2) | Context limit ×2 |
| GPT-6 Astra | 0/2 | 100.0% | 100.0% | — | — | — | — | $0.000000 + ? (2) | Context limit ×2 |
| GPT-6 Luna | 0/2 | 100.0% | 100.0% | — | — | — | — | $0.000000 + ? (2) | Context limit ×2 |
| GPT-6 Sol | 0/2 | 100.0% | 100.0% | — | — | — | — | $0.000000 + ? (2) | Context limit ×2 |
| Daybreak Blue: GPT-5.6 Sol | Not invoked | — | — | — | — | — | — | — | — |
| Qwen3 235B A22B 2507 | 0/2 | 0.0% | 100.0% | 97.96%–97.96% | — | — | — | $0.112148 | Output cap ×2 |
| Qwen3 32B | 2/2 | 0.0% | 0.0% | 97.72%–97.72% | 2.137 | 2.432 | 16.462 | $0.009428 | — |
| Qwen3-Coder-30B-A3B-Instruct | 2/2 | 0.0% | 0.0% | 97.96%–97.96% | 34.445 | 34.446 | 1.379 | $0.075284 | — |
| Qwen3 Coder 480B A35B Instruct | 2/2 | 0.0% | 0.0% | 97.96%–97.96% | 42.538 | 45.565 | 0.880 | $0.225854 | — |
| Qwen3 Coder Next | 2/2 | 0.0% | 0.0% | 97.96%–97.96% | 6.578 | 6.579 | 5.776 | $0.250876 | — |
| Qwen3 Next 80B A3B | 2/2 | 0.0% | 0.0% | 97.96%–97.96% | 7.599 | 7.636 | 4.848 | $0.070308 | — |
| Qwen3 VL 235B A22B | 0/2 | 0.0% | 100.0% | 97.96%–97.96% | — | — | — | $0.266694 | Retrieval mismatch ×2 |
| Grok 4.3 | 2/2 | 0.0% | 0.0% | 97.99%–97.99% | 68.928 | 69.048 | 3.027 | $2.450847 | — |
| Grok 4.6 | 2/2 | 0.0% | 0.0% | 97.98%–97.98% | 34.696 | 34.819 | 22.060 | $1.227398 | — |
| Grok 4.7 | 2/2 | 0.0% | 0.0% | 97.98%–97.98% | 32.925 | 33.007 | 17.957 | $1.286242 | — |
| GLM 4.7 Flash | 0/2 | 0.0% | 100.0% | 97.95%–97.95% | — | — | — | $0.028656 | Output cap ×2 |
| GLM 4.7 | 0/2 | 0.0% | 100.0% | 97.95%–97.95% | — | — | — | $0.243110 | Output cap ×2 |
| GLM 5.3 | 0/2 | 0.0% | 100.0% | 97.99%–97.99% | — | — | — | $1.953280 | Empty output ×2 |
| GLM 5 | 0/2 | 0.0% | 100.0% | 97.95%–97.95% | — | — | — | $0.391865 | Retrieval mismatch ×2 |
<!-- /compatibility:repeat-near-full -->

</div>

TTFT and E2E are medians of **successful tasks only**. Billed output tok/s is output tokens divided by whole-request time; it includes prefill and may include hidden reasoning. It is neither sustained decode speed nor a score for failed tasks. All-call cost includes unsuccessful attempts with known usage; `+ ? (n)` preserves the number of attempts with unknown cost. It is a configured-price estimate, not an invoice or a complete total when any cost is unknown.

Models run sequentially within each round, with up to three different models in flight. Model order is shuffled between rounds. Identical prompts can benefit from caches: the raw token classes in the download identify observed reads and writes. The short and long campaigns change both input length and filler style; their differences cannot isolate a context-length effect. Two or five repetitions do not establish an SLA. Even 5/5 successes have a Wilson 95% lower bound of approximately 56.6% under independent Bernoulli assumptions; repeated cached calls need not be independent. With five observations, a nearest-rank p95 is simply the maximum, so these tables use medians.

## Actual coding-client tasks

These rows run the installed clients against an isolated inferplane gateway built from commit `a24b6dd`. All tested routes use its **stateless Responses bridge** for Codex and Messages adaptation for Claude Code. They do not qualify every model's native Responses transport. The gateway received the no-tool compaction fix during the campaign; each run records its build stage. Ordinary smoke/tool requests are unchanged by that narrow fix. Coding-client tests interleave with the API campaign; their timings are not a gateway-overhead comparison.

The smoke task requests one exact marker, repeated three times. Its success check requires the marker to be present, not an otherwise word-for-word response. The coding task requires reading a private fixture, fixing a subtraction bug, running its Python unit tests, and returning a file-specific marker. Success requires unchanged test contents, passing tests, an observed tool call, the marker and a successful CLI exit. Each coding-task cell has one run. `—` means no valid live observation, including models without configured prices; it does not mean unsupported.

<div class="ip-performance-table" markdown>

<!-- compatibility:cli -->
| Model | Claude smoke | Claude tools | Codex smoke | Codex tools |
| --- | --- | --- | --- | --- |
| Claude Fable 5.1 | 0/3 | 0/1 | 3/3 | 1/1 |
| Claude Fable 5 | 3/3 | 1/1 | 3/3 | 1/1 |
| Claude Haiku 4.5 | 3/3 | 1/1 | 3/3 | 1/1 |
| Claude Haiku 5.5 | 0/3 | 0/1 | 3/3 | 1/1 |
| Claude Mythos 5.1 | — | — | — | — |
| Claude Mythos 5 | — | — | — | — |
| Claude Opus 4.5 | 0/3 | 0/1 | 3/3 | 1/1 |
| Claude Opus 4.6 | 3/3 | 1/1 | 3/3 | 1/1 |
| Claude Opus 4.7 | 3/3 | 1/1 | 3/3 | 1/1 |
| Claude Opus 4.8 | 3/3 | 1/1 | 3/3 | 1/1 |
| Claude Opus 5.5 | 0/3 | 0/1 | 3/3 | 1/1 |
| Claude Opus 5 | 3/3 | 1/1 | 3/3 | 1/1 |
| Claude Sonnet 4.5 | 3/3 | 1/1 | 3/3 | 1/1 |
| Claude Sonnet 4.6 | 3/3 | 1/1 | 3/3 | 1/1 |
| Claude Sonnet 5.5 | 0/3 | 0/1 | 3/3 | 1/1 |
| Claude Sonnet 5 | 3/3 | 1/1 | 3/3 | 1/1 |
| DeepSeek-R1 | 3/3 | 0/1 | 0/3 | 0/1 |
| DeepSeek-V3.1 | 3/3 | 0/1 | 3/3 | 1/1 |
| DeepSeek V3.2 | 3/3 | 1/1 | 3/3 | 1/1 |
| MiniMax M2.1 | 3/3 | 1/1 | 3/3 | 1/1 |
| MiniMax M2.5 | 3/3 | 1/1 | 3/3 | 1/1 |
| MiniMax M2 | 3/3 | 1/1 | 3/3 | 1/1 |
| Kimi K2.5 | 3/3 | 1/1 | 3/3 | 1/1 |
| Kimi K2 Thinking | 3/3 | 1/1 | 3/3 | 1/1 |
| Kimi K3 | 3/3 | 1/1 | 3/3 | 1/1 |
| GPT-5.5 | 3/3 | 1/1 | 3/3 | 1/1 |
| Daybreak Red: GPT-5.6 Cyber | — | — | — | — |
| GPT-5.6 Luna | 3/3 | 1/1 | 3/3 | 1/1 |
| GPT-5.6 Sol | 3/3 | 1/1 | 3/3 | 1/1 |
| GPT-5.6 Terra | 3/3 | 1/1 | 3/3 | 1/1 |
| GPT-6.1 Sol | 3/3 | 1/1 | 3/3 | 1/1 |
| GPT-6 Astra | 3/3 | 1/1 | 3/3 | 1/1 |
| GPT-6 Luna | 3/3 | 1/1 | 3/3 | 1/1 |
| GPT-6 Sol | 3/3 | 1/1 | 3/3 | 1/1 |
| Daybreak Blue: GPT-5.6 Sol | — | — | — | — |
| Qwen3 235B A22B 2507 | 3/3 | 1/1 | 3/3 | 1/1 |
| Qwen3 32B | 3/3 | 0/1 | 3/3 | 0/1 |
| Qwen3-Coder-30B-A3B-Instruct | 3/3 | 1/1 | 3/3 | 1/1 |
| Qwen3 Coder 480B A35B Instruct | 3/3 | 1/1 | 3/3 | 1/1 |
| Qwen3 Coder Next | 3/3 | 1/1 | 3/3 | 0/1 |
| Qwen3 Next 80B A3B | 3/3 | 1/1 | 3/3 | 1/1 |
| Qwen3 VL 235B A22B | 3/3 | 1/1 | 3/3 | 1/1 |
| Grok 4.3 | 3/3 | 1/1 | 3/3 | 1/1 |
| Grok 4.6 | 3/3 | 1/1 | 3/3 | 1/1 |
| Grok 4.7 | 3/3 | 1/1 | 3/3 | 1/1 |
| GLM 4.7 Flash | 3/3 | 1/1 | 3/3 | 1/1 |
| GLM 4.7 | 3/3 | 1/1 | 3/3 | 1/1 |
| GLM 5.3 | 3/3 | 1/1 | 3/3 | 1/1 |
| GLM 5 | 3/3 | 1/1 | 3/3 | 1/1 |
<!-- /compatibility:cli -->

</div>

Both clients use isolated homes and no inherited user configuration. Claude Code has a 1,024-token output cap; its default task gets no tools, while the coding task permits Read, Edit and the test command. Codex uses the inferplane launcher with reasoning disabled for the stateless bridge. Retries remain visible as separate request IDs and costs in the download. A local per-run call guard and budget guard can stop further attempts; a guarded run does not demonstrate an upstream incompatibility. Only trials with zero model requests during a documented test-host availability failure are excluded from model compatibility denominators and retained in private evidence. Invoked failures remain in the results. Gateway request counts include admission rejections and do not imply every request reached a billed provider invocation.

The next table counts every invoked gateway request, including retries and failed tasks. Smoke latency is a successful-run median; coding latency is the single successful coding run, if any. Costs sum both task types and therefore are workload totals, not per-call prices. Guard refusals are local experiment limits and are not provider errors.

<div class="ip-performance-table" markdown>

<!-- compatibility:cli-metrics -->
| Model | CLI | Requests | Smoke p50 (s) | Tools (s) | All-call cost | Guard refusals | Task failures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Claude Fable 5.1 | claude | 8 | — | — | $0.000000 + ? (8) | 0 | Upstream error ×4 |
| Claude Fable 5.1 | codex | 6 | 3.338 | 12.062 | $0.527970 | 0 | — |
| Claude Fable 5 | claude | 11 | 2.583 | 15.490 | $0.079102 + ? (4) | 0 | — |
| Claude Fable 5 | codex | 6 | 2.504 | 9.449 | $0.524790 | 0 | — |
| Claude Haiku 4.5 | claude | 7 | 1.195 | 7.981 | $0.012788 | 0 | — |
| Claude Haiku 4.5 | codex | 9 | 1.724 | 12.259 | $0.064597 | 0 | — |
| Claude Haiku 5.5 | claude | 8 | — | — | $0.000000 + ? (8) | 0 | Upstream error ×4 |
| Claude Haiku 5.5 | codex | 6 | 2.078 | 7.603 | $0.005442 | 0 | — |
| Claude Opus 4.5 | claude | 4 | — | — | $0.000000 + ? (4) | 0 | Upstream error ×4 |
| Claude Opus 4.5 | codex | 7 | 3.551 | 12.661 | $0.236240 | 0 | — |
| Claude Opus 4.6 | claude | 7 | 1.847 | 15.951 | $0.060670 | 0 | — |
| Claude Opus 4.6 | codex | 8 | 2.206 | 17.064 | $0.274620 | 0 | — |
| Claude Opus 4.7 | claude | 7 | 2.843 | 14.340 | $0.074375 | 0 | — |
| Claude Opus 4.7 | codex | 6 | 2.871 | 10.596 | $0.274650 | 0 | — |
| Claude Opus 4.8 | claude | 7 | 1.330 | 9.669 | $0.039235 | 0 | — |
| Claude Opus 4.8 | codex | 6 | 1.581 | 13.445 | $0.264440 | 0 | — |
| Claude Opus 5.5 | claude | 8 | — | — | $0.000000 + ? (8) | 0 | Upstream error ×4 |
| Claude Opus 5.5 | codex | 6 | 1.794 | 11.629 | $0.210404 | 0 | — |
| Claude Opus 5 | claude | 7 | 1.541 | 30.700 | $0.040556 | 0 | — |
| Claude Opus 5 | codex | 6 | 1.710 | 21.154 | $0.264865 | 0 | — |
| Claude Sonnet 4.5 | claude | 7 | 1.904 | 56.489 | $0.022503 | 0 | — |
| Claude Sonnet 4.5 | codex | 7 | 2.206 | 11.242 | $0.147219 | 0 | — |
| Claude Sonnet 4.6 | claude | 7 | 1.195 | 7.205 | $0.022410 | 0 | — |
| Claude Sonnet 4.6 | codex | 6 | 1.408 | 6.757 | $0.125472 | 0 | — |
| Claude Sonnet 5.5 | claude | 8 | — | — | $0.000000 + ? (8) | 0 | Upstream error ×4 |
| Claude Sonnet 5.5 | codex | 6 | 1.773 | 5.356 | $0.105020 | 0 | — |
| Claude Sonnet 5 | claude | 7 | 2.140 | 9.664 | $0.012521 | 0 | — |
| Claude Sonnet 5 | codex | 6 | 2.100 | 8.136 | $0.106724 | 0 | — |
| DeepSeek-R1 | claude | 4 | 2.736 | — | $0.003812 + ? (1) | 0 | Upstream error ×1 |
| DeepSeek-R1 | codex | 4 | — | — | $0.000000 + ? (4) | 0 | Upstream error ×4 |
| DeepSeek-V3.1 | claude | 9 | 3.433 | — | $0.005714 | 0 | Coding task failed ×1 |
| DeepSeek-V3.1 | codex | 11 | 2.501 | 8.161 | $0.037390 | 0 | — |
| DeepSeek V3.2 | claude | 10 | 0.857 | 12.858 | $0.009387 | 0 | — |
| DeepSeek V3.2 | codex | 13 | 1.099 | 22.984 | $0.055156 | 0 | — |
| MiniMax M2.1 | claude | 7 | 0.938 | 6.789 | $0.002729 | 0 | — |
| MiniMax M2.1 | codex | 7 | 1.303 | 7.055 | $0.012698 | 0 | — |
| MiniMax M2.5 | claude | 7 | 1.441 | 8.916 | $0.002955 | 0 | — |
| MiniMax M2.5 | codex | 7 | 1.371 | 6.651 | $0.012597 | 0 | — |
| MiniMax M2 | claude | 7 | 1.388 | 9.234 | $0.003265 | 0 | — |
| MiniMax M2 | codex | 7 | 1.458 | 8.633 | $0.013099 | 0 | — |
| Kimi K2.5 | claude | 7 | 1.041 | 10.668 | $0.004097 | 0 | — |
| Kimi K2.5 | codex | 7 | 1.111 | 10.352 | $0.020680 | 0 | — |
| Kimi K2 Thinking | claude | 11 | 1.226 | 11.146 | $0.009076 | 0 | — |
| Kimi K2 Thinking | codex | 7 | 1.399 | 8.001 | $0.023953 | 0 | — |
| Kimi K3 | claude | 7 | 2.330 | 9.159 | $0.031215 | 0 | — |
| Kimi K3 | codex | 6 | 4.061 | 10.116 | $0.081113 | 0 | — |
| GPT-5.5 | claude | 7 | 1.067 | 13.208 | $0.040338 | 0 | — |
| GPT-5.5 | codex | 6 | 1.378 | 7.991 | $0.131447 | 0 | — |
| GPT-5.6 Luna | claude | 7 | 0.759 | 4.102 | $0.001435 | 0 | — |
| GPT-5.6 Luna | codex | 6 | 1.120 | 3.444 | $0.004809 | 0 | — |
| GPT-5.6 Sol | claude | 7 | 0.965 | 5.194 | $0.029923 | 0 | — |
| GPT-5.6 Sol | codex | 6 | 1.079 | 5.103 | $0.097938 | 0 | — |
| GPT-5.6 Terra | claude | 7 | 0.849 | 4.155 | $0.015256 | 0 | — |
| GPT-5.6 Terra | codex | 6 | 1.081 | 4.894 | $0.049038 | 0 | — |
| GPT-6.1 Sol | claude | 8 | 2.405 | 7.738 | $0.015040 | 0 | — |
| GPT-6.1 Sol | codex | 6 | 1.292 | 5.865 | $0.047303 | 0 | — |
| GPT-6 Astra | claude | 9 | 1.111 | 18.614 | $0.096333 | 0 | — |
| GPT-6 Astra | codex | 6 | 1.349 | 5.062 | $0.238154 | 0 | — |
| GPT-6 Luna | claude | 7 | 0.798 | 3.619 | $0.000742 | 0 | — |
| GPT-6 Luna | codex | 6 | 0.857 | 3.054 | $0.002406 | 0 | — |
| GPT-6 Sol | claude | 7 | 0.873 | 4.166 | $0.014447 | 0 | — |
| GPT-6 Sol | codex | 6 | 1.172 | 3.893 | $0.047241 | 0 | — |
| Qwen3 235B A22B 2507 | claude | 7 | 1.005 | 3.935 | $0.001519 | 0 | — |
| Qwen3 235B A22B 2507 | codex | 13 | 1.088 | 17.543 | $0.019039 | 0 | — |
| Qwen3 32B | claude | 10 | 0.699 | — | $0.002364 | 0 | Coding task failed ×1 |
| Qwen3 32B | codex | 5 | 1.071 | — | $0.003492 + ? (1) | 0 | Upstream error ×1 |
| Qwen3-Coder-30B-A3B-Instruct | claude | 9 | 0.601 | 3.980 | $0.002105 | 0 | — |
| Qwen3-Coder-30B-A3B-Instruct | codex | 11 | 0.866 | 6.341 | $0.011398 | 0 | — |
| Qwen3 Coder 480B A35B Instruct | claude | 9 | 0.688 | 6.542 | $0.006274 | 0 | — |
| Qwen3 Coder 480B A35B Instruct | codex | 11 | 1.091 | 7.521 | $0.033902 | 0 | — |
| Qwen3 Coder Next | claude | 7 | 0.982 | 4.403 | $0.004455 | 0 | — |
| Qwen3 Coder Next | codex | 4 | 0.862 | — | $0.009293 + ? (1) | 0 | Upstream error ×1 |
| Qwen3 Next 80B A3B | claude | 7 | 0.661 | 6.348 | $0.001367 | 0 | — |
| Qwen3 Next 80B A3B | codex | 7 | 0.942 | 4.418 | $0.006056 | 0 | — |
| Qwen3 VL 235B A22B | claude | 7 | 0.853 | 14.071 | $0.004949 | 0 | — |
| Qwen3 VL 235B A22B | codex | 9 | 1.844 | 34.538 | $0.032242 | 0 | — |
| Grok 4.3 | claude | 7 | 9.697 | 37.188 | $0.008588 | 0 | — |
| Grok 4.3 | codex | 9 | 14.156 | 48.700 | $0.057619 | 0 | — |
| Grok 4.6 | claude | 7 | 4.468 | 7.265 | $0.010922 | 0 | — |
| Grok 4.6 | codex | 9 | 3.429 | 5.501 | $0.063178 | 0 | — |
| Grok 4.7 | claude | 7 | 1.457 | 7.526 | $0.015826 | 0 | — |
| Grok 4.7 | codex | 6 | 1.749 | 6.656 | $0.053482 | 0 | — |
| GLM 4.7 Flash | claude | 7 | 0.732 | 3.481 | $0.000596 | 0 | — |
| GLM 4.7 Flash | codex | 7 | 0.962 | 4.164 | $0.002927 | 0 | — |
| GLM 4.7 | claude | 7 | 0.798 | 5.388 | $0.004770 | 0 | — |
| GLM 4.7 | codex | 7 | 1.109 | 5.430 | $0.024750 | 0 | — |
| GLM 5.3 | claude | 7 | 0.854 | 6.239 | $0.008525 | 0 | — |
| GLM 5.3 | codex | 6 | 1.026 | 3.063 | $0.043050 | 0 | — |
| GLM 5 | claude | 7 | 0.948 | 6.523 | $0.007518 | 0 | — |
| GLM 5 | codex | 7 | 1.581 | 5.191 | $0.040698 | 0 | — |
<!-- /compatibility:cli-metrics -->

</div>

## What the clients think the context window is

The following **294 local fixture checks** exercise each installed CLI, with no paid model invocation. A fixture returning a valid response proves client configuration and request formatting, not that a real model accepts that amount of input. Values are the window reported by the client after the request.

<div class="ip-performance-table" markdown>

<!-- compatibility:windows -->
| Model | Declared | Claude default | Claude [1m] | Claude MAX env | Codex default | Codex explicit | icodex |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Claude Fable 5.1 | 1,000,000 | 1,000,000 | 1,000,000 | 1,000,000 | 258,400 | 258,400 | 800,000 |
| Claude Fable 5 | 1,000,000 | 1,000,000 | 1,000,000 | 1,000,000 | 258,400 | 258,400 | 800,000 |
| Claude Haiku 4.5 | 200,000 | 200,000 | 1,000,000 | 200,000 | 258,400 | 190,000 | 160,000 |
| Claude Haiku 5.5 | 1,000,000 | 1,000,000 | 1,000,000 | 1,000,000 | 258,400 | 258,400 | 800,000 |
| Claude Mythos 5.1 | 1,000,000 | 1,000,000 | 1,000,000 | 1,000,000 | 258,400 | 258,400 | 800,000 |
| Claude Mythos 5 | 1,000,000 | 1,000,000 | 1,000,000 | 1,000,000 | 258,400 | 258,400 | 800,000 |
| Claude Opus 4.5 | 200,000 | 200,000 | 1,000,000 | 200,000 | 258,400 | 190,000 | 160,000 |
| Claude Opus 4.6 | 1,000,000 | 200,000 | 1,000,000 | 200,000 | 258,400 | 258,400 | 800,000 |
| Claude Opus 4.7 | 1,000,000 | 1,000,000 | 1,000,000 | 1,000,000 | 258,400 | 258,400 | 800,000 |
| Claude Opus 4.8 | 1,000,000 | 1,000,000 | 1,000,000 | 1,000,000 | 258,400 | 258,400 | 800,000 |
| Claude Opus 5.5 | 1,000,000 | 1,000,000 | 1,000,000 | 1,000,000 | 258,400 | 258,400 | 800,000 |
| Claude Opus 5 | 1,000,000 | 1,000,000 | 1,000,000 | 1,000,000 | 258,400 | 258,400 | 800,000 |
| Claude Sonnet 4.5 | 200,000 | 200,000 | 1,000,000 | 200,000 | 258,400 | 190,000 | 160,000 |
| Claude Sonnet 4.6 | 1,000,000 | 200,000 | 1,000,000 | 200,000 | 258,400 | 258,400 | 800,000 |
| Claude Sonnet 5.5 | 1,000,000 | 1,000,000 | 1,000,000 | 1,000,000 | 258,400 | 258,400 | 800,000 |
| Claude Sonnet 5 | 1,000,000 | 1,000,000 | 1,000,000 | 1,000,000 | 258,400 | 258,400 | 800,000 |
| DeepSeek-R1 | 128,000 | 200,000 | 1,000,000 | 128,000 | 258,400 | 121,600 | 102,400 |
| DeepSeek-V3.1 | 128,000 | 200,000 | 1,000,000 | 128,000 | 258,400 | 121,600 | 102,400 |
| DeepSeek V3.2 | 164,000 | 200,000 | 1,000,000 | 164,000 | 258,400 | 155,800 | 131,200 |
| MiniMax M2.1 | 196,000 | 200,000 | 1,000,000 | 196,000 | 258,400 | 186,200 | 156,800 |
| MiniMax M2.5 | 196,000 | 200,000 | 1,000,000 | 196,000 | 258,400 | 186,200 | 156,800 |
| MiniMax M2 | 1,000,000 | 200,000 | 1,000,000 | 1,000,000 | 258,400 | 258,400 | 800,000 |
| Kimi K2.5 | 256,000 | 200,000 | 1,000,000 | 256,000 | 258,400 | 243,200 | 204,800 |
| Kimi K2 Thinking | 256,000 | 200,000 | 1,000,000 | 256,000 | 258,400 | 243,200 | 204,800 |
| Kimi K3 | 1,000,000 | 200,000 | 1,000,000 | 1,000,000 | 258,400 | 258,400 | 800,000 |
| GPT-5.5 | 1,050,000 | 200,000 | 1,000,000 | 1,050,000 | 258,400 | 258,400 | 840,000 |
| Daybreak Red: GPT-5.6 Cyber | 1,050,000 | 200,000 | 1,000,000 | 1,050,000 | 258,400 | 258,400 | 840,000 |
| GPT-5.6 Luna | 1,050,000 | 200,000 | 1,000,000 | 1,050,000 | 258,400 | 258,400 | 840,000 |
| GPT-5.6 Sol | 1,050,000 | 200,000 | 1,000,000 | 1,050,000 | 258,400 | 258,400 | 840,000 |
| GPT-5.6 Terra | 1,050,000 | 200,000 | 1,000,000 | 1,050,000 | 258,400 | 258,400 | 840,000 |
| GPT-6.1 Sol | 1,000,000 | 200,000 | 1,000,000 | 1,000,000 | 258,400 | 258,400 | 800,000 |
| GPT-6 Astra | 1,050,000 | 200,000 | 1,000,000 | 1,050,000 | 258,400 | 258,400 | 840,000 |
| GPT-6 Luna | 1,050,000 | 200,000 | 1,000,000 | 1,050,000 | 258,400 | 258,400 | 840,000 |
| GPT-6 Sol | 1,050,000 | 200,000 | 1,000,000 | 1,050,000 | 258,400 | 258,400 | 840,000 |
| Daybreak Blue: GPT-5.6 Sol | 1,050,000 | 200,000 | 1,000,000 | 1,050,000 | 258,400 | 258,400 | 840,000 |
| Qwen3 235B A22B 2507 | 256,000 | 200,000 | 1,000,000 | 256,000 | 258,400 | 243,200 | 204,800 |
| Qwen3 32B | 32,000 | 200,000 | 1,000,000 | 32,000 | 258,400 | 30,400 | 25,600 |
| Qwen3-Coder-30B-A3B-Instruct | 256,000 | 200,000 | 1,000,000 | 256,000 | 258,400 | 243,200 | 204,800 |
| Qwen3 Coder 480B A35B Instruct | 256,000 | 200,000 | 1,000,000 | 256,000 | 258,400 | 243,200 | 204,800 |
| Qwen3 Coder Next | 256,000 | 200,000 | 1,000,000 | 256,000 | 258,400 | 243,200 | 204,800 |
| Qwen3 Next 80B A3B | 256,000 | 200,000 | 1,000,000 | 256,000 | 258,400 | 243,200 | 204,800 |
| Qwen3 VL 235B A22B | 256,000 | 200,000 | 1,000,000 | 256,000 | 258,400 | 243,200 | 204,800 |
| Grok 4.3 | 1,000,000 | 200,000 | 1,000,000 | 1,000,000 | 258,400 | 258,400 | 800,000 |
| Grok 4.6 | 500,000 | 200,000 | 1,000,000 | 500,000 | 258,400 | 258,400 | 400,000 |
| Grok 4.7 | 524,288 | 200,000 | 1,000,000 | 524,288 | 258,400 | 258,400 | 419,430 |
| GLM 4.7 Flash | 203,000 | 200,000 | 1,000,000 | 203,000 | 258,400 | 192,850 | 162,400 |
| GLM 4.7 | 203,000 | 200,000 | 1,000,000 | 203,000 | 258,400 | 192,850 | 162,400 |
| GLM 5.3 | 1,000,000 | 200,000 | 1,000,000 | 1,000,000 | 258,400 | 258,400 | 800,000 |
| GLM 5 | 200,000 | 200,000 | 1,000,000 | 200,000 | 258,400 | 190,000 | 160,000 |
<!-- /compatibility:windows -->

</div>

For Qwen3 Coder Next, Claude Code reports **200,000** by default, **1,000,000** with `[1m]`, and **256,000** with `CLAUDE_CODE_MAX_CONTEXT_TOKENS=256000`. The suffix is removed from the outgoing model ID and adds the `context-1m-2025-08-07` beta header. It does not increase a 256K backend window.

Claude Code also supports an explicit context environment setting; `[1m]` is not its only mechanism. The [official model configuration documentation](https://code.claude.com/docs/en/model-config) makes its precedence conditional:

- For an unrecognized provider model without `[1m]`, `CLAUDE_CODE_MAX_CONTEXT_TOKENS` sets the assumed window while keeping proactive compaction.
- For such a model with `[1m]`, the suffix wins unless `CLAUDE_CODE_DISABLE_1M_CONTEXT=1` is also set.
- Recognized Claude model names have different handling: applying an explicit override may require `DISABLE_COMPACT`, which disables compaction. The fixture matrix above, rather than a provider-name guess, records the behavior of the exact tested IDs.

Codex supports `model_context_window`, `model_auto_compact_token_limit` and `model_catalog_json`; see its [configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference). For the same Qwen model, the unconfigured custom-provider fallback reports **258,400**, explicit 256,000 reports **243,200** after the client's 95% effective-window calculation, and the tested inferplane launcher reports **204,800** from its conservative 80% catalog policy. The latter is usable client context, not a smaller backend declaration. Local context profiles and inherited global overrides can change these values; the fixture uses clean configuration.

## Long input through the actual CLIs

Each row below is an actual installed-client synthetic retrieval attempt, not a fixture. Client rejection before any model request is retained, with no observed input-token count. Claude Code uses `[1m]` and targets about 98% of the declared backend window, allowing for its prompt overhead. Codex targets about 98% of the launcher's 80% effective window, with an additional prompt allowance. Therefore its target is approximately 78.4% of the backend declaration. Actual provider-reported input is shown; these workloads are deliberately different and must not be ranked as equal work. CLI wall time includes startup, retries and shutdown. Requests and guard refusals are shown separately; a timeout with guard refusals is not attributable solely to the provider.

The initial budget guard stopped three long-input trials before any model request. Later funded attempts are separate rows: Kimi K3 retrieved all values at 979,261 input tokens; Sonnet 5.5 returned an upstream error and then timed out with further requests blocked by the experiment guard. Claude Fable 5.1 still has no paid long-input CLI observation. These gaps do not establish model incompatibility.

<div class="ip-performance-table" markdown>

<!-- compatibility:long-cli -->
| Model | CLI | Variant | Run | Input tokens | Requests | Guard refusals | CLI wall (s) | Success | Cost |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Claude Haiku 5.5 | codex | default | 1 | — | 0 | 0 | 0.546 | ✗ (CLI text length limit) | $0.000000 |
| GPT-5.5 | claude | suffix_1m | 1 | — | 2 | 0 | 3.943 | ✗ (Upstream error) | $0.000000 + ? (2) |
| Claude Opus 5.5 | claude | suffix_1m | 1 | — | 2 | 0 | 8.922 | ✗ (Upstream error) | $0.000000 + ? (2) |
| Claude Opus 4.6 | claude | suffix_1m | 1 | 979,171 | 1 | 0 | 47.856 | ✓ | $6.120740 |
| Grok 4.3 | codex | default | 1 | — | 0 | 0 | 0.548 | ✗ (CLI text length limit) | $0.000000 |
| GPT-5.6 Terra | claude | suffix_1m | 1 | — | 2 | 0 | 3.171 | ✗ (Upstream error) | $0.000000 + ? (2) |
| GPT-6 Astra | codex | default | 1 | — | 0 | 0 | 0.549 | ✗ (CLI text length limit) | $0.000000 |
| Qwen3 Next 80B A3B | codex | default | 1 | 202,130 | 1 | 0 | 6.684 | ✓ | $0.028352 |
| GPT-5.6 Luna | claude | suffix_1m | 1 | — | 2 | 0 | 2.585 | ✗ (Upstream error) | $0.000000 + ? (2) |
| Claude Haiku 4.5 | codex | default | 1 | 158,851 | 1 | 0 | 4.766 | ✓ | $0.159041 |
| Claude Fable 5 | claude | suffix_1m | 1 | — | 1 | 11 | 176.356 | ✗ (Upstream error) | $0.000000 + ? (1) |
| Claude Sonnet 4.6 | codex | default | 1 | — | 0 | 0 | 0.548 | ✗ (CLI text length limit) | $0.000000 |
| GLM 4.7 Flash | claude | suffix_1m | 1 | 198,119, 199,034 | 2 | 9 | 180.080 | ✗ (Timeout) | $0.028620 |
| GLM 5.3 | claude | suffix_1m | 1 | — | 1 | 0 | 180.494 | ✗ (Timeout) | $0.000000 + ? (1) |
| Claude Opus 4.7 | codex | default | 1 | — | 0 | 0 | 0.548 | ✗ (CLI text length limit) | $0.000000 |
| Kimi K2.5 | codex | default | 1 | 200,956 | 1 | 0 | 24.014 | ✓ | $0.120673 |
| Claude Opus 4.8 | claude | suffix_1m | 1 | 979,227 | 1 | 0 | 28.828 | ✓ | $6.121391 |
| Claude Opus 4.7 | claude | suffix_1m | 1 | 979,229 | 1 | 0 | 29.834 | ✓ | $6.121374 |
| Qwen3 Coder Next | claude | suffix_1m | 1 | 250,087 | 1 | 0 | 7.297 | ✓ | $0.125087 |
| Qwen3 VL 235B A22B | claude | suffix_1m | 1 | 250,085 | 1 | 0 | 26.811 | ✗ (Retrieval mismatch) | $0.132625 |
| Grok 4.7 | claude | suffix_1m | 1 | 513,009 | 1 | 0 | 56.854 | ✓ | $1.026774 |
| MiniMax M2 | claude | suffix_1m | 1 | — | 1 | 0 | 3.802 | ✗ (Upstream error) | $0.000000 + ? (1) |
| Kimi K3 | codex | default | 1 | — | 0 | 0 | 0.548 | ✗ (CLI text length limit) | $0.000000 |
| Qwen3 235B A22B 2507 | claude | suffix_1m | 1 | 250,091, 251,164 | 2 | 7 | 180.260 | ✗ (Timeout) | $0.112078 |
| Kimi K2 Thinking | claude | suffix_1m | 1 | 250,062 | 1 | 0 | 34.350 | ✓ | $0.150595 |
| Qwen3 Coder 480B A35B Instruct | codex | default | 1 | 202,579 | 1 | 0 | 30.215 | ✓ | $0.091242 |
| Claude Fable 5 | codex | default | 1 | — | 0 | 0 | 0.551 | ✗ (CLI text length limit) | $0.000000 |
| Claude Opus 5 | codex | default | 1 | — | 0 | 0 | 0.546 | ✗ (CLI text length limit) | $0.000000 |
| DeepSeek V3.2 | claude | suffix_1m | 1 | 159,898, 160,955 | 2 | 0 | 142.699 | ✗ (Retrieval mismatch) | $0.201472 |
| GLM 4.7 Flash | codex | default | 1 | 160,462 | 1 | 0 | 60.369 | ✗ (Incomplete stream) | $0.000000 + ? (1) |
| Claude Opus 4.5 | claude | suffix_1m | 1 | — | 1 | 0 | 2.353 | ✗ (Upstream error) | $0.000000 + ? (1) |
| Qwen3 Coder 480B A35B Instruct | claude | suffix_1m | 1 | — | 1 | 0 | 1.792 | ✗ (Upstream error) | $0.000000 + ? (1) |
| GPT-6 Astra | codex | default | 2 | 303,936 | 1 | 0 | 10.690 | ✓ | $7.600265 |
| Qwen3-Coder-30B-A3B-Instruct | codex | default | 1 | 202,578 | 1 | 0 | 14.702 | ✓ | $0.030413 |
| Claude Sonnet 5.5 | codex | default | 1 | — | 0 | 0 | 0.547 | ✗ (CLI text length limit) | $0.000000 |
| Claude Sonnet 5.5 | claude | suffix_1m | 2 | — | 1 | 11 | 180.492 | ✗ (Timeout) | $0.000000 + ? (1) |
| DeepSeek V3.2 | codex | default | 1 | 130,066 | 1 | 0 | 67.756 | ✗ (Incomplete stream) | $0.000000 + ? (1) |
| Kimi K2.5 | claude | suffix_1m | 1 | 250,065 | 1 | 0 | 31.991 | ✓ | $0.150126 |
| GPT-5.6 Sol | codex | default | 1 | — | 0 | 0 | 0.554 | ✗ (CLI text length limit) | $0.000000 |
| GLM 5.3 | codex | default | 1 | — | 0 | 0 | 0.548 | ✗ (CLI text length limit) | $0.000000 |
| GPT-6 Sol | codex | default | 1 | — | 0 | 0 | 0.549 | ✗ (CLI text length limit) | $0.000000 |
| Qwen3 Next 80B A3B | claude | suffix_1m | 1 | 250,091 | 1 | 0 | 7.800 | ✓ | $0.035056 |
| MiniMax M2.1 | claude | suffix_1m | 1 | 191,267, 191,306 | 2 | 10 | 180.397 | ✗ (Timeout) | $0.117230 |
| Claude Haiku 4.5 | claude | suffix_1m | 1 | 195,169 | 1 | 0 | 5.323 | ✗ (Retrieval mismatch) | $0.244230 |
| Claude Opus 5.5 | codex | default | 1 | — | 0 | 0 | 0.548 | ✗ (CLI text length limit) | $0.000000 |
| GPT-6.1 Sol | codex | default | 1 | — | 0 | 0 | 0.548 | ✗ (CLI text length limit) | $0.000000 |
| Qwen3-Coder-30B-A3B-Instruct | claude | suffix_1m | 1 | 250,090 | 1 | 0 | 49.341 | ✓ | $0.037541 |
| Kimi K2 Thinking | codex | default | 1 | 201,392 | 1 | 0 | 23.266 | ✗ (Retrieval mismatch) | $0.120920 |
| Qwen3 VL 235B A22B | codex | default | 1 | 202,131 | 1 | 0 | 117.306 | ✗ (Incomplete stream) | $0.000000 + ? (1) |
| GPT-5.5 | codex | default | 1 | — | 0 | 0 | 0.556 | ✗ (CLI text length limit) | $0.000000 |
| GPT-6 Luna | codex | default | 1 | — | 0 | 0 | 0.552 | ✗ (CLI text length limit) | $0.000000 |
| GLM 4.7 | claude | suffix_1m | 1 | 198,119, 199,185 | 2 | 8 | 180.097 | ✗ (Timeout) | $0.242888 |
| GPT-6 Astra | codex | default | 1 | 303,942 | 1 | 0 | 10.253 | ✓ | $7.600565 |
| Claude Haiku 5.5 | claude | suffix_1m | 1 | — | 2 | 0 | 5.083 | ✗ (Upstream error) | $0.000000 + ? (2) |
| Claude Sonnet 4.5 | codex | default | 1 | 158,846 | 1 | 0 | 8.126 | ✓ | $0.477123 |
| GPT-6 Sol | claude | suffix_1m | 1 | — | 2 | 0 | 3.234 | ✗ (Upstream error) | $0.000000 + ? (2) |
| Claude Sonnet 4.5 | claude | suffix_1m | 1 | 195,171 | 1 | 0 | 8.812 | ✗ (Retrieval mismatch) | $0.733149 |
| GPT-6.1 Sol | claude | suffix_1m | 1 | — | 2 | 0 | 5.033 | ✗ (Upstream error) | $0.000000 + ? (2) |
| Kimi K3 | claude | suffix_1m | 2 | 979,261 | 1 | 0 | 67.366 | ✓ | $2.939943 |
| MiniMax M2.5 | claude | suffix_1m | 1 | 191,265, 191,304 | 2 | 7 | 191.344 | ✗ (Timeout) | $0.117228 |
| MiniMax M2.1 | codex | default | 1 | 154,891 | 1 | 0 | 12.336 | ✓ | $0.046623 |
| Qwen3 32B | claude | suffix_1m | 1 | 30,571 | 1 | 0 | 2.772 | ✓ | $0.004612 |
| Claude Opus 4.5 | codex | default | 1 | 158,861 | 1 | 0 | 8.236 | ✓ | $0.795280 |
| Grok 4.6 | claude | suffix_1m | 1 | 489,210 | 1 | 0 | 63.355 | ✓ | $0.979542 |
| Grok 4.7 | codex | default | 1 | 412,264 | 1 | 0 | 41.227 | ✓ | $0.825350 |
| DeepSeek-R1 | claude | suffix_1m | 1 | 124,612, 124,651 | 2 | 0 | 76.376 | ✓ | $0.343612 |
| Claude Sonnet 4.6 | claude | suffix_1m | 1 | 979,171 | 1 | 0 | 33.856 | ✓ | $3.672444 |
| Claude Sonnet 5 | claude | suffix_1m | 1 | 979,225 | 1 | 10 | 180.274 | ✗ (Timeout) | $2.448062 |
| Claude Sonnet 5.5 | claude | suffix_1m | 1 | — | 0 | 10 | 180.315 | ✗ (Test guard) | $0.000000 |
| Qwen3 235B A22B 2507 | codex | default | 1 | 202,132 | 1 | 0 | 65.595 | ✗ (Incomplete stream) | $0.000000 + ? (1) |
| Qwen3 Coder Next | codex | default | 1 | 202,548 | 1 | 0 | 5.682 | ✓ | $0.101322 |
| GPT-6 Astra | claude | suffix_1m | 1 | — | 2 | 0 | 3.850 | ✗ (Upstream error) | $0.000000 + ? (2) |
| GPT-5.6 Sol | claude | suffix_1m | 1 | — | 2 | 0 | 3.288 | ✗ (Upstream error) | $0.000000 + ? (2) |
| Grok 4.3 | claude | suffix_1m | 1 | 979,198 | 1 | 0 | 67.877 | ✓ | $1.224522 |
| Grok 4.6 | codex | default | 1 | 393,217 | 1 | 0 | 43.799 | ✓ | $0.787244 |
| GLM 5 | claude | suffix_1m | 1 | 195,176 | 1 | 0 | 43.341 | ✗ (Retrieval mismatch) | $0.195198 |
| Claude Opus 4.8 | codex | default | 1 | — | 0 | 0 | 0.548 | ✗ (CLI text length limit) | $0.000000 |
| Qwen3 32B | codex | default | 1 | 26,519 | 1 | 0 | 2.669 | ✓ | $0.004005 |
| Claude Fable 5.1 | claude | suffix_1m | 1 | — | 0 | 10 | 180.328 | ✗ (Test guard) | $0.000000 |
| Claude Opus 4.6 | codex | default | 1 | — | 0 | 0 | 0.548 | ✗ (CLI text length limit) | $0.000000 |
| GLM 4.7 | codex | default | 1 | 160,447 | 1 | 0 | 44.309 | ✓ | $0.096345 |
| Claude Sonnet 5 | codex | default | 1 | — | 0 | 0 | 0.549 | ✗ (CLI text length limit) | $0.000000 |
| MiniMax M2.5 | codex | default | 1 | 154,888 | 1 | 0 | 15.043 | ✓ | $0.046638 |
| DeepSeek-R1 | codex | default | 1 | — | 1 | 0 | 1.137 | ✗ (Upstream error) | $0.000000 + ? (1) |
| DeepSeek-V3.1 | claude | suffix_1m | 1 | 124,613 | 1 | 0 | 14.794 | ✓ | $0.072329 |
| DeepSeek-V3.1 | codex | default | 1 | 101,497 | 1 | 0 | 11.682 | ✓ | $0.058920 |
| Kimi K3 | claude | suffix_1m | 1 | — | 0 | 11 | 177.272 | ✗ (Test guard) | $0.000000 |
| GPT-5.6 Luna | codex | default | 1 | — | 0 | 0 | 0.550 | ✗ (CLI text length limit) | $0.000000 |
| Claude Fable 5.1 | codex | default | 1 | — | 0 | 0 | 0.547 | ✗ (CLI text length limit) | $0.000000 |
| GPT-6 Luna | claude | suffix_1m | 1 | — | 2 | 0 | 2.296 | ✗ (Upstream error) | $0.000000 + ? (2) |
| Claude Opus 5 | claude | suffix_1m | 1 | — | 1 | 10 | 179.696 | ✗ (Upstream error) | $0.000000 + ? (1) |
| GPT-5.6 Terra | codex | default | 1 | — | 0 | 0 | 0.553 | ✗ (CLI text length limit) | $0.000000 |
| MiniMax M2 | codex | default | 1 | — | 0 | 0 | 0.549 | ✗ (CLI text length limit) | $0.000000 |
| GLM 5 | codex | default | 1 | 158,107 | 1 | 0 | 18.251 | ✗ (Retrieval mismatch) | $0.158110 |
<!-- /compatibility:long-cli -->

</div>

**A separate Codex single-input character limit was observed.** This installed version rejects text over **1,048,576 characters** in `turn/start` with `input_too_large`. For example, a 1,638,627-character attempt made zero gateway requests. “CLI text length limit” is a real client-side failure, not a model token-window rejection or a billed invocation. It does not establish a limit on accumulated multi-turn history. Separate the direct API large-window tests from this single-input CLI test.

Astra additionally completed two icodex-path retrievals at **303,942 and 303,936 input tokens**. These cold synthetic tasks demonstrate acceptance above 200K on that tested route. They do not establish automatic-compaction success, the maximum window, or recovery of a previously rejected conversation.

## Context limit and compaction errors are different

A configured automatic-compaction threshold can be lower than the model window. An error near that threshold is not sufficient evidence of a backend context limit. Compare the upstream error code, transport, reported input usage and the operation that failed:

| Observation | What it establishes |
| --- | --- |
| Explicit context-window rejection | This request was too large under the tested route; missing usage leaves the exact boundary unmeasured |
| `conversation_blocked` / `this conversation cannot be continued` | The provider rejected the conversation; it is not an explicit context-length error |
| The same rejection during ordinary response and subsequent compaction | Compaction also failed; increasing the assumed window has not been shown to fix the rejection |
| `no policy-permitted compatible Responses target` | Gateway admission/conversion has no compatible permitted target; examine route capabilities and request features |

## Reproduced Astra automatic-compaction failure and fix

A separate two-turn test deliberately sets `model_auto_compact_token_limit=180000`. The first turn sends about 204K synthetic input tokens; the next turn triggers automatic compaction. Before the fix, the initial turn completed but compaction received HTTP 400 from the gateway, before provider dispatch. Codex sent `parallel_tool_calls:false` with `tools:[]`; the portable Responses validator rejected the nonparallel preference even though no tools could execute.

The fix permits that preference only when tools are absent, null or empty. Actual tool declarations still require the bridge's existing supported semantics; remote state, opaque reasoning and unsupported reasoning modes remain rejected. Native Responses passthrough is unchanged. This is a conversion compatibility fix, not a larger model window or a change to the user's compaction threshold.

<div class="ip-performance-table" markdown>

<!-- compatibility:compaction -->
| Run | Build | Trigger | First input | Compaction input | Next input | Compacted | Turns | Cost |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| astra-180k | baseline-a24b6dd | 180,000 | 204,605 | — | — | ✗ | completed → failed | $2.558058 |
| astra-180k-capture | baseline-a24b6dd | 180,000 | 204,611 | — | — | ✗ | completed → failed | $2.558132 |
| astra-180k-fixed | compaction-fix | 180,000 | 204,608 | 201,604 | 44,874 | ✓ | completed → completed | $5.650710 |
| astra-180k-fixed-local | local-compaction-fix | 180,000 | 204,602 | 201,607 | 44,847 | ✓ | completed → completed | $5.649284 |
| astra-180k-fixed-repeat2 | compaction-fix | 180,000 | 204,614 | 201,610 | 44,872 | ✓ | completed → completed | $5.650435 |
<!-- /compatibility:compaction -->

</div>

Two isolated patched runs completed the compaction event and the next response: initial inputs **204,608 / 204,614**, compaction inputs **201,604 / 201,610**, and subsequent inputs **44,874 / 44,872** tokens. An additional check using the installed launcher and updated local service completed at **204,602 → 201,607 → 44,847** tokens. Its target uses the `global.` alias; the public data preserves that requested ID separately. These are actual automatic-compaction cycles with synthetic history, not merely large single-turn acceptance.

Compaction is itself a model request. The two patched isolated runs cost **$2.531295 / $2.530970** for compaction and **$0.561320 / $0.561295** for the following turn. First turn + compaction + next turn totals were **$5.650710 / $5.650435**. These estimates include the reported cache-write token class at configured prices; they are not invoice amounts. The baseline failed before compaction, so its smaller total covers less completed work and is not a cost or performance regression comparison.

An offline regression also drives the installed Codex app server through the compaction event using a local fixture. Conversion tests verify both the newly accepted no-tool request and rejection with real tools or unsupported state. The live result qualifies this tested Astra bridge path with reasoning disabled. Compaction for every other model, resumed private histories, provider changes, other reasoning modes, MCP servers and multi-agent workflows remain unqualified. The gateway's [deployment and accounting profile](../getting-started/deployment-profiles.md) still applies.
