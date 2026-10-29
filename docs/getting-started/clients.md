# Connect clients and providers

Clients authenticate to `mayu` with a virtual key. Providers use separate server-side credentials. A compatible protocol does not guarantee every model supports tools, reasoning, media, or session state.

| Client / integration | Ingress | Setup |
| --- | --- | --- |
| Claude Code | Anthropic Messages | Follow the [quickstart](quickstart.md); use the gateway base URL and virtual key |
| Codex | Responses | Use the [model-aware launcher](../codex-launcher.md) and its declared compatibility limits |
| OpenCode / Chat Completions clients | OpenAI-compatible Chat Completions | Set the client's compatible provider URL to the gateway `/v1`, use its virtual key, and select a configured public model |
| Bedrock-compatible tooling | Bedrock InvokeModel routes | Check [HTTP reference](../api-reference.md) and the supported request/authentication shape before changing a client |

A client integration must be tested with the installed client version and selected backend. Codex has local fixtures and installed-client tests; these do not establish universal live-provider compatibility.

## Provider selection

| Provider type | Server-side setup | Important boundary |
| --- | --- | --- |
| `anthropic` | `api_key_ref` and Anthropic base URL | Same-protocol forwarding preserves raw request bytes unless an enabled transform applies |
| `bedrock` | Region and configured AWS auth mode | Model API, guardrail support and region eligibility differ by route |
| `bedrock_responses` | Native Bedrock Responses configuration and AWS credentials | Native Responses capabilities and model availability need separate qualification |
| `openai_compatible` | Compatible base URL, optional key reference | Capabilities and pricing must match the deployed model |
| `openai_responses` | Native Responses endpoint and key reference | Native capabilities must be declared and verified |

Examples: [Anthropic/Bedrock](../../examples/config.json), [self-hosted](../../examples/config.selfhosted.json), [adaptive routing](../../examples/config.adaptive-routing.json). Illustrative provider/model/pricing declarations are not a list of guaranteed available products.

## Validate the integration

1. Query `GET /v1/models` with the client key and verify the public model is visible.
2. Send one non-streaming request and inspect usage/accounting.
3. Send a streaming request and test cancellation.
4. Exercise a tool round trip if the client uses tools.
5. Verify a forbidden model is refused and a configured fallback preserves access, privacy, and budget constraints.

## Claude Code MCP tool search

Claude Code 2.1.304 and later enable MCP tool search (deferred tool loading)
by default through a custom `ANTHROPIC_BASE_URL`, and 2.1.311 keeps it on through
the gateway on every route: it turns tool search off only for some models, for
`ENABLE_TOOL_SEARCH=false` or with experimental betas disabled, never because of
the endpoint. Older clients require
`ENABLE_TOOL_SEARCH=true` (or an `auto` setting) to enable it through `mayu`.
Set `ENABLE_TOOL_SEARCH=false` to send every MCP tool definition up front.
From 2.1.305, an unrecognized HTTP 400/422 can trigger a retry without tool
search, enlarging the prompt. A context refusal must retain its recognizable
error semantics; a generic validation error is insufficient.
See the [official gateway tool-search behavior](https://code.claude.com/docs/en/mcp#tool-search-through-an-llm-gateway).
Anthropic and Bedrock `invoke_model` routes forward `defer_loading`, the search tool
and `tool_reference` history unchanged, and privacy inspection accepts those blocks.
On Bedrock Converse (non-Claude models) Claude Code still defers tools: a loaded
tool's definition arrives in the next request's tools, and the gateway sends a
`tool_reference` result as `Loaded tool: <name>` text. Earlier gateway builds sent
an empty tool result there, so the model could conclude the search found nothing.
Verified on 2026-10-27 with Claude Code 2.1.311 through the gateway: deferred
loading on Haiku 5.5 (InvokeModel) and a ToolSearch → CronList call on
gpt-6.1-terra (Converse); and on 2026-09-24 against Bedrock InvokeModel (Claude
Haiku 4.5): server-side search, a client-side `tool_reference` result and
streaming, with and without a `sensitiveData` policy. This is not qualification of
every MCP server or model.

## Claude Code model aliases and auto mode

Claude Code resolves its built-in `sonnet` alias to Sonnet 5 through the gateway
unless `ANTHROPIC_DEFAULT_SONNET_MODEL` names another public model ID, for example
`global.anthropic.claude-sonnet-5-5`; `ANTHROPIC_DEFAULT_OPUS_MODEL` and
`ANTHROPIC_DEFAULT_HAIKU_MODEL` pin the other aliases. Auto mode
(`--permission-mode auto`) facts measured with Claude Code 2.1.311 on 2026-10-27:

- The main model must be on Claude Code's allow-list. On Bedrock it rejects
  Opus/Sonnet 4.6 and earlier and every Haiku except Haiku 5.5; non-Claude models
  pass and use the local classifier.
- Claude model IDs must use the hyphenated version (`global.anthropic.claude-opus-4-8`).
  The dotted form (`claude-opus-4.8`) is read as an older, unsupported version.
- The local classifier model (`global.anthropic.claude-sonnet-5` unless
  `CLAUDE_CODE_AUTO_MODE_MODEL` is set) must be routed by the gateway and allowed
  for the key. Claude Code ignores Sonnet 5.5, Opus 5.5 and Opus 5 there. A
  mistyped Claude ID can be served by another model through the gateway's
  same-family fallback; check the `x-inferplane-model-fallback` header or
  `inferplane_model_fallback_total`.
- With a non-Claude main model every tool call also sends a classifier request;
  include it in the budget.

For Responses, distinguish native mode from the stateless bridge. Opaque native history and hosted/native-only tools cannot simply move to an arbitrary Chat Completions backend. Start a new session when required by the [launcher contract](../codex-launcher.md).

For priced Kimi K3 and Fable 5.1 route examples and the dated acceptance limits,
see [Bedrock coding models](../reference/bedrock-coding-models.md).
