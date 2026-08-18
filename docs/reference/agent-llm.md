# Agent · LLM

### 1. Overview
The LLM-facing core: the provider abstraction that talks to Anthropic, Amazon Bedrock,
and OpenAI-compatible upstreams, plus the canonical schema used to convert between
client and upstream protocols without losing thinking blocks or `cache_control`.

### 2. Components
| Component | Path | Purpose |
|---|---|---|
| Provider interface | `providers/provider.go` | `Name`/`Models`/`Complete`/`Stream`, optional `TokenCounter` |
| Registry | `providers/registry.go` | `Register`/`New` factory by type string |
| Anthropic provider | `providers/anthropic/` | Messages passthrough, verbatim body, byte-exact SSE |
| Bedrock provider | `providers/bedrock/` | Claude→InvokeModel, non-Claude→Converse; SDK isolated |
| OpenAI-compatible | `providers/openaicompat/` | vLLM/Ollama; order-preserving model rewrite |
| Mock provider | `providers/testing/mockprovider/` | deterministic provider for unit tests |
| Canonical schema | `pkg/schema/` | Anthropic-superset types, Extra preservation, SSE writer |
| Filter chain | `internal/filter/` | `RequestFilter` interface + registry (the spec's filter chain ⑥, ADR-009) |
| PII mask filter | `plugins/piimask/` | opt-in regex+Luhn PII masking → typed placeholders; one-way (no vault); masks messages text only |
| Tracing | `internal/tracing/` | opt-in OTel: OTLP exporter + GenAI-semconv spans + W3C propagation + trace_id in audit; no-op default (ADR-011) |

### 3. Key Decisions
- One package per provider; adding a provider is one package + a blank import (zero core diff, §8).
- Canonical schema is an Anthropic-superset so thinking blocks and `cache_control` survive conversion.
- Bedrock Claude uses InvokeModel with a cache-safe top-level-only model rewrite; the event stream is re-serialized to Anthropic SSE.
- PII masking is OPT-IN per team: it re-serializes the body (cache loss, ~10× cost — warned, not silent), updates both RawBody and Parsed (so the openai_compatible Parsed-conversion path can't leak), masks text only (never system/tool/cache_control), and fails CLOSED (ADR-009).
- Bedrock Guardrails (D6, ADR-019) are applied on the DATA PLANE — every InvokeModel/InvokeModelWithResponseStream/Converse/ConverseStream call — not just surfaced in the console: a provider-level default (config `guardrail_id`/`guardrail_version`) plus an optional per-team override (`teams.guardrail_id`/`guardrail_version`), with the override winning but no per-team opt-out (a team can pick a different guardrail, never remove the default).
- Bedrock upstream errors (e.g. a throttled model) are classified into their real HTTP status (`providers/bedrock/errors.go`) and returned as a `providers.UpstreamError`, so the client sees the actual 429/4xx/5xx instead of a generic 502 — applied on non-streaming calls and the pre-first-byte error of a stream open; a mid-stream error (after the first SSE event is already committed) cannot change the HTTP status and is left as-is (existing truncated-stream handling).
- Newer Bedrock models (Opus 4.7/4.8, Fable 5, Sonnet 5, Mythos — an allow-list in `providers/bedrock/thinking.go`) reject the legacy extended-thinking shape Claude Code still sends (`thinking: {"type":"enabled","budget_tokens":N}`) with a 400; `toInvokeBody` rewrites it to `thinking: {"type":"adaptive"}` + top-level `output_config: {"effort":...}` for those models only, leaving every other model's `thinking` field untouched (ADR-022 — a compatibility shim for the CLI/model schema gap, not a permanent fix).

### 4. Code Pointers
- `providers/provider.go` — the interface every provider implements; `ProxyRequest.GuardrailID`/`GuardrailVersion` (D6) is the narrow provider-isolation exception carrying a per-team override
- `providers/bedrock/invoke.go` — InvokeModel body build + SSE re-serialization
- `providers/bedrock/client.go` — `Guardrail` type, `buildGuardrailConfig`/`buildGuardrailStreamConfig`
- `providers/bedrock/errors.go` — AWS SDK error → HTTP status classification, synthesized Anthropic-shaped error body
- `providers/bedrock/thinking.go` — legacy `thinking` → adaptive+`effort` rewrite for the allow-listed broken models (ADR-022)
- `pkg/schema/extra.go` — unknown-field preservation + case-collision rejection

### 5. Cross-references
- Related modules: `internal/router` (resolution/fallback), `internal/openai` (conversion), `internal/keystore` (team-record guardrail override)
- Related ADRs: docs/decisions/ADR-019-bedrock-guardrails-data-plane.md, docs/decisions/ADR-022-bedrock-legacy-thinking-rewrite.md
- Related runbooks: docs/runbooks/
