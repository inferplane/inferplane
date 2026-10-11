# ADR-047: Codex provider compatibility

- **Status:** Accepted
- **Date:** 2026-09-13
- **Extends:** ADR-044

## Context

The gateway exposed a Responses endpoint, but its compatibility gate rejected
existing Bedrock routes. A working native Astra route therefore did not let a
Codex user select the configured Grok, Claude, Qwen or Kimi routes. The local
launcher also supplied one context limit for every model.

Backend protocols differ in tool names, message envelopes and stream
termination. Backend availability and native tool support are separate from
protocol conversion: the gateway cannot grant an account's model access or
assert an unsupported tool capability.

## Decision

Extend the existing portable text/tool bridge to declared Bedrock Converse,
InvokeModel and Mantle routes. Keep transformations in the provider and protocol
packages. The shared Anthropic-envelope renderer supplies a bounded default
output limit and removes Responses message-phase bookkeeping.

Map incompatible Bedrock tool names to deterministic, collision-checked names.
Restore names before returning calls to the client, and apply the same mapping
to history and tool choice. A backend-generated unknown tool is an error;
observed usage remains chargeable.

Accept a clean Chat Completions EOF without `[DONE]` only after a recognized
finish reason and complete nonnegative usage. Synthesize a canonical terminal
event without inventing upstream wire bytes. Partial, malformed and failed
streams retain their failure behavior.

Expose configured context bounds, capabilities and Responses mode through the
authenticated OpenAI model-list endpoint. Native client metadata bindings use
public model names. A model-aware Codex launcher builds a private local catalog
from this information, so each model retains its own limits and tool profile.

The portable profile accepts neutral `reasoning.effort: "none"` to clear a
native-model preference inherited from client configuration. Requested effort
control, strict guarantees and provider-owned reasoning state continue to
require a compatible native route.

## Consequences

- Authentication, privacy inspection, budget admission and retry constraints
  remain ahead of provider invocation; the bridge does not expand permissions.
- Existing native-protocol paths retain their original request behavior.
- The default portable session can change between compatible text/tool models.
  Native encrypted history may require starting a new portable session.
- Model declarations must reflect observed backend capabilities. Models without
  qualified tools remain usable through their supported text API paths and are
  excluded from the coding-client catalog.
- Account-level retention/access requirements are not automatically changed to
  make a backend available.
- This adds no inference service or central routing dependency.

## Validation

Offline tests cover provider envelopes, tool aliases and replay, strict/opaque
input refusal, guardrails, stream termination, accounting and discovery metadata.
Installed-client tests exercise real Codex against local fake native Responses,
Chat Completions and Bedrock Converse endpoints. Deployment acceptance uses
separate synthetic live probes and records model-specific availability.

## 2026-09-27 addendum: opt-in native hosted search

The local launcher accepts `INFERPLANE_CODEX_WEB_SEARCH=cached` for explicitly
selected native Responses models. It keeps search disabled otherwise and
rejects other modes. Search sessions expose only mapped native models: Codex's
search setting is session-wide, so offering a bridge in that catalog would
allow an incompatible hosted-tool/history switch.

Search-enabled native templates use text-only search and full Responses.
Installed Codex 0.157 tests establish that its Responses Lite profile omits the
hosted search tool; full Responses sends `external_web_access:false` and avoids
the OpenAI image-search extensions unsupported by Mantle. Other native profile
features and default non-search behavior remain unchanged.

The existing SigV4 transport and gateway policy gates do not change. Model and
region support and IAM Search/Fetch permissions remain deployment requirements.
This is not a gateway-side MCP executor, and it does not authorize external-web
retrieval. Separate search-operation fees remain outside token-cost accounting;
enabling this option is not a claim of total AWS-spend enforcement.

## 2026-10-02 addendum: qualified operator context profiles

