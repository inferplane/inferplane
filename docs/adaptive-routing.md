# Adaptive routing and Codex

Configure approved destinations and constraints; `mayu` applies them to eligible
requests without requiring users to switch models for each task. Context-based
model selection, PII handling and budget-driven cutover run in the data plane
within the configured rules. Operators choose the policy and verify the targets;
context starts in Shadow while privacy rules enforce independently.

[Why this matters](why-inferplane.md) · [Architecture](architecture.md)

Use `examples/config.adaptive-routing.json` as an isolated node-local example.
The endpoints, model IDs, context windows, capabilities and prices are
illustrative; replace them with verified deployment values. Qwen, Gemma and Kimi
names do not determine cost, model size, protocol support or data boundary.

## Routing contract

| Requirement | Behavior |
|---|---|
| Weak / normal / strong | `simpleModel` / optional `normalModel` / `complexModel`; local size and latest-user keyword signals |
| Avoid frequent changes | Explicit `stability`; default 5-minute hold, 3 successful requests, 30-minute session TTL |
| PII dedicated model | `InternalOnly` with explicit approved models and internal provider metadata; optional `pii_classification` (Shadow/Enforce) adds a learned trigger |
| PII masking | `Mask` on detected inspectable content; complete transformation plus reinspection before egress |
| Budget cutover | `budgetTiers.enforceTargets: true`; threshold 100 is supported; all retries stay within the selected model |
| Total hard limit | Remains independent; exhaustion denies even a cheap/internal target |
| Availability | Local decisions and approved backend failover; no central classifier/affinity service |

An incompatible target, unknown boundary, denied model or empty intersection
does not become an external/expensive fallback. Provider cache warmth is a
preference beneath these constraints. A hold can delay a new context preference;
choose its duration according to task quality as well as cache cost.

The session key is scoped to the authenticated key/team, ingress and requested
model. `X-Inferplane-Session-ID` is an optional opaque performance hint. Without
it, mayu derives a stable conversation-prefix fingerprint. Neither identifies
or authorizes a user. Do not put personal information into session headers.
Pins are local and bounded; restarting/rerouting a data plane may lose warmth.

## Example configuration

The example's `auto` model initially resolves to the normal route. Its policy
chooses weak, normal or strong after workload gates pass. It has a $100 switching
threshold and a separate $150 total hard budget. These are demonstration
settings, not recommended spending limits.

Load `governance.yaml` with **one** of `privacy-internal.yaml` or
`privacy-mask.yaml`. Do not load the whole example directory unless the
intersection of both privacy policies is intended. Loading both requires masking
and an internal destination, not either/or.

```bash
mkdir -p /tmp/inferplane-adaptive-demo
mayu pricing check --config examples/config.adaptive-routing.json
mayu keys create --team engineering --models '*' --store /tmp/inferplane-adaptive-demo/keys.db
mayu serve --config examples/config.adaptive-routing.json
```

Supply the referenced environment secrets through your secret manager. The
virtual key is displayed once; do not put it in config files or commit it.

Context starts in Shadow. To activate selection, change that rule's mode to
Enforce after representative evaluation. `normalModel` requires
`maxNormalInputTokens > maxSimpleInputTokens`. The estimator includes the full
request, including tool schemas; these are conservative estimates rather than
a provider tokenizer's exact counts.

Each policy supports at most 256 rules; tier ladders have at most 100 entries.
The CRD includes finite collection bounds and is checked with Kubernetes' native
schema/CEL cost validator in CI, not only with a YAML shape check.

The soft threshold referenced by a strict tier is accounting-only for admission.
It does not replace a higher total hard cap with a smaller blocking limit.
Usage is still measured; absent any real cap, a warn-only meter feeds the routing
decision. Actual costs, including approved low-cost traffic, continue to count
toward the total cap. Existing local/lease accuracy limits still apply.

## Codex local configuration

Configure the custom provider in your user-level `~/.codex/config.toml`.
Project-local provider configuration is not the supported path in current
Codex. Store the gateway virtual key in `INFERPLANE_API_KEY`.

```toml
model = "auto"
model_provider = "inferplane"
web_search = "disabled"

[model_providers.inferplane]
name = "inferplane"
base_url = "http://127.0.0.1:8080/v1"
wire_api = "responses"
env_key = "INFERPLANE_API_KEY"
```

The gateway supports HTTP Responses streaming; this example does not advertise
WebSocket support. `auto` can use the stateless Chat Completions bridge to
approved local models. The separate `codex-native` model demonstrates a native
Responses provider for compatible OpenAI-style upstreams.

