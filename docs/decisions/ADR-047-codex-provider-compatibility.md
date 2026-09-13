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