The launcher accepts an opt-in local `INFERPLANE_CODEX_CONTEXT_PROFILES` file
to tune client usable-context and automatic-compaction percentages per public
model ID. Profiles must exactly match the current declared context bound;
they cannot increase it or qualify an undeclared fallback. Unlisted models keep
the existing defaults. Gateway authorization, privacy and monetary admission
remain unchanged; no new distributed policy/schema field is introduced.

Synthetic live probes use only the gateway and retain a durable cost reservation
before each invocation. Unknown errors and missing usage retain that reservation.
Generic upstream 400s cannot establish a maximum. Accepted token counts qualify
only a lower bound for the tested text shape. The recorded Qwen/Grok examples
increase client headroom inside those successful bounds; Sol 6.1 is not widened
because its observed acceptance and unclassified failures do not locate a
maximum. See [validation evidence](../reference/context-window-validation.md).

## 2026-10-07 addendum: Claude Code context-overflow recovery

The Messages, Chat Completions and Bedrock Invoke preflight used a descriptive
`request is ~N tokens ... context window` 400. Claude Code 2.1.292 treated that
as an unclassified request failure and tried fallback models with the same
oversized history. Its installed error predicates recognize `prompt is too
long` on 400 and parse `N tokens > M` to size the overage. Emit that spelling
inside each existing ingress error envelope, followed by an explicit gateway
byte-estimate qualification and compaction guidance.

The estimator, declared window, pre-billing gate, policy checks and upstream
error sanitization are unchanged. This is a client recovery compatibility fix,
not a tokenizer accuracy claim or permission to raise context declarations.
Cross-ingress tests pin the client recognition/count-extraction contract,
pre-provider refusal, unauthenticated behavior and HTTP-200 count endpoints.
An offline check of the installed client's predicates confirms the old/new
classification difference; it does not establish end-to-end live compaction.

### Inline-image preflight correction

Follow-up diagnosis found screenshot-heavy compaction requests rejected before
any upstream attempt: the raw-bytes/4 heuristic counted PNG base64 as text tokens.
Recognizing the error alone could not repair those false refusals. For generation
preflight, when that first heuristic exceeds the model declaration, subtract only
valid inline-image base64 at known protocol content positions and recheck the
remaining bytes/4. This covers Anthropic/Bedrock image blocks (including immediate
tool-result images), Chat image data URLs and native Responses input-image data
URLs. The helper recognizes PNG/JPEG/GIF/WebP MIME declarations and base64 syntax;
it does not decode image pixels or claim a model-specific visual token count.

The upstream validates the actual image and combined context. Remaining text
can still trigger local refusal. Unknown media, document/reasoning payloads,
application objects and stringified JSON retain conservative accounting. No
request bytes are rewritten or dropped. This narrowly corrects the generation
preflight; count API estimates, TPM/token quota charging, durable money bounds,
PII/identity/readiness checks and automatic/strict-target capacity checks remain
unchanged. Those conservative checks can still refuse a multimodal request.
Offline tests cover transport-size independence, oversized visible text,
opaque/malformed inputs, exact forwarding and preserved governance refusal.

### Native error recovery signals (2026-10-08)

Native Responses HTTP errors must preserve a bounded allowlist of protocol
recovery codes (`context_length_exceeded`, `invalid_encrypted_content`) through
provider and ingress. Returning only a generic `upstream_error` erases information
available to directly connected clients. Read at most 64 KiB plus an overflow
sentinel; malformed, oversized and unknown errors stay generic. Never propagate
raw error messages/params or infer a code by searching arbitrary message text.
Reuse the existing audit outcome error field, without new identity/metric labels.
No retry, history rewrite, permission relaxation or usage fabrication is implied.
Committed SSE handling is unchanged. This fixes loss of recovery evidence; it
does not establish the cause of a historical 400 whose body was discarded.

The local Codex launcher may wrap interactive sessions in a PATH-discovered
`codex-hud` without evaluating shell functions. Noninteractive and management
commands retain direct execution. Model selection, private catalog lifetime and
gateway credential environment remain owned by the launcher.

Responses local context preflight uses the same `context_length_exceeded` wire code.
Its existing audit reason and conservative admission threshold are unchanged.