The bridge supports text, function/custom tools, tool results and compatible
stateless history over OpenAI Chat Completions, Anthropic Messages, Bedrock
Converse, Bedrock InvokeModel and Mantle Chat/Messages routes. Bedrock tool names
are mapped to deterministic backend-safe aliases and restored before the
Responses client receives them. Provider-owned conversation identifiers, encrypted reasoning
transfer, asynchronous response storage and unsupported built-in tools are not
silently converted. Use a native compatible target or a fresh portable
conversation when those features are involved. Models still need their own
coding/tool-use evaluation.

An explicitly requested Responses model uses the ingress context estimate and
requested output budget for capacity admission. The conservative inspection
ceiling remains in use when selecting another model automatically or enforcing
an internal-only or strict budget destination. Keeping these estimates separate
prevents ordinary long tool histories from being rejected as incompatible solely
because decoded bytes were counted as input tokens. Neither estimate is a model
tokenizer; upstream usage remains authoritative for settlement.

Function-schema strictness is preserved on the Chat bridge; a strict function
request requires a target explicitly declaring `structured_output`. The
Anthropic and Bedrock bridges accept non-strict tools. They emit a real
Messages envelope and supply `max_tokens: 4096` when no output bound was sent.
Responses message-phase bookkeeping stays on the Responses side of that adapter.
Failed and truncated attempts retain observed usage, and retry admission checks
the remaining hard budget and current strict target again.

OpenAI reference material used:
- https://developers.openai.com/codex/config-reference
- https://developers.openai.com/codex/config-advanced
- https://developers.openai.com/api/reference/resources/responses/methods/create
- https://developers.openai.com/api/docs/guides/streaming-responses

## Codex with Amazon Bedrock IAM credentials

The `bedrock` provider bridges portable Responses requests to its configured
Converse, InvokeModel or Mantle route. Model declarations need a context bound,
the `tools` capability for coding clients, and a price for governed bridge
selection. The actual backend must support the requested tools.

Use `bedrock_responses` for Bedrock Mantle's native Responses API:

```json
{
  "providers": {
    "codex-bedrock": {
      "type": "bedrock_responses",
      "base_url": "https://bedrock-mantle.us-west-2.api.aws/openai"
    }
  },
  "models": {
    "openai.gpt-6-astra": {
      "targets": [
        {"provider": "codex-bedrock", "model": "openai.gpt-6-astra"}
      ]
    }
  }
}
```

Merge these entries into an existing gateway configuration and declare the
model's verified price under `pricing.overrides.codex-bedrock`. Confirm model
access in the selected region. The endpoint determines the signing region;
only explicit commercial Bedrock Mantle HTTPS endpoints are accepted.
The Astra example uses `us-west-2`, its documented Mantle region:
[AWS model card](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-openai-gpt-6-astra.html).

Mayu uses its own standard AWS credential chain and signs requests for the
`bedrock` service. This provider uses IAM credentials, not `api_key_ref` or
the credential broker. Client virtual keys terminate at mayu. The native
Responses transport supplies streaming and usage accounting; the signing
transport preserves request bytes. No Converse conversion is involved.
Requests requiring Bedrock Guardrails are refused because this transport
cannot enforce them.

Use the custom Codex provider configuration above with
`model = "openai.gpt-6-astra"` and the local gateway's actual port. Keep
`supports_websockets = false` and `web_search = "disabled"` unless a supported
search path has separately been configured. The `amazon-bedrock` Codex
provider connects to AWS directly; select the custom `inferplane` provider
to use gateway authentication, routing and accounting.

`GET /v1/models` also supplies `capabilities` and `responses_mode`
(`native`, `bridge` or `unsupported`). Native entries include `codex_model`,
derived from the public name for matching installed Codex metadata; private
upstream deployment names are not exposed. A mixed native/bridge target chain
advertises the portable bridge contract. Metadata describes configured
capabilities, while inference-time policy and provider checks remain authoritative.

### Opt-in Mantle Web Search

With a verified native `bedrock_responses` route, opt the launcher into
text-only, AWS-index/cache search:

```bash
INFERPLANE_CODEX_WEB_SEARCH=cached scripts/inferplane-codex -m openai.gpt-6-astra
# Or a separately configured native GPT-5.6 route:
INFERPLANE_CODEX_WEB_SEARCH=cached scripts/inferplane-codex -m openai.gpt-5.6-sol
```

