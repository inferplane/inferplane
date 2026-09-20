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

For Responses, distinguish native mode from the stateless bridge. Opaque native history and hosted/native-only tools cannot simply move to an arbitrary Chat Completions backend. Start a new session when required by the [launcher contract](../codex-launcher.md).

For priced Kimi K3 and Fable 5.1 route examples and the dated acceptance limits,
see [Bedrock coding models](../reference/bedrock-coding-models.md).
