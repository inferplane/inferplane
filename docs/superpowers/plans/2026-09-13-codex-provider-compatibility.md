# Codex Provider Compatibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement the independently owned tasks below.

**Goal:** Let Codex select the gateway's coding-capable models through one Responses API, including the existing Grok 4.6 Bedrock route.

**Architecture:** Retain native Responses passthrough and extend the existing stateless canonical bridge to Bedrock Converse, InvokeModel and Mantle. Provider adapters own backend envelopes, tool-name constraints and streaming translation; the gateway retains authentication, privacy, budget enforcement, audit and fallback boundaries. Model selection must use per-model context metadata instead of Astra's hardcoded context window.

**Tech Stack:** Go 1.25, existing AWS SDK v2, Python standard library for a local Codex launcher, Codex CLI 0.154.0.

**Spec:** ADR-044 and the user's requirement to make provider compatibility work through a unified endpoint like OpenRouter.

## Global Constraints

- Existing native-protocol traffic keeps its byte-preserving path.
- No new inference service, credential broker or central routing dependency.
- Gateway keys never reach upstreams; IAM and configured upstream credentials remain gateway-owned.
- Strict schemas, private reasoning state, unsupported hosted tools and policy restrictions are not silently weakened.
- Tests use fake upstreams and credentials; separate, explicitly recorded acceptance probes may use the configured live models.
- DCO commits; latest-HEAD AI review and all CI before automatic merge and local deployment.

## Task 1: Portable bridge envelope and request admission

**Owner:** main agent.

**Files:** `internal/responses/anthropic.go`, `internal/server/responsesapi/anthropic.go`, `internal/server/responsesapi/handler.go`, `internal/router/request_routing.go`, associated tests.

**Interfaces:** Produce `responses.CanonicalToAnthropicRequest(*schema.ChatRequest) ([]byte, error)` from the existing Anthropic adapter. Preserve the original canonical view for Responses response rendering.

- [ ] Move the tested envelope renderer into the protocol leaf and retain its message-phase removal and bounded default output.
- [ ] Add a failing gateway case for a declared Bedrock Responses bridge, then admit that bridge under complete text/tool inspection and the existing callback.
- [ ] Keep native-only fields and strict/schema checks fail closed; test privacy, budget, RBAC and fallback paths for bridged requests.
- [ ] Run protocol/router/handler tests and the installed-client acceptance matrix.

## Task 2: Bedrock provider bridge

**Owner:** provider worker. **Write set:** `providers/bedrock/*`.

**Consumes:** the Task 1 envelope renderer and the existing `SupportsIngress(string) bool` capability seam.

- [ ] Add failing complete/stream tests using the existing fake Converse, InvokeModel and Mantle seams.
- [ ] For Responses ingress only, validate and render canonical text, function/custom/namespace tools, tool results and bounded output into the backend envelope.
- [ ] Map tool names that violate backend restrictions to stable, collision-checked aliases and reverse them on responses and streams; preserve replay references and input objects.
- [ ] Preserve tool-choice semantics, guardrails, usage observations, error handling and original request objects.
- [ ] Test rejected opaque/stateful/strict inputs and secret isolation before upstream calls.

## Task 3: Model-aware Codex launcher

**Owner:** launcher worker. **Write set:** `scripts/inferplane-codex`, launcher tests/documentation only; do not edit `.bashrc` until integration.

- [ ] Inspect the installed Codex model catalog contract and test CLI config precedence.
- [ ] Query authenticated gateway model metadata and supply a compatible catalog or equivalent per-model limits so `-m` and interactive model selection do not inherit Astra's 1M limit.
- [ ] Preserve stdin, all normal CLI arguments, environment-scoped gateway credentials and the explicit `--` separator.
- [ ] Keep model-provider selection on the chosen subcommand; no direct-provider fallback on gateway failure.
- [ ] Test catalog generation, model selection, missing credentials, context metadata and argument handling offline.

## Task 4: Deployment model matrix and integration

**Owner:** main agent.

- [ ] Inventory every configured model and collect backend capability/context evidence.
- [ ] Run isolated live text and tool probes for each configured model; distinguish adapter defects from backend availability or unsupported capabilities.
- [ ] Run installed Codex tool round trips on Grok 4.6 and representative Claude, Qwen, Gemma, Kimi and native Astra paths.
- [ ] Update the local model metadata and install the model-aware launcher without changing governance policy or widening key access.
- [ ] Run pure-Go builds, full race tests, vet, gofmt and the shell harness.
- [ ] Obtain independent review, push a DCO PR, resolve Critical/Major findings, merge after latest-HEAD AI and CI.
- [ ] Deploy the clean merged binary with rollback backups, check health/readiness and verify the actual `icodex` Grok path.

## Progress

- Baseline: deployed main `4e5ea7c`; native Astra works. Existing Grok route returns HTTP 400 before upstream invocation because the canonical bridge rejects Bedrock.
- Existing checkout `/home/atomoh/inferplane` remains on its user-owned branch; implementation uses the established isolated worktree.