`INFERPLANE_API_KEY` must contain the gateway virtual key as usual. The default
remains `disabled`; only `disabled` and `cached` are accepted. Search sessions
use a native-only model catalog, `web_search_tool_type: text`, and full Responses
instead of Responses Lite (Codex 0.157 omits hosted search on the Lite path).
The launcher overrides inherited `web_search="live"` with `cached`; the client
sends `external_web_access:false`. Do not advertise OpenAI's image-search
extensions on Mantle. Restart without this opt-in for portable model switching.
The `--search` CLI flag requests live search and is rejected by this launcher.

Use Mantle model IDs such as `openai.gpt-5.6-sol`, not the `global.*` runtime
profile IDs. Adding `api: mantle` to the ordinary `bedrock` provider selects
Chat/Messages, **not** native Responses, and does not enable hosted search.
Keep region-specific providers: GPT-5.6 Sol and GPT-6 Sol/Luna can use
`https://bedrock-mantle.us-east-1.api.aws/openai`, whereas GPT-6 Astra requires
`us-west-2`. GPT-5.6 Terra/Luna are also available in `us-west-2`. Check each
model card instead of assuming one Mantle region serves the entire family.
The model name or native transport alone does not prove upstream tool support:
verify the model and region, and grant the gateway IAM identity
`bedrock-websearch:InvokeSearch` and `bedrock-websearch:InvokeFetch`. This mode
does not need or grant `bedrock-websearch:ExternalWebAccess`.

Mayu preserves search-call output, citations and SSE, with existing identity,
privacy, guardrail-refusal and token-accounting gates unchanged. **Separate AWS
Web Search operation fees are not included in mayu's token-based cost meters or
hard caps.** Account for those fees separately. Static price overrides also
need an explicit policy for short/long-context inference tiers; they do not
automatically reproduce every upstream pricing tier.

References:
- [AWS Web Search](https://docs.aws.amazon.com/bedrock/latest/userguide/web-search.html)
- [AWS Web Search IAM](https://docs.aws.amazon.com/bedrock/latest/userguide/security-web-search.html)
- [GPT-5.6 Sol regions](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-openai-gpt-56-sol.html)
- [GPT-6 Sol regions](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-openai-gpt-6-sol.html)
- [GPT-6 Luna regions](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-openai-gpt-6-luna.html)
- [OpenAI Web Search](https://developers.openai.com/api/docs/guides/tools-web-search)
- [Codex configuration](https://developers.openai.com/codex/config-reference)

Portable clients may send `reasoning: {"effort": "none"}` to clear an inherited
native-model effort preference. This requests no backend effort-control feature;
the bridge carries text and tool results, and generation remains provider-defined.
Explicit effort requirements and encrypted reasoning history still require a
compatible native target.

## Privacy and cache limits

An enabled legacy PII filter also applies to Responses through the complete
request transformer. Clean inspectable requests remain usable; protected text
must satisfy both detector sets, and opaque/unsafe transformations still refuse.

Finite detectors cover email, supported phone formats, Luhn-valid cards, SSNs,
IPv4 and Korean resident-ID shapes. This is not universal PII recognition.
Unknown/opaque content takes the configured internal/block path. Masking refuses
protected structural keys, identifiers or numeric values when a safe
type-preserving transformation is unavailable.

Identical transformed requests produce identical bytes, so subsequent masked
prefixes can still benefit from provider caching. Switching model/provider or
introducing masking starts a different cache namespace. No cache-hit or savings
percentage is promised without measurements.

## Availability and verification

Mayu performs deterministic inspection, policy evaluation and pin lookup locally.
Opt-in `pii_classification` can call an HTTP endpoint, including a pod-local
SystemOne sidecar. Classifier timeout, saturation and failure apply
`onUninspectable` in Enforce; Shadow records without restricting the request.
Backend failover is constrained by the same PII, capability, region and cost
limits. Replicate approved backends and keep control-plane management off the
inference path. Global monetary authority now has an opt-in Postgres ledger and
durable node journals; see [durable budgets](durable-budgets.md) and
[ADR-045](decisions/ADR-045-durable-budget-authority.md). For shared keys and global rate/token quotas, use the explicit
[shared gateway profile](shared-governance.md), which requires an available HA
Postgres service. The node-local profile retains its existing rate/key limits.

Regular repository tests use fake providers and disposable stores. An optional
installed-client check performs a real Codex CLI tool round trip against local
fake upstreams with isolated client configuration:

```bash
INFERPLANE_TEST_CODEX=1 go test ./cmd/mayu -run TestE2ECodexCLI -v -count=1
```

This checks the protocol and gateway pipeline. It does not call a real model,
prove production model quality, or certify multi-replica budget correctness.
