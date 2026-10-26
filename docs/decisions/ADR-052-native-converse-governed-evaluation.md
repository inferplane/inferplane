# ADR-052: Native Converse ingress and governed administrator comparison

Date: 2026-10-20  
Status: Accepted

## Context

The 2026-10-19 comparison found that Bedrock Converse worked as an upstream
adapter but `/model/{modelId}/converse` returned 404 at the gateway. Re-rendering
native requests through Anthropic structs would lose native content and cache
bytes. The administrator could inspect spend but could not run a paired model
comparison while retaining ordinary key and governance controls.

## Decision

Add native Converse and ConverseStream branches to the existing Bedrock provider
and shared Bedrock ingress. Reuse virtual-key authentication, readiness, model
authorization, region/privacy/strict-budget routing, per-attempt admission,
settlement and audit. This is a core ingress feature using an existing provider,
not a new provider extension.

Native egress signs fresh HTTP requests with the resolved gateway credentials.
Use the SDK's fixed regional endpoint resolver, respecting FIPS and dualstack;
disable native capability when a custom SDK endpoint override is present, leaving
the established SDK paths available. Do not follow redirects or add transport
retries. Gateway fallback remains observable and policy constrained. Native
client 4xx errors are not broadly retried; explicit 429 and eligible model-not-found
fallback are handled by the shared attempt loop.

Preserve native request/response bytes and named eventstream frames. Parse a
separate observation for accounting; validate frame CRC and require messageStop
and usage metadata for a complete stream. Required guardrail insertion and fully
verified privacy masking are narrow exceptions. Reject conflicting guardrails
and unpriced service tiers/latency modifiers. Do not silently rewrite sampling
parameters. Missing counts, unknown cache TTL and unqualified cached OpenAI input
semantics cannot establish a known financial total.

Expose `POST /admin/evaluations` only in mayu's administrator plane. Require both
full admin authorization and a separate virtual key; create fresh headers and
invoke the ordinary data handler in-process. No arbitrary URL or upstream
credential input, no control-plane inference path, no alternate admission lane.
Request-scoped observers expose settlement evidence without modifying authority.
Multiple attempts cannot be presented as a known total from the last settlement.

The UI runs sequential A/B then B/A rounds, at most five rounds, with a shared
prompt and explicit output limit. It distinguishes HTTP failures, refusals,
truncations and empty/malformed responses. Costs use integer microUSD; missing
prices are unknown. Non-streaming output tokens/E2E second is explicitly not
decode throughput or TTFT. In-memory export omits virtual keys, prompts and output
text. This does not add persistent benchmark storage or a quality grader.

## Consequences and qualification

Configured Bedrock Runtime targets can receive native requests; upstream model,
region and feature availability still apply. Mantle is not a Converse backend.
This does not establish compatibility for every Bedrock model.

The live qualification consists of one administrator Converse request and one
boto3 ConverseStream request each for Claude Haiku 4.5 and Qwen3 32B. Offline
adversarial tests cover auth, policy, wire format, stream failures and cost
uncertainty; those fixtures are not model quality evidence. Long-context results
remain in the earlier benchmark, not implied by the four short calls.

Existing aggregate analytics are not upgraded into a complete uncertainty-aware
financial ledger by this UI feature. Prices remain configured estimates until
reconciled to billing records.

See [contract and use](../bedrock-converse-evaluation.md) and the
[published measurements](../performance/index.md#native-converse-and-administrator-ui).
