# Gateway Path Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the code-fixable findings of the 2026-10-27 Claude Code / Codex path check: recoverable context overflow from Bedrock Converse and the Responses bridge, Converse tool search results, visible model fallback, runtime metrics, an optional memory bound with protocol-shaped refusals, per-kid JWKS isolation, corrected client docs, and a content-aware token estimate in place of bytes/4.

**Architecture:** Overflow evidence is recognized once (`providers.ContextOverflowCounts`, `providers.PromptTooLongBody`) and reused by the Bedrock SDK, native Converse and OpenAI-compatible paths; every ingress already understands that body. The router distinguishes a size-only refusal (`context_window_exceeded`) from other no-route reasons. A new stdlib leaf, `internal/tokenest`, estimates request tokens deterministically and offline and replaces every bytes/4 site. Server-wide refusals made before a handler runs (413, the optional in-flight budget's 503) share one protocol-aware writer. Fallback evidence rides the existing audit routing projection.

**Tech Stack:** Go 1.26.10 (`CGO_ENABLED=0` binaries), `prometheus/client_golang` collectors, `coreos/go-oidc/v3` with `go-jose/v4`, MkDocs for docs; o200k_base (tiktoken 0.14.0) was used offline, by the plan author only, to calibrate the estimator.

**Spec:** No separate spec, by user instruction. Inputs: `/home/atomoh/.tmp/inferplane-remediation-20261029/inputs/program.md` (Plan 2 items 1–10) and `/home/atomoh/.tmp/inferplane-remediation-20261029/inputs/path-check-doc-extract.txt` (measurements). The plan argues from commit `0360e81` / PR #127 (`60baa71`, the bedrock and openaicompat overflow mapping), ADR-029 (model fallback), ADR-043/044 (policy routing), ADR-054 (local attempt liability), ADR-057 (audit console evidence, now on main), `docs/reference/api.md` and `docs/getting-started/clients.md`. Every code block below was applied task by task to a copy of main `26ad568` and its task checks and the final repository gates were run (see Task 12).

## Design decisions

1. Decision: recognize Converse overflow only from anchored wordings — Claude's `prompt is too long: N tokens > M maximum`, the relayed serving-engine `Input length (N) exceeds model's maximum context length (M).` (captured from the 2026-10-27 mayu log for `openai.gpt-oss-120b-1:0`, with and without the `Mantle streaming error for requestId <uuid>: ` prefix), the classic vLLM wordings already recognized by `openaicompat`, and a relayed OpenAI error whose code is exactly `context_length_exceeded` (no counts) — through one helper in `providers/context_overflow.go` that both `bedrock` and `openaicompat` call — why: reuses PR #127's evidence rule (only validated numbers cross protocols, never provider text) instead of a second recognizer — cost if wrong: an unobserved model-family phrasing still surfaces as the scrubbed `bedrock upstream error (ValidationException)`; the full SDK text is already in the server log, so adding a pattern is a one-line follow-up.
2. Decision: native `/converse` maps overflow too, by reading at most 64 KiB of the AWS error body (it already drained that much) and requiring a `ValidationException` type from `X-Amzn-ErrorType` or `__type` — why: the same non-Claude families are reachable there — cost if wrong: none beyond the bounded read; unrecognized bodies keep the fixed message.
3. Decision: a size-only routing refusal is detected by re-running the boundary filter with the context bound removed (`requestBoundary.contextOnly`); the router reason becomes `context_window_exceeded` for every protocol, but only Responses changes its wire answer (400 `context_length_exceeded`, the shape its own preflight already used) — why: item 2's scope, and Codex retries the 403 unchanged; the reason change elsewhere is more accurate audit evidence — cost if wrong: on Messages/Chat/Bedrock a privacy- or context-policy route that is too small still answers 403, which Claude Code does not compact on (recorded as a follow-up, not changed here).
4. Decision: Converse renders `tool_reference` as `Loaded tool: <name>` lines and keeps dropping `thinking`/`redacted_thinking` instead of mapping them to `reasoningContent` — why: this adapter never returns reasoning, so a history thinking block comes from another model with a signature the Converse model cannot verify, and Bedrock does not document reasoning input for the non-Claude families routed here — cost if wrong: if a family starts requiring prior reasoning, its multi-turn quality drops until a family-specific mapping is added.
5. Decision: register `collectors.NewGoCollector()` and `collectors.NewProcessCollector(...)` on the existing registry — why: no new module (procfs is already an indirect requirement), fixed labels only — cost if wrong: 38 more metric families (a few dozen series) per scrape.
6. Decision: model fallback evidence goes into `audit.RoutingRef` as two scalar fields (`model_fallback_from`, `model_fallback_source`), and a request without a policy decision gets a synthesized routing ref with `reason: model_fallback`, rather than a new `RequestRef` field — why: the audit console (ADR-057, on main) projects `routing.reason` into `audit_events.routing_reason`, so the console shows it without code changes; `TestRoutingDTOContainsOnlyScalarMetadata` forbids nested structs; `RoutingFrom` is already called by every record builder, so no record literal changes — cost if wrong: fallback requests that used to omit `routing` now carry it (append-only; mixed-chain verification pinned in Task 5).
7. Decision: the fallback log is one line per distinct (requested, served) pair per process, at most 256 pairs, the requested name quoted and cut at 128 bytes; the counter is labeled only `{source, model}` with the configured served model — why: requested names are client input (cardinality and log injection) and auto mode sends a classifier request on every tool call — cost if wrong: after 256 distinct typos new ones are only counted, with one cap notice in the log.
8. Decision: replace go-oidc's `RemoteKeySet` with a cache-first key set (`internal/adminauth/jwks.go`): a cached kid never consults the limiter, a miss refetches single-flight with concurrent misses waiting for that fetch, and only a fetch that still leaves the token unverified (or fails) arms the backoff window; `go-jose/v4` becomes a direct requirement (already in the module graph through go-oidc) — why: "per kid" isolation of verification without per-kid fetch budgets, which an attacker could multiply with random kids into IdP load — cost if wrong: a key rotated just after a flood armed the window is picked up up to one window (10 s) later; the flood's own next refetch also picks it up.
9. Decision: `server.max_inflight_request_bytes` defaults to 0 (off) and, when set, must be at least `server.max_request_bytes` — why: every other new refusal mode in this repository is opt-in, the right value depends on the memory limit the gateway cannot always see (systemd `MemoryMax`, `GOMEMLIMIT`), and Claude Code/Codex turn a 503 into backoff retries; the docs give the sizing rule (memory limit − idle RSS) / 6 from the measured ≈5.5× body amplification — cost if wrong: deployments that do not set it keep today's unbounded aggregate body memory (the current state on this host).
10. Decision: the budget never queues; a request reserves its Content-Length, or the whole per-request cap when the length is undeclared, until its handler returns (a stream until it ends); a count that does not fit is answered locally as `max(1, bytes/4)` from its length (or a bounded discard read) without buffering — why: no head-of-line blocking or timers, chunked bodies are rare, and counts must stay HTTP 200 without holding memory — cost if wrong: under overload a count is less accurate than the content estimate, and one chunked upload can occupy a budget equal to the cap.
11. Decision: error shapes for gateway refusals: 413 — Anthropic `{"type":"error","error":{"type":"request_too_large",…}}`, Chat/Responses `{"error":{"type":"invalid_request_error","code":null,…}}`, Bedrock `ValidationException`; in-flight 503 + `Retry-After: 2` — Anthropic `overloaded_error`, Chat/Responses `server_error` with code `gateway_overloaded`, Bedrock `ServiceUnavailableException` — all through `writeIngressError` (path prefix `/model/` → AWS, `/v1/messages` → Anthropic, else OpenAI) and the existing `writeErr`/`writeError` helpers inside handlers — why: mirrors `auditGateMiddleware`'s existing routing and Anthropic's own 413 type — cost if wrong: a client that special-cases another type string sees a generic error, as before.
12. Decision: the estimator lives in `internal/tokenest` (stdlib leaf) and uses BPE-style character-class runs over decoded JSON text, keys included; inline images at recognized positions cost 1,600 tokens each (Claude's ≈1.15 MP ceiling; OpenAI high detail is lower); other base64 (documents, signatures, encrypted reasoning) keeps four bytes per token; Hangul costs 0.8 per syllable (natural Korean is 0.77 on o200k; uniformly random syllables, the measured dense case, are ≈2.1 and stay under-estimated) — why: calibrated against o200k_base, the tokenizer gpt-oss uses and whose count Bedrock reported in the measured rejection, the generators land within 1% for prose, code, Read output and digits and reproduce the three measured cases within 10% — cost if wrong: Claude's tokenizer can differ by tens of percent on some content; an under-estimate costs one free upstream rejection that Task 1 makes recoverable, an over-estimate compacts early.
13. Decision: no exact provider token-count call near the window boundary — why: only the direct `anthropic` provider implements `providers.TokenCounter`; Bedrock's CountTokens API is documented for Anthropic Claude models, while the measured failures were non-Claude Converse models; a remote count would add a network round trip and make the gate depend on another service; a core interface change would be needed for Bedrock — cost if wrong: a borderline request costs one rejected (unbilled, ≈1 s) upstream call before the client compacts.
14. Decision: one estimate serves the context gate, every retry target's gate, both count fallbacks, the TPM PreCheck and the router's capacity for the explicitly requested Responses model; the sensitivity inspector's conservative ceiling for automatic, strict-budget and InternalOnly choices stays unchanged — why: the count a client reads, the overflow it is told about and the TPM reservation must agree, while ADR-043/044 deliberately keep automatic choices conservative — cost if wrong: TPM-limited keys see admission shift in both directions (pinned in Task 11).
15. Decision: item 10 (partial or stalled stream cost) is a ruling, not a task — why: the code matches ADR-054: a stream without final usage settles with `complete=false`, `SettleBudget` keeps the local hold as liability (`internal/server/requestpolicy/authority.go` `SettleBudget`, `localObservedCost`), and analytics classifies it as `cost_state` `unavailable` (no cost) or `uncertain` (partial) in `internal/analytics/evidence.go` `EvidenceOf`; no spend is invented — cost if wrong: none found.
16. Decision: the live check sends at most two requests that Bedrock can bill (gpt-oss at $0.15/M input, `max_tokens` 16: ≈94k and ≈110k input tokens, about $0.03 together); every other live request is refused either by the gateway or by Bedrock because it exceeds the model window by at least 2% (gpt-oss) or 2× by the o200k count (Claude Haiku 4.5), and Bedrock does not bill refused requests (2026-10-27 measurement) — why: the program caps live cost at US$1 — cost if wrong: every loop stops at the first unexpected 200, so the worst case is one accepted Claude body (about $0.40) plus the expected $0.03.

## Global Constraints

- Worktree `/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree`, branch `fix/gateway-path-remediation`, base main `26ad568`. Scratch and logs: `/home/atomoh/.tmp/inferplane-path-remediation-20261029/` (`$L`), logs in `$L/logs/`, live check in `$L/verify/`. Never `/tmp`; no `rm -rf`.
- Never run `git stash`, `git checkout`, `git restore`, `git reset` or `git clean`. Commit with `git commit -s` and the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. No push, no merge.
- Go 1.26.10, module `github.com/inferplane/inferplane`, pure-Go dependencies, both binaries `CGO_ENABLED=0`. Every `go build`/`go test` uses `-buildvcs=false` (VCS stamping fails in this linked worktree). Never pipe build or test output to `head`/`tail`: redirect to a log and grep it (`grep -a`, logs can contain binary stream frames).
- Gates: `gofmt -l .` empty, `go vet ./...` clean, `go test ./... -race` green, `bash tests/run-all.sh`, `python3 scripts/check_docs_translations.py`. Every edited doc under `docs/` with a `.ko.md` pair changes both and refreshes `translation_source_sha256` (the snippet in each task). Korean text is written as literal UTF-8.
- Each `In <file>, replace` step is an exact `Edit` (old text must match once); line numbers in **Files** are where each edit lands when the task's edits are applied in order. Code is gofmt-clean as written.
- Security: secrets only via `env:`/`file:` refs; never print the admin token or a virtual key (use files with mode 0600 and `curl -H @file`); `/metrics` must carry no secret, `key_id` or client input as a label; virtual keys stay SHA-256 at rest.
- Authenticated `count_tokens` answers stay HTTP 200, including policy, readiness and over-budget refusals; missing or invalid keys still get 401 first.
- Cache invariant: same-protocol requests forward `RawBody` verbatim; nothing here rewrites request bodies.
- Audit fields are append-only and `omitempty`; old audit files must still pass `mayu audit verify`.
- Provider isolation: no provider package is added; `providers/context_overflow.go` is shared provider-surface code, not core.
- When a task changes a layer it updates `docs/reference/<layer>.md` (+ko) and the matching module `CLAUDE.md` in the same commit. `AGENTS.md` is generated: never edit it; Task 10 changes the root `CLAUDE.md`, so the controller regenerates `AGENTS.md` in Task 12 Step 9.
- Do not touch `mayu.service`, `/home/atomoh/inferplane-local` or the shared control plane. The live check runs a separate test `mayu` on 127.0.0.1:18281 (data) and 127.0.0.1:19291 (admin) with throwaway config and keys under `$L/verify/`, deleted at the end.

## Review Focus

Five failure modes most likely to bite a user that the item tests alone would not catch; each has a pinning test in the owning task.

1. **Screenshots in Claude Code tool results counted as base64 text.** Claude Code's Read tool returns an image inside `tool_result` content, often with `cache_control`. If that position were not recognized, a few screenshots would cost hundreds of thousands of estimated tokens and the gate would refuse at a fraction of the window. Pinned by the `anthropic#01` case of `TestEstimateChargesInlineImagesAtTheirCeiling` (Task 10).
2. **count_tokens and the overflow refusal disagreeing.** Claude Code reads both; a count below the window followed by a 400 with a larger `N` defeats its compaction sizing. Pinned by `TestCountAndContextGateAgree` on both count APIs (Task 11).
3. **TPM admission shifting for rate-limited keys.** The PreCheck reservation now follows the content estimate: prose that bytes/4 over-counted is admitted, hex and digit-heavy bodies reserve more. Pinned in both directions by `TestTPMPreCheckUsesContentEstimate` (Task 11).
4. **The in-flight budget held longer than expected.** A stream keeps its body (and its reservation) until it ends, and a request without Content-Length reserves the whole per-request cap; with a tight budget one long stream or chunked upload refuses everything else. Pinned by `TestInflightBudgetHeldByStreamsAndUndeclaredLengths` (Task 9).
5. **Audit compatibility of the new fallback evidence.** Fallback requests now carry `request.routing` even without a policy; a chain mixing old records with new ones must verify, and records without a fallback must keep their exact bytes. Pinned by `TestModelFallbackRecordsVerifyInMixedChains` (Task 5).

## File Structure

| Path | Task | Responsibility |
| --- | --- | --- |
| `providers/context_overflow.go` (+test) | 1 | Anchored overflow recognizer and recoverable body shared by provider packages |
| `providers/bedrock/errors.go`, `native_converse.go` (+`converse_overflow_test.go`) | 1 | Converse SDK and native overflow mapping |
| `providers/openaicompat/context_error.go` | 1 | Delegates to the shared recognizer |
| `internal/server/routingtest/fixture.go`, `internal/server/converse_overflow_test.go` | 1 | `Spy.Err`; cross-ingress overflow contract |
| `internal/router/request_routing.go` | 2, 11 | `context_window_exceeded` reason; estimator for the explicit Responses capacity |
| `internal/server/responsesapi/handler.go` | 2, 5, 8, 11 | 400 for size-only refusal; fallback evidence; 413 text; estimator |
| `providers/bedrock/client.go`, `converse.go` (+`tool_reference_test.go`) | 3 | `tool_reference` text; thinking decision |
| `internal/metrics/metrics.go` (+`runtime_test.go`) | 2, 4, 5, 9 | Reason allowlist; runtime/process collectors; fallback and in-flight metrics |
| `internal/live/live.go`, `internal/router/router.go`, `internal/router/fallback_log.go`, `internal/audit/routing.go` | 5 | Fallback source, bounded log, audit evidence |
| `internal/server/{anthropicapi,openaiapi,bedrockapi}` handlers | 5, 8, 11 | Fallback evidence; 413 on undeclared lengths; estimator |
| `internal/adminauth/jwks.go`, `oidc.go`, `go.mod` | 7 | Cache-first JWKS key set |
| `internal/server/ingress_error.go`, `server.go`, `inflight.go`, `internal/config/config.go`, `cmd/mayu/gateway.go` | 8, 9 | Protocol-shaped refusals; in-flight budget |
| `internal/tokenest/` (`text.go`, `estimate.go`, `media.go`, `tokenesttest/`, `testdata/fixtures/`) | 10 | Estimator, calibration generators, live-check bodies |
| `internal/server/requestpolicy/context.go` (+test) | 11 | Deleted (superseded by `tokenest`) |
| `docs/reference/{api,data,security,infrastructure,agent-llm}.md`, `docs/operations/observability.md`, `docs/getting-started/clients.md`, `docs/codex-launcher.md` (+ko) | 1–11 | User and operator docs |
| `CLAUDE.md`, `internal/CLAUDE.md`, `providers/CLAUDE.md`, `charts/inferplane/values.yaml` | 1–11 | Module notes and chart guidance |


---

### Task 1: Recoverable context overflow from Bedrock Converse

**Items:** program item 1

**User-visible change:** A non-Claude model on Bedrock Converse (SDK path, and the native `/model/{id}/converse[-stream]` ingress) that refuses an over-window request with `Input length (N) exceeds model's maximum context length (M).` (also behind the `Mantle streaming error for requestId …:` prefix) or with a relayed OpenAI `context_length_exceeded` error now reaches every ingress as the recoverable error: `prompt is too long: N tokens > M maximum` plus `error.code: context_length_exceeded`. Before: `bedrock upstream error (ValidationException)` on Messages, Chat and Bedrock and `upstream request failed` without a recovery code on Responses, so Claude Code ended the session instead of compacting (2026-10-27 path check). Unrecognized validation errors stay scrubbed.

**Files:**
- Test (create): `providers/context_overflow_test.go`
- Test (create): `providers/bedrock/converse_overflow_test.go`
- Test (modify): `internal/server/routingtest/fixture.go` (edits at lines 31–35, 47–51, 57–61)
- Test (create): `internal/server/converse_overflow_test.go`
- Create: `providers/context_overflow.go`
- Modify: `providers/openaicompat/context_error.go` (edits at lines 3–18, 46, 77–106)
- Modify: `providers/bedrock/errors.go` (edits at lines 6–10, 126–152)
- Modify: `providers/bedrock/native_converse.go` (edits at lines 66–71, 88)
- Modify: `docs/reference/api.md` (edits at lines 50)
- Modify: `docs/reference/api.ko.md` (edits at lines 69)
- Modify: `providers/CLAUDE.md` (edits at lines 11)

**Interfaces:**
- Produces: `func providers.ContextOverflowCounts(message string) (tokens, maximum int64, ok bool)`; `func providers.PromptTooLongBody(tokens, maximum int64) []byte` (tokens 0 = no counts); bedrock-internal `promptOverflowMessage(message string) (tokens, maximum int64, ok bool)` and `nativeOverflow(resp *http.Response, raw []byte) *providers.UpstreamError`; test helper field `routingtest.Spy.Err error`.
- Consumes: `providers.ResponseErrorCode`, `providers.UpstreamError`, `nativeFixture` (bedrock tests), `policyIngresses`/`policyMux`/`policyDo` (server tests).

- [ ] **Step 1: Write the failing tests**

Create `providers/context_overflow_test.go`:

```go
package providers

import (
	"encoding/json"
	"testing"
)

func TestContextOverflowCounts(t *testing.T) {
	for _, tc := range []struct {
		name, message   string
		tokens, maximum int64
		ok              bool
	}{
		{"anthropic", "prompt is too long: 250001 tokens > 200000 maximum", 250001, 200000, true},
		{"anthropic capitalized", "Prompt is too long: 250001 tokens > 200000 maximum", 250001, 200000, true},
		{"converse input length", "Input length (135043) exceeds model's maximum context length (131072).", 135043, 131072, true},
		{"vllm messages", "This model's maximum context length is 4096 tokens. However, you requested 5000 tokens in the messages, Please reduce the length of the messages.", 5000, 4096, true},
		{"vllm completion", "This model's maximum context length is 4096 tokens. However, you requested 5500 tokens (5000 in the messages, 500 in the completion). Please reduce the length of the messages or completion.", 5500, 4096, true},
		{"suffix", "Input length (135043) exceeds model's maximum context length (131072). arn:aws:private", 0, 0, false},
		{"not over", "Input length (131072) exceeds model's maximum context length (131072).", 0, 0, false},
		{"zero limit", "prompt is too long: 1 tokens > 0 maximum", 0, 0, false},
		{"overflowing number", "prompt is too long: 99999999999999999999999 tokens > 1 maximum", 0, 0, false},
		{"inconsistent split", "This model's maximum context length is 4096 tokens. However, you requested 5500 tokens (4000 in the messages, 500 in the completion). Please reduce the length of the messages or completion.", 0, 0, false},
		{"unknown wording", "context window exceeded", 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokens, maximum, ok := ContextOverflowCounts(tc.message)
			if tokens != tc.tokens || maximum != tc.maximum || ok != tc.ok {
				t.Fatalf("got (%d, %d, %t), want (%d, %d, %t)", tokens, maximum, ok, tc.tokens, tc.maximum, tc.ok)
			}
		})
	}
}

func TestPromptTooLongBody(t *testing.T) {
	for _, tc := range []struct {
		tokens, maximum int64
		message         string
	}{
		{135043, 131072, "prompt is too long: 135043 tokens > 131072 maximum"},
		{0, 0, "prompt is too long"},
	} {
		var got struct {
			Type  string `json:"type"`
			Error struct{ Type, Code, Message string }
		}
		body := PromptTooLongBody(tc.tokens, tc.maximum)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.Type != "error" || got.Error.Type != "invalid_request_error" || got.Error.Code != "context_length_exceeded" ||
			got.Error.Message != tc.message || ResponseErrorCode(body) != "context_length_exceeded" {
			t.Fatalf("unrecoverable overflow body: %s", body)
		}
	}
}
```

Create `providers/bedrock/converse_overflow_test.go`:

```go
package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/inferplane/inferplane/providers"
)

// Captured from Bedrock Converse for openai.gpt-oss-120b-1:0 on 2026-10-27
// (mayu server log): the non-streaming and ConverseStream rejections of a
// 484 KB request whose real size was 135,043 tokens.
const (
	converseOverflow       = `The model returned the following errors: ErrorEvent { error: APIError { type: "BadRequestError", code: Some(400), message: "Input length (135043) exceeds model's maximum context length (131072).", param: None } }`
	converseStreamOverflow = `The model returned the following errors: Mantle streaming error for requestId 05a72250-6b0b-4e21-9207-b29de3146875: ErrorEvent { error: APIError { type: "BadRequestError", code: Some(400), message: "Input length (140026) exceeds model's maximum context length (131072).", param: None } }`
	// Observed for gpt-6 family models: the relayed OpenAI error envelope.
	converseMinTokens = `The model returned the following errors: {"error":{"code":"integer_below_min_value","message":"Invalid 'max_output_tokens': integer below minimum value. Expected a value >= 16, but got 1 instead.","param":"max_output_tokens","type":"invalid_request_error"}}`
)

func overflowMessage(t *testing.T, err error) (status int, code, message string) {
	t.Helper()
	var upstream *providers.UpstreamError
	if !errors.As(err, &upstream) {
		t.Fatalf("not an upstream error: %v", err)
	}
	var body struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if json.Unmarshal(upstream.Body, &body) != nil {
		t.Fatalf("unreadable body: %s", upstream.Body)
	}
	return upstream.StatusCode, body.Error.Code, body.Error.Message
}

func TestConverseOverflowBecomesRecoverablePromptTooLong(t *testing.T) {
	for _, tc := range []struct{ name, upstream, want string }{
		{"converse", converseOverflow, "prompt is too long: 135043 tokens > 131072 maximum"},
		{"converse stream", converseStreamOverflow, "prompt is too long: 140026 tokens > 131072 maximum"},
		{"relayed openai code", `The model returned the following errors: {"error":{"code":"context_length_exceeded","message":"private prompt text","param":"input","type":"invalid_request_error"}}`, "prompt is too long"},
		{"claude on converse", "prompt is too long: 250001 tokens > 200000 maximum", "prompt is too long: 250001 tokens > 200000 maximum"},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				sdkErr := fmt.Errorf("bedrock: converse %q: %w", "m", &brtypes.ValidationException{Message: aws.String(tc.upstream)})
				p := &provider{conv: &fakeConverser{err: sdkErr}, modelAPI: map[string]string{"m": "converse"}}
				req := &providers.ProxyRequest{Model: "m", Upstream: "m", RawBody: []byte(`{"messages":[{"role":"user","content":"hi"}]}`), Stream: stream}
				var err error
				if stream {
					_, err = p.Stream(context.Background(), req)
				} else {
					_, err = p.Complete(context.Background(), req)
				}
				status, code, message := overflowMessage(t, err)
				if status != 400 || code != "context_length_exceeded" || message != tc.want {
					t.Fatalf("got %d %q %q, want 400 context_length_exceeded %q", status, code, message, tc.want)
				}
			})
		}
	}
}

func TestConverseNonOverflowValidationStaysScrubbed(t *testing.T) {
	for _, upstream := range []string{
		converseMinTokens,
		strings.Replace(converseOverflow, "(131072).", "(131072). arn:aws:iam::123456789012:role/private", 1),
		strings.Replace(converseOverflow, "Input length", "Output length", 1),
		strings.Replace(converseOverflow, "BadRequestError", "InternalError", 1),
		"The model returned the following errors: " + strings.Repeat("x", 70<<10),
	} {
		got := upstreamError(&brtypes.ValidationException{Message: aws.String(upstream)})
		if providers.ResponseErrorCode(got.Body) != "" || strings.Contains(string(got.Body), "arn:aws") ||
			strings.Contains(string(got.Body), "Invalid 'max_output_tokens'") || got.StatusCode != 400 {
			t.Fatalf("unrecognized validation was classified or leaked: %s", got.Body)
		}
	}
}

func TestNativeConverseOverflowBecomesRecoverablePromptTooLong(t *testing.T) {
	for _, tc := range []struct {
		name, header, body string
		recognized         bool
	}{
		{"typed header", "ValidationException:http://internal.amazon.com/coral/com.amazon.bedrock/", mustJSON(t, map[string]string{"message": converseOverflow}), true},
		{"typed body", "", mustJSON(t, map[string]string{"__type": "com.amazon.bedrock#ValidationException", "message": converseStreamOverflow}), true},
		{"untyped", "", mustJSON(t, map[string]string{"message": converseOverflow}), false},
		{"other validation", "ValidationException", mustJSON(t, map[string]string{"message": converseMinTokens}), false},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				n := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) {
					if tc.header != "" {
						w.Header().Set("X-Amzn-ErrorType", tc.header)
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(tc.body))
				})
				req := &providers.ProxyRequest{Upstream: "m", RawBody: []byte(`{"messages":[]}`)}
				var err error
				if stream {
					_, err = n.Stream(context.Background(), req)
				} else {
					_, err = n.Complete(context.Background(), req)
				}
				status, code, message := overflowMessage(t, err)
				if !tc.recognized {
					if code != "" || message != "bedrock Converse upstream rejected the request" || status != 400 {
						t.Fatalf("unrecognized native error classified or leaked: %d %q %q", status, code, message)
					}
					return
				}
				if status != 400 || code != "context_length_exceeded" || !strings.HasPrefix(message, "prompt is too long: 1") {
					t.Fatalf("native overflow not recoverable: %d %q %q", status, code, message)
				}
			})
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
```

In `internal/server/routingtest/fixture.go`, replace:

```go
type Spy struct {
	Calls         []Call
	Fail, Partial bool
	Usage         *schema.Usage
}
```

with:

```go
type Spy struct {
	Calls         []Call
	Fail, Partial bool
	Usage         *schema.Usage
	// Err, when set, is returned by Complete and Stream before any output,
	// e.g. a provider's *providers.UpstreamError.
	Err error
}
```

In `internal/server/routingtest/fixture.go`, replace:

```go
	s.record(ctx, r)
	if s.Fail {
		return nil, errors.New("transport failed")
	}
	return &providers.ProxyResponse{
```

with:

```go
	s.record(ctx, r)
	if s.Err != nil {
		return nil, s.Err
	}
	if s.Fail {
		return nil, errors.New("transport failed")
	}
	return &providers.ProxyResponse{
```

In `internal/server/routingtest/fixture.go`, replace:

```go
	s.record(ctx, r)
	if s.Fail {
		return nil, errors.New("transport failed")
	}
	return func(yield
```

with:

```go
	s.record(ctx, r)
	if s.Err != nil {
		return nil, s.Err
	}
	if s.Fail {
		return nil, errors.New("transport failed")
	}
	return func(yield
```

Create `internal/server/converse_overflow_test.go`:

```go
package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/inferplane/inferplane/internal/server/routingtest"
	"github.com/inferplane/inferplane/providers"
)

// Bedrock Converse and native Converse now return providers.PromptTooLongBody
// for recognized overflow. Pin what each ingress's client receives: its own
// envelope, the recoverable code or wording, and no retry elsewhere.
func TestRecoverableOverflowOnEveryIngress(t *testing.T) {
	const want = "prompt is too long: 135043 tokens > 131072 maximum"
	ingresses := append(append([]policyIngress(nil), policyIngresses...),
		policyIngress{"responses", "/v1/responses", false}, policyIngress{"responses", "/v1/responses", true})
	for _, ing := range ingresses {
		t.Run(ing.name(), func(t *testing.T) {
			f := routingtest.New(t, nil)
			f.Public.Err = &providers.UpstreamError{StatusCode: 400, Body: providers.PromptTooLongBody(135043, 131072)}
			body := routingtest.Body(ing.protocol, "clean", ing.stream)
			if ing.protocol == "responses" {
				body = `{"model":"premium","stream":` + strconv.FormatBool(ing.stream) + `,"store":false,"max_output_tokens":16,"input":"hello"}`
			}
			rec := policyDo(policyMux(f, nil, nil, nil, nil), ing.path, body, true)
			var reply struct {
				Message string                               `json:"message"`
				Error   struct{ Type, Code, Message string } `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
				t.Fatalf("unreadable reply %d: %s", rec.Code, rec.Body.String())
			}
			if rec.Code != http.StatusBadRequest || len(f.Public.Calls) != 1 {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, len(f.Public.Calls), rec.Body.String())
			}
			switch ing.protocol {
			case "bedrock":
				if rec.Header().Get("X-Amzn-ErrorType") != "ValidationException" || reply.Message != want {
					t.Fatalf("Bedrock client cannot recover: %s", rec.Body.String())
				}
			case "responses":
				if reply.Error.Code != "context_length_exceeded" || reply.Error.Type != "invalid_request_error" {
					t.Fatalf("Codex cannot compact: %s", rec.Body.String())
				}
			default: // Messages tees the Anthropic body; Chat clients read error.code
				if reply.Error.Message != want || reply.Error.Code != "context_length_exceeded" {
					t.Fatalf("client cannot recover: %s", rec.Body.String())
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run them and confirm they fail**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
go test -buildvcs=false -count=1 -run 'ContextOverflow|PromptTooLong|ConverseOverflow|NonOverflow|RecoverableOverflow' ./providers/ ./providers/bedrock/ ./internal/server/ > $L/logs/t1-fail.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL|ok )|undefined|unknown field' $L/logs/t1-fail.log | sort -u
```

Expected: `exit=1`; `providers [build failed]` (undefined: ContextOverflowCounts, PromptTooLongBody), `internal/server [build failed]` (undefined: providers.PromptTooLongBody), and in `providers/bedrock` `--- FAIL: TestConverseOverflowBecomesRecoverablePromptTooLong` and `--- FAIL: TestNativeConverseOverflowBecomesRecoverablePromptTooLong`.

- [ ] **Step 3: Implement**

Create `providers/context_overflow.go`:

```go
package providers

import (
	"encoding/json"
	"regexp"
	"strconv"
)

// Anchored context-overflow wordings that carry numeric evidence. Only these
// exact shapes are recognized; suffixes, unknown wording and provider text
// never cross protocols.
var (
	// Anthropic, including Claude on Bedrock InvokeModel and Converse.
	anthropicOverflow = regexp.MustCompile(`(?i)^prompt is too long: ([0-9]+) tokens > ([0-9]+) maximum$`)
	// vLLM v0.8.5 serving_engine.py context validation (Chat Completions).
	vllmMessagesOverflow   = regexp.MustCompile(`^This model's maximum context length is ([0-9]+) tokens\. However, you requested ([0-9]+) tokens in the messages, Please reduce the length of the messages\.$`)
	vllmCompletionOverflow = regexp.MustCompile(`^This model's maximum context length is ([0-9]+) tokens\. However, you requested ([0-9]+) tokens \(([0-9]+) in the messages, ([0-9]+) in the completion\)\. Please reduce the length of the messages or completion\.$`)
	// The serving engine behind Bedrock Converse for open-weight models
	// (observed 2026-10-27, openai.gpt-oss-120b-1:0).
	inputLengthOverflow = regexp.MustCompile(`^Input length \(([0-9]+)\) exceeds model's maximum context length \(([0-9]+)\)\.$`)
)

// ContextOverflowCounts returns the validated input total and limit of a
// recognized context-overflow message. The total must exceed a positive
// limit; a reported messages/completion split must add up exactly.
func ContextOverflowCounts(message string) (tokens, maximum int64, ok bool) {
	var values []int64
	tokensFirst := true
	for _, pattern := range []*regexp.Regexp{anthropicOverflow, inputLengthOverflow, vllmMessagesOverflow, vllmCompletionOverflow} {
		match := pattern.FindStringSubmatch(message)
		if match == nil {
			continue
		}
		tokensFirst = pattern == anthropicOverflow || pattern == inputLengthOverflow
		for _, raw := range match[1:] {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value <= 0 {
				return 0, 0, false
			}
			values = append(values, value)
		}
		break
	}
	if values == nil {
		return 0, 0, false
	}
	if tokensFirst {
		tokens, maximum = values[0], values[1]
	} else {
		maximum, tokens = values[0], values[1]
	}
	if tokens <= maximum {
		return 0, 0, false
	}
	// Validate the provider's split without overflowing addition.
	if len(values) == 4 && (values[2] >= tokens || tokens-values[2] != values[3]) {
		return 0, 0, false
	}
	return tokens, maximum, true
}

// PromptTooLongBody is the recoverable overflow error every ingress already
// understands: Anthropic's envelope with error.code context_length_exceeded
// (Responses and Chat clients read the code) and Claude Code's "prompt is too
// long" wording, with the validated counts when known (tokens > 0).
func PromptTooLongBody(tokens, maximum int64) []byte {
	message := "prompt is too long"
	if tokens > 0 && maximum > 0 {
		message += ": " + strconv.FormatInt(tokens, 10) + " tokens > " + strconv.FormatInt(maximum, 10) + " maximum"
	}
	body, _ := json.Marshal(struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{Type: "error", Error: struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Type: "invalid_request_error", Code: "context_length_exceeded", Message: message}})
	return body
}
```

In `providers/openaicompat/context_error.go`, replace:

```go
import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"

	"github.com/inferplane/inferplane/providers"
)

// These exact Chat Completions messages come from vLLM v0.8.5's
// serving_engine.py context validation. Anchor the whole message: only validated
// numeric counts may cross protocols, never arbitrary provider text or suffixes.
var vllmMessagesOverflow = regexp.MustCompile(`^This model's maximum context length is ([0-9]+) tokens\. However, you requested ([0-9]+) tokens in the messages, Please reduce the length of the messages\.$`)
var vllmCompletionOverflow = regexp.MustCompile(`^This model's maximum context length is ([0-9]+) tokens\. However, you requested ([0-9]+) tokens \(([0-9]+) in the messages, ([0-9]+) in the completion\)\. Please reduce the length of the messages or completion\.$`)
```

with:

```go
import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/inferplane/inferplane/providers"
)
```

In `providers/openaicompat/context_error.go`, replace:

```go
			if tokens, maximum, ok := vllmOverflowCounts(upstreamMessage); ok {
```

with:

```go
			// Exact vLLM v0.8.5 wording (and the other anchored shapes
			// providers.ContextOverflowCounts knows): only validated numeric
			// counts cross protocols, never provider text or suffixes.
			if tokens, maximum, ok := providers.ContextOverflowCounts(upstreamMessage); ok {
```

In `providers/openaicompat/context_error.go`, replace:

```go
	return converted, header
}

func vllmOverflowCounts(message string) (tokens, maximum int64, ok bool) {
	match := vllmMessagesOverflow.FindStringSubmatch(message)
	if match == nil {
		match = vllmCompletionOverflow.FindStringSubmatch(message)
	}
	if match == nil {
		return 0, 0, false
	}
	values := make([]int64, len(match)-1)
	for i, raw := range match[1:] {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			return 0, 0, false
		}
		values[i] = value
	}
	maximum, tokens = values[0], values[1]
	if tokens <= maximum {
		return 0, 0, false
	}
	// Validate the provider's split without overflowing addition.
	if len(values) == 4 && (values[2] >= tokens || tokens-values[2] != values[3]) {
		return 0, 0, false
	}
	return tokens, maximum, true
}
```

with:

```go
	return converted, header
}
```

In `providers/bedrock/errors.go`, replace:

```go
	"log"
	"regexp"
	"strconv"

	smithy
```

with:

```go
	"log"
	"regexp"
	"strings"

	smithy
```

In `providers/bedrock/errors.go`, replace:

```go
var promptOverflowPattern = regexp.MustCompile(`(?i)^prompt is too long: ([0-9]+) tokens > ([0-9]+) maximum$`)

// Only the SDK's typed validation exception with this complete numeric shape
// is recovery evidence. Extra text, unknown wording and generic errors retain
// the normal scrubbed error; no provider string becomes a client message.
func promptOverflow(err error) *providers.UpstreamError {
	var validation *brtypes.ValidationException
	if !errors.As(err, &validation) {
		return nil
	}
	match := promptOverflowPattern.FindStringSubmatch(validation.ErrorMessage())
	if match == nil {
		return nil
	}
	input, inputErr := strconv.ParseInt(match[1], 10, 64)
	limit, limitErr := strconv.ParseInt(match[2], 10, 64)
	if inputErr != nil || limitErr != nil || limit <= 0 || input <= limit {
		return nil
	}
	body, _ := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]string{"type": "invalid_request_error", "code": "context_length_exceeded",
			"message": "prompt is too long: " + strconv.FormatInt(input, 10) + " tokens > " + strconv.FormatInt(limit, 10) + " maximum"},
	})
	return &providers.UpstreamError{StatusCode: 400, Body: body}
}
```

with:

```go
// Open-weight models on Converse relay their serving engine's error inside
// this prefix, optionally after a Mantle streaming prefix (both observed
// 2026-10-27 for openai.gpt-oss-120b-1:0, streaming and not).
const relayedErrorPrefix = "The model returned the following errors: "

var (
	mantleStreamingPrefix = regexp.MustCompile(`^Mantle streaming error for requestId [0-9A-Fa-f-]{36}: `)
	relayedErrorEvent     = regexp.MustCompile(`^ErrorEvent \{ error: APIError \{ type: "BadRequestError", code: Some\(400\), message: "([^"\\]*)", param: None \} \}$`)
)

// promptOverflowMessage recognizes context-overflow evidence in a Bedrock
// ValidationException message: Claude's own wording, a relayed serving-engine
// ErrorEvent with a recognized overflow message, or a relayed OpenAI error
// whose code is exactly context_length_exceeded (no counts). tokens is 0 when
// the evidence carries no counts. Nothing else is evidence.
func promptOverflowMessage(message string) (tokens, maximum int64, ok bool) {
	if tokens, maximum, ok := providers.ContextOverflowCounts(message); ok {
		return tokens, maximum, true
	}
	relayed, found := strings.CutPrefix(message, relayedErrorPrefix)
	if !found {
		return 0, 0, false
	}
	if prefix := mantleStreamingPrefix.FindString(relayed); prefix != "" {
		relayed = relayed[len(prefix):]
	}
	if match := relayedErrorEvent.FindStringSubmatch(relayed); match != nil {
		return providers.ContextOverflowCounts(match[1])
	}
	if len(relayed) <= 64<<10 && providers.ResponseErrorCode([]byte(relayed)) == "context_length_exceeded" {
		return 0, 0, true
	}
	return 0, 0, false
}

// Only the SDK's typed validation exception with recognized overflow evidence
// is recovery evidence. Extra text, unknown wording and generic errors retain
// the normal scrubbed error; no provider string becomes a client message.
func promptOverflow(err error) *providers.UpstreamError {
	var validation *brtypes.ValidationException
	if !errors.As(err, &validation) {
		return nil
	}
	tokens, maximum, ok := promptOverflowMessage(validation.ErrorMessage())
	if !ok {
		return nil
	}
	return &providers.UpstreamError{StatusCode: 400, Body: providers.PromptTooLongBody(tokens, maximum)}
}
```

In `providers/bedrock/native_converse.go`, replace:

```go
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		// Do not echo AWS error messages (may contain account IDs, ARNs or
		// request content). Ingress renders the status in AWS error shape.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		status := resp.StatusCode
```

with:

```go
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		// Do not echo AWS error messages (may contain account IDs, ARNs or
		// request content). Ingress renders the status in AWS error shape;
		// only recognized context-overflow evidence survives (errors.go).
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if overflow := nativeOverflow(resp, raw); overflow != nil {
			return nil, overflow
		}
		status := resp.StatusCode
```

In `providers/bedrock/native_converse.go`, replace:

```go
func (n *nativeConverseClient) Complete(ctx context.Context, req *providers.ProxyRequest) (*providers.ProxyResponse, error) {
```

with:

```go
// nativeOverflow applies the SDK path's overflow recognition to a native
// Converse error: an AWS JSON {"message": ...} body typed ValidationException
// by the X-Amzn-ErrorType header or the body's __type.
func nativeOverflow(resp *http.Response, raw []byte) *providers.UpstreamError {
	if resp.StatusCode != http.StatusBadRequest {
		return nil
	}
	var body struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return nil
	}
	kind, _, _ := strings.Cut(resp.Header.Get("X-Amzn-ErrorType"), ":")
	if kind != "ValidationException" && !strings.HasSuffix(body.Type, "ValidationException") {
		return nil
	}
	tokens, maximum, ok := promptOverflowMessage(body.Message)
	if !ok {
		return nil
	}
	return &providers.UpstreamError{StatusCode: http.StatusBadRequest, Body: providers.PromptTooLongBody(tokens, maximum)}
}

func (n *nativeConverseClient) Complete(ctx context.Context, req *providers.ProxyRequest) (*providers.ProxyResponse, error) {
```

In `docs/reference/api.md`, replace:

```markdown
A strictly recognized numeric prompt-overflow SDK validation error retains safe `context_length_exceeded` evidence and recognizable overflow wording; other Bedrock details remain scrubbed.
```

with:

```markdown
A strictly recognized numeric prompt-overflow SDK validation error retains safe `context_length_exceeded` evidence and recognizable overflow wording; other Bedrock details remain scrubbed. Bedrock Converse (non-Claude models, on the SDK path and the native `/converse` ingress) also recognizes the relayed serving-engine wording `Input length (N) exceeds model's maximum context length (M).` (observed 2026-10-27 for `openai.gpt-oss-120b-1:0`, with or without the Mantle streaming prefix) and a relayed OpenAI error whose code is exactly `context_length_exceeded` (no counts). Messages, Chat Completions, Bedrock Invoke/Converse and Responses then all return the same recoverable `prompt is too long` error in their own envelopes, instead of `bedrock upstream error (ValidationException)`.
```

In `docs/reference/api.ko.md`, replace:

```markdown
엄격한 숫자 형식의 프롬프트 초과 SDK 검증 오류만 안전한 context_length_exceeded 코드와 인식 가능한 초과 문구를 보존하고 나머지 Bedrock 원문은 숨깁니다.
```

with:

```markdown
엄격한 숫자 형식의 프롬프트 초과 SDK 검증 오류만 안전한 context_length_exceeded 코드와 인식 가능한 초과 문구를 보존하고 나머지 Bedrock 원문은 숨깁니다. Bedrock Converse(Claude 외 모델, SDK 경로와 네이티브 `/converse` 수신)는 서빙 엔진이 전달한 `Input length (N) exceeds model's maximum context length (M).` 문구(2026-10-27 `openai.gpt-oss-120b-1:0`에서 관측, Mantle 스트리밍 접두어 유무와 무관)와 코드가 정확히 `context_length_exceeded`인 OpenAI 오류(숫자 없음)도 인식합니다. 그러면 Messages·Chat Completions·Bedrock Invoke/Converse·Responses가 `bedrock upstream error (ValidationException)` 대신 각자의 오류 형식으로 같은 복구 가능한 `prompt is too long` 오류를 반환합니다.
```

Refresh the Korean companion hash after the English edit:

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
python3 - <<'EOF'
import hashlib, pathlib, re
for en in ["docs/reference/api.md"]:
    p = pathlib.Path(en); ko = p.with_suffix(".ko.md")
    h = hashlib.sha256(p.read_bytes()).hexdigest()
    t, n = re.subn(r"(?m)^translation_source_sha256: \S+$", "translation_source_sha256: " + h, ko.read_text(encoding="utf-8"), count=1)
    assert n == 1, ko
    ko.write_text(t, encoding="utf-8")
EOF
```

In `providers/CLAUDE.md`, replace:

```markdown
Recognized context-limit errors are normalized using bounded allowlisted evidence so clients can recover without receiving arbitrary upstream detail. Native same-protocol passthrough and translated errors have different contracts.
```

with:

```markdown
Recognized context-limit errors are normalized using bounded allowlisted evidence so clients can recover without receiving arbitrary upstream detail. Native same-protocol passthrough and translated errors have different contracts. `context_overflow.go` holds the one recognizer (`ContextOverflowCounts`: anchored Anthropic, vLLM and Converse `Input length` wordings) and recovery body (`PromptTooLongBody`) that `openaicompat/` and `bedrock/` share.
```

- [ ] **Step 4: Run the task checks**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
gofmt -l providers internal/server | (! grep .) \
  && go vet ./providers/... ./internal/server/ \
  && go test -buildvcs=false -race -count=1 ./providers/... ./internal/server/ > $L/logs/t1-pass.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL)' $L/logs/t1-pass.log
```

Expected: `exit=0` and no FAIL lines.

- [ ] **Step 5: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
git -C $W add providers/context_overflow_test.go providers/bedrock/converse_overflow_test.go internal/server/routingtest/fixture.go internal/server/converse_overflow_test.go providers/context_overflow.go providers/openaicompat/context_error.go providers/bedrock/errors.go providers/bedrock/native_converse.go docs/reference/api.md docs/reference/api.ko.md providers/CLAUDE.md
git -C $W commit -s -m "fix(bedrock): map Converse context overflow to the recoverable prompt-too-long error" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git -C $W status --short | grep -v '^??' ; echo clean-check-done
```

Expected: one new commit; `git status --short` lists no modified tracked files.


---

### Task 2: Responses bridge answers a context overflow with 400 context_length_exceeded

**Items:** program item 2

**User-visible change:** On `/v1/responses` through a stateless bridge (Bedrock, Anthropic or Chat Completions target), a request that every compatible target refuses only because of its declared context window now gets HTTP 400 `{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"request exceeds the configured context window"}}` and audit `outcome.error: context_window_exceeded`. Before: 403 `no policy-permitted compatible Responses target` (`no_safe_route`), which Codex retried five times. Capability, pricing, region, unknown-window and invalid-estimate refusals keep 403. On Messages, Chat and Bedrock a size-only refusal under a privacy or context policy now reports router reason `context_window_exceeded` instead of `no_safe_route` (audit, header, metric label); their wire status stays 403.

**Files:**
- Test (modify): `internal/router/responses_context_test.go` (edits at lines 78–93, 126–129, 157–160, 201–204)
- Test (create): `internal/server/responses_overflow_test.go`
- Modify: `internal/router/request_routing.go` (edits at lines 217–220, 376–377, 594–596, 716, 728)
- Modify: `internal/metrics/metrics.go` (edits at lines 153)
- Modify: `internal/server/responsesapi/handler.go` (edits at lines 121–123)
- Modify: `docs/reference/api.md` (edits at lines 89–91)
- Modify: `docs/reference/api.ko.md` (edits at lines 146)
- Modify: `docs/codex-launcher.md` (edits at lines 390)
- Modify: `docs/codex-launcher.ko.md` (edits at lines 209)
- Modify: `internal/CLAUDE.md` (edits at lines 55)

**Interfaces:**
- Produces: router denial reason `context_window_exceeded` (in `RequestRoutingError.Reason` and `RoutingDecision.Reason`, allowed by `metrics.ObserveRoutingDecision`); unexported `requestBoundary.ignoreContext bool` and `func (b requestBoundary) contextOnly(chain []ChainTarget) bool`; router test helper `requestedTokens(raw []byte) int64` (Task 11 changes its body).
- Consumes: `fitsRequest`, `denyRouting`, `finishRouting`, `writeCodedError`, `(*Handler).denied`.

- [ ] **Step 1: Write the failing tests**

In `internal/router/responses_context_test.go`, replace:

```go
func TestResponsesRequestedContextRetainsAdmissionGuards(t *testing.T) {
	for _, tt := range []struct {
		name   string
		window int64
		deny   bool
	}{
		{"exact input plus output", 2112, false},
		{"output exceeds remaining context", 2111, true},
		{"input exceeds context", 2047, true},
		{"unknown context", 0, true},
		{"missing tools capability", 4096, true},
		{"missing price", 4096, true},
		{"region denied", 4096, true},
		{"negative input estimate", 4096, true},
		{"negative output estimate", 4096, true},
	} {
```

with:

```go
// requestedTokens is the ingress estimate the router applies to the explicitly
// requested Responses model (handler preflight uses the same value).
func requestedTokens(raw []byte) int64 { return max(int64(1), int64(len(raw)/4)) }

func TestResponsesRequestedContextRetainsAdmissionGuards(t *testing.T) {
	_, probe := responsesContextSetup(t, responsesContextConfig(), true)
	est := requestedTokens(probe.RawBody)
	for _, tt := range []struct {
		name   string
		window int64
		reason string // empty: admitted
	}{
		{"exact input plus output", est + 64, ""},
		{"output exceeds remaining context", est + 63, "context_window_exceeded"},
		{"input exceeds context", est - 1, "context_window_exceeded"},
		{"unknown context", 0, "no_safe_route"},
		{"missing tools capability", 4096, "no_safe_route"},
		{"missing price", 4096, "no_safe_route"},
		{"region denied", 4096, "no_safe_route"},
		{"negative input estimate", 4096, "no_safe_route"},
		{"negative output estimate", 4096, "no_safe_route"},
	} {
```

In `internal/router/responses_context_test.go`, replace:

```go
			got, err := r.RouteRequest(context.Background(), in)
			if tt.deny {
				requireDenied(t, got, err, "no_safe_route")
			} else if err != nil || len(got.Chain) != 1 {
```

with:

```go
			got, err := r.RouteRequest(context.Background(), in)
			if tt.reason != "" {
				requireDenied(t, got, err, tt.reason)
			} else if err != nil || len(got.Chain) != 1 {
```

In `internal/router/responses_context_test.go`, replace:

```go
	// An ingress-promoted fallback must not inherit the requested-model estimate.
	in.Chain = in.Chain[1:]
	got, err = r.RouteRequest(context.Background(), in)
	requireDenied(t, got, err, "no_safe_route")
```

with:

```go
	// An ingress-promoted fallback must not inherit the requested-model estimate;
	// refused for its size alone, it reports the recoverable context reason.
	in.Chain = in.Chain[1:]
	got, err = r.RouteRequest(context.Background(), in)
	requireDenied(t, got, err, "context_window_exceeded")
```

In `internal/router/responses_context_test.go`, replace:

```go
			case "legacy substitute", "strict substitute":
				in.Model = "private"
				in.Chain, in.State, _ = r.ResolveChain(in.Model)
				if name == "strict substitute" {
```

with:

```go
			case "legacy substitute", "strict substitute":
				in.Model = "private"
				in.Chain, in.State, _ = r.ResolveChain(in.Model)
				// Too large for the substitute's conservative capacity alone.
				reason = "context_window_exceeded"
				if name == "strict substitute" {
```

Create `internal/server/responses_overflow_test.go`:

```go
package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/server/routingtest"
)

func responsesBody(t *testing.T, stream bool, input string, tools bool) string {
	t.Helper()
	body := map[string]any{"model": "premium", "stream": stream, "store": false, "max_output_tokens": 16, "input": input}
	if tools {
		body["tools"] = []any{map[string]any{"type": "function", "name": "read", "strict": false, "parameters": map[string]any{"type": "object"}}}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// The 2026-10-27 path check: Codex on the stateless bridge got 403 "no
// policy-permitted compatible Responses target" for an over-window request
// and retried it unchanged five times. A size-only refusal is now the same
// recoverable 400 the native preflight returns; other refusals keep 403.
func TestResponsesBridgeOverflowIsRecoverable(t *testing.T) {
	big := strings.Repeat("word ", 150_000) // ≥150k tokens by any estimate; window 100k
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "stream"}[stream], func(t *testing.T) {
			f := routingtest.New(t, nil)
			rec := policyDo(policyMux(f, nil, nil, nil, nil), "/v1/responses", responsesBody(t, stream, big, false), true)
			var reply struct {
				Error struct{ Type, Code, Message string } `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
				t.Fatalf("unreadable reply %d: %s", rec.Code, rec.Body.String())
			}
			if rec.Code != http.StatusBadRequest || reply.Error.Code != "context_length_exceeded" ||
				reply.Error.Type != "invalid_request_error" || len(f.Public.Calls) != 0 {
				t.Fatalf("over-window bridge request = %d %s, calls=%d; want recoverable 400 without an attempt", rec.Code, rec.Body.String(), len(f.Public.Calls))
			}

			// Not a size problem: the only target lacks the declared tools capability.
			f = routingtest.New(t, func(cfg *config.Config) {
				m := cfg.Models["premium"]
				m.Capabilities = nil
				cfg.Models["premium"] = m
			})
			rec = policyDo(policyMux(f, nil, nil, nil, nil), "/v1/responses", responsesBody(t, stream, "hello", true), true)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "no policy-permitted compatible Responses target") {
				t.Fatalf("non-context refusal changed: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}
```

- [ ] **Step 2: Run them and confirm they fail**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
go test -buildvcs=false -count=1 -run 'ResponsesRequestedContext|ResponsesRestricted|ResponsesBridgeOverflow' ./internal/router/ ./internal/server/ > $L/logs/t2-fail.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL|ok )|undefined|unknown field' $L/logs/t2-fail.log | sort -u
```

Expected: `exit=1`; `--- FAIL` for `TestResponsesRequestedContextRetainsAdmissionGuards`, `TestResponsesRequestedContextDoesNotRelaxModelFallback` and `TestResponsesRestrictedTargetsKeepConservativeCapacity` (the router still says `no_safe_route`) and for `TestResponsesBridgeOverflowIsRecoverable` (403 instead of 400).

- [ ] **Step 3: Implement**

In `internal/router/request_routing.go`, replace:

```go
		if responsesInspection != nil {
			transport := requestBoundary{router: r, in: in, apis: requestAPIs(st), inspection: responsesInspection}
			out.Chain = transport.filter(in.Chain, false)
		} else {
```

with:

```go
		if responsesInspection != nil {
			transport := requestBoundary{router: r, in: in, apis: requestAPIs(st), inspection: responsesInspection}
			out.Chain = transport.filter(in.Chain, false)
			if len(out.Chain) == 0 && transport.contextOnly(in.Chain) {
				return denyRouting(out, "context_window_exceeded", nil)
			}
		} else {
```

In `internal/router/request_routing.go`, replace:

```go
	out, err = finishRouting(out, safe)
	if err != nil || in.CountOnly {
```

with:

```go
	if len(safe) == 0 && !strictBudget && boundary.models == nil && boundary.contextOnly(in.Chain) {
		return denyRouting(out, "context_window_exceeded", nil)
	}
	out, err = finishRouting(out, safe)
	if err != nil || in.CountOnly {
```

In `internal/router/request_routing.go`, replace:

```go
	converseSafe bool
	budgetModel  string
}
```

with:

```go
	converseSafe bool
	budgetModel  string
	// ignoreContext drops only the context-size bound, so contextOnly can tell
	// a request that is too large from every other reason for no route.
	ignoreContext bool
}
```

In `internal/router/request_routing.go`, replace:

```go
			if !fitsRequest(mc, capacity) || st.Pricing() == nil || !st.Pricing().HasRate(ct.ProviderName, ct.Upstream) {
```

with:

```go
			if b.ignoreContext && capacity.InputTokens >= 0 && capacity.OutputTokens >= 0 {
				capacity.InputTokens, capacity.OutputTokens = 0, 0
			}
			if !fitsRequest(mc, capacity) || st.Pricing() == nil || !st.Pricing().HasRate(ct.ProviderName, ct.Upstream) {
```

In `internal/router/request_routing.go`, replace:

```go
func (b requestBoundary) compatible(ct ChainTarget, target config.Target) bool {
```

with:

```go
// contextOnly reports whether chain would route if the request fit: at least
// one candidate that is otherwise permitted, compatible, capable and priced was
// refused by its declared context window alone. Unknown windows and invalid
// (negative) estimates are not context-size refusals.
func (b requestBoundary) contextOnly(chain []ChainTarget) bool {
	if b.inspection == nil {
		return false
	}
	b.ignoreContext = true
	return len(b.filter(chain, false)) > 0
}

func (b requestBoundary) compatible(ct ChainTarget, target config.Target) bool {
```

In `internal/metrics/metrics.go`, replace:

```go
		"budget_target", "budget_target_unavailable", "legacy_mask":
```

with:

```go
		"budget_target", "budget_target_unavailable", "legacy_mask", "context_window_exceeded":
```

In `internal/server/responsesapi/handler.go`, replace:

```go
	if routeErr != nil {
		req = requestpolicy.Observe(w, req, result, h.metrics)
		status, reason := 403, result.Decision.Reason
```

with:

```go
	if routeErr != nil {
		req = requestpolicy.Observe(w, req, result, h.metrics)
		if result.Decision.Reason == "context_window_exceeded" {
			// Every compatible target refused the request for its size alone:
			// answer with the recoverable context error Codex compacts on, like
			// the preflight below, never a 403 it retries unchanged.
			h.denied(req, p, model, 400, "context_window_exceeded", start)
			writeCodedError(w, 400, "request exceeds the configured context window", "context_length_exceeded")
			return
		}
		status, reason := 403, result.Decision.Reason
```

In `docs/reference/api.md`, replace:

```markdown
Responses context-window preflight failures also return HTTP 400 with
`error.code="context_length_exceeded"`, without changing admission thresholds
or bypassing authentication.
```

with:

```markdown
Responses context-window preflight failures also return HTTP 400 with
`error.code="context_length_exceeded"`, without changing admission thresholds
or bypassing authentication. The stateless bridge returns the same 400 when every
compatible target refused the request for its declared context window alone
(router reason `context_window_exceeded`); it used to answer 403 `no
policy-permitted compatible Responses target`, which Codex retried unchanged.
Every other no-route reason keeps 403.
```

In `docs/reference/api.ko.md`, replace:

```markdown
Responses의 컨텍스트 사전 검사 거부도 HTTP 400과 `error.code="context_length_exceeded"`를 반환합니다. 기존 검사 기준과 인증 순서는 유지합니다.
```

with:

```markdown
Responses의 컨텍스트 사전 검사 거부도 HTTP 400과 `error.code="context_length_exceeded"`를 반환합니다. 기존 검사 기준과 인증 순서는 유지합니다. 무상태 브리지에서 모든 호환 대상이 선언된 컨텍스트 창 때문에만 요청을 거부하면(라우터 사유 `context_window_exceeded`) 같은 400을 반환합니다. 이전에는 403 `no policy-permitted compatible Responses target`을 반환해 Codex가 같은 요청을 다시 보냈습니다. 그 밖의 경로 없음 사유는 403을 유지합니다.
```

Refresh the Korean companion hash after the English edit:

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
python3 - <<'EOF'
import hashlib, pathlib, re
for en in ["docs/reference/api.md"]:
    p = pathlib.Path(en); ko = p.with_suffix(".ko.md")
    h = hashlib.sha256(p.read_bytes()).hexdigest()
    t, n = re.subn(r"(?m)^translation_source_sha256: \S+$", "translation_source_sha256: " + h, ko.read_text(encoding="utf-8"), count=1)
    assert n == 1, ko
    ko.write_text(t, encoding="utf-8")
EOF
```

In `docs/codex-launcher.md`, replace:

```markdown
inventing usage. Already-committed SSE failure handling is unchanged.
```

with:

```markdown
inventing usage. Already-committed SSE failure handling is unchanged.

On the stateless bridge, a request too large for every compatible target's
declared context window returns HTTP 400 with
`error.code="context_length_exceeded"`, so Codex compacts. It used to get 403
`no policy-permitted compatible Responses target` and resend the same request
five times (2026-10-27 path check).
```

In `docs/codex-launcher.ko.md`, replace:

```markdown
이미 전송이 시작된 SSE의 실패 처리는 변경하지 않습니다.
```

with:

```markdown
이미 전송이 시작된 SSE의 실패 처리는 변경하지 않습니다.

무상태 브리지에서 모든 호환 대상의 선언된 컨텍스트 창보다 큰 요청은 HTTP 400과
`error.code="context_length_exceeded"`를 반환하므로 Codex가 압축합니다. 이전에는
403 `no policy-permitted compatible Responses target`을 받아 같은 요청을 다섯 번
다시 보냈습니다(2026-10-27 경로 점검).
```

Refresh the Korean companion hash after the English edit:

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
python3 - <<'EOF'
import hashlib, pathlib, re
for en in ["docs/codex-launcher.md"]:
    p = pathlib.Path(en); ko = p.with_suffix(".ko.md")
    h = hashlib.sha256(p.read_bytes()).hexdigest()
    t, n = re.subn(r"(?m)^translation_source_sha256: \S+$", "translation_source_sha256: " + h, ko.read_text(encoding="utf-8"), count=1)
    assert n == 1, ko
    ko.write_text(t, encoding="utf-8")
EOF
```

In `internal/CLAUDE.md`, replace:

```markdown
`live.State`. Lookup rejection must be checked even if there are no returned rules.
```

with:

```markdown
`live.State`. Lookup rejection must be checked even if there are no returned rules.
A chain emptied only by declared context windows denies with reason
`context_window_exceeded` (Responses answers 400 `context_length_exceeded`; other
ingresses keep their 403 shape).
```

- [ ] **Step 4: Run the task checks**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
gofmt -l internal | (! grep .) \
  && go vet ./internal/router/ ./internal/metrics/ ./internal/server/... \
  && go test -buildvcs=false -race -count=1 ./internal/router/ ./internal/metrics/ ./internal/server/... > $L/logs/t2-pass.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL)' $L/logs/t2-pass.log
```

Expected: `exit=0` and no FAIL lines.

- [ ] **Step 5: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
git -C $W add internal/router/responses_context_test.go internal/server/responses_overflow_test.go internal/router/request_routing.go internal/metrics/metrics.go internal/server/responsesapi/handler.go docs/reference/api.md docs/reference/api.ko.md docs/codex-launcher.md docs/codex-launcher.ko.md internal/CLAUDE.md
git -C $W commit -s -m "fix(responses): return context_length_exceeded when every bridge target is too small" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git -C $W status --short | grep -v '^??' ; echo clean-check-done
```

Expected: one new commit; `git status --short` lists no modified tracked files.


---

### Task 3: Converse renders ToolSearch tool_reference results; thinking stays dropped

**Items:** program item 3

**User-visible change:** On Bedrock Converse a Claude Code ToolSearch result (a `tool_result` whose content holds `tool_reference` blocks) is sent as `Loaded tool: <name>` lines instead of an empty tool result, so the model no longer concludes the search found nothing. `thinking` and `redacted_thinking` blocks keep being dropped on Converse; that decision is now documented in code and docs and pinned by a test.

**Files:**
- Test (create): `providers/bedrock/tool_reference_test.go`
- Modify: `providers/bedrock/client.go` (edits at lines 10–12, 459–470)
- Modify: `providers/bedrock/converse.go` (edits at lines 162–163)
- Modify: `docs/reference/agent-llm.md` (edits at lines 88)
- Modify: `docs/reference/agent-llm.ko.md` (edits at lines 65)
- Modify: `providers/CLAUDE.md` (edits at lines 13)

**Interfaces:**
- No exported change. `toolResultText(raw json.RawMessage) string` renders `tool_reference` blocks; `messageBlocks` documents the thinking decision.

- [ ] **Step 1: Write the failing tests**

Create `providers/bedrock/tool_reference_test.go`:

```go
package bedrock

import (
	"encoding/json"
	"testing"

	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// The 2026-10-27 path check: Claude Code's ToolSearch result on a Converse
// model became an empty tool result, so the model could conclude the search
// found nothing even though the tool definitions arrived on the next turn.
func TestToolResultTextNamesLoadedTools(t *testing.T) {
	for _, tc := range []struct{ name, content, want string }{
		{"references only", `[{"type":"tool_reference","tool_name":"CronList"},{"type":"tool_reference","tool_name":"mcp__probe__tool-17"}]`, "Loaded tool: CronList\nLoaded tool: mcp__probe__tool-17"},
		{"text then reference", `[{"type":"text","text":"Found 1 tool."},{"type":"tool_reference","tool_name":"CronList"}]`, "Found 1 tool.\nLoaded tool: CronList"},
		{"nameless or invalid references are skipped", `[{"type":"tool_reference"},{"type":"tool_reference","tool_name":7},{"type":"tool_reference","tool_name":""}]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolResultText(json.RawMessage(tc.content)); got != tc.want {
				t.Fatalf("toolResultText = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConverseToolSearchHistoryKeepsReferencesAndDropsThinking(t *testing.T) {
	cr, err := toConverseRequest([]byte(`{"messages":[
		{"role":"user","content":"schedule a job"},
		{"role":"assistant","content":[{"type":"thinking","thinking":"use search","signature":"c2ln"},{"type":"redacted_thinking","data":"ZW5j"},{"type":"tool_use","id":"t1","name":"ToolSearch","input":{"query":"cron"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"tool_reference","tool_name":"CronList"}]}]}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	msgs := buildMessages(cr.Messages)
	if len(msgs) != 3 || len(msgs[1].Content) != 1 {
		t.Fatalf("thinking must be dropped and tool_use kept: %+v", msgs)
	}
	if _, ok := msgs[1].Content[0].(*brtypes.ContentBlockMemberToolUse); !ok {
		t.Fatalf("assistant turn lost its tool_use: %T", msgs[1].Content[0])
	}
	result, ok := msgs[2].Content[0].(*brtypes.ContentBlockMemberToolResult)
	if !ok {
		t.Fatalf("tool result missing: %T", msgs[2].Content[0])
	}
	text, ok := result.Value.Content[0].(*brtypes.ToolResultContentBlockMemberText)
	if !ok || text.Value != "Loaded tool: CronList" {
		t.Fatalf("tool search result = %+v, want the loaded tool named", result.Value.Content)
	}
}
```

- [ ] **Step 2: Run them and confirm they fail**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
go test -buildvcs=false -count=1 -run 'ToolResultTextNamesLoadedTools|ToolSearchHistory' ./providers/bedrock/ > $L/logs/t3-fail.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL|ok )|undefined|unknown field' $L/logs/t3-fail.log | sort -u
```

Expected: `exit=1`; `--- FAIL: TestToolResultTextNamesLoadedTools` and `--- FAIL: TestConverseToolSearchHistoryKeepsReferencesAndDropsThinking` (empty tool result text).

- [ ] **Step 3: Implement**

In `providers/bedrock/client.go`, replace:

```go
	"fmt"
	"iter"
```

with:

```go
	"fmt"
	"iter"
	"strings"
```

In `providers/bedrock/client.go`, replace:

```go
	if raw[0] == '[' {
		var blocks []schema.ContentBlock
		if json.Unmarshal(raw, &blocks) == nil {
			var out string
			for _, blk := range blocks {
				if blk.Text != nil {
					out += *blk.Text
				}
			}
			return out
		}
	}
```

with:

```go
	if raw[0] == '[' {
		var blocks []schema.ContentBlock
		if json.Unmarshal(raw, &blocks) == nil {
			var out strings.Builder
			for _, blk := range blocks {
				switch {
				case blk.Text != nil:
					out.WriteString(*blk.Text)
				case blk.Type == "tool_reference":
					// Claude Code's ToolSearch answers with tool_reference
					// blocks naming the deferred tools it loaded; their
					// definitions arrive in the next request's tools. Converse
					// has no such block, and an empty result reads as "nothing
					// found", so name each loaded tool in text.
					var name string
					if json.Unmarshal(blk.Extra["tool_name"], &name) == nil && name != "" {
						if out.Len() > 0 {
							out.WriteByte('\n')
						}
						out.WriteString("Loaded tool: " + name)
					}
				}
			}
			return out.String()
		}
	}
```

In `providers/bedrock/converse.go`, replace:

```go
// (out of scope — see the design doc). The SDK-specific mapping, including
// tool_result flattening, happens in buildMessages (client.go).
```

with:

```go
// (out of scope — see the design doc). The SDK-specific mapping, including
// tool_result flattening, happens in buildMessages (client.go).
//
// thinking and redacted_thinking stay dropped rather than becoming Converse
// reasoningContent: this adapter never returns reasoning to the client, so a
// history thinking block was produced by another model and carries that
// model's signature, which a Converse model cannot verify, and Bedrock does
// not document reasoningContent input for the non-Claude families routed here.
```

In `docs/reference/agent-llm.md`, replace:

```markdown
cache_creation ≈ full 475k-token input on every request).
```

with:

```markdown
cache_creation ≈ full 475k-token input on every request).

Converse flattens `tool_result` content to one text block. A `tool_reference`
block (Claude Code's ToolSearch result) becomes a `Loaded tool: <name>` line per
tool instead of empty text; the definitions arrive in the next request's tools.
`thinking` and `redacted_thinking` blocks stay dropped rather than becoming
Converse `reasoningContent`: this path never returns reasoning, so a history
thinking block comes from another model and carries a signature the Converse
model cannot verify, and reasoning input is not documented for the non-Claude
families routed here.
```

In `docs/reference/agent-llm.ko.md`, replace:

```markdown
이를 system에 합쳤던 이전 동작은 매 턴 머리를 바꿔 전체 약 475k 토큰을 캐시 쓰기하는 현상이 관측되었습니다.
```

with:

```markdown
이를 system에 합쳤던 이전 동작은 매 턴 머리를 바꿔 전체 약 475k 토큰을 캐시 쓰기하는 현상이 관측되었습니다.

Converse는 `tool_result` 내용을 텍스트 블록 하나로 평탄화합니다. `tool_reference` 블록(Claude Code ToolSearch 결과)은 빈 텍스트 대신 도구마다 `Loaded tool: <이름>` 한 줄이 되며, 도구 정의는 다음 요청의 tools에 들어갑니다. `thinking`과 `redacted_thinking` 블록은 Converse `reasoningContent`로 바꾸지 않고 계속 제외합니다. 이 경로는 추론을 클라이언트에 돌려주지 않으므로 이력의 thinking 블록은 다른 모델이 만든 것이고, Converse 모델이 검증할 수 없는 서명을 담고 있으며, 여기서 라우팅하는 Claude 외 계열의 추론 입력은 문서화되어 있지 않습니다.
```

Refresh the Korean companion hash after the English edit:

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
python3 - <<'EOF'
import hashlib, pathlib, re
for en in ["docs/reference/agent-llm.md"]:
    p = pathlib.Path(en); ko = p.with_suffix(".ko.md")
    h = hashlib.sha256(p.read_bytes()).hexdigest()
    t, n = re.subn(r"(?m)^translation_source_sha256: \S+$", "translation_source_sha256: " + h, ko.read_text(encoding="utf-8"), count=1)
    assert n == 1, ko
    ko.write_text(t, encoding="utf-8")
EOF
```

In `providers/CLAUDE.md`, replace:

```markdown
unlisted families pass through untouched (Claude on Converse is disjoint, ADR-030).
```

with:

```markdown
unlisted families pass through untouched (Claude on Converse is disjoint, ADR-030). Converse flattens `tool_result` to text: `tool_reference` blocks become `Loaded tool: <name>` lines, and `thinking`/`redacted_thinking` stay dropped (their signatures belong to another model).
```

- [ ] **Step 4: Run the task checks**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
gofmt -l providers | (! grep .) \
  && go vet ./providers/bedrock/ \
  && go test -buildvcs=false -race -count=1 ./providers/bedrock/ > $L/logs/t3-pass.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL)' $L/logs/t3-pass.log
```

Expected: `exit=0` and no FAIL lines.

- [ ] **Step 5: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
git -C $W add providers/bedrock/tool_reference_test.go providers/bedrock/client.go providers/bedrock/converse.go docs/reference/agent-llm.md docs/reference/agent-llm.ko.md providers/CLAUDE.md
git -C $W commit -s -m "fix(bedrock): name ToolSearch tool_reference results on Converse" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git -C $W status --short | grep -v '^??' ; echo clean-check-done
```

Expected: one new commit; `git status --short` lists no modified tracked files.


---

### Task 4: Go runtime and process collectors on /metrics

**Items:** program item 5

**User-visible change:** `GET :9090/metrics` now also exposes the Go runtime families (`go_*`: heap, GC, goroutines, GOMEMLIMIT) and the process families (`process_*`: RSS, CPU, file descriptors, network bytes). Their labels are fixed (`version`, `quantile`) and carry no key, user or request data. Before, memory had to be read from /proc.

**Files:**
- Test (create): `internal/metrics/runtime_test.go`
- Modify: `internal/metrics/metrics.go` (edits at lines 12–13, 128)
- Modify: `docs/operations/observability.md` (edits at lines 32)
- Modify: `docs/operations/observability.ko.md` (edits at lines 37)
- Modify: `internal/CLAUDE.md` (edits at lines 106)

**Interfaces:**
- No exported change; `metrics.New` registers `collectors.NewGoCollector()` and `collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})` (package `github.com/prometheus/client_golang/prometheus/collectors`, already in the required module; procfs is already an indirect requirement).

- [ ] **Step 1: Write the failing tests**

Create `internal/metrics/runtime_test.go`:

```go
package metrics

import (
	"runtime"
	"strings"
	"testing"
)

func TestRegistryExposesRuntimeAndProcessCollectors(t *testing.T) {
	m := New()
	m.ObserveRequest("anthropic", "model-a", "prov-a", "team-a", 200, 0.1, 0)
	mfs, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]map[string]bool{}
	for _, mf := range mfs {
		names := map[string]bool{}
		for _, metric := range mf.GetMetric() {
			for _, lp := range metric.GetLabel() {
				names[lp.GetName()] = true
				if strings.HasPrefix(lp.GetValue(), "ik_") {
					t.Errorf("%s exposes a key-shaped label value", mf.GetName())
				}
			}
		}
		labels[mf.GetName()] = names
	}
	want := []string{"go_goroutines", "go_memstats_heap_alloc_bytes", "go_memstats_heap_inuse_bytes", "go_gc_duration_seconds"}
	if runtime.GOOS == "linux" {
		want = append(want, "process_resident_memory_bytes", "process_open_fds", "process_cpu_seconds_total")
	}
	for _, name := range want {
		if _, ok := labels[name]; !ok {
			t.Errorf("/metrics lacks %s", name)
		}
	}
	for name, set := range labels {
		if !strings.HasPrefix(name, "go_") && !strings.HasPrefix(name, "process_") {
			continue
		}
		for label := range set {
			if label != "version" && label != "quantile" {
				t.Errorf("runtime family %s has unexpected label %q", name, label)
			}
		}
	}
}
```

- [ ] **Step 2: Run them and confirm they fail**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
go test -buildvcs=false -count=1 -run 'TestRegistryExposesRuntimeAndProcessCollectors' ./internal/metrics/ > $L/logs/t4-fail.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL|ok )|undefined|unknown field' $L/logs/t4-fail.log | sort -u
```

Expected: `exit=1`; `--- FAIL: TestRegistryExposesRuntimeAndProcessCollectors` with `/metrics lacks go_goroutines` and the other runtime/process families.

- [ ] **Step 3: Implement**

In `internal/metrics/metrics.go`, replace:

```go
	"github.com/prometheus/client_golang/prometheus"
)
```

with:

```go
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)
```

In `internal/metrics/metrics.go`, replace:

```go
	m.classifiers = newClassifierMetrics(reg)
```

with:

```go
	// Go runtime (heap, GC, goroutines) and process (RSS, CPU, file
	// descriptors) collectors: their families carry no request data and only
	// fixed labels. Without them a large-body memory burst was visible only
	// through /proc (2026-10-27 path check).
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	m.classifiers = newClassifierMetrics(reg)
```

In `docs/operations/observability.md`, replace:

```markdown
| `inferplane_usage_windows_dropped_total` | Analytics delivery loss; distinguish from authority accounting |
```

with:

```markdown
| `inferplane_usage_windows_dropped_total` | Analytics delivery loss; distinguish from authority accounting |
| `process_resident_memory_bytes`, `go_memstats_heap_inuse_bytes`, `go_goroutines` | Memory growth: the gateway holds several copies of every request body being processed (about 5.5× measured), so concurrent large bodies dominate |
```

In `docs/operations/observability.ko.md`, replace:

```markdown
| `inferplane_usage_windows_dropped_total` | 분석 전달 손실. 권한 회계와 구분 |
```

with:

```markdown
| `inferplane_usage_windows_dropped_total` | 분석 전달 손실. 권한 회계와 구분 |
| `process_resident_memory_bytes`, `go_memstats_heap_inuse_bytes`, `go_goroutines` | 메모리 증가. 처리 중인 요청 본문마다 여러 사본을 보유하므로(실측 약 5.5배) 동시에 들어온 큰 본문이 대부분을 차지 |
```

Refresh the Korean companion hash after the English edit:

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
python3 - <<'EOF'
import hashlib, pathlib, re
for en in ["docs/operations/observability.md"]:
    p = pathlib.Path(en); ko = p.with_suffix(".ko.md")
    h = hashlib.sha256(p.read_bytes()).hexdigest()
    t, n = re.subn(r"(?m)^translation_source_sha256: \S+$", "translation_source_sha256: " + h, ko.read_text(encoding="utf-8"), count=1)
    assert n == 1, ko
    ko.write_text(t, encoding="utf-8")
EOF
```

In `internal/CLAUDE.md`, replace:

```markdown
- `metrics/` — Prometheus registry + GenAI collectors + nil-safe hooks.
```

with:

```markdown
- `metrics/` — Prometheus registry + GenAI collectors + nil-safe hooks. The Go runtime and process collectors are registered too (fixed labels only, no request data).
```

- [ ] **Step 4: Run the task checks**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
gofmt -l internal/metrics | (! grep .) \
  && go vet ./internal/metrics/ \
  && go test -buildvcs=false -race -count=1 ./internal/metrics/ ./internal/server/ \
  && python3 scripts/check_docs_translations.py > $L/logs/t4-pass.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL)' $L/logs/t4-pass.log
```

Expected: `exit=0` and no FAIL lines; the translation checker passes.

- [ ] **Step 5: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
git -C $W add internal/metrics/runtime_test.go internal/metrics/metrics.go docs/operations/observability.md docs/operations/observability.ko.md internal/CLAUDE.md
git -C $W commit -s -m "feat(metrics): expose Go runtime and process collectors" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git -C $W status --short | grep -v '^??' ; echo clean-check-done
```

Expected: one new commit; `git status --short` lists no modified tracked files.


---

### Task 5: Make model fallback visible in logs, metrics and audit

**Items:** program item 4

**User-visible change:** The served model does not change. When an unrouted requested model is served through `model_fallbacks` or the same-family heuristic, every generation ingress now sets `x-inferplane-model-fallback` (Responses did not), logs `router: model fallback source=<model_fallbacks|family> requested="<id>" served=<model>` once per distinct pair per process (at most 256 pairs; the requested name is quoted and cut at 128 bytes; no key, principal or prompt data), counts `inferplane_model_fallback_total{source,model}`, and its audit records carry `request.routing` with `reason: model_fallback`, `model_fallback_from` and `model_fallback_source`. A request that also has a policy decision keeps that reason and gains the two fields. Count endpoints are unchanged.

**Files:**
- Test (create): `internal/live/fallback_source_test.go`
- Test (create): `internal/audit/routing_fallback_test.go`
- Test (create): `internal/router/fallback_log_test.go`
- Test (create): `internal/server/model_fallback_test.go`
- Test (modify): `internal/server/bedrockapi/resolve_test.go` (edits at lines 31–32, 49–50, 99–100)
- Test (modify): `internal/server/policy_routing_test.go` (edits at lines 408–410)
- Modify: `internal/live/live.go` (edits at lines 125–137, 153–158)
- Modify: `internal/router/router.go` (edits at lines 36–38, 72–82)
- Create: `internal/router/fallback_log.go`
- Modify: `internal/metrics/metrics.go` (edits at lines 43–44, 123–124, 134, 249)
- Modify: `internal/audit/routing.go` (edits at lines 27–29, 59–60, 101–104)
- Modify: `internal/server/anthropicapi/messages.go` (edits at lines 133–140)
- Modify: `internal/server/openaiapi/chat.go` (edits at lines 146–152)
- Modify: `internal/server/bedrockapi/invoke.go` (edits at lines 143–146)
- Modify: `internal/server/bedrockapi/resolve.go` (edits at lines 20–28, 36–40, 56–62)
- Modify: `internal/server/responsesapi/handler.go` (edits at lines 85–86)
- Modify: `docs/reference/api.md` (edits at lines 49)
- Modify: `docs/reference/api.ko.md` (edits at lines 67)
- Modify: `docs/reference/data.md` (edits at lines 162)
- Modify: `docs/reference/data.ko.md` (edits at lines 103)
- Modify: `docs/operations/observability.md` (edits at lines 33)
- Modify: `docs/operations/observability.ko.md` (edits at lines 38)
- Modify: `internal/CLAUDE.md` (edits at lines 92, 96)

**Interfaces:**
- Produces: `func (s *live.State) FallbackSource(model string) (served, source string)`; `func (r *router.Router) ResolveModelSource(requested string) (served, source string)`; `func (r *router.Router) ObserveModelFallback(requested, served, source string)`; `func (m *metrics.Metrics) ObserveModelFallback(source, model string)`; `func audit.WithModelFallback(ctx context.Context, requested, served, source string) context.Context`; fields `audit.RoutingRef.ModelFallbackFrom` and `ModelFallbackSource` (`json:"model_fallback_from,omitempty"`, `json:"model_fallback_source,omitempty"`); bedrockapi `resolveModel(r, holder, urlID) (model string, source string, ok bool)`.
- Consumes: `router.ResolveModel` keeps its signature (count handlers still call it); `audit.RoutingFrom` keeps its signature and now merges the fallback evidence.

- [ ] **Step 1: Write the failing tests**

Create `internal/live/fallback_source_test.go`:

```go
package live

import "testing"

func TestFallbackSourceNamesWhereTheSubstitutionComesFrom(t *testing.T) {
	cfg := familyConfig()
	cfg.ModelFallbacks = map[string]string{"claude-haiku-9": "claude-sonnet-4-6"}
	st, _, err := BuildState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ requested, served, source string }{
		{"claude-haiku-9", "claude-sonnet-4-6", "model_fallbacks"},
		{"claude-opus-9", "claude-opus-4-8", "family"},
		{"claude-opus-4-2", "", ""},
		{"unrelated", "", ""},
	} {
		served, source := st.FallbackSource(tc.requested)
		if served != tc.served || source != tc.source || st.FallbackFor(tc.requested) != tc.served {
			t.Fatalf("FallbackSource(%q) = (%q, %q), want (%q, %q)", tc.requested, served, source, tc.served, tc.source)
		}
	}
}
```

Create `internal/audit/routing_fallback_test.go`:

```go
package audit

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoutingFromCarriesModelFallback(t *testing.T) {
	ctx := WithModelFallback(context.Background(), "claude-sonnet-9", "claude-sonnet-5-5", "family")
	got := RoutingFrom(ctx)
	if got == nil || got.Reason != "model_fallback" || got.RequestedModel != "claude-sonnet-9" || got.SelectedModel != "claude-sonnet-5-5" ||
		got.ModelFallbackFrom != "claude-sonnet-9" || got.ModelFallbackSource != "family" {
		t.Fatalf("legacy request lost its fallback evidence: %+v", got)
	}
	// A policy decision keeps its own reason and gains the evidence.
	ctx = WithRouting(ctx, &RoutingRef{RequestedModel: "claude-sonnet-5-5", Reason: "context_selected", Inspection: "complete"})
	got = RoutingFrom(ctx)
	if got.Reason != "context_selected" || got.RequestedModel != "claude-sonnet-5-5" ||
		got.ModelFallbackFrom != "claude-sonnet-9" || got.ModelFallbackSource != "family" {
		t.Fatalf("policy decision lost or replaced evidence: %+v", got)
	}
	// Attempt fields ride along with the evidence.
	if got := RoutingFrom(WithRoutingAttempt(ctx, "claude-sonnet-5-5", "public", "")); got.ActualProvider != "public" || got.ModelFallbackSource != "family" {
		t.Fatalf("attempt dropped the evidence: %+v", got)
	}
	if RoutingFrom(context.Background()) != nil {
		t.Fatal("a request without routing or fallback must keep omitting routing")
	}
	long := RoutingFrom(WithModelFallback(context.Background(), strings.Repeat("x", 1000), "m", "family"))
	if len(long.ModelFallbackFrom) != maxFallbackRequested || len(long.RequestedModel) != maxFallbackRequested {
		t.Fatalf("client model name not bounded: %d", len(long.ModelFallbackFrom))
	}
}

// Records with fallback evidence verify in a chain beside records written
// before the fields existed, and records without a fallback keep their bytes.
func TestModelFallbackRecordsVerifyInMixedChains(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter("i", filepath.Join(t.TempDir(), "a.wal"), []Sink{NewWriterSink("b", &buf, true)})
	if err != nil {
		t.Fatal(err)
	}
	legacy := Record{SchemaVersion: 1, Event: "request_completed", ID: "01A", TS: "2026-10-29T00:00:00Z",
		Request: RequestRef{Ingress: "anthropic", ModelRequested: "m"}, Outcome: &OutcomeRef{Status: 200}}
	before, err := legacy.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithModelFallback(context.Background(), "claude-sonnet-9", "m", "family")
	fallback := legacy
	fallback.ID = "01B"
	fallback.Request.Routing = RoutingFrom(ctx)
	w.Append(legacy)
	w.Append(fallback)
	w.Close()
	res, err := Verify(bytes.NewReader(buf.Bytes()))
	if err != nil || !res.OK || res.Records != 2 {
		t.Fatalf("verify = %+v, %v", res, err)
	}
	after, _ := legacy.Canonical()
	if !bytes.Equal(before, after) || bytes.Contains(after, []byte("model_fallback")) {
		t.Fatalf("legacy bytes changed: %s", after)
	}
	if !strings.Contains(buf.String(), `"reason":"model_fallback"`) || !strings.Contains(buf.String(), `"model_fallback_source":"family"`) {
		t.Fatalf("fallback evidence missing: %s", buf.String())
	}
}
```

Create `internal/router/fallback_log_test.go`:

```go
package router

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/metrics"
)

func TestObserveModelFallbackLogsOncePerPairAndCounts(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	r := New(&live.Holder{})
	m := metrics.New()
	r.SetMetrics(m)
	for range 3 {
		r.ObserveModelFallback("claude-sonnet-9\nforged line", "claude-sonnet-5-5", "family")
	}
	r.ObserveModelFallback(strings.Repeat("y", 500), "claude-sonnet-5-5", "family")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `requested="claude-sonnet-9\nforged line" served=claude-sonnet-5-5`) ||
		strings.Contains(lines[1], strings.Repeat("y", maxFallbackLogName+1)) {
		t.Fatalf("fallback log = %q, want one quoted, bounded line per pair", buf.String())
	}
	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range families {
		if f.GetName() != "inferplane_model_fallback_total" {
			continue
		}
		found = true
		if len(f.GetMetric()) != 1 || f.GetMetric()[0].GetCounter().GetValue() != 4 {
			t.Fatalf("fallback counter = %v", f.GetMetric())
		}
		for _, l := range f.GetMetric()[0].GetLabel() {
			if l.GetName() != "source" && l.GetName() != "model" {
				t.Fatalf("unbounded label %s", l.GetName())
			}
		}
	}
	if !found {
		t.Fatal("inferplane_model_fallback_total missing")
	}
}

func TestFallbackLogIsBounded(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	var l fallbackLog
	for i := range maxFallbackLogPairs + 50 {
		l.note(strings.Repeat("m", i%100)+string(rune('a'+i%26))+strings.Repeat("z", i/26), "served", "family")
	}
	if n := strings.Count(buf.String(), "\n"); n != maxFallbackLogPairs+1 {
		t.Fatalf("logged %d lines, want %d pairs plus one cap notice", n, maxFallbackLogPairs+1)
	}
}
```

Create `internal/server/model_fallback_test.go`:

```go
package server

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/metrics"
	"github.com/inferplane/inferplane/internal/server/routingtest"
	"github.com/inferplane/inferplane/pkg/schema"
)

// The 2026-10-27 path check: CLAUDE_CODE_AUTO_MODE_MODEL set to a nonexistent
// global.anthropic.claude-sonnet-9 was silently served by sonnet-5-5. The
// served model stays the same; the substitution is now visible.
func TestModelFallbackIsVisibleOnEveryIngress(t *testing.T) {
	ingresses := append(append([]policyIngress(nil), policyIngresses...), policyIngress{"responses", "/v1/responses", false})
	for _, ing := range ingresses {
		t.Run(ing.name(), func(t *testing.T) {
			f := routingtest.New(t, func(cfg *config.Config) {
				cfg.Models["claude-sonnet-5-5"] = config.ModelConfig{ContextWindow: 100000, Capabilities: []string{"tools"},
					Targets: []config.Target{{Provider: "public", Model: "up"}}}
			})
			in, out := int64(5), int64(1)
			f.Public.Usage = &schema.Usage{InputTokens: &in, OutputTokens: &out} // Responses requires usage
			m := metrics.New()
			f.Router.SetMetrics(m)
			aud, buf := policyAudit(t)
			body := strings.Replace(routingtest.Body(ing.protocol, "clean", ing.stream), `"premium"`, `"claude-sonnet-9"`, 1)
			path := strings.Replace(ing.path, "premium", "claude-sonnet-9", 1)
			if ing.protocol == "responses" {
				body = `{"model":"claude-sonnet-9","stream":false,"store":false,"max_output_tokens":16,"input":"hello"}`
			}
			rec := policyDo(policyMux(f, aud, nil, m, nil), path, body, true)
			aud.Close()
			if rec.Code != 200 || len(f.Public.Calls) != 1 || f.Public.Calls[0].Model != "claude-sonnet-5-5" {
				t.Fatalf("fallback changed the served model: %d calls=%+v body=%s", rec.Code, f.Public.Calls, rec.Body.String())
			}
			if got := rec.Header().Get("x-inferplane-model-fallback"); got != "claude-sonnet-5-5" {
				t.Fatalf("fallback header = %q", got)
			}
			var completed *audit.Record
			scanner := bufio.NewScanner(buf)
			scanner.Buffer(make([]byte, 1<<20), 1<<20)
			for scanner.Scan() {
				var rec audit.Record
				if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
					t.Fatal(err)
				}
				if rec.Event == "request_completed" {
					completed = &rec
				}
			}
			if completed == nil || completed.Request.Routing == nil || completed.Request.Routing.Reason != "model_fallback" ||
				completed.Request.Routing.ModelFallbackFrom != "claude-sonnet-9" || completed.Request.Routing.ModelFallbackSource != "family" {
				t.Fatalf("audit lacks the fallback: %+v", completed)
			}
			families, err := m.Registry().Gather()
			if err != nil {
				t.Fatal(err)
			}
			counted := 0.0
			for _, family := range families {
				if family.GetName() == "inferplane_model_fallback_total" {
					for _, metric := range family.GetMetric() {
						counted += metric.GetCounter().GetValue()
					}
				}
			}
			if counted != 1 {
				t.Fatalf("inferplane_model_fallback_total = %v, want 1", counted)
			}
		})
	}
}
```

In `internal/server/bedrockapi/resolve_test.go`, replace:

```go
	got, sub, ok := resolveModel(r, h, "claude-x")
	if sub {
```

with:

```go
	got, sub, ok := resolveModel(r, h, "claude-x")
	if sub != "" {
```

In `internal/server/bedrockapi/resolve_test.go`, replace:

```go
	got, sub, ok := resolveModel(r, h, "global.anthropic.claude-x-v1:0")
	if sub {
```

with:

```go
	got, sub, ok := resolveModel(r, h, "global.anthropic.claude-x-v1:0")
	if sub != "" {
```

In `internal/server/bedrockapi/resolve_test.go`, replace:

```go
	if !ok || got != "claude-opus-4-8" || !sub {
		t.Fatalf("fallback resolution failed: %q ok=%v substituted=%v, want claude-opus-4-8 substituted", got, ok, sub)
```

with:

```go
	if !ok || got != "claude-opus-4-8" || sub != "model_fallbacks" {
		t.Fatalf("fallback resolution failed: %q ok=%v source=%q, want claude-opus-4-8 from model_fallbacks", got, ok, sub)
```

In `internal/server/policy_routing_test.go`, replace:

```go
				} else if strings.Contains(buf.String(), `"routing":`) || rec.Header().Get("x-inferplane-routing-reason") != "" {
					t.Fatal("legacy traffic acquired new evidence")
				}
```

with:

```go
				} else if !strings.Contains(buf.String(), `"reason":"model_fallback"`) ||
					!strings.Contains(buf.String(), `"model_fallback_from":"unrouted","model_fallback_source":"model_fallbacks"`) ||
					strings.Contains(buf.String(), `"policies":`) || rec.Header().Get("x-inferplane-routing-reason") != "" {
					// Legacy traffic gains only the ADR-029 fallback evidence,
					// never policy-routing evidence or headers.
					t.Fatalf("legacy fallback evidence wrong: %s", buf)
				}
```

- [ ] **Step 2: Run them and confirm they fail**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
go test -buildvcs=false -count=1 ./internal/live/ ./internal/audit/ ./internal/router/ ./internal/server/ ./internal/server/bedrockapi/ > $L/logs/t5-fail.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL|ok )|undefined|unknown field' $L/logs/t5-fail.log | sort -u
```

Expected: `exit=1`; build failures in `internal/live` (FallbackSource), `internal/audit` (WithModelFallback, ModelFallbackFrom/Source), `internal/router` (ObserveModelFallback, fallbackLog), `internal/server` and `internal/server/bedrockapi` (a string source).

- [ ] **Step 3: Implement**

In `internal/live/live.go`, replace:

```go
// Callers only consult this after Route has already failed, so `model` is
// never itself a routed name here.
func (s *State) FallbackFor(model string) string {
	if served, ok := s.fallbacks[model]; ok {
		return served
	}
	if !s.fallbackFamily {
		return ""
	}
	family, version, ok := familyOf(model)
	if !ok {
		return ""
	}
```

with:

```go
// Callers only consult this after Route has already failed, so `model` is
// never itself a routed name here.
func (s *State) FallbackFor(model string) string {
	served, _ := s.FallbackSource(model)
	return served
}

// FallbackSource is FallbackFor plus where the substitution comes from:
// "model_fallbacks" for an operator-declared entry, "family" for the
// same-family heuristic, "" when nothing applies.
func (s *State) FallbackSource(model string) (served, source string) {
	if served, ok := s.fallbacks[model]; ok {
		return served, "model_fallbacks"
	}
	if !s.fallbackFamily {
		return "", ""
	}
	family, version, ok := familyOf(model)
	if !ok {
		return "", ""
	}
```

In `internal/live/live.go`, replace:

```go
		if best == "" || compareVersions(v, bestVersion) > 0 {
			best, bestVersion = name, v
		}
	}
	return best
}
```

with:

```go
		if best == "" || compareVersions(v, bestVersion) > 0 {
			best, bestVersion = name, v
		}
	}
	if best == "" {
		return "", ""
	}
	return best, "family"
}
```

In `internal/router/router.go`, replace:

```go
	budgetConstraintGate func(keystore.Principal) map[string]string
	affinity             *affinityStore
}
```

with:

```go
	budgetConstraintGate func(keystore.Principal) map[string]string
	affinity             *affinityStore
	fallbackLog          fallbackLog
}
```

In `internal/router/router.go`, replace:

```go
func (r *Router) ResolveModel(requested string) (served string, substituted bool) {
	st := r.live.Load()
	canonical := st.Canonical(requested)
	if _, ok := st.Route(canonical); ok {
		return canonical, false
	}
	if fb := st.FallbackFor(canonical); fb != "" {
		return fb, true
	}
	return canonical, false
}
```

with:

```go
func (r *Router) ResolveModel(requested string) (served string, substituted bool) {
	served, source := r.ResolveModelSource(requested)
	return served, source != ""
}

// ResolveModelSource is ResolveModel plus the substitution's source
// ("model_fallbacks" or "family", see live.State.FallbackSource); source is
// "" when the requested model is served as is. It has no side effects:
// generation ingresses report an applied substitution via ObserveModelFallback.
func (r *Router) ResolveModelSource(requested string) (served, source string) {
	st := r.live.Load()
	canonical := st.Canonical(requested)
	if _, ok := st.Route(canonical); ok {
		return canonical, ""
	}
	if fb, source := st.FallbackSource(canonical); fb != "" {
		return fb, source
	}
	return canonical, ""
}
```

Create `internal/router/fallback_log.go`:

```go
package router

import (
	"log"
	"sync"
)

// maxFallbackLogPairs bounds the distinct (requested, served) pairs logged per
// process; requested names are client input, so the set must not grow freely.
const maxFallbackLogPairs = 256

// maxFallbackLogName bounds how much of a client's model string is logged.
const maxFallbackLogName = 128

type fallbackLog struct {
	mu     sync.Mutex
	seen   map[[2]string]bool
	capped bool
}

// ObserveModelFallback reports a model fallback a generation ingress applied:
// the inferplane_model_fallback_total counter (labels: source and the
// configured served model only) and one log line per (requested, served)
// pair per process. The log carries no key, principal or prompt data; the
// requested name is truncated and quoted.
func (r *Router) ObserveModelFallback(requested, served, source string) {
	r.metrics.ObserveModelFallback(source, served)
	r.fallbackLog.note(requested, served, source)
}

func (l *fallbackLog) note(requested, served, source string) {
	if len(requested) > maxFallbackLogName {
		requested = requested[:maxFallbackLogName]
	}
	key := [2]string{requested, served}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen[key] {
		return
	}
	if len(l.seen) >= maxFallbackLogPairs {
		if !l.capped {
			l.capped = true
			log.Printf("router: model fallback logging capped at %d distinct requested models; inferplane_model_fallback_total keeps counting", maxFallbackLogPairs)
		}
		return
	}
	if l.seen == nil {
		l.seen = make(map[[2]string]bool)
	}
	l.seen[key] = true
	log.Printf("router: model fallback source=%s requested=%q served=%s", source, requested, served)
}
```

In `internal/metrics/metrics.go`, replace:

```go
	routingDecisions   *prometheus.CounterVec // inferplane_routing_decisions_total
```

with:

```go
	routingDecisions   *prometheus.CounterVec // inferplane_routing_decisions_total
	modelFallback      *prometheus.CounterVec // inferplane_model_fallback_total (ADR-029)
```

In `internal/metrics/metrics.go`, replace:

```go
		}, []string{"team", "from_model", "to_model"}),
	}
```

with:

```go
		}, []string{"team", "from_model", "to_model"}),
		modelFallback: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "inferplane_model_fallback_total",
			Help: "Requests for an unrouted model served by a configured one (ADR-029), by source and served model.",
			// The requested name is client input and never a label: source is
			// a closed set and model is the configured served model.
		}, []string{"source", "model"}),
	}
```

In `internal/metrics/metrics.go`, replace:

```go
		m.autoModeUnparsed)
```

with:

```go
		m.autoModeUnparsed, m.modelFallback)
```

In `internal/metrics/metrics.go`, replace:

```go
func (m *Metrics) SetCircuitState(provider string, state int) {
```

with:

```go

// ObserveModelFallback counts one applied model fallback. source is
// "model_fallbacks" or "family"; model is the configured served model.
func (m *Metrics) ObserveModelFallback(source, model string) {
	if m == nil {
		return
	}
	if source != "model_fallbacks" && source != "family" {
		source = "unknown"
	}
	m.modelFallback.WithLabelValues(source, model).Inc()
}
func (m *Metrics) SetCircuitState(provider string, state int) {
```

In `internal/audit/routing.go`, replace:

```go
	Masked          bool                    `json:"masked,omitempty"`
	Classifiers     []ClassifierRef         `json:"classifiers,omitempty"`
}
```

with:

```go
	Masked          bool                    `json:"masked,omitempty"`
	Classifiers     []ClassifierRef         `json:"classifiers,omitempty"`
	// Append-only ADR-029 evidence: an unrouted requested model (the client's
	// string, bounded) was served by a configured model, the record's
	// request.model_requested. Source is "model_fallbacks" or "family".
	ModelFallbackFrom   string `json:"model_fallback_from,omitempty"`
	ModelFallbackSource string `json:"model_fallback_source,omitempty"`
}
```

In `internal/audit/routing.go`, replace:

```go
type routingKey struct{}
```

with:

```go
type routingKey struct{}
type modelFallbackKey struct{}

// maxFallbackRequested bounds the client-supplied model name kept as evidence.
const maxFallbackRequested = 256

// WithModelFallback records an applied model fallback for every record of the
// request. RoutingFrom attaches it to the routing decision; a request without
// one (no applicable policy) gets a routing ref whose reason is
// "model_fallback". served is the model the request is now resolved to.
func WithModelFallback(ctx context.Context, requested, served, source string) context.Context {
	if len(requested) > maxFallbackRequested {
		requested = requested[:maxFallbackRequested]
	}
	return context.WithValue(ctx, modelFallbackKey{}, &RoutingRef{
		RequestedModel: requested, SelectedModel: served, Reason: "model_fallback", Inspection: "not_inspected",
		ModelFallbackFrom: requested, ModelFallbackSource: source,
	})
}
```

In `internal/audit/routing.go`, replace:

```go
func RoutingFrom(ctx context.Context) *RoutingRef {
	r, _ := ctx.Value(routingKey{}).(*RoutingRef)
	return cloneRouting(r)
}
```

with:

```go
func RoutingFrom(ctx context.Context) *RoutingRef {
	r, _ := ctx.Value(routingKey{}).(*RoutingRef)
	fallback, _ := ctx.Value(modelFallbackKey{}).(*RoutingRef)
	if r == nil {
		return cloneRouting(fallback)
	}
	out := cloneRouting(r)
	if out.ModelFallbackSource == "" && fallback != nil {
		out.ModelFallbackFrom, out.ModelFallbackSource = fallback.ModelFallbackFrom, fallback.ModelFallbackSource
	}
	return out
}
```

In `internal/server/anthropicapi/messages.go`, replace:

```go
	model, modelSubstituted := h.r.ResolveModel(parsed.Model)
	if modelSubstituted {
		// Unconfigured/unrouted model substituted for a configured one
		// (model_fallbacks or the same-family default, live.State.FallbackFor)
		// BEFORE the allow-list check, so RBAC/audit/metrics/pricing below all
		// key off the model actually served — advertise it to the client.
		w.Header().Set("x-inferplane-model-fallback", model)
	}
```

with:

```go
	model, fallbackSource := h.r.ResolveModelSource(parsed.Model)
	if fallbackSource != "" {
		// Unconfigured/unrouted model substituted for a configured one
		// (model_fallbacks or the same-family default, live.State.FallbackFor)
		// BEFORE the allow-list check, so RBAC/audit/metrics/pricing below all
		// key off the model actually served — advertise it to the client and
		// keep the requested name in the audit routing evidence.
		w.Header().Set("x-inferplane-model-fallback", model)
		req = req.WithContext(audit.WithModelFallback(req.Context(), parsed.Model, model, fallbackSource))
		h.r.ObserveModelFallback(parsed.Model, model, fallbackSource)
	}
```

In `internal/server/openaiapi/chat.go`, replace:

```go
	model, modelSubstituted := h.r.ResolveModel(canonical.Model)
	if modelSubstituted {
		// Unconfigured/unrouted model substituted for a configured one
		// (model_fallbacks or the same-family default, D5) BEFORE the
		// allow-list check — advertise it to the client.
		w.Header().Set("x-inferplane-model-fallback", model)
	}
```

with:

```go
	model, fallbackSource := h.r.ResolveModelSource(canonical.Model)
	if fallbackSource != "" {
		// Unconfigured/unrouted model substituted for a configured one
		// (model_fallbacks or the same-family default, D5) BEFORE the
		// allow-list check — advertise it to the client and audit it.
		w.Header().Set("x-inferplane-model-fallback", model)
		req = req.WithContext(audit.WithModelFallback(req.Context(), canonical.Model, model, fallbackSource))
		h.r.ObserveModelFallback(canonical.Model, model, fallbackSource)
	}
```

In `internal/server/bedrockapi/invoke.go`, replace:

```go
	model, substituted, resolved := resolveModel(h.r, h.holder, urlID)
	if substituted {
		w.Header().Set("x-inferplane-model-fallback", model)
	}
```

with:

```go
	model, fallbackSource, resolved := resolveModel(h.r, h.holder, urlID)
	if fallbackSource != "" {
		w.Header().Set("x-inferplane-model-fallback", model)
		req = req.WithContext(audit.WithModelFallback(req.Context(), urlID, model, fallbackSource))
		h.r.ObserveModelFallback(urlID, model, fallbackSource)
	}
```

In `internal/server/bedrockapi/resolve.go`, replace:

```go
// resolveModel returns the model to serve, whether that model SUBSTITUTES for
// the requested one (D5 model-level fallback — the caller must then set
// x-inferplane-model-fallback, same as the Anthropic/OpenAI ingresses do with
// router.ResolveModel), and whether it resolved at all.
func resolveModel(r *router.Router, holder *live.Holder, urlID string) (string, bool, bool) {
	canonical := r.Canonical(urlID)
	if _, _, err := r.ResolveChain(canonical); err == nil {
		return canonical, false, true
	}
```

with:

```go
// resolveModel returns the model to serve, the source when that model
// SUBSTITUTES for the requested one (D5 model-level fallback, "" otherwise —
// the caller must then set x-inferplane-model-fallback, same as the
// Anthropic/OpenAI ingresses do with router.ResolveModelSource), and whether
// it resolved at all.
func resolveModel(r *router.Router, holder *live.Holder, urlID string) (string, string, bool) {
	canonical := r.Canonical(urlID)
	if _, _, err := r.ResolveChain(canonical); err == nil {
		return canonical, "", true
	}
```

In `internal/server/bedrockapi/resolve.go`, replace:

```go
	if fb := st.FallbackFor(canonical); fb != "" {
		if _, ok := st.Route(fb); ok {
			return fb, true, true
		}
	}
```

with:

```go
	if fb, source := st.FallbackSource(canonical); fb != "" {
		if _, ok := st.Route(fb); ok {
			return fb, source, true
		}
	}
```

In `internal/server/bedrockapi/resolve.go`, replace:

```go
			if ok && servesBedrockIngress(prov.Name()) {
				return name, false, true
			}
		}
	}
	return "", false, false
}
```

with:

```go
			if ok && servesBedrockIngress(prov.Name()) {
				return name, "", true
			}
		}
	}
	return "", "", false
}
```

In `internal/server/responsesapi/handler.go`, replace:

```go
	model, _ := h.r.ResolveModel(parsed.Model)
	if !h.r.Allows(p, model) {
```

with:

```go
	model, fallbackSource := h.r.ResolveModelSource(parsed.Model)
	if fallbackSource != "" {
		w.Header().Set("x-inferplane-model-fallback", model)
		req = req.WithContext(audit.WithModelFallback(req.Context(), parsed.Model, model, fallbackSource))
		h.r.ObserveModelFallback(parsed.Model, model, fallbackSource)
	}
	if !h.r.Allows(p, model) {
```

In `docs/reference/api.md`, replace:

```markdown
Either path sets `x-inferplane-model-fallback: <served model>` on the response (independent of the existing per-provider `x-inferplane-fallback: <provider>`).
```

with:

```markdown
Either path sets `x-inferplane-model-fallback: <served model>` on the response (independent of the existing per-provider `x-inferplane-fallback: <provider>`). Every generation ingress, Responses included, also logs `router: model fallback source=… requested=… served=…` once per distinct pair per process, counts `inferplane_model_fallback_total{source,model}` and records `routing.reason: model_fallback` with `model_fallback_from` and `model_fallback_source` in the audit; the served model is unchanged.
```

In `docs/reference/api.ko.md`, replace:

```markdown
응답 x-inferplane-model-fallback은 공급자 폴백 헤더와 독립적입니다.
```

with:

```markdown
응답 x-inferplane-model-fallback은 공급자 폴백 헤더와 독립적입니다. Responses를 포함한 모든 생성 수신 경로는 이 대체를 프로세스당 서로 다른 쌍마다 한 번 `router: model fallback source=… requested=… served=…`로 기록하고, `inferplane_model_fallback_total{source,model}`을 세며, 감사에 `routing.reason: model_fallback`과 `model_fallback_from`·`model_fallback_source`를 남깁니다. 응답하는 모델은 바뀌지 않습니다.
```

Refresh the Korean companion hash after the English edit:

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
python3 - <<'EOF'
import hashlib, pathlib, re
for en in ["docs/reference/api.md"]:
    p = pathlib.Path(en); ko = p.with_suffix(".ko.md")
    h = hashlib.sha256(p.read_bytes()).hexdigest()
    t, n = re.subn(r"(?m)^translation_source_sha256: \S+$", "translation_source_sha256: " + h, ko.read_text(encoding="utf-8"), count=1)
    assert n == 1, ko
    ko.write_text(t, encoding="utf-8")
EOF
```

In `docs/reference/data.md`, replace:

```markdown
No-rule traffic omits the field. See [decision semantics](../policy-routing.md#read-decisions-and-evaluate-rollout).
```

with:

```markdown
No-rule traffic omits the field unless an ADR-029 model fallback applied: then
`routing` carries `reason: model_fallback`, the client's requested model in
`requested_model` and `model_fallback_from` (at most 256 bytes), and
`model_fallback_source` (`model_fallbacks` or `family`). A policy decision keeps
its own reason and gains the same two fields. See [decision semantics](../policy-routing.md#read-decisions-and-evaluate-rollout).
```

In `docs/reference/data.ko.md`, replace:

```markdown
본문 기록은 별도이며 규칙이 없으면 필드를 생략합니다.
```

with:

```markdown
본문 기록은 별도이며 규칙이 없으면 필드를 생략합니다. 단 ADR-029 모델 대체가 적용되면 `routing`에 `reason: model_fallback`, 클라이언트가 요청한 모델(`requested_model`과 `model_fallback_from`, 최대 256바이트), `model_fallback_source`(`model_fallbacks` 또는 `family`)를 기록합니다. 정책 판단이 있으면 그 사유를 유지하고 같은 두 필드를 추가합니다.
```

Refresh the Korean companion hash after the English edit:

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
python3 - <<'EOF'
import hashlib, pathlib, re
for en in ["docs/reference/data.md"]:
    p = pathlib.Path(en); ko = p.with_suffix(".ko.md")
    h = hashlib.sha256(p.read_bytes()).hexdigest()
    t, n = re.subn(r"(?m)^translation_source_sha256: \S+$", "translation_source_sha256: " + h, ko.read_text(encoding="utf-8"), count=1)
    assert n == 1, ko
    ko.write_text(t, encoding="utf-8")
EOF
```

In `docs/operations/observability.md`, replace:

```markdown
so concurrent large bodies dominate |
```

with:

```markdown
so concurrent large bodies dominate |
| `inferplane_model_fallback_total` | Clients requesting unrouted model IDs (a typo or a newer alias); the log line `router: model fallback` names the requested ID |
```

In `docs/operations/observability.ko.md`, replace:

```markdown
동시에 들어온 큰 본문이 대부분을 차지 |
```

with:

```markdown
동시에 들어온 큰 본문이 대부분을 차지 |
| `inferplane_model_fallback_total` | 라우팅되지 않은 모델 ID 요청(오타·새 별칭). 로그 `router: model fallback`에 요청 ID가 남음 |
```

Refresh the Korean companion hash after the English edit:

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
python3 - <<'EOF'
import hashlib, pathlib, re
for en in ["docs/operations/observability.md"]:
    p = pathlib.Path(en); ko = p.with_suffix(".ko.md")
    h = hashlib.sha256(p.read_bytes()).hexdigest()
    t, n = re.subn(r"(?m)^translation_source_sha256: \S+$", "translation_source_sha256: " + h, ko.read_text(encoding="utf-8"), count=1)
    assert n == 1, ko
    ko.write_text(t, encoding="utf-8")
EOF
```

In `internal/CLAUDE.md`, replace:

```markdown
so it can never widen access or turn into a denial.
```

with:

```markdown
so it can never widen access or turn into a denial. `ResolveModelSource` adds the fallback's source (`model_fallbacks`|`family`); generation ingresses report an applied fallback with `ObserveModelFallback` (one bounded log line per pair, `inferplane_model_fallback_total{source,model}`) and `audit.WithModelFallback`.
```

In `internal/CLAUDE.md`, replace:

```markdown
thread it via request context from the ingress substitution point down to the audit call.
```

with:

```markdown
thread it via request context from the ingress substitution point down to the audit call. `RoutingRef.ModelFallbackFrom`/`ModelFallbackSource` (append-only, ADR-029) ride `WithModelFallback`; `RoutingFrom` attaches them to a policy decision or, without one, returns a ref with reason `model_fallback`.
```

- [ ] **Step 4: Run the task checks**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
gofmt -l internal | (! grep .) \
  && go vet ./internal/... ./cmd/... \
  && go test -buildvcs=false -race -count=1 ./internal/live/ ./internal/audit/ ./internal/router/ ./internal/metrics/ ./internal/server/... ./cmd/mayu/ \
  && python3 scripts/check_docs_translations.py > $L/logs/t5-pass.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL)' $L/logs/t5-pass.log
```

Expected: `exit=0` and no FAIL lines (cmd/mayu included); the translation checker passes.

- [ ] **Step 5: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
git -C $W add internal/live/fallback_source_test.go internal/audit/routing_fallback_test.go internal/router/fallback_log_test.go internal/server/model_fallback_test.go internal/server/bedrockapi/resolve_test.go internal/server/policy_routing_test.go internal/live/live.go internal/router/router.go internal/router/fallback_log.go internal/metrics/metrics.go internal/audit/routing.go internal/server/anthropicapi/messages.go internal/server/openaiapi/chat.go internal/server/bedrockapi/invoke.go internal/server/bedrockapi/resolve.go internal/server/responsesapi/handler.go docs/reference/api.md docs/reference/api.ko.md docs/reference/data.md docs/reference/data.ko.md docs/operations/observability.md docs/operations/observability.ko.md internal/CLAUDE.md
git -C $W commit -s -m "feat(router): make model fallback visible in logs, metrics and audit" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git -C $W status --short | grep -v '^??' ; echo clean-check-done
```

Expected: one new commit; `git status --short` lists no modified tracked files.


---

### Task 6: Client docs: tool search through the gateway, aliases and auto mode

**Items:** program item 8

**User-visible change:** Docs only. `docs/getting-started/clients.md` (+ko) no longer says Converse drops the search tool so deferred tools load up front. It states that Claude Code 2.1.311 keeps tool search on through the gateway, how Converse now sends `tool_reference` results (Task 3), that the built-in `sonnet` alias resolves to Sonnet 5 unless `ANTHROPIC_DEFAULT_SONNET_MODEL` is set, and the measured auto mode checklist (hyphenated Claude IDs, routable and allowed classifier model, fallback visibility from Task 5).

**Files:**
- Modify: `docs/getting-started/clients.md` (edits at lines 36–37, 47–52)
- Modify: `docs/getting-started/clients.ko.md` (edits at lines 41–42, 52–55)

**Interfaces:**
- None.

- [ ] **Step 1: Confirm the stale text is present (the check fails)**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
grep -q 'drops the search tool, so deferred tools' docs/getting-started/clients.md && ! grep -q 'ANTHROPIC_DEFAULT_SONNET_MODEL' docs/getting-started/clients.md && exit 1 > $L/logs/t6-fail.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL|ok )|undefined|unknown field' $L/logs/t6-fail.log | sort -u
```

Expected: `exit=1` (the stale sentence is present and the alias note is missing).

- [ ] **Step 2: Implement**

In `docs/getting-started/clients.md`, replace:

```markdown
Claude Code 2.1.304 and later enable MCP tool search (deferred tool loading)
by default through a custom `ANTHROPIC_BASE_URL`. Older clients require
```

with:

```markdown
Claude Code 2.1.304 and later enable MCP tool search (deferred tool loading)
by default through a custom `ANTHROPIC_BASE_URL`, and 2.1.311 keeps it on through
the gateway on every route: it turns tool search off only for some models, for
`ENABLE_TOOL_SEARCH=false` or with experimental betas disabled, never because of
the endpoint. Older clients require
```

In `docs/getting-started/clients.md`, replace:

```markdown
Anthropic and Bedrock `invoke_model` routes forward `defer_loading`, the search tool
and `tool_reference` history unchanged, and privacy inspection accepts those blocks;
Bedrock Converse cannot express them and drops the search tool, so deferred tools
load up front there. Verified on 2026-09-24 against Bedrock InvokeModel (Claude Haiku 4.5):
server-side search, a client-side `tool_reference` result and streaming, with and
without a `sensitiveData` policy. This is not qualification of every MCP server or model.
```

with:

```markdown
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
```

In `docs/getting-started/clients.ko.md`, replace:

```markdown
Claude Code 2.1.304부터 사용자 지정 `ANTHROPIC_BASE_URL`에서도 MCP 도구 검색(지연 로딩)을
기본으로 켭니다. 이전 버전은
```

with:

```markdown
Claude Code 2.1.304부터 사용자 지정 `ANTHROPIC_BASE_URL`에서도 MCP 도구 검색(지연 로딩)을
기본으로 켜며, 2.1.311은 게이트웨이를 거치는 모든 경로에서 이를 유지합니다. 도구 검색을
끄는 조건은 일부 모델, `ENABLE_TOOL_SEARCH=false`, 실험 beta 비활성화뿐이며 엔드포인트는
조건이 아닙니다. 이전 버전은
```

In `docs/getting-started/clients.ko.md`, replace:

```markdown
블록을 인식합니다. Bedrock Converse는 이를 표현할 수 없어 검색 도구를 버리므로 지연 도구가
처음부터 로드됩니다. 2026-09-24 Bedrock InvokeModel(Claude Haiku 4.5)에서 서버 측 검색,
클라이언트 측 `tool_reference` 결과, 스트리밍을 `sensitiveData` 정책 유무별로 검증했습니다.
모든 MCP 서버·모델에 대한 검증은 아닙니다.
```

with:

```markdown
블록을 인식합니다. Bedrock Converse(Claude 외 모델)에서도 Claude Code는 도구를 지연
로딩합니다. 불러온 도구 정의는 다음 요청의 tools에 들어가고, 게이트웨이는 `tool_reference`
결과를 `Loaded tool: <이름>` 텍스트로 보냅니다. 이전 게이트웨이 빌드는 이 결과를 빈 도구
결과로 보내 모델이 검색 결과가 없다고 판단할 수 있었습니다. 2026-10-27 Claude Code
2.1.311로 게이트웨이를 거쳐 Haiku 5.5(InvokeModel)의 지연 로딩과 gpt-6.1-terra(Converse)의
ToolSearch → CronList 호출을 확인했고, 2026-09-24 Bedrock InvokeModel(Claude Haiku 4.5)에서
서버 측 검색, 클라이언트 측 `tool_reference` 결과, 스트리밍을 `sensitiveData` 정책 유무별로
검증했습니다. 모든 MCP 서버·모델에 대한 검증은 아닙니다.

## Claude Code 모델 별칭과 auto mode {#claude-code-model-aliases-and-auto-mode}

Claude Code는 게이트웨이를 거칠 때 기본 `sonnet` 별칭을 Sonnet 5로 해석합니다.
다른 공개 모델 ID를 쓰려면 `ANTHROPIC_DEFAULT_SONNET_MODEL`을 지정하세요(예:
`global.anthropic.claude-sonnet-5-5`). 나머지 별칭은 `ANTHROPIC_DEFAULT_OPUS_MODEL`,
`ANTHROPIC_DEFAULT_HAIKU_MODEL`로 고정합니다. 2026-10-27 Claude Code 2.1.311로 측정한
auto mode(`--permission-mode auto`) 조건입니다.

- 메인 모델이 Claude Code 허용 목록에 있어야 합니다. Bedrock에서는 Opus·Sonnet 4.6 이하와
  Haiku 5.5를 제외한 모든 Haiku를 거부하며, Claude 외 모델은 통과해 로컬 분류기를 씁니다.
- Claude 모델 ID는 하이픈 버전 표기(`global.anthropic.claude-opus-4-8`)여야 합니다.
  점 표기(`claude-opus-4.8`)는 지원하지 않는 구버전으로 인식됩니다.
- 로컬 분류기 모델(`CLAUDE_CODE_AUTO_MODE_MODEL`이 없으면 `global.anthropic.claude-sonnet-5`)이
  게이트웨이에 라우팅되어 있고 키에 허용되어야 합니다. 이 자리에 넣은 Sonnet 5.5·Opus 5.5·Opus 5는
  무시됩니다. Claude ID를 잘못 입력하면 게이트웨이의 같은 계열 대체로 다른 모델이 응답할 수
  있으므로 `x-inferplane-model-fallback` 헤더나 `inferplane_model_fallback_total`을 확인하세요.
- Claude 외 메인 모델은 도구 호출마다 분류기 요청이 추가되므로 예산에 반영하세요.
```

Refresh the Korean companion hash after the English edit:

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
python3 - <<'EOF'
import hashlib, pathlib, re
for en in ["docs/getting-started/clients.md"]:
    p = pathlib.Path(en); ko = p.with_suffix(".ko.md")
    h = hashlib.sha256(p.read_bytes()).hexdigest()
    t, n = re.subn(r"(?m)^translation_source_sha256: \S+$", "translation_source_sha256: " + h, ko.read_text(encoding="utf-8"), count=1)
    assert n == 1, ko
    ko.write_text(t, encoding="utf-8")
EOF
```

- [ ] **Step 3: Run the task checks**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
! grep -q 'drops the search tool' docs/getting-started/clients.md \
  && grep -q 'ANTHROPIC_DEFAULT_SONNET_MODEL' docs/getting-started/clients.ko.md \
  && python3 scripts/check_docs_translations.py > $L/logs/t6-pass.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL)' $L/logs/t6-pass.log
```

Expected: `exit=0`; the checker prints `Validated … Korean/English document pairs and both ADR indexes.`

- [ ] **Step 4: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
git -C $W add docs/getting-started/clients.md docs/getting-started/clients.ko.md
git -C $W commit -s -m "docs(clients): correct tool search on Converse and add alias and auto mode notes" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git -C $W status --short | grep -v '^??' ; echo clean-check-done
```

Expected: one new commit; `git status --short` lists no modified tracked files.


---

### Task 7: Admin OIDC: per-kid isolation of JWKS verification

**Items:** program item 7

**User-visible change:** Admin-plane OIDC tokens whose `kid` is already cached verify without touching any limiter, even during an unknown-kid or forged-signature flood. Before, any failed verification refused every token for 10 s (`jwks: verification rate-limited after recent failure`) and concurrent verifications failed with `already in flight`. A newly rotated kid verifies as soon as a refetch is allowed: immediately when no miss armed the window, otherwise at the next allowed refetch, and a flood's own refetch also brings it in. The IdP still sees at most one miss-triggered JWKS fetch per backoff window.

**Files:**
- Test (modify): `internal/adminauth/oidc_test.go` (edits at lines 370–403)
- Create: `internal/adminauth/jwks.go`
- Modify: `internal/adminauth/oidc.go` (edits at lines 76–80, 92–96, 183–228)
- Modify: `go.mod` (edits at lines 14–15, 45–46)
- Modify: `docs/reference/security.md` (edits at lines 13)
- Modify: `docs/reference/security.ko.md` (edits at lines 18)
- Modify: `internal/CLAUDE.md` (edits at lines 110)

**Interfaces:**
- Produces unexported `newJWKSKeySet(url string, client *http.Client, window time.Duration) *jwksKeySet`, which implements go-oidc's `KeySet` (`VerifySignature(ctx context.Context, jwt string) ([]byte, error)`). Removes `missLimitedKeySet`.
- go.mod: `github.com/go-jose/go-jose/v4 v4.1.4` moves from indirect to direct (already in the module graph through go-oidc; no go.sum change).

- [ ] **Step 1: Write the failing tests**

In `internal/adminauth/oidc_test.go`, replace:

```go
// slowFailingKeySet fails verification after a delay and counts entries —
// the concurrency probe for the miss limiter's single-flight gate.
type slowFailingKeySet struct {
	entered atomic.Int64
	delay   time.Duration
}

func (s *slowFailingKeySet) VerifySignature(ctx context.Context, _ string) ([]byte, error) {
	s.entered.Add(1)
	time.Sleep(s.delay)
	return nil, context.DeadlineExceeded
}

// TestMissLimiterSingleFlight (P4 gate): a concurrent forged-kid burst must
// reach the inner (network-backed) key set at most once — concurrent callers
// fail fast instead of racing past an un-armed lastMiss.
func TestMissLimiterSingleFlight(t *testing.T) {
	inner := &slowFailingKeySet{delay: 100 * time.Millisecond}
	l := &missLimitedKeySet{inner: inner, window: time.Second}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.VerifySignature(context.Background(), "a.b.c")
		}()
	}
	wg.Wait()
	if got := inner.entered.Load(); got != 1 {
		t.Fatalf("inner key set entered %d times during concurrent burst, want 1", got)
	}
}
```

with:

```go
// A forged-kid burst must reach the IdP's JWKS endpoint at most once per
// window: concurrent misses share one fetch instead of racing past an
// un-armed window (P4 gate).
func TestJWKSForgedKidBurstFetchesOncePerWindow(t *testing.T) {
	idp := newFakeIdP(t)
	v := newTestVerifier(idp, cid)
	if _, err := v.Verify(context.Background(), idp.mint(t, "RS256", "r1", cid, nil)); err != nil {
		t.Fatal(err)
	}
	before := idp.hits.jwks.Load()
	forged := idp.mint(t, "RS256", "unknown-kid", cid, nil)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := v.Verify(context.Background(), forged); err == nil {
				t.Error("unknown kid verified")
			}
		}()
	}
	wg.Wait()
	if got := idp.hits.jwks.Load() - before; got != 1 {
		t.Fatalf("forged-kid burst fetched the JWKS %d times, want 1", got)
	}
}

// The old global limiter refused every token for the window after any
// failure, so a forged-kid flood locked out a legitimate, cached key.
func TestJWKSKnownKidVerifiesDuringForgedFlood(t *testing.T) {
	idp := newFakeIdP(t)
	v := newTestVerifier(idp, cid)
	valid := idp.mint(t, "RS256", "r1", cid, nil)
	if _, err := v.Verify(context.Background(), valid); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), idp.mint(t, "RS256", "unknown-kid", cid, nil)); err == nil {
		t.Fatal("unknown kid verified")
	}
	before := idp.hits.jwks.Load()
	if _, err := v.Verify(context.Background(), valid); err != nil {
		t.Fatalf("cached kid refused inside the miss window: %v", err)
	}
	if idp.hits.jwks.Load() != before {
		t.Fatal("a cached kid must verify without a fetch")
	}
}

// A rotated kid is picked up by the next fetch the window allows, even when
// a flood token triggers that fetch; it then verifies from cache.
func TestJWKSRotationSurvivesUnknownKidFlood(t *testing.T) {
	idp := newFakeIdP(t)
	v := newTestVerifier(idp, cid)
	if _, err := v.Verify(context.Background(), idp.mint(t, "RS256", "r1", cid, nil)); err != nil {
		t.Fatal(err)
	}
	forged := idp.mint(t, "RS256", "unknown-kid", cid, nil)
	_, _ = v.Verify(context.Background(), forged) // arms the miss window
	idp.rotated.Store(true)
	time.Sleep(250 * time.Millisecond)            // past newTestVerifier's 200ms window
	_, _ = v.Verify(context.Background(), forged) // the flood's allowed fetch also brings r2
	before := idp.hits.jwks.Load()
	if _, err := v.Verify(context.Background(), idp.mint(t, "RS256", "r2", cid, nil)); err != nil {
		t.Fatalf("rotated kid blocked by the flood: %v", err)
	}
	if idp.hits.jwks.Load() != before {
		t.Fatal("rotated kid should verify from the flood-refreshed cache")
	}
}

// Concurrent verifications of a valid token never wait on or fail because of
// one another (the old single-flight gate refused all but one).
func TestJWKSConcurrentValidTokensNeverRefused(t *testing.T) {
	idp := newFakeIdP(t)
	v := newTestVerifier(idp, cid)
	valid := idp.mint(t, "ES256", "e1", cid, nil)
	if _, err := v.Verify(context.Background(), valid); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var failed atomic.Int64
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := v.Verify(context.Background(), valid); err != nil {
				failed.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := failed.Load(); n != 0 {
		t.Fatalf("%d concurrent valid verifications refused", n)
	}
}
```

- [ ] **Step 2: Run them and confirm they fail**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
go test -buildvcs=false -count=1 -run 'TestJWKS|TestVerifyKeyRotation' ./internal/adminauth/ > $L/logs/t7-fail.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL|ok )|undefined|unknown field' $L/logs/t7-fail.log | sort -u
```

Expected: `exit=1`; `--- FAIL: TestJWKSKnownKidVerifiesDuringForgedFlood` and `--- FAIL: TestJWKSRotationSurvivesUnknownKidFlood`; `TestJWKSConcurrentValidTokensNeverRefused` normally fails as well. `TestVerifyKeyRotation` and `TestJWKSForgedKidBurstFetchesOncePerWindow` pass on the old code.

- [ ] **Step 3: Implement**

Create `internal/adminauth/jwks.go`:

```go
package adminauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// maxJWKSBytes bounds one key-set response; real sets are a few KiB.
const maxJWKSBytes = 1 << 20

// jwksKeySet verifies a token against the keys it already holds first, so a
// token whose kid is cached never touches the network and is never refused
// because another kid failed. Only a token no cached key verifies may refetch
// the issuer's JWKS: fetches are single-flight, concurrent misses wait for the
// same fetch, and a fetch that still left a token unverified (an unknown kid
// or a forged signature) arms a window in which misses fail fast without the
// network. A forged-kid flood therefore costs the IdP at most one fetch per
// window and cannot block a known key; a newly rotated kid is picked up by
// the next fetch the window allows, including one a flood triggers (P2 gate
// r1/r3, 2026-10-27 path check).
type jwksKeySet struct {
	url    string
	client *http.Client
	window time.Duration
	now    func() time.Time

	fetches atomic.Int64 // observability for tests

	mu       sync.Mutex
	keys     []jose.JSONWebKey
	lastMiss time.Time     // a fetch left a token unverified, or failed
	inflight chan struct{} // closed when the running fetch is settled
}

func newJWKSKeySet(url string, client *http.Client, window time.Duration) *jwksKeySet {
	return &jwksKeySet{url: url, client: client, window: window, now: time.Now}
}

func (s *jwksKeySet) VerifySignature(ctx context.Context, jwt string) ([]byte, error) {
	// The IDTokenVerifier has already enforced the RS256/ES256 allow-list.
	jws, err := jose.ParseSigned(jwt, []jose.SignatureAlgorithm{jose.RS256, jose.ES256})
	if err != nil || len(jws.Signatures) != 1 {
		return nil, errors.New("jwks: malformed token")
	}
	kid := jws.Signatures[0].Header.KeyID
	s.mu.Lock()
	keys := s.keys
	s.mu.Unlock()
	if payload, ok := verifyWith(keys, kid, jws); ok {
		return payload, nil
	}
	return s.verifyAfterFetch(ctx, kid, jws)
}

var errNoKey = errors.New("jwks: no key verifies the token signature")

// verifyAfterFetch handles a cache miss. It waits for a running fetch, fails
// fast inside the miss window, or performs the fetch itself: the fetching
// caller keeps the fetch in flight until it has armed the window, so a
// concurrent miss can never start a second fetch in between.
func (s *jwksKeySet) verifyAfterFetch(ctx context.Context, kid string, jws *jose.JSONWebSignature) ([]byte, error) {
	s.mu.Lock()
	if done := s.inflight; done != nil {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-done:
		}
		s.mu.Lock()
		keys := s.keys
		s.mu.Unlock()
		if payload, ok := verifyWith(keys, kid, jws); ok {
			return payload, nil
		}
		return nil, errNoKey
	}
	if !s.lastMiss.IsZero() && s.now().Sub(s.lastMiss) < s.window {
		s.mu.Unlock()
		return nil, errors.New("jwks: refetch rate-limited after a recent miss")
	}
	done := make(chan struct{})
	s.inflight = done
	s.mu.Unlock()

	// Detached from ctx: waiters share this fetch, and a canceled request
	// must not abort it for them. The client timeout bounds it.
	s.fetches.Add(1)
	keys, err := s.download()
	var payload []byte
	ok := false
	if err == nil {
		payload, ok = verifyWith(keys, kid, jws)
	}
	s.mu.Lock()
	if err == nil {
		s.keys = keys
	}
	if !ok {
		s.lastMiss = s.now() // unknown kid, forged signature or failed fetch
	}
	s.inflight = nil
	s.mu.Unlock()
	close(done)
	switch {
	case err != nil:
		return nil, err
	case !ok:
		return nil, errNoKey
	}
	return payload, nil
}

func verifyWith(keys []jose.JSONWebKey, kid string, jws *jose.JSONWebSignature) ([]byte, bool) {
	for i := range keys {
		if kid != "" && keys[i].KeyID != kid {
			continue
		}
		if payload, err := jws.Verify(&keys[i]); err == nil {
			return payload, true
		}
	}
	return nil, false
}

func (s *jwksKeySet) download() ([]jose.JSONWebKey, error) {
	resp, err := s.client.Get(s.url)
	if err != nil {
		return nil, fmt.Errorf("jwks: fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxJWKSBytes))
		return nil, fmt.Errorf("jwks: fetch: status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes+1))
	if err != nil {
		return nil, fmt.Errorf("jwks: fetch: %w", err)
	}
	if len(raw) > maxJWKSBytes {
		return nil, errors.New("jwks: key set too large")
	}
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, errors.New("jwks: malformed key set")
	}
	return set.Keys, nil
}
```

In `internal/adminauth/oidc.go`, replace:

```go
	// Background context, NOT the request context: go-oidc's remote key set
	// keeps this context for every future JWKS fetch — a canceled request
	// context would poison later refreshes. The client timeout bounds each
	// individual fetch instead.
	bg := oidc.ClientContext(context.Background(), &http.Client{Timeout: 5 * time.Second})
```

with:

```go
	// Background context, NOT the request context: a canceled request must not
	// fail discovery for later callers. The client timeout bounds the fetch;
	// jwksKeySet (jwks.go) owns key fetches with its own client.
	bg := oidc.ClientContext(context.Background(), &http.Client{Timeout: 5 * time.Second})
```

In `internal/adminauth/oidc.go`, replace:

```go
	keys := &missLimitedKeySet{
		inner:  oidc.NewRemoteKeySet(bg, meta.JWKSURL),
		window: v.backoff,
	}
	v.lastErr = nil
```

with:

```go
	keys := newJWKSKeySet(meta.JWKSURL, &http.Client{Timeout: 5 * time.Second}, v.backoff)
	v.lastErr = nil
```

In `internal/adminauth/oidc.go`, delete this block:

```go

// missLimitedKeySet rate-limits signature-verification failures: after a
// failed VerifySignature (which is when go-oidc refetches the JWKS for an
// unknown kid), further attempts inside the window fail fast WITHOUT touching
// the network — a forged-kid flood cannot hammer the IdP (P2 gate r1/r3).
// Successful verifications never arm the limiter, so legitimate key rotation
// (miss → refetch → success) is unaffected.
type missLimitedKeySet struct {
	inner  oidc.KeySet
	window time.Duration

	mu       sync.Mutex
	lastMiss time.Time
	inFlight bool
}

func (l *missLimitedKeySet) VerifySignature(ctx context.Context, jwt string) ([]byte, error) {
	// Single-flight gate (P4 gate): without it, a concurrent forged-kid burst
	// all passes the empty lastMiss check before the first failure arms the
	// window — N parallel verifications reach the network-backed key set.
	l.mu.Lock()
	if !l.lastMiss.IsZero() && time.Since(l.lastMiss) < l.window {
		l.mu.Unlock()
		return nil, errors.New("jwks: verification rate-limited after recent failure")
	}
	if l.inFlight {
		l.mu.Unlock()
		return nil, errors.New("jwks: verification already in flight, retry shortly")
	}
	l.inFlight = true
	l.mu.Unlock()

	payload, err := l.inner.VerifySignature(ctx, jwt)

	l.mu.Lock()
	l.inFlight = false
	if err != nil {
		l.lastMiss = time.Now()
	}
	l.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return payload, nil
}
```

In `go.mod`, replace:

```text
	github.com/coreos/go-oidc/v3 v3.18.0
```

with:

```text
	github.com/coreos/go-oidc/v3 v3.18.0
	github.com/go-jose/go-jose/v4 v4.1.4
```

In `go.mod`, delete this block:

```text
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
```

In `docs/reference/security.md`, replace:

```markdown
go-oidc verifier (alg pin, aud/azp, ±60s skew, JWKS negative cache) |
```

with:

```markdown
go-oidc verifier (alg pin, aud/azp, ±60s skew, discovery negative cache); `jwks.go` verifies against cached keys first and refetches the JWKS single-flight, at most once per backoff window after a miss, so an unknown-kid or forged-signature flood never blocks a cached or newly rotated key |
```

In `docs/reference/security.ko.md`, replace:

```markdown
| OIDC 검증 | `internal/adminauth/` | 형태 판정, 그룹→팀, 알고리즘 고정, aud/azp, ±60초 오차, JWKS 부정 캐시 |
```

with:

```markdown
| OIDC 검증 | `internal/adminauth/` | 형태 판정, 그룹→팀, 알고리즘 고정, aud/azp, ±60초 오차, 발견 부정 캐시. `jwks.go`는 캐시된 키로 먼저 검증하고, 실패 후에는 대기 시간창마다 한 번만 JWKS를 단일 요청으로 다시 받으므로 모르는 kid·위조 서명 범람이 캐시된 키나 새로 교체된 키를 막지 못함 |
```

Refresh the Korean companion hash after the English edit:

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
python3 - <<'EOF'
import hashlib, pathlib, re
for en in ["docs/reference/security.md"]:
    p = pathlib.Path(en); ko = p.with_suffix(".ko.md")
    h = hashlib.sha256(p.read_bytes()).hexdigest()
    t, n = re.subn(r"(?m)^translation_source_sha256: \S+$", "translation_source_sha256: " + h, ko.read_text(encoding="utf-8"), count=1)
    assert n == 1, ko
    ko.write_text(t, encoding="utf-8")
EOF
```

In `internal/CLAUDE.md`, replace:

```markdown
- `adminauth/` — admin-plane OIDC identity leaf (ADR-004): shared bearer-shape predicate, groups→team mapping, go-oidc verifier.
```

with:

```markdown
- `adminauth/` — admin-plane OIDC identity leaf (ADR-004): shared bearer-shape predicate, groups→team mapping, go-oidc verifier. `jwks.go` verifies cached keys first; only a miss refetches (single-flight), and a fetch that leaves a token unverified arms the backoff window, so key-rotation pickup is never blocked by another kid's failures.
```

- [ ] **Step 4: Run the task checks**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
gofmt -l internal/adminauth | (! grep .) \
  && go mod tidy -diff \
  && go vet ./internal/adminauth/ ./internal/server/ \
  && go test -buildvcs=false -race -count=3 ./internal/adminauth/ \
  && go test -buildvcs=false -count=1 ./internal/server/ \
  && python3 scripts/check_docs_translations.py > $L/logs/t7-pass.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL)' $L/logs/t7-pass.log
```

Expected: `exit=0`; `go mod tidy -diff` prints nothing; three race runs of `internal/adminauth` pass.

- [ ] **Step 5: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
git -C $W add internal/adminauth/oidc_test.go internal/adminauth/jwks.go internal/adminauth/oidc.go go.mod docs/reference/security.md docs/reference/security.ko.md internal/CLAUDE.md
git -C $W commit -s -m "fix(adminauth): verify cached JWKS keys first and limit refetches per miss window" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git -C $W status --short | grep -v '^??' ; echo clean-check-done
```

Expected: one new commit; `git status --short` lists no modified tracked files.


---

### Task 8: Protocol-shaped 413 for oversized bodies

**Items:** program item 6 (first half)

**User-visible change:** A body over `server.max_request_bytes` gets 413 in the ingress's own envelope, declared length or not: `/v1/messages` `{"type":"error","error":{"type":"request_too_large","message":"request body too large"}}`; `/v1/chat/completions` and `/v1/responses` `{"error":{"type":"invalid_request_error","message":"request body too large","code":null,"param":null}}`; `/model/*` `X-Amzn-ErrorType: ValidationException` with `{"message":"request body too large"}`. Before: `{"error":"request body too large"}` for a declared length on every non-Bedrock path, and 400 `could not read request body` for an undeclared oversized body on Messages, Chat and Bedrock. Counts still return 200.

**Files:**
- Test (create): `internal/server/oversized_body_test.go`
- Create: `internal/server/ingress_error.go`
- Modify: `internal/server/server.go` (edits at lines 337–346)
- Modify: `internal/server/anthropicapi/messages.go` (edits at lines 122–127)
- Modify: `internal/server/openaiapi/chat.go` (edits at lines 133–138)
- Modify: `internal/server/bedrockapi/invoke.go` (edits at lines 117–122)
- Modify: `internal/server/responsesapi/handler.go` (edits at lines 64–70)
- Modify: `docs/reference/api.md` (edits at lines 48)
- Modify: `docs/reference/api.ko.md` (edits at lines 65)
- Modify: `internal/CLAUDE.md` (edits at lines 91)

**Interfaces:**
- Produces unexported `writeIngressError(w http.ResponseWriter, path string, status int, anthropicType, openaiType, code, message string)` in package `server` (empty code renders `null`).

- [ ] **Step 1: Write the failing tests**

Create `internal/server/oversized_body_test.go`:

```go
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
)

// The 2026-10-27 path check: a 65 MiB Content-Length got
// {"error":"request body too large"} on every ingress, an envelope no client
// parses, and an undeclared oversized body got a generic 400 on most of them.
func TestOversizedBodyIsProtocolShaped(t *testing.T) {
	provs := map[string]providers.Provider{"p": mockprovider.New("m")}
	holder := newHolder(provs, map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}}})
	store := stubStore{key: "dev-key", p: keystore.Principal{KeyID: "ik_abc", Team: "t", AllowedModels: []string{"*"}}}
	mux := DataMux(router.New(holder), holder, store, nil, nil, nil, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0, WithMaxRequestBytes(64))
	body := `{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("x", 256) + `"}]}`
	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses", "/model/m/invoke", "/model/m/converse"} {
		for _, declared := range []bool{true, false} {
			t.Run(path+map[bool]string{true: "/declared", false: "/undeclared"}[declared], func(t *testing.T) {
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
				req.Header.Set("x-api-key", "dev-key")
				if !declared {
					req.ContentLength = -1
				}
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				var reply struct {
					Type    string                         `json:"type"`
					Message string                         `json:"message"`
					Error   struct{ Type, Message string } `json:"error"`
				}
				if rec.Code != http.StatusRequestEntityTooLarge || json.Unmarshal(rec.Body.Bytes(), &reply) != nil {
					t.Fatalf("oversized body = %d %s, want a parseable 413", rec.Code, rec.Body.String())
				}
				switch {
				case strings.HasPrefix(path, "/model/"):
					if rec.Header().Get("X-Amzn-ErrorType") != "ValidationException" || reply.Message != "request body too large" {
						t.Fatalf("not AWS-shaped: %s", rec.Body.String())
					}
				case path == "/v1/messages":
					if reply.Type != "error" || reply.Error.Type != "request_too_large" || reply.Error.Message != "request body too large" {
						t.Fatalf("not Anthropic-shaped: %s", rec.Body.String())
					}
				default:
					if reply.Error.Type != "invalid_request_error" || reply.Error.Message != "request body too large" {
						t.Fatalf("not OpenAI-shaped: %s", rec.Body.String())
					}
				}
			})
		}
	}
}
```

- [ ] **Step 2: Run them and confirm they fail**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
go test -buildvcs=false -count=1 -run 'TestOversizedBodyIsProtocolShaped' ./internal/server/ > $L/logs/t8-fail.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL|ok )|undefined|unknown field' $L/logs/t8-fail.log | sort -u
```

Expected: `exit=1`; `--- FAIL: TestOversizedBodyIsProtocolShaped` (`want a parseable 413`).

- [ ] **Step 3: Implement**

Create `internal/server/ingress_error.go`:

```go
package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/inferplane/inferplane/internal/server/bedrockapi"
)

// writeIngressError renders a refusal made before any ingress handler runs in
// the envelope that route's clients parse: AWS JSON with X-Amzn-ErrorType on
// /model/*, Anthropic's envelope on /v1/messages*, and the OpenAI envelope
// (Chat Completions, Responses) everywhere else. An empty code is null.
func writeIngressError(w http.ResponseWriter, path string, status int, anthropicType, openaiType, code, message string) {
	if strings.HasPrefix(path, "/model/") {
		bedrockapi.WriteError(w, status, message)
		return
	}
	var body any
	if strings.HasPrefix(path, "/v1/messages") {
		body = map[string]any{"type": "error", "error": map[string]string{"type": anthropicType, "message": message}}
	} else {
		var openaiCode any
		if code != "" {
			openaiCode = code
		}
		body = map[string]any{"error": map[string]any{"type": openaiType, "message": message, "param": nil, "code": openaiCode}}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
```

In `internal/server/server.go`, replace:

```go
		if r.ContentLength > limit && !count {
			if strings.HasPrefix(r.URL.Path, "/model/") {
				bedrockapi.WriteError(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_, _ = w.Write([]byte(`{"error":"request body too large"}`))
			return
		}
```

with:

```go
		if r.ContentLength > limit && !count {
			writeIngressError(w, r.URL.Path, http.StatusRequestEntityTooLarge, "request_too_large", "invalid_request_error", "", "request body too large")
			return
		}
```

In `internal/server/anthropicapi/messages.go`, replace:

```go
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		writeErr(w, 400, "invalid_request_error", "could not read request body")
		return
	}
	// Parse for routing/observation ONLY.
```

with:

```go
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			// An undeclared length over server.max_request_bytes gets the same
			// 413 as a declared one (server.maxBytesMiddleware).
			writeErr(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body too large")
			return
		}
		writeErr(w, 400, "invalid_request_error", "could not read request body")
		return
	}
	// Parse for routing/observation ONLY.
```

In `internal/server/openaiapi/chat.go`, replace:

```go
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		writeErr(w, 400, "invalid_request_error", "could not read request body")
		return
	}
	// Parse OpenAI body
```

with:

```go
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			// An undeclared length over server.max_request_bytes gets the same
			// 413 as a declared one (server.maxBytesMiddleware).
			writeErr(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request body too large")
			return
		}
		writeErr(w, 400, "invalid_request_error", "could not read request body")
		return
	}
	// Parse OpenAI body
```

In `internal/server/bedrockapi/invoke.go`, replace:

```go
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not read request body")
		return
	}
	if h.converse {
```

with:

```go
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			// An undeclared length over server.max_request_bytes gets the same
			// 413 as a declared one (server.maxBytesMiddleware).
			writeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeErr(w, http.StatusBadRequest, "could not read request body")
		return
	}
	if h.converse {
```

In `internal/server/responsesapi/handler.go`, replace:

```go
		status := http.StatusBadRequest
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			status = http.StatusRequestEntityTooLarge
		}
		writeError(w, status, "invalid_request_error", "could not read request body")
		return
```

with:

```go
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			writeError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request_error", "could not read request body")
		return
```

In `docs/reference/api.md`, replace:

```markdown
- Errors are returned in the ingress protocol's own error shape; the unknown/disallowed-model messages append the allow-filtered available-model list (ADR-021).
```

with:

```markdown
- Errors are returned in the ingress protocol's own error shape; the unknown/disallowed-model messages append the allow-filtered available-model list (ADR-021).
- A body over `server.max_request_bytes` gets HTTP 413 in the ingress's own envelope, whether or not Content-Length was declared: Anthropic `request_too_large`, Chat Completions and Responses `invalid_request_error`, Bedrock `ValidationException` (it used to be `{"error":"request body too large"}`, or a 400 when the length was undeclared). Count endpoints still answer 200 with a local estimate.
```

In `docs/reference/api.ko.md`, replace:

```markdown
오류는 수신 형식이며 허용된 가용 모델 목록을 표시합니다.
```

with:

```markdown
오류는 수신 형식이며 허용된 가용 모델 목록을 표시합니다. `server.max_request_bytes`를 넘는 본문은 Content-Length 선언 여부와 관계없이 수신 형식의 HTTP 413을 받습니다. Anthropic은 `request_too_large`, Chat Completions·Responses는 `invalid_request_error`, Bedrock은 `ValidationException`입니다(이전에는 `{"error":"request body too large"}`였고 길이를 선언하지 않으면 400이었습니다). count 엔드포인트는 계속 로컬 추정치로 200을 반환합니다.
```

Refresh the Korean companion hash after the English edit:

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
python3 - <<'EOF'
import hashlib, pathlib, re
for en in ["docs/reference/api.md"]:
    p = pathlib.Path(en); ko = p.with_suffix(".ko.md")
    h = hashlib.sha256(p.read_bytes()).hexdigest()
    t, n = re.subn(r"(?m)^translation_source_sha256: \S+$", "translation_source_sha256: " + h, ko.read_text(encoding="utf-8"), count=1)
    assert n == 1, ko
    ko.write_text(t, encoding="utf-8")
EOF
```

In `internal/CLAUDE.md`, replace:

```markdown
stays for the terminal pre-commit rejects, where there is no usage or cost to report.
```

with:

```markdown
stays for the terminal pre-commit rejects, where there is no usage or cost to report. Refusals made before a handler runs use `writeIngressError` (`ingress_error.go`) for the route's envelope; an oversized body gets 413 in that envelope whether its length was declared (middleware) or not (each handler's `MaxBytesError` branch).
```

- [ ] **Step 4: Run the task checks**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
gofmt -l internal/server | (! grep .) \
  && go vet ./internal/server/... \
  && go test -buildvcs=false -race -count=1 ./internal/server/... \
  && python3 scripts/check_docs_translations.py > $L/logs/t8-pass.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL)' $L/logs/t8-pass.log
```

Expected: `exit=0` and no FAIL lines; the translation checker passes.

- [ ] **Step 5: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
git -C $W add internal/server/oversized_body_test.go internal/server/ingress_error.go internal/server/server.go internal/server/anthropicapi/messages.go internal/server/openaiapi/chat.go internal/server/bedrockapi/invoke.go internal/server/responsesapi/handler.go docs/reference/api.md docs/reference/api.ko.md internal/CLAUDE.md
git -C $W commit -s -m "fix(server): return protocol-shaped 413 for oversized bodies on every ingress" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git -C $W status --short | grep -v '^??' ; echo clean-check-done
```

Expected: one new commit; `git status --short` lists no modified tracked files.


---

### Task 9: Optional in-flight request byte budget

**Items:** program item 6 (second half)

**User-visible change:** New optional `server.max_inflight_request_bytes`: 0 (the default) keeps today's behavior. When set (it must be at least `server.max_request_bytes`), a generation request that does not fit the budget gets 503 with `Retry-After: 2` (Anthropic `overloaded_error`; Chat and Responses `server_error` with code `gateway_overloaded`; Bedrock `ServiceUnavailableException`), and a count request that does not fit gets HTTP 200 with `max(1, bytes/4)` without its body being buffered. New metrics `inferplane_inflight_request_bytes` and `inferplane_inflight_refusals_total{route}`.

**Files:**
- Test (create): `internal/server/inflight_test.go`
- Test (modify): `internal/config/max_request_bytes_test.go` (edits at lines 42)
- Modify: `internal/config/config.go` (edits at lines 198–199, 220–224)
- Modify: `internal/server/server.go` (edits at lines 53–58, 100, 250–253)
- Create: `internal/server/inflight.go`
- Modify: `internal/metrics/metrics.go` (edits at lines 44–45, 131–132, 143, 182)
- Modify: `cmd/mayu/gateway.go` (edits at lines 927)
- Modify: `docs/reference/infrastructure.md` (edits at lines 44–45)
- Modify: `docs/reference/infrastructure.ko.md` (edits at lines 36)
- Modify: `docs/reference/api.md` (edits at lines 49)
- Modify: `docs/reference/api.ko.md` (edits at lines 65)
- Modify: `docs/operations/observability.md` (edits at lines 34, 47)
- Modify: `docs/operations/observability.ko.md` (edits at lines 39, 52)
- Modify: `internal/CLAUDE.md` (edits at lines 113)
- Modify: `charts/inferplane/values.yaml` (edits at lines 120)

**Interfaces:**
- Produces: `config.ServerConfig.MaxInflightRequestBytes int64` (`server.max_inflight_request_bytes`); `func server.WithMaxInflightRequestBytes(n int64) DataMuxOption`; unexported `inflightBudget{limit}` with `acquire(n int64) (int64, bool)`/`release(n int64) int64`, `inflightMiddleware(b *inflightBudget, requestLimit int64, m *metrics.Metrics, next http.Handler) http.Handler`, `writeLocalCount`, `countPath`; `func (m *metrics.Metrics) SetInflightRequestBytes(n int64)`; `func (m *metrics.Metrics) IncInflightRefusal(route string)`.
- Consumes: `writeIngressError` (Task 8), `clientctx.Middleware` ordering in `DataMux` (the budget sits inside it).

- [ ] **Step 1: Write the failing tests**

Create `internal/server/inflight_test.go`:

```go
package server

import (
	"context"
	"encoding/json"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/metrics"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
)

// heldProvider blocks Complete until released, holding its request's budget.
type heldProvider struct {
	providers.Provider
	entered, release chan struct{}
}

func (p *heldProvider) Complete(ctx context.Context, r *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	p.entered <- struct{}{}
	<-p.release
	return p.Provider.Complete(ctx, r)
}

func inflightDo(mux http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("x-api-key", "dev-key")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestInflightBudgetRefusesGenerationAndKeepsCountsLocal(t *testing.T) {
	held := &heldProvider{Provider: mockprovider.New("m"), entered: make(chan struct{}), release: make(chan struct{})}
	holder := newHolder(map[string]providers.Provider{"p": held}, map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}}})
	store := stubStore{key: "dev-key", p: keystore.Principal{KeyID: "ik_abc", Team: "t", AllowedModels: []string{"*"}}}
	m := metrics.New()
	mux := DataMux(router.New(holder), holder, store, nil, nil, m, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0,
		WithMaxRequestBytes(4096), WithMaxInflightRequestBytes(4096))
	body := `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"` + strings.Repeat("x", 3000) + `"}]}`

	first := make(chan *httptest.ResponseRecorder)
	go func() { first <- inflightDo(mux, "/v1/messages", body) }()
	<-held.entered // the first request now holds its bytes

	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses", "/model/m/invoke", "/model/m/converse-stream"} {
		rec := inflightDo(mux, path, body)
		var reply struct {
			Type    string                               `json:"type"`
			Message string                               `json:"message"`
			Error   struct{ Type, Code, Message string } `json:"error"`
		}
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "2" || json.Unmarshal(rec.Body.Bytes(), &reply) != nil {
			t.Fatalf("%s over budget = %d %q %s, want 503 + Retry-After", path, rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
		}
		switch {
		case strings.HasPrefix(path, "/model/"):
			if rec.Header().Get("X-Amzn-ErrorType") != "ServiceUnavailableException" || reply.Message == "" {
				t.Fatalf("%s: not AWS-shaped: %s", path, rec.Body.String())
			}
		case path == "/v1/messages":
			if reply.Type != "error" || reply.Error.Type != "overloaded_error" {
				t.Fatalf("%s: not Anthropic-shaped: %s", path, rec.Body.String())
			}
		default:
			if reply.Error.Type != "server_error" || reply.Error.Code != "gateway_overloaded" {
				t.Fatalf("%s: not OpenAI-shaped: %s", path, rec.Body.String())
			}
		}
	}
	for path, field := range map[string]string{"/v1/messages/count_tokens": "input_tokens", "/model/m/count-tokens": "inputTokens"} {
		rec := inflightDo(mux, path, body)
		var out map[string]int64
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out[field] != int64(len(body)/4) {
			t.Fatalf("%s over budget = %d %s, want a local HTTP-200 estimate", path, rec.Code, rec.Body.String())
		}
	}
	refusals := map[string]float64{}
	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "inferplane_inflight_refusals_total" {
			for _, metric := range family.GetMetric() {
				refusals[metric.GetLabel()[0].GetValue()] = metric.GetCounter().GetValue()
			}
		}
	}
	if refusals["generation"] != 5 || refusals["count"] != 2 {
		t.Fatalf("refusal metrics = %v, want 5 generation and 2 count", refusals)
	}

	close(held.release)
	if rec := <-first; rec.Code != http.StatusOK {
		t.Fatalf("held request = %d %s", rec.Code, rec.Body.String())
	}
	go func() {
		for range held.entered {
		}
	}()
	if rec := inflightDo(mux, "/v1/messages", body); rec.Code != http.StatusOK {
		t.Fatalf("budget not released: %d %s", rec.Code, rec.Body.String())
	}
	close(held.entered)
}

func TestInflightBudgetOffByDefault(t *testing.T) {
	held := &heldProvider{Provider: mockprovider.New("m"), entered: make(chan struct{}, 2), release: make(chan struct{})}
	holder := newHolder(map[string]providers.Provider{"p": held}, map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}}})
	store := stubStore{key: "dev-key", p: keystore.Principal{KeyID: "ik_abc", Team: "t", AllowedModels: []string{"*"}}}
	mux := DataMux(router.New(holder), holder, store, nil, nil, nil, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0, WithMaxRequestBytes(4096))
	body := `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"` + strings.Repeat("x", 3000) + `"}]}`
	done := make(chan int, 2)
	for range 2 {
		go func() { done <- inflightDo(mux, "/v1/messages", body).Code }()
	}
	<-held.entered
	<-held.entered // both requests are in flight together
	close(held.release)
	if a, b := <-done, <-done; a != http.StatusOK || b != http.StatusOK {
		t.Fatalf("unset budget refused concurrent requests: %d %d", a, b)
	}
}

// heldStream yields the first event, then blocks until released.
type heldStream struct {
	providers.Provider
	entered, release chan struct{}
}

func (p *heldStream) Stream(ctx context.Context, r *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	inner, err := p.Provider.Stream(ctx, r)
	if err != nil {
		return nil, err
	}
	return func(yield func(*providers.StreamEvent, error) bool) {
		first := true
		for ev, err := range inner {
			if !yield(ev, err) {
				return
			}
			if first {
				first = false
				p.entered <- struct{}{}
				<-p.release
			}
		}
	}, nil
}

// A stream keeps its body (for fallback) until it ends, so it holds its bytes
// for the whole stream; a request without Content-Length reserves the whole
// per-request cap even when its body is small.
func TestInflightBudgetHeldByStreamsAndUndeclaredLengths(t *testing.T) {
	held := &heldStream{Provider: mockprovider.New("m"), entered: make(chan struct{}), release: make(chan struct{})}
	holder := newHolder(map[string]providers.Provider{"p": held}, map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}}})
	store := stubStore{key: "dev-key", p: keystore.Principal{KeyID: "ik_abc", Team: "t", AllowedModels: []string{"*"}}}
	mux := DataMux(router.New(holder), holder, store, nil, nil, nil, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0,
		WithMaxRequestBytes(4096), WithMaxInflightRequestBytes(4096))
	small := `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
	stream := `{"model":"m","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"` + strings.Repeat("x", 3000) + `"}]}`

	done := make(chan int)
	go func() { done <- inflightDo(mux, "/v1/messages", stream).Code }()
	<-held.entered // first event written, stream still open
	if rec := inflightDo(mux, "/v1/messages", strings.Repeat(" ", 1200)+small); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("open stream released its budget early: %d", rec.Code)
	}
	close(held.release)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("stream = %d", code)
	}

	undeclared := func() int {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(small))
		req.Header.Set("x-api-key", "dev-key")
		req.ContentLength = -1
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := undeclared(); code != http.StatusOK {
		t.Fatalf("undeclared length alone = %d, want admitted", code)
	}
	held.entered, held.release = make(chan struct{}), make(chan struct{})
	go func() {
		done <- inflightDo(mux, "/v1/messages", strings.Replace(stream, strings.Repeat("x", 3000), "x", 1)).Code
	}()
	<-held.entered // a small declared stream holds a few bytes
	if code := undeclared(); code != http.StatusServiceUnavailable {
		t.Fatalf("undeclared length beside another request = %d, want 503 (it reserves the 4096-byte cap)", code)
	}
	close(held.release)
	<-done
}
```

In `internal/config/max_request_bytes_test.go`, replace:

```go
// models.<name>.context_window: negative rejected
```

with:

```go
// server.max_inflight_request_bytes: 0 is off, negative is an error, and a
// positive budget must admit the largest single request on its own.
func TestValidateServerMaxInflightRequestBytes(t *testing.T) {
	for _, tc := range []struct {
		name            string
		request, budget int64
		wantErr         bool
	}{
		{"off by default", 0, 0, false},
		{"negative rejected", 0, -1, true},
		{"below the defaulted request cap", 0, 32 << 20, true},
		{"equal to the request cap", 16 << 20, 16 << 20, false},
		{"above the request cap", 16 << 20, 96 << 20, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := ServerConfig{MaxRequestBytes: tc.request, MaxInflightRequestBytes: tc.budget}
			if err := validateServer(&s); (err != nil) != tc.wantErr {
				t.Fatalf("validateServer error = %v, wantErr %t", err, tc.wantErr)
			}
		})
	}
}

// models.<name>.context_window: negative rejected
```

- [ ] **Step 2: Run them and confirm they fail**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
go test -buildvcs=false -count=1 -run 'Inflight|ValidateServerMaxInflight' ./internal/server/ ./internal/config/ > $L/logs/t9-fail.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL|ok )|undefined|unknown field' $L/logs/t9-fail.log | sort -u
```

Expected: `exit=1`; `internal/config [build failed]` (unknown field MaxInflightRequestBytes) and `internal/server [build failed]` (undefined: WithMaxInflightRequestBytes).

- [ ] **Step 3: Implement**

In `internal/config/config.go`, replace:

```go
	MaxRequestBytes int64 `json:"max_request_bytes,omitempty"`
}
```

with:

```go
	MaxRequestBytes int64 `json:"max_request_bytes,omitempty"`
	// MaxInflightRequestBytes bounds the request-body bytes all data-plane
	// requests hold at once; a request that does not fit is refused with a
	// protocol-shaped 503 (counts are answered locally, still HTTP 200).
	// 0 (unset) disables it; otherwise it must be >= MaxRequestBytes so any
	// admissible request can run alone.
	MaxInflightRequestBytes int64 `json:"max_inflight_request_bytes,omitempty"`
}
```

In `internal/config/config.go`, replace:

```go
	if s.MaxRequestBytes == 0 {
		s.MaxRequestBytes = defaultMaxRequestBytes
	}
	return nil
}
```

with:

```go
	if s.MaxRequestBytes == 0 {
		s.MaxRequestBytes = defaultMaxRequestBytes
	}
	if s.MaxInflightRequestBytes < 0 {
		return fmt.Errorf("config: server.max_inflight_request_bytes must be >= 0")
	}
	if s.MaxInflightRequestBytes > 0 && s.MaxInflightRequestBytes < s.MaxRequestBytes {
		return fmt.Errorf("config: server.max_inflight_request_bytes must be 0 (off) or >= server.max_request_bytes (%d)", s.MaxRequestBytes)
	}
	return nil
}
```

In `internal/server/server.go`, replace:

```go
type dataMuxOptions struct {
	usage           *telemetry.Collector
	maxRequestBytes int64
	governanceGate  func() (bool, string)
	clientEvidence  clientctx.Mode
}
```

with:

```go
type dataMuxOptions struct {
	usage            *telemetry.Collector
	maxRequestBytes  int64
	maxInflightBytes int64
	governanceGate   func() (bool, string)
	clientEvidence   clientctx.Mode
}
```

In `internal/server/server.go`, replace:

```go
// DataMux builds the data-plane (:8080) handler
```

with:

```go
// WithMaxInflightRequestBytes bounds the request-body bytes all KeyAuth-guarded
// requests hold at once (server.max_inflight_request_bytes). n<=0 leaves it
// off, the default.
func WithMaxInflightRequestBytes(n int64) DataMuxOption {
	return func(o *dataMuxOptions) { o.maxInflightBytes = n }
}

// DataMux builds the data-plane (:8080) handler
```

In `internal/server/server.go`, replace:

```go
	if aud != nil {
		governed = auditGateMiddleware(aud.Ready, governed)
	}
	dataMux.Handle("/", maxBytesMiddleware(limit, KeyAuth(store, clientctx.Middleware(o.clientEvidence, governed))))
```

with:

```go
	if aud != nil {
		governed = auditGateMiddleware(aud.Ready, governed)
	}
	if o.maxInflightBytes > 0 {
		governed = inflightMiddleware(&inflightBudget{limit: o.maxInflightBytes}, limit, m, governed)
	}
	dataMux.Handle("/", maxBytesMiddleware(limit, KeyAuth(store, clientctx.Middleware(o.clientEvidence, governed))))
```

Create `internal/server/inflight.go`:

```go
package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/inferplane/inferplane/internal/metrics"
)

// inflightRetryAfterSeconds is the polling hint for a refused request: one
// large body usually finishes within seconds.
const inflightRetryAfterSeconds = "2"

// inflightBudget bounds the request-body bytes held across all data-plane
// requests (server.max_inflight_request_bytes). It never queues.
type inflightBudget struct {
	mu    sync.Mutex
	used  int64
	limit int64
}

// acquire reserves n bytes, reporting the new total, or refuses.
func (b *inflightBudget) acquire(n int64) (int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > b.limit-b.used {
		return b.used, false
	}
	b.used += n
	return b.used, true
}

func (b *inflightBudget) release(n int64) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.used -= n
	return b.used
}

func countPath(path string) bool {
	return strings.HasSuffix(path, "/count_tokens") || strings.HasSuffix(path, "/count-tokens")
}

// inflightMiddleware sits inside KeyAuth, so only authenticated callers hold
// budget. A request reserves its declared Content-Length, or the whole
// per-request cap when the length is undeclared, until its handler returns —
// a stream until it ends, since its body is kept for fallback. Ingress
// processing holds several copies of each body (≈5.5× measured 2026-10-27),
// so size the budget from the memory limit, not the body sizes. Generation
// that does not fit is refused with a protocol-shaped 503 and Retry-After.
// A count that does not fit stays HTTP 200: it is answered locally at four
// bytes per token, without buffering the body or calling a provider.
func inflightMiddleware(b *inflightBudget, requestLimit int64, m *metrics.Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := r.ContentLength
		if n < 0 {
			n = requestLimit
		}
		if n == 0 {
			next.ServeHTTP(w, r)
			return
		}
		used, ok := b.acquire(n)
		if !ok {
			if countPath(r.URL.Path) {
				m.IncInflightRefusal("count")
				writeLocalCount(w, r)
				return
			}
			m.IncInflightRefusal("generation")
			w.Header().Set("Retry-After", inflightRetryAfterSeconds)
			writeIngressError(w, r.URL.Path, http.StatusServiceUnavailable, "overloaded_error", "server_error", "gateway_overloaded",
				"gateway is processing too many large requests; retry shortly")
			return
		}
		m.SetInflightRequestBytes(used)
		defer func() { m.SetInflightRequestBytes(b.release(n)) }()
		next.ServeHTTP(w, r)
	})
}

// writeLocalCount answers a count request without holding its body: the
// declared length, or the bytes drained from a bounded undeclared body.
func writeLocalCount(w http.ResponseWriter, r *http.Request) {
	size := r.ContentLength
	if size < 0 {
		size, _ = io.Copy(io.Discard, r.Body)
	}
	field := "input_tokens"
	if strings.HasPrefix(r.URL.Path, "/model/") {
		field = "inputTokens" // Bedrock CountTokens shape
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]int64{field: max(int64(1), size/4)})
}
```

In `internal/metrics/metrics.go`, replace:

```go
	modelFallback      *prometheus.CounterVec // inferplane_model_fallback_total (ADR-029)
```

with:

```go
	modelFallback      *prometheus.CounterVec // inferplane_model_fallback_total (ADR-029)
	inflightBytes      prometheus.Gauge       // inferplane_inflight_request_bytes
	inflightRefusals   *prometheus.CounterVec // inferplane_inflight_refusals_total
```

In `internal/metrics/metrics.go`, replace:

```go
		}, []string{"source", "model"}),
	}
```

with:

```go
		}, []string{"source", "model"}),
		inflightBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "inferplane_inflight_request_bytes", Help: "Request-body bytes reserved against server.max_inflight_request_bytes.",
		}),
		inflightRefusals: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "inferplane_inflight_refusals_total",
			Help: "Requests that did not fit server.max_inflight_request_bytes: generation refused with 503, count answered locally.",
		}, []string{"route"}),
	}
```

In `internal/metrics/metrics.go`, replace:

```go
		m.autoModeUnparsed, m.modelFallback)
```

with:

```go
		m.autoModeUnparsed, m.modelFallback, m.inflightBytes, m.inflightRefusals)
```

In `internal/metrics/metrics.go`, replace:

```go
// Registry exposes the registry for the /metrics handler.
```

with:

```go
// SetInflightRequestBytes reports the bytes currently reserved by the
// data plane's in-flight request budget.
func (m *Metrics) SetInflightRequestBytes(n int64) {
	if m == nil {
		return
	}
	m.inflightBytes.Set(float64(n))
}

// IncInflightRefusal counts one request that did not fit the in-flight budget;
// route is "generation" or "count".
func (m *Metrics) IncInflightRefusal(route string) {
	if m == nil {
		return
	}
	if route != "count" {
		route = "generation"
	}
	m.inflightRefusals.WithLabelValues(route).Inc()
}

// Registry exposes the registry for the /metrics handler.
```

In `cmd/mayu/gateway.go`, replace:

```go
server.WithMaxRequestBytes(cfg.Server.MaxRequestBytes), server.WithGovernanceGate(governanceGate),
```

with:

```go
server.WithMaxRequestBytes(cfg.Server.MaxRequestBytes), server.WithMaxInflightRequestBytes(cfg.Server.MaxInflightRequestBytes), server.WithGovernanceGate(governanceGate),
```

In `docs/reference/infrastructure.md`, replace:

```markdown
- Both gateway listeners have a 10-second request-header deadline. There is no
  total response deadline that would cut off long-running SSE streams.
```

with:

```markdown
- Both gateway listeners have a 10-second request-header deadline. There is no
  total response deadline that would cut off long-running SSE streams.
- `server.max_request_bytes` (default 64 MiB) bounds one request body.
  `server.max_inflight_request_bytes` (default 0, off; otherwise at least the
  per-request cap) bounds the body bytes all data-plane requests hold at once.
  Each request reserves its Content-Length, or the whole per-request cap when the
  length is undeclared, until its handler returns; a stream holds it until it
  ends. A generation request that does not fit gets a protocol-shaped 503 with
  `Retry-After: 2`; a count request stays HTTP 200 with a local estimate of four
  bytes per token. Ingress holds several copies of each body (about 5.5×
  measured on 2026-10-27), so size the budget as (memory limit − idle RSS) / 6
  and also set `GOMEMLIMIT` and the container or systemd memory limit.
```

In `docs/reference/infrastructure.ko.md`, replace:

```markdown
- 두 게이트웨이 리스너에는 요청 헤더를 읽는 10초 제한이 있습니다. 긴 SSE 스트림을 끊는 전체 응답 시간 제한은 없습니다.
```

with:

```markdown
- 두 게이트웨이 리스너에는 요청 헤더를 읽는 10초 제한이 있습니다. 긴 SSE 스트림을 끊는 전체 응답 시간 제한은 없습니다.
- `server.max_request_bytes`(기본 64 MiB)는 요청 본문 하나를 제한합니다. `server.max_inflight_request_bytes`(기본 0, 꺼짐. 켜면 요청당 한도 이상)는 모든 데이터 영역 요청이 동시에 보유하는 본문 바이트를 제한합니다. 요청마다 Content-Length만큼, 길이를 선언하지 않으면 요청당 한도 전체를 핸들러가 끝날 때까지 예약하며 스트림은 끝날 때까지 유지합니다. 들어가지 못한 생성 요청은 수신 형식의 503과 `Retry-After: 2`를 받고, count 요청은 4바이트당 1토큰의 로컬 추정치로 HTTP 200을 유지합니다. 수신 경로는 본문마다 여러 사본을 보유하므로(2026-10-27 실측 약 5.5배) 예산은 (메모리 한도 − 유휴 RSS) / 6로 잡고 `GOMEMLIMIT`과 컨테이너·systemd 메모리 한도도 설정하세요.
```

Refresh the Korean companion hash after the English edit:

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
python3 - <<'EOF'
import hashlib, pathlib, re
for en in ["docs/reference/infrastructure.md"]:
    p = pathlib.Path(en); ko = p.with_suffix(".ko.md")
    h = hashlib.sha256(p.read_bytes()).hexdigest()
    t, n = re.subn(r"(?m)^translation_source_sha256: \S+$", "translation_source_sha256: " + h, ko.read_text(encoding="utf-8"), count=1)
    assert n == 1, ko
    ko.write_text(t, encoding="utf-8")
EOF
```

In `docs/reference/api.md`, replace:

```markdown
Count endpoints still answer 200 with a local estimate.
```

with:

```markdown
Count endpoints still answer 200 with a local estimate. With `server.max_inflight_request_bytes` set, a generation request that does not fit the in-flight byte budget gets 503 with `Retry-After: 2` in the same envelopes (Anthropic `overloaded_error`, Chat Completions and Responses `server_error` with code `gateway_overloaded`, Bedrock `ServiceUnavailableException`); a count request that does not fit is answered locally (HTTP 200, four bytes per token) without buffering its body.
```

In `docs/reference/api.ko.md`, replace:

```markdown
count 엔드포인트는 계속 로컬 추정치로 200을 반환합니다.
```

with:

```markdown
count 엔드포인트는 계속 로컬 추정치로 200을 반환합니다. `server.max_inflight_request_bytes`를 설정하면 동시 처리 본문 예산에 들어가지 못한 생성 요청은 같은 형식의 503과 `Retry-After: 2`를 받습니다(Anthropic `overloaded_error`, Chat Completions·Responses는 코드 `gateway_overloaded`의 `server_error`, Bedrock `ServiceUnavailableException`). 들어가지 못한 count 요청은 본문을 버퍼링하지 않고 로컬에서 답합니다(HTTP 200, 4바이트당 1토큰).
```

Refresh the Korean companion hash after the English edit:

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
python3 - <<'EOF'
import hashlib, pathlib, re
for en in ["docs/reference/api.md"]:
    p = pathlib.Path(en); ko = p.with_suffix(".ko.md")
    h = hashlib.sha256(p.read_bytes()).hexdigest()
    t, n = re.subn(r"(?m)^translation_source_sha256: \S+$", "translation_source_sha256: " + h, ko.read_text(encoding="utf-8"), count=1)
    assert n == 1, ko
    ko.write_text(t, encoding="utf-8")
EOF
```

In `docs/operations/observability.md`, replace:

```markdown
the log line `router: model fallback` names the requested ID |
```

with:

```markdown
the log line `router: model fallback` names the requested ID |
| `inferplane_inflight_refusals_total`, `inferplane_inflight_request_bytes` | In-flight body budget (`server.max_inflight_request_bytes`) too small for the workload, or a burst of large requests |
```

In `docs/operations/observability.md`, replace:

```markdown
| 503 before first request | Required sync, shared namespace/binding, initial grant, DB/journal availability |
```

with:

```markdown
| 503 before first request | Required sync, shared namespace/binding, initial grant, DB/journal availability |
| 503 with `Retry-After: 2` during a burst of large requests | In-flight body budget (`server.max_inflight_request_bytes`); `inferplane_inflight_refusals_total` |
```

In `docs/operations/observability.ko.md`, replace:

```markdown
로그 `router: model fallback`에 요청 ID가 남음 |
```

with:

```markdown
로그 `router: model fallback`에 요청 ID가 남음 |
| `inferplane_inflight_refusals_total`, `inferplane_inflight_request_bytes` | 동시 처리 본문 예산(`server.max_inflight_request_bytes`)이 작업량보다 작거나 큰 요청이 몰림 |
```

In `docs/operations/observability.ko.md`, replace:

```markdown
| 첫 요청 전 503 | 필수 동기화, 공유 네임스페이스·바인딩, 최초 권한, DB·저널 |
```

with:

```markdown
| 첫 요청 전 503 | 필수 동기화, 공유 네임스페이스·바인딩, 최초 권한, DB·저널 |
| 큰 요청이 몰릴 때 `Retry-After: 2`와 503 | 동시 처리 본문 예산(`server.max_inflight_request_bytes`), `inferplane_inflight_refusals_total` |
```

Refresh the Korean companion hash after the English edit:

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
python3 - <<'EOF'
import hashlib, pathlib, re
for en in ["docs/operations/observability.md"]:
    p = pathlib.Path(en); ko = p.with_suffix(".ko.md")
    h = hashlib.sha256(p.read_bytes()).hexdigest()
    t, n = re.subn(r"(?m)^translation_source_sha256: \S+$", "translation_source_sha256: " + h, ko.read_text(encoding="utf-8"), count=1)
    assert n == 1, ko
    ko.write_text(t, encoding="utf-8")
EOF
```

In `internal/CLAUDE.md`, replace:

```markdown
it does not bound aggregate concurrent memory.
```

with:

```markdown
`server.max_inflight_request_bytes` (0 = off, else ≥ max_request_bytes) bounds the body bytes held across requests: generation that does not fit gets a protocol-shaped 503, counts a local HTTP-200 estimate (`server/inflight.go`).
```

In `charts/inferplane/values.yaml`, replace:

```yaml
# and lower config.server.max_request_bytes when large bodies are unnecessary.
```

with:

```yaml
# and lower config.server.max_request_bytes when large bodies are unnecessary.
# config.server.max_inflight_request_bytes (off by default) bounds the body
# bytes all requests hold at once; each body costs about 5.5x in memory.
```

- [ ] **Step 4: Run the task checks**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
gofmt -l internal cmd | (! grep .) \
  && go vet ./internal/... ./cmd/... \
  && go test -buildvcs=false -race -count=1 ./internal/config/ ./internal/metrics/ ./internal/server/ ./cmd/mayu/ \
  && python3 scripts/check_docs_translations.py > $L/logs/t9-pass.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL)' $L/logs/t9-pass.log
```

Expected: `exit=0` and no FAIL lines; the translation checker passes.

- [ ] **Step 5: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
git -C $W add internal/server/inflight_test.go internal/config/max_request_bytes_test.go internal/config/config.go internal/server/server.go internal/server/inflight.go internal/metrics/metrics.go cmd/mayu/gateway.go docs/reference/infrastructure.md docs/reference/infrastructure.ko.md docs/reference/api.md docs/reference/api.ko.md docs/operations/observability.md docs/operations/observability.ko.md internal/CLAUDE.md charts/inferplane/values.yaml
git -C $W commit -s -m "feat(server): optional in-flight request byte budget with protocol-shaped 503" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git -C $W status --short | grep -v '^??' ; echo clean-check-done
```

Expected: one new commit; `git status --short` lists no modified tracked files.


---

### Task 10: Content-aware token estimator package

**Items:** program item 9 (estimator)

**User-visible change:** None yet: the package is added and tested but not wired (Task 11 wires it). Also adds the `testdata/fixtures` command that writes the live-check request bodies.

**Files:**
- Test (create): `internal/tokenest/tokenesttest/fixtures.go`
- Test (create): `internal/tokenest/estimate_test.go`
- Create: `internal/tokenest/text.go`
- Create: `internal/tokenest/estimate.go`
- Create: `internal/tokenest/media.go`
- Create: `internal/tokenest/testdata/fixtures/main.go`
- Modify: `CLAUDE.md` (edits at lines 202)
- Modify: `internal/CLAUDE.md` (edits at lines 93)

**Interfaces:**
- Produces: `func tokenest.Estimate(protocol string, raw []byte) int64` (protocol `anthropic`, `openai`, `responses`, `bedrock` or `bedrock-converse`; result ≥ 1; invalid JSON is costed as plain text); package `tokenest/tokenesttest`: `type Class struct{ Name string; … }`, `var Classes []Class` (prose, code, read, digits, hex, hangul, korean), `func ClassNamed(name string) Class`, `func (c Class) Text(seed uint64, n int) string`, `func Dense(seed uint64, n int) string`, `func AnthropicBody(model, text string) []byte`. Unexported: `textCost`, `decodedCost`, `valueCost`, `scan`, `imagePayloads`, `imageTokens = 1600`.

- [ ] **Step 1: Write the failing tests**

Create `internal/tokenest/tokenesttest/fixtures.go`:

```go
// Package tokenesttest generates the deterministic synthetic request bodies
// that calibrate internal/tokenest. Each class mirrors one content class of
// the 2026-10-27 gateway path measurement (bytes per token there: prose 6.3,
// code-shaped lines 4.8, Claude Code Read output ≈4.6, digit-heavy 2.9–3.5,
// hex 1.7, Hangul 1.3), plus natural Korean prose. Nothing here is compiled
// into a binary.
package tokenesttest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"unicode/utf8"
)

// Class is one content class.
type Class struct {
	Name string
	gen  func(r *rand.Rand, b *strings.Builder)
}

// Classes lists the content classes; "hangul" is uniformly random syllables
// (the measured dense case), "korean" is natural Korean prose.
var Classes = []Class{
	{"prose", prose},
	{"code", code},
	{"read", read},
	{"digits", digits},
	{"hex", hexLines},
	{"hangul", hangul},
	{"korean", korean},
}

// ClassNamed returns the class with name; it panics on an unknown name.
func ClassNamed(name string) Class {
	for _, c := range Classes {
		if c.Name == name {
			return c
		}
	}
	panic("tokenesttest: unknown class " + name)
}

// Text returns exactly n bytes (at a rune boundary, so possibly a few fewer)
// of class text generated from seed.
func (c Class) Text(seed uint64, n int) string {
	r := rand.New(rand.NewPCG(seed, 0x746f6b656e657374))
	var b strings.Builder
	for b.Len() < n {
		c.gen(r, &b)
	}
	return truncate(b.String(), n)
}

// denseShares are byte shares chosen so the measured class rates combine to
// the measured 3.58 bytes per token of the dense requests (484 KB → 135,043
// and 398 KB → 111,165 real tokens); on o200k_base this mixture is within 1%
// of both.
var denseShares = []struct {
	class string
	share int // percent of bytes
}{{"prose", 25}, {"code", 30}, {"digits", 35}, {"hex", 5}, {"hangul", 5}}

// Dense interleaves 4 KiB chunks of the dense mixture, n bytes in total.
func Dense(seed uint64, n int) string {
	var b strings.Builder
	for chunk := uint64(0); b.Len() < n; chunk++ {
		for _, s := range denseShares {
			b.WriteString(ClassNamed(s.class).Text(seed+chunk*7919+uint64(s.share), 4096*s.share/100))
		}
	}
	return truncate(b.String(), n)
}

// AnthropicBody wraps text as one user text block of a Messages request,
// encoded like a JavaScript client (no HTML escaping).
func AnthropicBody(model, text string) []byte {
	return encode(map[string]any{
		"model": model, "max_tokens": 1024,
		"messages": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": text},
		}}},
	})
}

func encode(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

var words = strings.Fields(`the system request gateway model context window budget policy
provider routing response stream token client server session history message
content record account network service traffic control plane measure limit
between before through during against within without under around across
people report answer change number second little public country general
however always several already another because example certain question
process develop support provide include continue consider follow present`)

func prose(r *rand.Rand, b *strings.Builder) {
	for s := 0; s < 6; s++ {
		n := 10 + r.IntN(9)
		for i := 0; i < n; i++ {
			w := words[r.IntN(len(words))]
			if i == 0 {
				w = strings.ToUpper(w[:1]) + w[1:]
			} else {
				b.WriteByte(' ')
			}
			b.WriteString(w)
			if i > 1 && i < n-2 && r.IntN(9) == 0 {
				b.WriteByte(',')
			}
		}
		b.WriteString(". ")
	}
	b.WriteString("\n\n")
}

var idents = strings.Fields(`request response handler config router target model
provider budget window stream reader writer buffer record cache token limit
status result value index count state`)

func ident(r *rand.Rand) string {
	a, c := idents[r.IntN(len(idents))], idents[r.IntN(len(idents))]
	return a + strings.ToUpper(c[:1]) + c[1:]
}

func codeLine(r *rand.Rand) string {
	switch r.IntN(8) {
	case 0:
		return fmt.Sprintf("func (h *%s) %s(ctx context.Context, %s []byte) (int64, error) {", ident(r), ident(r), ident(r))
	case 1:
		return fmt.Sprintf("\tif err := %s.%s(%s); err != nil {", ident(r), ident(r), ident(r))
	case 2:
		return fmt.Sprintf("\t\treturn 0, fmt.Errorf(\"%s: %%w\", err)", ident(r))
	case 3:
		return "\t}"
	case 4:
		return fmt.Sprintf("\t%s := %s[%d]", ident(r), ident(r), r.IntN(64))
	case 5:
		return fmt.Sprintf("\t// %s keeps the %s %s for the %s.", ident(r), words[r.IntN(len(words))], words[r.IntN(len(words))], words[r.IntN(len(words))])
	case 6:
		return fmt.Sprintf("\tfor _, %s := range %s.%s {", ident(r), ident(r), ident(r))
	default:
		return fmt.Sprintf("\t%s.%s = append(%s.%s, %s)", ident(r), ident(r), ident(r), ident(r), ident(r))
	}
}

func code(r *rand.Rand, b *strings.Builder) {
	b.WriteString(codeLine(r))
	b.WriteByte('\n')
}

// read mirrors Claude Code's Read tool output: a right-aligned line number,
// an arrow and the file line.
func read(r *rand.Rand, b *strings.Builder) {
	fmt.Fprintf(b, "%6d→%s\n", 1+r.IntN(5000), codeLine(r))
}

func digits(r *rand.Rand, b *strings.Builder) {
	fmt.Fprintf(b, "id %012d total %09d status %s\n", r.Int64N(1e12), r.Int64N(1e9), words[r.IntN(len(words))])
}

func hexLines(r *rand.Rand, b *strings.Builder) {
	const digits = "0123456789abcdef"
	for i := 0; i < 64; i++ {
		b.WriteByte(digits[r.IntN(16)])
	}
	b.WriteByte('\n')
}

func hangul(r *rand.Rand, b *strings.Builder) {
	for w := 0; w < 16; w++ {
		if w > 0 {
			b.WriteByte(' ')
		}
		for s := 0; s < 2+r.IntN(4); s++ {
			b.WriteRune(rune(0xAC00 + r.IntN(11172)))
		}
	}
	b.WriteByte('\n')
}

const koreanParagraph = "게이트웨이는 요청 본문을 읽은 뒤 모델의 컨텍스트 창과 비교합니다. 추정치가 창보다 크면 업스트림을 부르지 않고 바로 거절하므로 비용이 들지 않습니다. 클라이언트는 이 오류를 보고 대화를 압축한 다음 다시 보냅니다. 압축은 손실이 있으므로 너무 일찍 거절하면 답변의 정확도가 떨어집니다. 반대로 너무 늦게 거절하면 업스트림이 거절하고, 그 오류 문구를 클라이언트가 이해하지 못하면 세션이 끝납니다. 그래서 추정은 실제 토큰 수에 가까워야 하고, 업스트림의 거절도 같은 형식으로 바꿔 전달해야 합니다. 운영자는 메트릭과 감사 기록으로 거절 이유를 확인할 수 있습니다."

// korean numbers repetitions of one natural paragraph; repetition does not
// change how a tokenizer splits it.
func korean(r *rand.Rand, b *strings.Builder) {
	fmt.Fprintf(b, "%d. %s\n\n", 1+r.IntN(1000), koreanParagraph)
}
```

Create `internal/tokenest/estimate_test.go`:

```go
package tokenest

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/tokenest/tokenesttest"
)

// realTokens are o200k_base counts (tiktoken 0.14.0) of tokenesttest's text,
// recorded when the estimator was calibrated. gpt-oss on Bedrock uses this
// tokenizer; its Converse rejection reported the measured dense case exactly
// ("Input length (135043)"). The generators are deterministic, so a changed
// count means a changed generator: re-measure instead of editing the number.
var realTokens = map[string]int64{
	"prose": 30155, "code": 47210, "read": 55373, "digits": 60176, "hex": 116292, "hangul": 137787, "korean": 46139,
}

func ratio(got, want int64) float64 { return float64(got) / float64(want) }

func TestEstimateTracksTokenizerPerClass(t *testing.T) {
	for _, c := range tokenesttest.Classes {
		t.Run(c.Name, func(t *testing.T) {
			got := Estimate("anthropic", tokenesttest.AnthropicBody("m", c.Text(1, 200_000)))
			low, high := 0.97, 1.03
			switch c.Name {
			case "hex":
				low, high = 0.94, 1.06
			case "korean":
				// Natural Korean is ≈0.77 tokens per syllable on o200k; the
				// 0.8 rate leaves room for less Korean-efficient tokenizers.
				low, high = 1.00, 1.20
			case "hangul":
				// Known limit: uniformly random syllables are mostly rare and
				// byte-encoded (≈2 tokens each), so they are under-estimated;
				// an upstream overflow is still recoverable (errors.go).
				low, high = 0.30, 0.42
			}
			if r := ratio(got, realTokens[c.Name]); r < low || r > high {
				t.Fatalf("estimate %d vs tokenizer %d: ratio %.3f outside [%.2f, %.2f]", got, realTokens[c.Name], r, low, high)
			}
		})
	}
}

// The 2026-10-27 path check measured these whole requests through the
// gateway. bytes/4 rejected the prose case at 76% of the real window and let
// the dense case through to a Bedrock rejection.
func TestEstimateReproducesMeasuredCases(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     []byte
		measured int64
	}{
		{"prose 624 KB", tokenesttest.AnthropicBody("m", tokenesttest.ClassNamed("prose").Text(2, 624_000)), 97_000},
		{"dense 484 KB", tokenesttest.AnthropicBody("m", tokenesttest.Dense(3, 484_000)), 135_043},
		{"dense 398 KB", tokenesttest.AnthropicBody("m", tokenesttest.Dense(3, 398_000)), 111_165},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Estimate("anthropic", tc.body)
			if r := ratio(got, tc.measured); r < 0.88 || r > 1.12 {
				t.Fatalf("estimate %d vs measured %d: ratio %.3f outside ±12%%", got, tc.measured, r)
			}
			if old := int64(len(tc.body) / 4); tc.name == "prose 624 KB" && ratio(old, tc.measured) < 1.5 {
				t.Fatalf("fixture no longer reproduces the bytes/4 over-estimate: %d", old)
			}
		})
	}
}

func TestEstimateTextRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want int64 // tokens of the text alone
	}{
		{"word absorbs one space", "hello world", 2},
		{"camelCase splits", "requestHandler", 2},
		{"long word splits every eight letters", "internationalization", 3},
		{"digits group by three", "1234567", 3},
		{"space before digits is its own token", "id 123", 3},
		{"single punctuation joins a word", ".Context", 1},
		{"punctuation run", "\");", 2},
		{"newlines", "a\n\n\nb", 3},
		{"escaped newline and tab decode", `a\n\tb`, 3},
		{"hangul syllables", "한국어", 3},
		{"cjk", "日本語", 3},
		{"accented letters stay in the word", "Größe", 1},
		{"symbol", "→", 1},
		{"unicode escape decodes", `été`, 1},
		{"surrogate pair decodes to one symbol", `🙂`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := (decodedCost([]byte(tc.text)) + 99) / 100; got != tc.want {
				t.Fatalf("decodedCost(%q) = %d tokens, want %d", tc.text, got, tc.want)
			}
		})
	}
}

func TestEstimateStructure(t *testing.T) {
	if got := Estimate("anthropic", nil); got != 1 {
		t.Fatalf("empty body = %d, want 1", got)
	}
	if got := Estimate("anthropic", []byte(`{"text":`)); got != (textCost([]byte(`{"text":`))+99)/100 {
		t.Fatalf("invalid JSON must be costed as plain text: %d", got)
	}
	a := Estimate("anthropic", []byte(`{"model":"m","messages":[{"role":"user","content":"hi there"}]}`))
	b := Estimate("anthropic", []byte(`{"messages":[{"content":"hi there","role":"user"}],"model":"m"}`))
	if a != b {
		t.Fatalf("member order changed the estimate: %d vs %d", a, b)
	}
	// keys: model, messages, role, content (2 each) + values m, user, "hi there" (1+1+2)
	if a != 12 {
		t.Fatalf("structure estimate = %d, want 12", a)
	}
}

func image(data string) string {
	return `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + data + `"}}`
}

func TestEstimateChargesInlineImagesAtTheirCeiling(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("image-payload", 4000)))
	text := `{"type":"text","text":"describe it"}`
	for _, tc := range []struct{ protocol, body string }{
		{"anthropic", `{"messages":[{"role":"user","content":[` + image(data) + `,` + text + `]}]}`},
		// Claude Code's Read tool returns a screenshot inside a tool result,
		// often with a cache breakpoint on the block.
		{"anthropic", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[` +
			strings.Replace(image(data), `{"type":"image",`, `{"type":"image","cache_control":{"type":"ephemeral"},`, 1) + `]},` + text + `]}]}`},
		{"bedrock", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call","content":[` + image(data) + `,` + text + `]}]}]}`},
		{"bedrock-converse", `{"messages":[{"role":"user","content":[{"image":{"format":"png","source":{"bytes":"` + data + `"}}},{"text":"describe it"}]}]}`},
		{"bedrock-converse", `{"messages":[{"role":"user","content":[{"toolResult":{"toolUseId":"call","content":[{"image":{"format":"jpeg","source":{"bytes":"` + data + `"}}}]}},{"text":"describe it"}]}]}`},
		{"openai", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + data + `","detail":"high"}},` + text + `]}]}`},
		{"responses", `{"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,` + data + `"},{"type":"input_text","text":"describe it"}]}]}`},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			got := Estimate(tc.protocol, []byte(tc.body))
			if got < imageTokens || got > imageTokens+60 {
				t.Fatalf("image request = %d tokens, want the %d-token image ceiling plus small text", got, imageTokens)
			}
			if old := int64(len(tc.body) / 4); old < 3*imageTokens {
				t.Fatalf("fixture too small to show the base64 discount: bytes/4 = %d", old)
			}
		})
	}
}

func TestEstimateKeepsOpaquePayloadRate(t *testing.T) {
	data := strings.Repeat("aGVsbG8gd29ybGQh", 1024) // valid base64, 16 KiB
	opaque := int64(len(data) / 4)
	for _, body := range []string{
		`{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + data + `"}}]}]}`,
		`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"n","input":` + image(data) + `}]}]}`,
		`{"tools":[{"name":"n","input_schema":` + image(data) + `}]}`,
		`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":"` + data + `"}]}]}`,
		`{"messages":[{"role":"system","content":[` + image(data) + `]}]}`,
		`{"messages":[{"role":"user","content":[` + strings.Replace(image(data), `"image/png"`, `"application/pdf"`, 1) + `]}]}`,
	} {
		got := Estimate("anthropic", []byte(body))
		if got < opaque || got > opaque+60 {
			t.Fatalf("opaque payload = %d tokens, want about %d (four bytes per token): %.80s", got, opaque, body)
		}
	}
	invalid := strings.Repeat("aGVsbG8=", 2048) // '=' inside: invalid base64, still the base64 alphabet
	if got := Estimate("anthropic", []byte(`{"messages":[{"role":"user","content":[`+image(invalid)+`]}]}`)); got < int64(len(invalid)/4) {
		t.Fatalf("invalid image payload was discounted: %d", got)
	}
}

func BenchmarkEstimateDense16MiB(b *testing.B) {
	body := tokenesttest.AnthropicBody("m", tokenesttest.Dense(9, 16<<20))
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		Estimate("anthropic", body)
	}
}
```

- [ ] **Step 2: Run them and confirm they fail**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
go test -buildvcs=false -count=1 ./internal/tokenest/ > $L/logs/t10-fail.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL|ok )|undefined|unknown field' $L/logs/t10-fail.log | sort -u
```

Expected: `exit=1`; `internal/tokenest [build failed]` (undefined: Estimate, decodedCost, textCost, imageTokens).

- [ ] **Step 3: Implement**

The algorithm, as implemented below: (1) `json.Valid`, else cost the raw bytes as text. (2) One pass over the JSON bytes: every key costs its decoded text plus one token, every string value its decoded text (escapes decoded in place, no allocation), numbers and literals their text, structural punctuation nothing. (3) Text is split into runs the way BPE pre-tokenizers split it: letters (camelCase splits; non-ASCII letters weigh 2) cost one token per 8, digits one per 3, ASCII punctuation one per 2, spaces/tabs one per 8 beyond the one absorbed by the next word, newlines one per 3, Hangul 0.8 per syllable, kana/CJK 0.7 per character, other symbols 1 each; a single space, tab or punctuation mark before a word is free, and a space before digits is its own token. (4) A string of at least 256 base64-alphabet bytes (or a base64 data URL) costs four bytes per token; when one was seen, inline images at recognized content positions (the same positions `requestpolicy.ContextTextEstimate` recognized) are re-costed at 1,600 tokens each. (5) Sum centitokens, round up once. Measured against o200k_base on the generators: prose 1.00, code 1.01, Read output 1.00, digits 1.00, hex 0.96, natural Korean 1.09, random Hangul 0.36; the 2026-10-27 cases: prose 624 KB 94,335 vs ~97k measured, dense 484 KB 123,053 vs 135,043, dense 398 KB 101,166 vs 111,165; about 125 MB/s with 3 allocations on a 16 MiB body.

Create `internal/tokenest/text.go`:

```go
package tokenest

import (
	"unicode"
	"unicode/utf8"
)

// Costs are centitokens (1/100 token) so that fractional per-character rates
// stay integer; Estimate rounds the request total up once. The run model and
// rates are calibrated against the o200k_base tokenizer and the 2026-10-27
// gateway measurements (see estimate_test.go).
const (
	letterRun  = 8   // letter weight per token in one word piece (non-ASCII letters weigh 2)
	digitRun   = 3   // digits per token: tokenizers group at most three
	punctRun   = 2   // ASCII punctuation per token
	spaceRun   = 8   // spaces or tabs per token
	breakRun   = 3   // newlines per token
	hangulCost = 80  // centitokens per Hangul syllable or jamo
	cjkCost    = 70  // centitokens per kana or CJK ideograph
	symbolCost = 100 // centitokens per other non-ASCII rune (arrows, emoji)
)

type class uint8

const (
	clsNone class = iota
	clsLetter
	clsDigit
	clsPunct
	clsSpace // spaces and tabs
	clsBreak // newlines
	clsHangul
	clsCJK
	clsSymbol
)

// asciiClass caches classOf for ASCII, the hot path.
var asciiClass = func() (t [utf8.RuneSelf]class) {
	for r := range t {
		t[r] = classOfSlow(rune(r))
	}
	return t
}()

func classOf(r rune) class {
	if r >= 0 && r < utf8.RuneSelf {
		return asciiClass[r]
	}
	return classOfSlow(r)
}

func classOfSlow(r rune) class {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		return clsLetter
	case r >= '0' && r <= '9':
		return clsDigit
	case r == ' ' || r == '\t' || r == '\v' || r == '\f':
		return clsSpace
	case r == '\n' || r == '\r':
		return clsBreak
	case r < utf8.RuneSelf:
		return clsPunct
	case r >= 0xAC00 && r <= 0xD7A3, r >= 0x1100 && r <= 0x11FF, r >= 0x3130 && r <= 0x318F:
		return clsHangul
	case r >= 0x3040 && r <= 0x30FF, r >= 0x3400 && r <= 0x4DBF, r >= 0x4E00 && r <= 0x9FFF,
		r >= 0xF900 && r <= 0xFAFF, r >= 0xFF00 && r <= 0xFFEF:
		return clsCJK
	case unicode.IsLetter(r):
		return clsLetter
	default:
		return clsSymbol
	}
}

// counter folds runes into pretoken-like runs the way BPE tokenizers pre-split
// text: a run ends where the character class changes, and a letter run also
// ends where a lowercase letter is followed by an uppercase one (camelCase).
// A run is charged when the next run starts, because tokenizers attach one
// preceding space, tab or single punctuation mark to a following word, while
// a space before digits stays its own token.
type counter struct {
	cls       class
	n         int // runes in the current run
	weight    int // letter weight of the current run
	lastLower bool
	cost      int64
}

func ceilDiv(n, d int) int64 { return int64((n + d - 1) / d) }

func word(next class) bool { return next == clsLetter || next == clsHangul || next == clsCJK }

// charge adds the current run's cost given the class of the run after it.
func (c *counter) charge(next class) {
	switch c.cls {
	case clsLetter:
		c.cost += 100 * ceilDiv(c.weight, letterRun)
	case clsDigit:
		c.cost += 100 * ceilDiv(c.n, digitRun)
	case clsPunct:
		if c.n > 1 || !word(next) {
			c.cost += 100 * ceilDiv(c.n, punctRun)
		}
	case clsSpace:
		switch next {
		case clsNone:
			c.cost += 100 * ceilDiv(c.n, spaceRun)
		case clsDigit:
			c.cost += 100 * (ceilDiv(c.n-1, spaceRun) + 1)
		default:
			c.cost += 100 * ceilDiv(c.n-1, spaceRun)
		}
	case clsBreak:
		c.cost += 100 * ceilDiv(c.n, breakRun)
	case clsHangul:
		c.cost += hangulCost * int64(c.n)
	case clsCJK:
		c.cost += cjkCost * int64(c.n)
	case clsSymbol:
		if c.n > 1 || !word(next) {
			c.cost += symbolCost * int64(c.n)
		}
	}
}

func (c *counter) add(r rune) {
	cls := classOf(r)
	upper := r >= 'A' && r <= 'Z'
	if cls != c.cls || (cls == clsLetter && upper && c.lastLower) {
		c.charge(cls)
		c.cls, c.n, c.weight = cls, 0, 0
	}
	c.n++
	if r < utf8.RuneSelf {
		c.weight++
	} else {
		c.weight += 2
	}
	c.lastLower = r >= 'a' && r <= 'z'
}

func (c *counter) total() int64 {
	c.charge(clsNone)
	c.cls, c.n, c.weight = clsNone, 0, 0
	return c.cost
}

// textCost returns the centitoken cost of decoded text.
func textCost(s []byte) int64 {
	var c counter
	for len(s) > 0 {
		if s[0] < utf8.RuneSelf {
			c.add(rune(s[0]))
			s = s[1:]
			continue
		}
		r, size := utf8.DecodeRune(s)
		c.add(r)
		s = s[size:]
	}
	return c.total()
}
```

Create `internal/tokenest/estimate.go`:

```go
// Package tokenest estimates how many input tokens a provider will count for a
// generation request body, offline and deterministically. It is not a
// tokenizer: it splits decoded JSON text into character-class runs the way BPE
// tokenizers pre-split text and charges each run a calibrated rate
// (estimate_test.go records the calibration against o200k_base and the
// 2026-10-27 gateway measurements). Inline images cost a fixed per-image
// ceiling; other base64 payloads keep the historical rate of four bytes per
// token. The package is a stdlib-only leaf.
package tokenest

import (
	"encoding/json"
	"unicode/utf8"
)

// imageTokens is the per-image ceiling: Claude resizes an image to at most
// about 1.15 megapixels (≈1,600 tokens) and OpenAI high detail stays below it.
const imageTokens = 1600

// minOpaque is the shortest string that may be an opaque base64 payload.
// Shorter strings (identifiers, hashes) are ordinary text.
const minOpaque = 256

// Estimate returns the estimated input tokens of raw, a complete request body
// for protocol "anthropic", "openai", "responses", "bedrock" or
// "bedrock-converse". JSON keys and every string, number and literal value
// count as text; structural punctuation is free. Invalid JSON is costed as
// plain text. The result is at least 1 and never depends on map order.
func Estimate(protocol string, raw []byte) int64 {
	cost, opaque, ok := scan(raw)
	if !ok {
		cost = textCost(raw)
	} else if opaque {
		for _, image := range imagePayloads(protocol, raw) {
			charged, _ := valueCost(image)
			cost += 100*imageTokens - charged
		}
	}
	return max(int64(1), (cost+99)/100)
}

// scan walks valid JSON once without building values. It reports whether any
// string value looked like an opaque base64 payload, so the structural image
// pass runs only when it can change the result.
func scan(raw []byte) (cost int64, opaque, ok bool) {
	if !json.Valid(raw) {
		return 0, false, false
	}
	var stack []byte // 'o' object, 'a' array
	key := false
	for i := 0; i < len(raw); {
		switch c := raw[i]; c {
		case '{':
			stack = append(stack, 'o')
			key = true
			i++
		case '[':
			stack = append(stack, 'a')
			key = false
			i++
		case '}', ']':
			stack = stack[:len(stack)-1]
			i++
		case ',':
			key = len(stack) > 0 && stack[len(stack)-1] == 'o'
			i++
		case ':':
			key = false
			i++
		case ' ', '\t', '\n', '\r':
			i++
		case '"':
			end := stringEnd(raw, i)
			if key {
				cost += 100 + decodedCost(raw[i+1:end])
			} else {
				charged, isOpaque := valueCost(raw[i : end+1])
				cost += charged
				opaque = opaque || isOpaque
			}
			i = end + 1
		default:
			end := i
			for end < len(raw) && !delimiter(raw[end]) {
				end++
			}
			cost += textCost(raw[i:end])
			i = end
		}
	}
	return cost, opaque, true
}

func delimiter(c byte) bool {
	switch c {
	case ',', '}', ']', ':', ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

// stringEnd returns the index of the closing quote of the string at raw[start].
func stringEnd(raw []byte, start int) int {
	for i := start + 1; i < len(raw); i++ {
		switch raw[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return len(raw) - 1
}

// valueCost charges one quoted string value. Opaque base64 (and base64 data
// URLs) keep four bytes per token: decoding and tokenizing them as text would
// overstate them, and only images have a known token ceiling.
func valueCost(quoted []byte) (int64, bool) {
	content := quoted[1 : len(quoted)-1]
	if opaqueBase64(content) {
		return 100 * ceilDiv(len(content), 4), true
	}
	return decodedCost(content), false
}

func opaqueBase64(content []byte) bool {
	if len(content) < minOpaque {
		return false
	}
	if len(content) > 5 && string(content[:5]) == "data:" {
		limit := min(len(content), 128)
		for i := 5; i+8 <= limit; i++ {
			if string(content[i:i+8]) == ";base64," {
				return base64Alphabet(content[i+8:])
			}
		}
		return false
	}
	return base64Alphabet(content)
}

func base64Alphabet(b []byte) bool {
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '+', c == '/', c == '=':
		case c == '\\' && i+1 < len(b) && b[i+1] == '/':
			i++
		default:
			return false
		}
	}
	return true
}

// decodedCost costs the decoded text of a JSON string's raw content without
// allocating: escapes are interpreted in place.
func decodedCost(content []byte) int64 {
	var c counter
	for i := 0; i < len(content); {
		if b := content[i]; b < utf8.RuneSelf && (b != '\\' || i+1 >= len(content)) {
			c.add(rune(b))
			i++
			continue
		} else if b != '\\' {
			r, size := utf8.DecodeRune(content[i:])
			c.add(r)
			i += size
			continue
		}
		switch e := content[i+1]; e {
		case 'n', 'r':
			c.add('\n')
			i += 2
		case 't', 'b', 'f':
			c.add('\t')
			i += 2
		case 'u':
			r, size := unicodeEscape(content[i:])
			c.add(r)
			i += size
		default: // \" \\ \/
			c.add(rune(e))
			i += 2
		}
	}
	return c.total()
}

// unicodeEscape decodes \uXXXX (and a following low surrogate) at b[0].
func unicodeEscape(b []byte) (rune, int) {
	r, ok := hex4(b)
	if !ok {
		return utf8.RuneError, 2
	}
	if r >= 0xD800 && r < 0xDC00 && len(b) >= 12 && b[6] == '\\' && b[7] == 'u' {
		if low, ok := hex4(b[6:]); ok && low >= 0xDC00 && low < 0xE000 {
			return 0x10000 + (r-0xD800)<<10 + (low - 0xDC00), 12
		}
	}
	return r, 6
}

func hex4(b []byte) (rune, bool) {
	if len(b) < 6 {
		return 0, false
	}
	var r rune
	for _, c := range b[2:6] {
		switch {
		case c >= '0' && c <= '9':
			r = r<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			r = r<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			r = r<<4 | rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	return r, true
}
```

Create `internal/tokenest/media.go`:

```go
package tokenest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
)

// imagePayloads returns the quoted base64 strings of inline images at
// recognized content positions: user/assistant message blocks and their tool
// results. Opaque fields (tool inputs, schemas, signatures, quoted JSON text)
// and invalid or non-image payloads are never returned, so they keep their
// scanned cost. Unknown protocols and unparsable shapes return nothing.
func imagePayloads(protocol string, raw []byte) []json.RawMessage {
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return nil
	}
	key := "messages"
	switch protocol {
	case "anthropic", "bedrock", "bedrock-converse", "openai":
	case "responses":
		key = "input"
	default:
		return nil
	}
	if protocol == "bedrock-converse" {
		protocol = "bedrock"
	}
	var messages []map[string]json.RawMessage
	if json.Unmarshal(root[key], &messages) != nil {
		return nil
	}
	var out []json.RawMessage
	for _, message := range messages {
		role := stringValue(message["role"])
		if role != "user" && role != "assistant" {
			continue
		}
		if protocol == "responses" {
			if kind := stringValue(message["type"]); kind != "" && kind != "message" {
				continue
			}
		}
		var blocks []map[string]json.RawMessage
		if json.Unmarshal(message["content"], &blocks) != nil {
			continue // string content is text, even when it contains JSON
		}
		for _, block := range blocks {
			out = appendImage(out, protocol, block)
			if protocol == "bedrock" && block["type"] == nil {
				var result struct {
					Content []map[string]json.RawMessage `json:"content"`
				}
				if json.Unmarshal(block["toolResult"], &result) == nil {
					for _, content := range result.Content {
						out = appendImage(out, protocol, content)
					}
				}
			}
			if (protocol == "anthropic" || protocol == "bedrock") && stringValue(block["type"]) == "tool_result" {
				var results []map[string]json.RawMessage
				if json.Unmarshal(block["content"], &results) == nil {
					for _, result := range results {
						out = appendImage(out, protocol, result)
					}
				}
			}
		}
	}
	return out
}

func stringValue(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func appendImage(out []json.RawMessage, protocol string, block map[string]json.RawMessage) []json.RawMessage {
	kind := stringValue(block["type"])
	switch {
	case protocol == "bedrock" && kind == "" && block["image"] != nil:
		var image struct {
			Format string `json:"format"`
			Source struct {
				Bytes json.RawMessage `json:"bytes"`
			} `json:"source"`
		}
		if json.Unmarshal(block["image"], &image) != nil || !imageType("image/"+image.Format) ||
			!validBase64(stringValue(image.Source.Bytes)) {
			return out
		}
		return append(out, bytes.TrimSpace(image.Source.Bytes))
	case (protocol == "anthropic" || protocol == "bedrock") && kind == "image":
		var source map[string]json.RawMessage
		if json.Unmarshal(block["source"], &source) != nil || stringValue(source["type"]) != "base64" ||
			!imageType(stringValue(source["media_type"])) {
			return out
		}
		data := bytes.TrimSpace(source["data"])
		if !validBase64(stringValue(data)) {
			return out
		}
		return append(out, data)
	case protocol == "openai" && kind == "image_url":
		var image map[string]json.RawMessage
		if json.Unmarshal(block["image_url"], &image) != nil {
			return out
		}
		return appendDataURL(out, image["url"])
	case protocol == "responses" && kind == "input_image":
		return appendDataURL(out, block["image_url"])
	}
	return out
}

func imageType(media string) bool {
	switch media {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

func validBase64(data string) bool {
	if data == "" {
		return false
	}
	// Validate without retaining decoded image bytes or interpreting pixels.
	_, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(data)))
	return err == nil
}

func appendDataURL(out []json.RawMessage, raw json.RawMessage) []json.RawMessage {
	url := stringValue(raw)
	header, data, ok := strings.Cut(url, ",")
	if !ok || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") ||
		!imageType(strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")) || !validBase64(data) {
		return out
	}
	return append(out, bytes.TrimSpace(raw))
}
```

Create `internal/tokenest/testdata/fixtures/main.go`:

```go
// Command fixtures writes the request bodies of the tokenest live check
// (plan 2026-10-29 gateway path remediation, final task): every calibration
// class sized past a model window, the 2026-10-27 measured cases, and the
// dense overflow case in each ingress's wire shape. It never sends anything.
//
//	go run -buildvcs=false ./internal/tokenest/testdata/fixtures -out DIR
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/inferplane/inferplane/internal/tokenest"
	"github.com/inferplane/inferplane/internal/tokenest/tokenesttest"
)

// bytesPerToken is each class's o200k_base rate (estimate_test.go realTokens
// over 200,000 bytes), used only to size bodies past a window.
var bytesPerToken = map[string]float64{
	"prose": 6.632, "code": 4.236, "read": 3.612, "digits": 3.324, "hex": 1.720, "hangul": 1.452, "korean": 4.335,
}

func main() {
	out := flag.String("out", "", "output directory")
	calib := flag.String("calib", "calib-gpt-oss", "model without a declared window (reaches Bedrock)")
	gated := flag.String("gated", "openai.gpt-oss-120b-1:0", "model with a declared 128k window")
	claude := flag.String("claude", "calib-haiku", "Claude model without a declared window")
	tokens := flag.Int("tokens", 260_000, "o200k tokens per calibration body (past a 131,072 window)")
	claudeTokens := flag.Int("claude-tokens", 400_000, "o200k tokens per Claude body (past a 200,000 window)")
	flag.Parse()
	if *out == "" {
		log.Fatal("-out is required")
	}
	estimates := new(bytes.Buffer)
	save := func(name string, body []byte, estimate int64) {
		if err := os.WriteFile(filepath.Join(*out, name+".json"), body, 0o600); err != nil {
			log.Fatal(err)
		}
		fmt.Fprintf(estimates, "%s\t%d\t%d\n", name, len(body), estimate)
	}
	write := func(name, protocol string, body []byte) { save(name, body, tokenest.Estimate(protocol, body)) }
	size := func(class string, target int) int { return int(float64(target) * bytesPerToken[class]) }
	for _, c := range tokenesttest.Classes {
		write("class-"+c.Name, "anthropic", tokenesttest.AnthropicBody(*calib, c.Text(1, size(c.Name, *tokens))))
	}
	for _, class := range []string{"prose", "korean"} {
		write("claude-"+class, "anthropic", tokenesttest.AnthropicBody(*claude, tokenesttest.ClassNamed(class).Text(1, size(class, *claudeTokens))))
	}
	prose := tokenesttest.ClassNamed("prose")
	write("measured-prose-624k", "anthropic", tokenesttest.AnthropicBody(*gated, prose.Text(2, 624_000)))
	write("measured-dense-484k", "anthropic", tokenesttest.AnthropicBody(*gated, tokenesttest.Dense(3, 484_000)))
	write("measured-dense-398k", "anthropic", tokenesttest.AnthropicBody(*gated, tokenesttest.Dense(3, 398_000)))
	write("bridge-prose-1000k", "responses", encode(map[string]any{
		"model": *gated, "store": false, "max_output_tokens": 16, "input": prose.Text(4, 1_000_000)}))

	dense := tokenesttest.Dense(3, 484_000)
	// The dense case passes the 128k gate by estimate and exceeds the real
	// 131,072-token window, so every ingress must relay Bedrock's refusal.
	write("overflow-messages", "anthropic", tokenesttest.AnthropicBody(*gated, dense))
	write("overflow-messages-stream", "anthropic", encode(map[string]any{"model": *gated, "max_tokens": 16, "stream": true,
		"messages": []any{map[string]any{"role": "user", "content": dense}}}))
	write("overflow-chat", "openai", encode(map[string]any{"model": *gated, "max_tokens": 16,
		"messages": []any{map[string]any{"role": "user", "content": dense}}}))
	write("overflow-invoke", "bedrock", encode(map[string]any{"anthropic_version": "bedrock-2023-05-31", "max_tokens": 16,
		"messages": []any{map[string]any{"role": "user", "content": dense}}}))
	write("overflow-converse", "bedrock-converse", encode(map[string]any{"inferenceConfig": map[string]any{"maxTokens": 16},
		"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"text": dense}}}}}))
	write("overflow-responses", "responses", encode(map[string]any{"model": *gated, "store": false, "max_output_tokens": 16, "input": dense}))
	for _, name := range []string{"class-prose", "class-korean"} {
		body, err := os.ReadFile(filepath.Join(*out, name+".json"))
		if err != nil {
			log.Fatal(err)
		}
		// Bedrock CountTokens wrapper of the same body; the count endpoint
		// estimates the decoded inner body.
		save("count-"+name, encode(map[string]any{"input": map[string]any{"invokeModel": map[string]any{
			"body": base64.StdEncoding.EncodeToString(body)}}}), tokenest.Estimate("bedrock", body))
	}
	if err := os.WriteFile(filepath.Join(*out, "estimates.tsv"), estimates.Bytes(), 0o600); err != nil {
		log.Fatal(err)
	}
}

func encode(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		log.Fatal(err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}
```

In `CLAUDE.md`, replace:

```markdown
  tier/            - ADR-041 budget-tier substitution: per-team Table, window-latched activation, shared by controlplane/ and proxy/
```

with:

```markdown
  tier/            - ADR-041 budget-tier substitution: per-team Table, window-latched activation, shared by controlplane/ and proxy/
  tokenest/        - Content-aware input-token estimate (stdlib leaf) for context gates, count fallbacks and TPM admission
```

In `internal/CLAUDE.md`, replace:

```markdown
Concrete types live in `plugins/<type>`.
```

with:

```markdown
Concrete types live in `plugins/<type>`.
- `tokenest/` — stdlib-only, deterministic input-token estimate of a request body: character-class runs of decoded JSON text (keys and values), inline images at a 1,600-token ceiling, other base64 at four bytes per token. Calibrated against o200k_base and the 2026-10-27 measurements (`tokenesttest/` generators, `estimate_test.go` bands); not a tokenizer. `testdata/fixtures` writes the live-check bodies.
```

- [ ] **Step 4: Run the task checks**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
gofmt -l internal/tokenest | (! grep .) \
  && go vet ./internal/tokenest/... \
  && go test -buildvcs=false -race -count=1 ./internal/tokenest/... \
  && mkdir -p /home/atomoh/.tmp/inferplane-path-remediation-20261029/logs/fx-smoke \
  && go run -buildvcs=false ./internal/tokenest/testdata/fixtures -out /home/atomoh/.tmp/inferplane-path-remediation-20261029/logs/fx-smoke \
  && test $(wc -l < /home/atomoh/.tmp/inferplane-path-remediation-20261029/logs/fx-smoke/estimates.tsv) -eq 21 \
  && rm -f /home/atomoh/.tmp/inferplane-path-remediation-20261029/logs/fx-smoke/*.json > $L/logs/t10-pass.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL)' $L/logs/t10-pass.log
```

Expected: `exit=0`; no FAIL lines; the fixtures command writes 21 bodies (the final `test` succeeds).

- [ ] **Step 5: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
git -C $W add internal/tokenest/tokenesttest/fixtures.go internal/tokenest/estimate_test.go internal/tokenest/text.go internal/tokenest/estimate.go internal/tokenest/media.go internal/tokenest/testdata/fixtures/main.go CLAUDE.md internal/CLAUDE.md
git -C $W commit -s -m "feat(tokenest): content-aware request token estimator calibrated on o200k" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git -C $W status --short | grep -v '^??' ; echo clean-check-done
```

Expected: one new commit; `git status --short` lists no modified tracked files.


---

### Task 11: Use the estimator for context gates, counts, TPM admission and Responses routing

**Items:** program item 9 (wiring)

**User-visible change:** Every former bytes/4 site now uses `tokenest.Estimate`: the context gate on Messages, Chat, Bedrock Invoke/Converse and Responses, the Messages per-retry-target gate, both count_tokens local estimates, the TPM PreCheck reservation, and the router's capacity for the explicitly requested Responses model. The image-excluding second pass (`requestpolicy.ContextTextEstimate`) is removed. Users see: English prose is no longer refused at about 76% of a window (624 KB prose: 156k → about 94k); digits, hex and Korean are estimated higher; each inline image counts 1,600 tokens in the gate (was 0) and in TPM (was its base64 bytes/4); the 400 text reads `(gateway estimate; model … has a … context window)`; count_tokens returns the new estimate; TPM-limited keys are admitted by it.

**Files:**
- Test (modify): `internal/router/responses_context_test.go` (edits at lines 80, 33–34, 13–14)
- Test (modify): `internal/server/responses_context_test.go` (edits at lines 53, 12–14)
- Test (modify): `internal/server/context_overflow_test.go` (edits at lines 53–55, 143–144, 20–21)
- Test (create): `internal/server/estimate_consistency_test.go`
- Modify: `internal/server/anthropicapi/messages.go` (edits at lines 295–317, 337, 360–368)
- Modify: `internal/server/anthropicapi/count_tokens.go` (edits at lines 158–168, 14–15)
- Modify: `internal/server/openaiapi/chat.go` (edits at lines 280–290, 309, 702–708, 34–35)
- Modify: `internal/server/bedrockapi/invoke.go` (edits at lines 317–327, 346, 758–764, 29–30)
- Modify: `internal/server/responsesapi/handler.go` (edits at lines 185–186, 26–27)
- Modify: `internal/router/request_routing.go` (edits at lines 714, 17–19)
- Delete: `internal/server/requestpolicy/context.go`
- Test (modify): `internal/server/requestpolicy/context_test.go`
- Modify: `docs/reference/api.md` (edits at lines 46)
- Modify: `docs/reference/api.ko.md` (edits at lines 51–63)
- Modify: `docs/codex-launcher.md` (edits at lines 90–92)
- Modify: `docs/codex-launcher.ko.md` (edits at lines 40)
- Modify: `docs/reference/data.md` (edits at lines 81)
- Modify: `docs/reference/data.ko.md` (edits at lines 62)
- Modify: `internal/CLAUDE.md` (edits at lines 114)

**Interfaces:**
- Consumes `tokenest.Estimate`. Package-local `estimateTokens(raw []byte) int64` in `anthropicapi`, `openaiapi` and `bedrockapi` now delegates to it. Removes `requestpolicy.ContextTextEstimate` and its test (no callers remain).

- [ ] **Step 1: Write the failing tests**

In `internal/router/responses_context_test.go`, replace:

```go
func requestedTokens(raw []byte) int64 { return max(int64(1), int64(len(raw)/4)) }
```

with:

```go
func requestedTokens(raw []byte) int64 { return tokenest.Estimate("responses", raw) }
```

In `internal/router/responses_context_test.go`, replace:

```go
	// Entirely synthetic, exactly 8192 wire bytes: the existing ingress
	// estimate is 2048 input tokens, plus the explicit 64-token output bound.
```

with:

```go
	// Entirely synthetic, exactly 8192 wire bytes: the ingress estimate stays
	// far below the 4096-token window while the inspector's conservative byte
	// ceiling exceeds it; the explicit output bound is 64 tokens.
```

In `internal/router/responses_context_test.go`, replace:

```go
	"github.com/inferplane/inferplane/internal/sensitivity"
)
```

with:

```go
	"github.com/inferplane/inferplane/internal/sensitivity"
	"github.com/inferplane/inferplane/internal/tokenest"
)
```

In `internal/server/responses_context_test.go`, replace:

```go
					int64(len(body)/4)+inspection.OutputTokens >= limit {
```

with:

```go
					tokenest.Estimate("responses", body)+inspection.OutputTokens >= limit {
```

In `internal/server/responses_context_test.go`, replace:

```go
	"github.com/inferplane/inferplane/internal/server/routingtest"
	"github.com/inferplane/inferplane/pkg/schema"
)
```

with:

```go
	"github.com/inferplane/inferplane/internal/server/routingtest"
	"github.com/inferplane/inferplane/internal/tokenest"
	"github.com/inferplane/inferplane/pkg/schema"
)
```

In `internal/server/context_overflow_test.go`, replace:

```go
				holder := newHolder(map[string]providers.Provider{"p": probe}, map[string]config.ModelConfig{
					"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}, ContextWindow: 1000, Capabilities: []string{"vision"}},
				})
```

with:

```go
				// The image counts at its 1,600-token ceiling: within 2,000
				// together with short text, over it with 1,000 more words.
				holder := newHolder(map[string]providers.Provider{"p": probe}, map[string]config.ModelConfig{
					"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}, ContextWindow: 2000, Capabilities: []string{"vision"}},
				})
```

In `internal/server/context_overflow_test.go`, replace:

```go
				counts := clientCounts.FindStringSubmatch(message)
				if len(counts) != 3 || counts[1] != strconv.Itoa(len(body)/4) || counts[2] != strconv.Itoa(window) {
```

with:

```go
				protocol := map[bool]string{true: "bedrock", false: "anthropic"}[strings.HasPrefix(path, "/model/")]
				if path == "/v1/chat/completions" {
					protocol = "openai"
				}
				counts := clientCounts.FindStringSubmatch(message)
				if len(counts) != 3 || counts[1] != strconv.FormatInt(tokenest.Estimate(protocol, []byte(body)), 10) || counts[2] != strconv.Itoa(window) {
```

In `internal/server/context_overflow_test.go`, replace:

```go
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/providers"
```

with:

```go
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/internal/tokenest"
	"github.com/inferplane/inferplane/providers"
```

Create `internal/server/estimate_consistency_test.go`:

```go
package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/internal/tokenest"
	"github.com/inferplane/inferplane/internal/tokenest/tokenesttest"
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
)

func estimateMux(window int64, gov *governance.Governor) http.Handler {
	holder := newHolder(map[string]providers.Provider{"p": mockprovider.New("m")},
		map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}, ContextWindow: window}})
	store := stubStore{key: "dev-key", p: keystore.Principal{KeyID: "ik_abc", Team: "t", AllowedModels: []string{"*"}}}
	return DataMux(router.New(holder), holder, store, nil, gov, nil, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0)
}

func estimateDo(mux http.Handler, path string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	req.Header.Set("x-api-key", "dev-key")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// Claude Code reads both the count endpoint and the overflow refusal; they
// must report the same estimate for the same body on both count APIs.
func TestCountAndContextGateAgree(t *testing.T) {
	mux := estimateMux(1000, nil)
	body := tokenesttest.AnthropicBody("m", tokenesttest.ClassNamed("prose").Text(5, 8000))
	overage := regexp.MustCompile(`prompt is too long: (\d+) tokens > 1000 maximum`)
	for _, tc := range []struct{ protocol, generate, count, field string }{
		{"anthropic", "/v1/messages", "/v1/messages/count_tokens", "input_tokens"},
		{"bedrock", "/model/m/invoke", "/model/m/count-tokens", "inputTokens"},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			want := tokenest.Estimate(tc.protocol, body)
			rec := estimateDo(mux, tc.generate, body)
			var refusal struct {
				Message string                   `json:"message"`
				Error   struct{ Message string } `json:"error"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &refusal)
			match := overage.FindStringSubmatch(refusal.Message + refusal.Error.Message)
			if rec.Code != http.StatusBadRequest || match == nil || match[1] != strconv.FormatInt(want, 10) {
				t.Fatalf("gate = %d %s, want estimate %d", rec.Code, rec.Body.String(), want)
			}
			countBody := body
			if tc.protocol == "bedrock" {
				countBody, _ = json.Marshal(map[string]any{"input": map[string]any{"invokeModel": map[string]any{"body": base64.StdEncoding.EncodeToString(body)}}})
			}
			var out map[string]int64
			rec = estimateDo(mux, tc.count, countBody)
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out[tc.field] != want {
				t.Fatalf("count = %d %s, want %d", rec.Code, rec.Body.String(), want)
			}
		})
	}
}

// The TPM reservation follows the content estimate: prose that bytes/4
// over-counted is now admitted, hex that it under-counted is now refused.
func TestTPMPreCheckUsesContentEstimate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   []byte
		status int
	}{
		{"prose 40 KB (bytes/4 10k, estimate ≈6k)", tokenesttest.AnthropicBody("m", tokenesttest.ClassNamed("prose").Text(6, 40_000)), http.StatusOK},
		{"hex 16 KB (bytes/4 4k, estimate ≈9k)", tokenesttest.AnthropicBody("m", tokenesttest.ClassNamed("hex").Text(6, 16_000)), http.StatusTooManyRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gov := governance.NewGovernor(map[string]governance.TeamPolicy{"t": {TokensPerMinute: 8000}}, limiter.NewMemory(), budget.NewMemory(), nil)
			if rec := estimateDo(estimateMux(0, gov), "/v1/messages", tc.body); rec.Code != tc.status {
				t.Fatalf("status = %d %s, want %d (estimate %d)", rec.Code, rec.Body.String(), tc.status, tokenest.Estimate("anthropic", tc.body))
			}
		})
	}
}
```

- [ ] **Step 2: Run them and confirm they fail**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
go test -buildvcs=false -count=1 ./internal/router/ ./internal/server/ > $L/logs/t11-fail.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL|ok )|undefined|unknown field' $L/logs/t11-fail.log | sort -u
```

Expected: `exit=1`; in `internal/server` `--- FAIL` for `TestContextOverflowSupportsClientCompaction`, `TestInlineImageContextPreflightPreservesAdmissionAndWire`, `TestCountAndContextGateAgree`, `TestTPMPreCheckUsesContentEstimate`; in `internal/router` `--- FAIL: TestResponsesRequestedContextRetainsAdmissionGuards` (the window offsets now follow the new estimate).

- [ ] **Step 3: Implement**

In `internal/server/anthropicapi/messages.go`, replace:

```go
	table := st.Pricing()
	// Context-window fast-fail: when the operator declared the model's window
	// (config models.<name>.context_window) and the COARSE non-image byte
	// estimate already exceeds it, refuse with a message naming both numbers —
	// the upstream's own rejection reaches the client as an opaque scrubbed
	// ValidationException (providers/bedrock/errors.go never echoes upstream
	// text). Estimate-only, no max_tokens added: the estimate is an upper-ish
	// bound and adding output budget would false-reject borderline VALID
	// requests; anything borderline falls through to the upstream's exact
	// check. Large inline-image requests need a second pass: base64 transport
	// bytes are not text tokens, and image/combined-context validation belongs
	// to the upstream. This does not change the governance/settlement estimate.
	// Runs before PreCheck so a refused request never charges the TPM estimate.
	// Keep the Anthropic "prompt is too long: N tokens > M" spelling: Claude
	// Code uses it to recognize overflow and size compaction. A generic 400
	// instead triggers model fallback with the same oversized history.
	if win := requestpolicy.ContextWindow(st, model); win > 0 {
		est := estimateTokens(raw)
		if est > win {
			est = requestpolicy.ContextTextEstimate("anthropic", raw)
		}
		if est > win {
			msg := fmt.Sprintf("prompt is too long: %d tokens > %d maximum (gateway text estimate excluding inline image payloads; model %s has a %d-token context window). Compact the conversation or reduce the input.", est, win, model, win)
```

with:

```go
	table := st.Pricing()
	// One content-aware estimate (internal/tokenest) of the final body serves
	// the context gate, every retry target's gate and the TPM PreCheck.
	est := estimateTokens(raw)
	// Context-window fast-fail: when the operator declared the model's window
	// (config models.<name>.context_window) and the estimate already exceeds
	// it, refuse with a message naming both numbers. Estimate-only, no
	// max_tokens added: adding output budget would false-reject borderline
	// VALID requests; anything borderline falls through to the upstream's
	// exact check, whose recognized overflow is mapped to the same recoverable
	// error (providers/bedrock/errors.go). Inline images count at their
	// per-image ceiling, not as base64 text. Runs before PreCheck so a refused
	// request never charges the TPM estimate. Keep the Anthropic "prompt is too
	// long: N tokens > M" spelling: Claude Code uses it to recognize overflow
	// and size compaction. A generic 400 instead triggers model fallback with
	// the same oversized history.
	if win := requestpolicy.ContextWindow(st, model); win > 0 {
		if est > win {
			msg := fmt.Sprintf("prompt is too long: %d tokens > %d maximum (gateway estimate; model %s has a %d-token context window). Compact the conversation or reduce the input.", est, win, model, win)
```

In `internal/server/anthropicapi/messages.go`, replace:

```go
		dec := h.gov.PreCheck(subjectOf(p), keyPolicyOf(p), estimateTokens(raw))
```

with:

```go
		dec := h.gov.PreCheck(subjectOf(p), keyPolicyOf(p), est)
```

In `internal/server/anthropicapi/messages.go`, replace:

```go
		if win := requestpolicy.ContextWindow(st, ct.Model); i > 0 && win > 0 && estimateTokens(raw) > win {
			if est := requestpolicy.ContextTextEstimate("anthropic", raw); est > win {
				msg := fmt.Sprintf("prompt is too long: %d tokens > %d maximum (gateway text estimate excluding inline image payloads; model %s has a %d-token context window). Compact the conversation or reduce the input.", est, win, ct.Model, win)
				h.audit(req.Context(), p, ct.Model, ct.Upstream, &audit.OutcomeRef{Status: http.StatusBadRequest}, false, traceID)
				h.metrics.ObserveRequest(ingressName, ct.Model, ct.ProviderName, p.Team, http.StatusBadRequest, time.Since(start).Seconds(), 0)
				writeErr(w, http.StatusBadRequest, "invalid_request_error", msg)
				return
			}
		}
```

with:

```go
		if win := requestpolicy.ContextWindow(st, ct.Model); i > 0 && win > 0 && est > win {
			msg := fmt.Sprintf("prompt is too long: %d tokens > %d maximum (gateway estimate; model %s has a %d-token context window). Compact the conversation or reduce the input.", est, win, ct.Model, win)
			h.audit(req.Context(), p, ct.Model, ct.Upstream, &audit.OutcomeRef{Status: http.StatusBadRequest}, false, traceID)
			h.metrics.ObserveRequest(ingressName, ct.Model, ct.ProviderName, p.Team, http.StatusBadRequest, time.Since(start).Seconds(), 0)
			writeErr(w, http.StatusBadRequest, "invalid_request_error", msg)
			return
		}
```

In `internal/server/anthropicapi/count_tokens.go`, replace:

```go
// estimateTokens is the conservative fallback for providers without a
// TokenCounter (M2: none; M4/M5 may bundle a tokenizer per §10 #1). ~4 bytes
// per token is a coarse upper-ish bound; valid output matters more than
// precision here.
func estimateTokens(raw []byte) int64 {
	n := int64(len(raw) / 4)
	if n < 1 {
		n = 1
	}
	return n
}
```

with:

```go
// estimateTokens is the local estimate for providers without a TokenCounter
// and for every refused or unreadable count, and the same value the Messages
// context gate and TPM PreCheck use (internal/tokenest).
func estimateTokens(raw []byte) int64 {
	return tokenest.Estimate("anthropic", raw)
}
```

In `internal/server/anthropicapi/count_tokens.go`, replace:

```go
	"github.com/inferplane/inferplane/internal/server/requestpolicy"
	"github.com/inferplane/inferplane/pkg/schema"
```

with:

```go
	"github.com/inferplane/inferplane/internal/server/requestpolicy"
	"github.com/inferplane/inferplane/internal/tokenest"
	"github.com/inferplane/inferplane/pkg/schema"
```

In `internal/server/openaiapi/chat.go`, replace:

```go
	table := st.Pricing()
	// Context-window fast-fail (same rule and rationale as anthropicapi's —
	// estimate-only, before PreCheck, clear message; deliberate per-package
	// duplication like subjectOf).
	if win := requestpolicy.ContextWindow(st, model); win > 0 {
		est := estimateTokens(raw)
		if est > win {
			est = requestpolicy.ContextTextEstimate("openai", raw)
		}
		if est > win {
			msg := fmt.Sprintf("prompt is too long: %d tokens > %d maximum (gateway text estimate excluding inline image payloads; model %s has a %d-token context window). Compact the conversation or reduce the input.", est, win, model, win)
```

with:

```go
	table := st.Pricing()
	// Context-window fast-fail (same rule and rationale as anthropicapi's —
	// estimate-only, before PreCheck, clear message; deliberate per-package
	// duplication like subjectOf). The same estimate feeds PreCheck.
	est := estimateTokens(raw)
	if win := requestpolicy.ContextWindow(st, model); win > 0 {
		if est > win {
			msg := fmt.Sprintf("prompt is too long: %d tokens > %d maximum (gateway estimate; model %s has a %d-token context window). Compact the conversation or reduce the input.", est, win, model, win)
```

In `internal/server/openaiapi/chat.go`, replace:

```go
		dec := h.gov.PreCheck(subjectOf(p), keyPolicyOf(p), estimateTokens(raw))
```

with:

```go
		dec := h.gov.PreCheck(subjectOf(p), keyPolicyOf(p), est)
```

In `internal/server/openaiapi/chat.go`, replace:

```go
func estimateTokens(raw []byte) int64 {
	n := int64(len(raw) / 4)
	if n < 1 {
		n = 1
	}
	return n
}
```

with:

```go
// estimateTokens is the Chat Completions context gate and PreCheck estimate
// (internal/tokenest).
func estimateTokens(raw []byte) int64 {
	return tokenest.Estimate("openai", raw)
}
```

In `internal/server/openaiapi/chat.go`, replace:

```go
	"github.com/inferplane/inferplane/internal/telemetry"
	"github.com/inferplane/inferplane/internal/tracing"
```

with:

```go
	"github.com/inferplane/inferplane/internal/telemetry"
	"github.com/inferplane/inferplane/internal/tokenest"
	"github.com/inferplane/inferplane/internal/tracing"
```

In `internal/server/bedrockapi/invoke.go`, replace:

```go
	table := st.Pricing()
	// Context-window fast-fail (same rule and rationale as anthropicapi's —
	// estimate-only, before PreCheck, clear message; deliberate per-package
	// duplication like subjectOf).
	if win := requestpolicy.ContextWindow(st, model); win > 0 {
		est := estimateTokens(raw)
		if est > win {
			est = requestpolicy.ContextTextEstimate("bedrock", raw)
		}
		if est > win {
			msg := fmt.Sprintf("prompt is too long: %d tokens > %d maximum (gateway text estimate excluding inline image payloads; model %s has a %d-token context window). Compact the conversation or reduce the input.", est, win, model, win)
```

with:

```go
	table := st.Pricing()
	// Context-window fast-fail (same rule and rationale as anthropicapi's —
	// estimate-only, before PreCheck, clear message; deliberate per-package
	// duplication like subjectOf). The same estimate feeds PreCheck.
	est := tokenest.Estimate(h.protocol(), raw)
	if win := requestpolicy.ContextWindow(st, model); win > 0 {
		if est > win {
			msg := fmt.Sprintf("prompt is too long: %d tokens > %d maximum (gateway estimate; model %s has a %d-token context window). Compact the conversation or reduce the input.", est, win, model, win)
```

In `internal/server/bedrockapi/invoke.go`, replace:

```go
		dec := h.gov.PreCheck(subjectOf(p), keyPolicyOf(p), estimateTokens(raw))
```

with:

```go
		dec := h.gov.PreCheck(subjectOf(p), keyPolicyOf(p), est)
```

In `internal/server/bedrockapi/invoke.go`, replace:

```go
func estimateTokens(raw []byte) int64 {
	n := int64(len(raw) / 4)
	if n < 1 {
		n = 1
	}
	return n
}
```

with:

```go
// estimateTokens is the count endpoint's local estimate of an InvokeModel
// body, the same estimator the generation gate and PreCheck use.
func estimateTokens(raw []byte) int64 {
	return tokenest.Estimate("bedrock", raw)
}
```

In `internal/server/bedrockapi/invoke.go`, replace:

```go
	"github.com/inferplane/inferplane/internal/telemetry"
	"github.com/inferplane/inferplane/internal/tracing"
```

with:

```go
	"github.com/inferplane/inferplane/internal/telemetry"
	"github.com/inferplane/inferplane/internal/tokenest"
	"github.com/inferplane/inferplane/internal/tracing"
```

In `internal/server/responsesapi/handler.go`, replace:

```go
	estimate := max(int64(1), int64(len(raw)/4))
	if limit := requestpolicy.ContextWindow(st, model); limit > 0 && estimate > limit && requestpolicy.ContextTextEstimate("responses", raw) > limit {
```

with:

```go
	// The router applied this same estimate to the explicitly requested model.
	estimate := tokenest.Estimate("responses", raw)
	if limit := requestpolicy.ContextWindow(st, model); limit > 0 && estimate > limit {
```

In `internal/server/responsesapi/handler.go`, replace:

```go
	"github.com/inferplane/inferplane/internal/telemetry"
	"github.com/inferplane/inferplane/internal/tracing"
```

with:

```go
	"github.com/inferplane/inferplane/internal/telemetry"
	"github.com/inferplane/inferplane/internal/tokenest"
	"github.com/inferplane/inferplane/internal/tracing"
```

In `internal/router/request_routing.go`, replace:

```go
				capacity.InputTokens = max(int64(1), int64(len(b.in.RawBody)/4))
```

with:

```go
				capacity.InputTokens = tokenest.Estimate(b.in.Protocol, b.in.RawBody)
```

In `internal/router/request_routing.go`, replace:

```go
	"github.com/inferplane/inferplane/internal/sensitivity"
	"github.com/inferplane/inferplane/providers"
)
```

with:

```go
	"github.com/inferplane/inferplane/internal/sensitivity"
	"github.com/inferplane/inferplane/internal/tokenest"
	"github.com/inferplane/inferplane/providers"
)
```

Delete `internal/server/requestpolicy/context.go` (staged in the commit step with `git rm`).

Delete `internal/server/requestpolicy/context_test.go` (staged in the commit step with `git rm`).

In `docs/reference/api.md`, replace:

```markdown
- Context-window preflight failures on Messages, Chat Completions and Bedrock Invoke return HTTP 400 with `prompt is too long: N tokens > M maximum` inside the ingress's own error envelope. Claude Code recognizes that wording for compaction rather than generic model fallback and can extract the overage. `N` is a coarse text estimate, not an upstream tokenizer result. When raw bytes/4 exceed the declaration, generation preflight (including Responses) excludes valid inline-image base64 at recognized content positions before checking again. Image tokens and the combined context are validated by the upstream; exclusion does not assert that the image fits. Text, tool schemas/arguments, unknown media and opaque state remain counted. This does not change count endpoints, TPM/quota estimates, monetary reservations, privacy inspection, or conservative automatic/strict-target capacity checks. A client's displayed window and the gateway declaration remain independent. Compaction may still fail if the remaining input does not fit; do not assume a larger model window.
```

with:

```markdown
- Context-window preflight failures on Messages, Chat Completions and Bedrock Invoke/Converse return HTTP 400 with `prompt is too long: N tokens > M maximum` inside the ingress's own error envelope. Claude Code recognizes that wording for compaction rather than generic model fallback and can extract the overage. `N` is the gateway's content-aware estimate (`internal/tokenest`), not an upstream tokenizer result: decoded JSON text is split into character-class runs with calibrated rates (within 3% of o200k_base for English prose, code and digit-heavy text, about 9% over for natural Korean, under for random-looking syllables and hex), each inline image at a recognized content position counts 1,600 tokens, and other base64 payloads (documents, signatures, opaque state) count four bytes per token. bytes/4 over-estimated English prose by about 60%, refusing at 76% of the real window, and under-estimated digits, hex and Korean. The same estimate is the local `count_tokens` answer, the TPM PreCheck reservation and the explicitly requested Responses model's routing capacity; monetary reservations, privacy inspection and conservative automatic/strict-target capacity checks are unchanged. Borderline requests reach the upstream, whose recognized overflow is mapped to the same recoverable error. A client's displayed window and the gateway declaration remain independent. Compaction may still fail if the remaining input does not fit; do not assume a larger model window.
```

In `docs/reference/api.ko.md`, replace:

```markdown
Messages·Chat Completions·Bedrock Invoke의 컨텍스트 사전 검사 실패는 각 수신
프로토콜의 오류 형식 안에 `prompt is too long: N tokens > M maximum` 문구를
넣어 HTTP 400으로 반환합니다. Claude Code는 이 문구를 일반 모델 폴백 대신
압축 복구 대상으로 인식하고 초과량을 추출합니다. `N`은 업스트림 토크나이저의
실측값이 아닌 거친 텍스트 추정치입니다. 원문 바이트/4가 선언 한도를 넘으면,
Responses를 포함한 생성 사전 검사에서 알려진 콘텐츠 위치의 유효한 인라인 이미지
base64를 제외하고 다시 검사합니다. 이미지 토큰과 전체 컨텍스트의 검증은
업스트림이 담당하며, 제외했다고 이미지가 한도 안에 들어간다는 뜻은 아닙니다.
텍스트·도구 스키마와 인자·알 수 없는 미디어·불투명 상태는 계속 계산합니다.
count 엔드포인트, TPM·쿼터 추정, 금전 예약, 개인정보 검사, 자동·엄격 타깃의
보수적인 용량 검사는 바꾸지 않습니다. 클라이언트 표시 윈도우와 게이트웨이 선언은
독립적입니다. 남은 입력이 한도에 들어가지 않으면 압축은 계속 실패할 수 있으며,
모델 한도를 더 크게 가정하면 안 됩니다.
```

with:

```markdown
Messages·Chat Completions·Bedrock Invoke/Converse의 컨텍스트 사전 검사 실패는 각 수신
프로토콜의 오류 형식 안에 `prompt is too long: N tokens > M maximum` 문구를
넣어 HTTP 400으로 반환합니다. Claude Code는 이 문구를 일반 모델 폴백 대신
압축 복구 대상으로 인식하고 초과량을 추출합니다. `N`은 업스트림 토크나이저의
실측값이 아닌 게이트웨이의 내용 기반 추정치(`internal/tokenest`)입니다. 디코딩한 JSON
텍스트를 문자 종류별 구간으로 나눠 보정된 비율을 적용하며(영문 산문·코드·숫자 위주
텍스트는 o200k_base 대비 3% 이내, 자연스러운 한국어는 약 9% 많게, 무작위에 가까운
음절과 hex는 적게 추정), 알려진 콘텐츠 위치의 인라인 이미지는 하나당 1,600토큰,
그 밖의 base64(문서·서명·불투명 상태)는 4바이트당 1토큰으로 셉니다. 바이트/4는 영문
산문을 약 60% 과대 추정해 실제 창의 76%에서 거절했고 숫자·hex·한국어는 과소
추정했습니다. 같은 추정치를 로컬 `count_tokens` 응답, TPM PreCheck 예약, 명시적으로
선택한 Responses 모델의 라우팅 용량에 사용합니다. 금전 예약, 개인정보 검사, 자동·엄격
타깃의 보수적인 용량 검사는 바꾸지 않습니다. 경계에 가까운 요청은 업스트림으로 가며,
인식된 업스트림 초과 오류는 같은 복구 가능 오류로 바뀝니다. 클라이언트 표시 윈도우와
게이트웨이 선언은 독립적입니다. 남은 입력이 한도에 들어가지 않으면 압축은 계속 실패할
수 있으며, 모델 한도를 더 크게 가정하면 안 됩니다.
```

Refresh the Korean companion hash after the English edit:

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
python3 - <<'EOF'
import hashlib, pathlib, re
for en in ["docs/reference/api.md"]:
    p = pathlib.Path(en); ko = p.with_suffix(".ko.md")
    h = hashlib.sha256(p.read_bytes()).hexdigest()
    t, n = re.subn(r"(?m)^translation_source_sha256: \S+$", "translation_source_sha256: " + h, ko.read_text(encoding="utf-8"), count=1)
    assert n == 1, ko
    ko.write_text(t, encoding="utf-8")
EOF
```

In `docs/codex-launcher.md`, replace:

```markdown
For an explicitly selected portable Responses model, gateway context admission
uses the existing ingress estimate, `max(1, request_bytes / 4)`, plus the requested
output budget. This is an estimate, not the backend's tokenizer.
```

with:

```markdown
For an explicitly selected portable Responses model, gateway context admission
uses the ingress estimate (`internal/tokenest`, the same value as the handler's
preflight) plus the requested output budget. This is an estimate, not the
backend's tokenizer.
```

In `docs/codex-launcher.ko.md`, replace:

```markdown
명시적으로 선택한 이식 가능한 Responses 모델의 게이트웨이 승인은 `max(1, request_bytes / 4)`와 출력 예산을 사용합니다.
```

with:

```markdown
명시적으로 선택한 이식 가능한 Responses 모델의 게이트웨이 승인은 수신 경로 추정치(`internal/tokenest`, 핸들러 사전 검사와 같은 값)와 출력 예산을 사용합니다.
```

Refresh the Korean companion hash after the English edit:

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
python3 - <<'EOF'
import hashlib, pathlib, re
for en in ["docs/codex-launcher.md"]:
    p = pathlib.Path(en); ko = p.with_suffix(".ko.md")
    h = hashlib.sha256(p.read_bytes()).hexdigest()
    t, n = re.subn(r"(?m)^translation_source_sha256: \S+$", "translation_source_sha256: " + h, ko.read_text(encoding="utf-8"), count=1)
    assert n == 1, ko
    ko.write_text(t, encoding="utf-8")
EOF
```

In `docs/reference/data.md`, replace:

```markdown
limits. See ADR-039.
```

with:

```markdown
limits. See ADR-039. The admission estimate is `internal/tokenest`'s
content-aware request estimate, the same value as the context gate and the local
count answer.
```

In `docs/reference/data.ko.md`, replace:

```markdown
ADR-039를 참고하세요.
```

with:

```markdown
ADR-039를 참고하세요. 승인 추정치는 컨텍스트 사전 검사·로컬 count 응답과 같은
`internal/tokenest`의 내용 기반 요청 추정치입니다.
```

Refresh the Korean companion hash after the English edit:

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
python3 - <<'EOF'
import hashlib, pathlib, re
for en in ["docs/reference/data.md"]:
    p = pathlib.Path(en); ko = p.with_suffix(".ko.md")
    h = hashlib.sha256(p.read_bytes()).hexdigest()
    t, n = re.subn(r"(?m)^translation_source_sha256: \S+$", "translation_source_sha256: " + h, ko.read_text(encoding="utf-8"), count=1)
    assert n == 1, ko
    ko.write_text(t, encoding="utf-8")
EOF
```

In `internal/CLAUDE.md`, replace:

```markdown
an estimated ingress fast-fail
```

with:

```markdown
an estimated (`internal/tokenest`) ingress fast-fail
```

- [ ] **Step 4: Run the task checks**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
gofmt -l . | (! grep .) \
  && go vet ./... \
  && go test -buildvcs=false -race -count=1 ./internal/... ./cmd/... ./providers/... \
  && python3 scripts/check_docs_translations.py > $L/logs/t11-pass.log 2>&1; echo "exit=$?"
grep -a -E -- '^(--- FAIL|FAIL)' $L/logs/t11-pass.log
```

Expected: `exit=0`; gofmt and vet clean; no FAIL lines in internal, cmd and providers; the translation checker passes.

- [ ] **Step 5: Commit**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; mkdir -p $L/logs; cd $W
git -C $W rm -q internal/server/requestpolicy/context.go internal/server/requestpolicy/context_test.go
git -C $W add internal/router/responses_context_test.go internal/server/responses_context_test.go internal/server/context_overflow_test.go internal/server/estimate_consistency_test.go internal/server/anthropicapi/messages.go internal/server/anthropicapi/count_tokens.go internal/server/openaiapi/chat.go internal/server/bedrockapi/invoke.go internal/server/responsesapi/handler.go internal/router/request_routing.go docs/reference/api.md docs/reference/api.ko.md docs/codex-launcher.md docs/codex-launcher.ko.md docs/reference/data.md docs/reference/data.ko.md internal/CLAUDE.md
git -C $W commit -s -m "feat: use the content-aware token estimate for context gates, counts and TPM admission" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git -C $W status --short | grep -v '^??' ; echo clean-check-done
```

Expected: one new commit; `git status --short` lists no modified tracked files.


---

### Task 12: Repository gates and live check on a test gateway (items 1, 2, 9)

**Items:** program items 1, 2 and 9 live; all items through the gates. Item 10 is a ruling (Design decision 15), not a task.

**User-visible change:** none (verification only; nothing in the repository changes).

**Files:**
- No repository files. Writes only under `/home/atomoh/.tmp/inferplane-path-remediation-20261029/verify/` (config, throwaway keys, fixture bodies, responses, logs) and `$L/logs/`.

**Interfaces:**
- Consumes the branch `mayu` binary, `internal/tokenest/testdata/fixtures` (Task 10), Bedrock through the EC2 instance role (`auth.mode: default`; the EC2 instance role's account).

**Cost control (Design decision 16):** only `measured-prose-624k` and `measured-dense-398k` are meant to be billed (gpt-oss, $0.15/M input, `max_tokens` 16: about $0.03 together). Every other request is refused by the gateway, or by Bedrock because it exceeds the model's real window (gpt-oss 131,072 tokens: the overflow bodies by ≈2%, the calibration bodies by ≈2×; Claude Haiku 4.5 200,000 tokens: the Claude bodies by ≈2× on the o200k count), and Bedrock does not bill a refused request. Each loop stops at the first unexpected 200, so the worst case is one accepted Claude body (≈$0.40) on top of the expected $0.03. Step 7 sums the audited cost.

- [ ] **Step 1: Repository gates**

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree; L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; D=/home/atomoh/.tmp/inferplane-audit-console-20261029/docs-venv/bin; mkdir -p $L/logs $L/verify; cd $W
go test -buildvcs=false ./... -race -count=1 > $L/logs/gate-test.log 2>&1; echo "test=$?"; grep -a -E '^(FAIL|--- FAIL)' $L/logs/gate-test.log
go vet ./... > $L/logs/gate-vet.log 2>&1; echo "vet=$?"
gofmt -l . > $L/logs/gate-fmt.log; echo "fmt-files=$(wc -l < $L/logs/gate-fmt.log)"
go mod tidy -diff > $L/logs/gate-tidy.log 2>&1; echo "tidy=$?"
bash tests/run-all.sh > $L/logs/gate-harness.log 2>&1; echo "harness=$?"
python3 scripts/check_docs_translations.py > $L/logs/gate-translations.log 2>&1; echo "translations=$?"
$D/python -B -m unittest discover -s tests/docs > $L/logs/gate-doctests.log 2>&1; echo "doctests=$?"
$D/python scripts/build_performance_docs.py --check > $L/logs/gate-perf.log 2>&1; echo "perf=$?"
$D/python scripts/build_client_compat_docs.py --check > $L/logs/gate-compat.log 2>&1; echo "compat=$?"
$D/mkdocs build --strict > $L/logs/gate-mkdocs.log 2>&1; echo "mkdocs=$?"
$D/python scripts/check_docs_site.py site > $L/logs/gate-site.log 2>&1; echo "site=$?"
CGO_ENABLED=0 go build -buildvcs=false -trimpath -o $L/verify/mayu ./cmd/mayu > $L/logs/gate-build.log 2>&1 \
  && CGO_ENABLED=0 go build -buildvcs=false -trimpath -o $L/logs/inferplaned ./cmd/inferplaned >> $L/logs/gate-build.log 2>&1; echo "build=$?"
```
Expected: `test=0` with no FAIL lines, `vet=0`, `fmt-files=0`, `tidy=0`, `harness=0`, `translations=0`, `doctests=0`, `perf=0`, `compat=0`, `mkdocs=0`, `site=0`, `build=0`. `site/` is ignored by git.

- [ ] **Step 2: Configure and start the test gateway**

The route shapes are copied from `/home/atomoh/.tmp/inferplane-audit-console-20261029/verify/config.json` (providers `bedrock-mantle` us-east-1 and `bedrock-global` ap-northeast-2, `auth.mode: default`, the gpt-oss and Haiku 4.5 rates); no secret is copied. `calib-*` models declare no window, so their requests reach Bedrock and its refusal reveals the real token count.

```bash
L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; V=$L/verify; W=$L/worktree
mkdir -p $V/fx $V/out && chmod 700 $V
cd $W && go run -buildvcs=false ./internal/tokenest/testdata/fixtures -out $V/fx > $V/fixtures.log 2>&1; echo "fixtures=$?"
(umask 077; openssl rand -hex 32 > $V/admin.txt)
python3 - "$V" <<'EOF'
import json, pathlib, sys
V = pathlib.Path(sys.argv[1])
oss = {"provider": "bedrock-mantle", "model": "openai.gpt-oss-120b-1:0"}
cfg = {
  "server": {"listen": "127.0.0.1:18281", "admin_listen": "127.0.0.1:19291",
             "admin_auth": {"token_refs": [{"env": "INFERPLANE_ADMIN_TOKEN"}]}},
  "key_store": {"type": "sqlite", "path": str(V / "keys.db")},
  "audit": {"failure_mode": "buffer_then_block", "buffer": {"path": str(V / "audit.wal")},
            "sinks": [{"type": "file", "path": str(V / "audit.jsonl")}]},
  "providers": {
    "bedrock-mantle": {"type": "bedrock", "region": "us-east-1", "auth": {"mode": "default"}},
    "bedrock-global": {"type": "bedrock", "region": "ap-northeast-2", "auth": {"mode": "default"}}},
  "models": {
    "openai.gpt-oss-120b-1:0": {"targets": [oss], "context_window": 128000, "capabilities": ["tools"]},
    "calib-gpt-oss": {"targets": [oss]},
    "calib-haiku": {"targets": [{"provider": "bedrock-global", "model": "global.anthropic.claude-haiku-4-5-20251001-v1:0", "api": "invoke_model"}]}},
  "pricing": {"on_missing": "block", "overrides": {
    "bedrock-mantle": {"openai.gpt-oss-120b-1:0": {"input_per_mtok": 0.15, "output_per_mtok": 0.6}},
    "bedrock-global": {"global.anthropic.claude-haiku-4-5-20251001-v1:0": {"input_per_mtok": 1.0, "output_per_mtok": 5.0}}}},
}
(V / "config.json").write_text(json.dumps(cfg, indent=2))
EOF
INFERPLANE_ADMIN_TOKEN="$(cat $V/admin.txt)" $V/mayu config validate --config $V/config.json > $V/validate.log 2>&1; echo "validate=$?"
INFERPLANE_ADMIN_TOKEN="$(cat $V/admin.txt)" $V/mayu keys create --team live --models '*' --store $V/keys.db > $V/key.out 2>&1; echo "key=$?"
(umask 077; head -n1 $V/key.out | grep -o 'ik_[A-Za-z0-9_-]*' > $V/key.txt; printf 'x-api-key: %s\n' "$(cat $V/key.txt)" > $V/auth.hdr); rm -f $V/key.out
cd $V && INFERPLANE_ADMIN_TOKEN="$(cat $V/admin.txt)" $V/mayu serve --config $V/config.json > $V/mayu.log 2>&1 &   # run_in_background
for i in $(seq 1 30); do curl -fsS -o /dev/null http://127.0.0.1:19291/healthz 2>/dev/null && break; sleep 1; done; echo "ready_after=${i}s"
```
Expected: `fixtures=0` (21 bodies and `fx/estimates.tsv`), `validate=0` (`configuration valid`), `key=0`, `ready_after` a few seconds. Check first that nothing listens on 18281/19291 (`ss -ltn | grep -E ':18281|:19291'` prints nothing).

- [ ] **Step 3: Free checks — counts, the bridge refusal (item 2), metrics**

```bash
L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; V=$L/verify; D=http://127.0.0.1:18281
post() { curl -sS -o "$V/out/$1.resp" -w '%{http_code}' -H @$V/auth.hdr -H 'content-type: application/json' -H 'anthropic-version: 2023-06-01' --data-binary @"$V/fx/$2.json" "$D$3"; }
want() { awk -v n="$1" -F'\t' '$1==n{print $3}' $V/fx/estimates.tsv; }
for f in class-prose class-korean claude-prose measured-dense-484k; do post "n-$f" "$f" /v1/messages/count_tokens > /dev/null; echo "$f count=$(python3 -c "import json;print(json.load(open('$V/out/n-$f.resp'))['input_tokens'])") estimate=$(want $f)"; done
for f in count-class-prose count-class-korean; do post "n-$f" "$f" /model/calib-gpt-oss/count-tokens > /dev/null; echo "$f count=$(python3 -c "import json;print(json.load(open('$V/out/n-$f.resp'))['inputTokens'])") estimate=$(want $f)"; done
echo "bridge status=$(post bridge bridge-prose-1000k /v1/responses)"; python3 -c "import json;print(json.load(open('$V/out/bridge.resp'))['error'])"
curl -fsS http://127.0.0.1:19291/metrics > $V/out/metrics.txt
echo "runtime_families=$(grep -c -E '^(go_goroutines|go_memstats_heap_inuse_bytes|process_resident_memory_bytes) ' $V/out/metrics.txt) key_in_metrics=$(grep -cF "$(cat $V/key.txt)" $V/out/metrics.txt) upstream_calls=$(grep -c 'bedrock: upstream error' $V/mayu.log)"
```
Expected (dry-run on the branch build: identical numbers): every `count=` equals its `estimate=` (class-prose 260487, class-korean 283482, claude-prose 400861, measured-dense-484k 123062); `bridge status=400` and `{'code': 'context_length_exceeded', … 'type': 'invalid_request_error'}` (item 2: was 403 `no policy-permitted compatible Responses target`); `runtime_families=3 key_in_metrics=0 upstream_calls=0` (nothing reached Bedrock yet).

- [ ] **Step 4: Item 1 — Bedrock Converse overflow is recoverable on every ingress**

The dense 484 KB body is estimated at 123,0xx tokens, under the 128,000-token declaration, so the gateway forwards it; its real size (about 133.8k) exceeds gpt-oss's 131,072 window, so Bedrock refuses it without billing.

```bash
L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; V=$L/verify; D=http://127.0.0.1:18281
post() { curl -sS -o "$V/out/$1.resp" -w '%{http_code}' -H @$V/auth.hdr -H 'content-type: application/json' -H 'anthropic-version: 2023-06-01' --data-binary @"$V/fx/$2.json" "$D$3"; }
M=openai.gpt-oss-120b-1:0
for row in "messages overflow-messages /v1/messages" "messages-stream overflow-messages-stream /v1/messages" \
           "chat overflow-chat /v1/chat/completions" "invoke overflow-invoke /model/$M/invoke" \
           "converse overflow-converse /model/$M/converse" "responses overflow-responses /v1/responses"; do
  set -- $row; code=$(post "o-$1" "$2" "$3"); echo "$1 status=$code"
  if [ "$code" = 200 ]; then echo "STOP: $2 was accepted (billed); skip the remaining overflow bodies"; break; fi
done
python3 - "$V" <<'EOF'
import pathlib, re, sys
for f in sorted((pathlib.Path(sys.argv[1]) / 'out').glob('o-*.resp')):
    t = f.read_text(errors='replace')
    m = re.search(r'prompt is too long: (\d+) tokens > (\d+) maximum', t)
    print(f.stem, ('N=%s M=%s' % m.groups()) if m else 'no-counts', 'code' if 'context_length_exceeded' in t else '-',
          'LEAK' if ('bedrock upstream error' in t or 'Input length' in t or 'arn:aws' in t) else 'clean')
EOF
```
Expected: all six `status=400`. `o-messages`, `o-messages-stream` and `o-chat`: `N=<about 133,800–134,200> M=131072 code clean`; `o-invoke` and `o-converse`: `N=… M=131072 - clean` (AWS envelope carries only the message); `o-responses`: `no-counts code clean`. Before this branch the same requests returned `bedrock upstream error (ValidationException)` (Messages, Chat, Bedrock) and a Responses error without a recovery code. Record the six `N` values.

- [ ] **Step 5: Item 9 — calibrate the estimate against real token counts (refusals only)**

```bash
L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; V=$L/verify; D=http://127.0.0.1:18281
post() { curl -sS -o "$V/out/$1.resp" -w '%{http_code}' -H @$V/auth.hdr -H 'content-type: application/json' -H 'anthropic-version: 2023-06-01' --data-binary @"$V/fx/$2.json" "$D$3"; }
for c in prose code read digits hex korean hangul; do code=$(post "c-$c" "class-$c" /v1/messages); echo "class-$c status=$code"; [ "$code" = 200 ] && { echo "STOP: class-$c was accepted (billed)"; break; }; done
for c in prose korean; do code=$(post "k-$c" "claude-$c" /v1/messages); echo "claude-$c status=$code"; [ "$code" = 200 ] && { echo "STOP: claude-$c was accepted (billed)"; break; }; done
python3 - "$V" <<'EOF' | tee $V/calibration.tsv
import pathlib, re, sys
V = pathlib.Path(sys.argv[1])
est = {l.split('\t')[0]: int(l.split('\t')[2]) for l in (V / 'fx/estimates.tsv').read_text().splitlines()}
for f in sorted((V / 'out').glob('[ck]-*.resp')):
    name = ('class-' if f.stem[0] == 'c' else 'claude-') + f.stem[2:]
    m = re.search(r'prompt is too long: (\d+) tokens > (\d+) maximum', f.read_text(errors='replace'))
    if not m:
        print(f'{name}\tno-count'); continue
    real = int(m.group(1))
    print(f'{name}\testimate={est[name]}\treal={real}\tratio={est[name] / real:.3f}')
EOF
```
Expected: every status `400`. gpt-oss ratios within the bands the unit tests pin against o200k_base (`estimate_test.go`), widened by a few hundred template tokens: class-prose, class-code, class-read and class-digits 0.97–1.04; class-hex 0.92–1.00; class-korean 1.04–1.14; class-hangul 0.32–0.40. The two Claude rows are informational: record them (a Claude ratio far from 1 is a follow-up for Claude-specific rates, not a failure of this plan). If a gpt-oss ratio falls outside its band, do not change constants in this plan: record the measured values in the report as a calibration follow-up.

- [ ] **Step 6: Item 9 — the 2026-10-27 measured cases on the declared 128k window**

```bash
L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; V=$L/verify; D=http://127.0.0.1:18281
post() { curl -sS -o "$V/out/$1.resp" -w '%{http_code}' -H @$V/auth.hdr -H 'content-type: application/json' -H 'anthropic-version: 2023-06-01' --data-binary @"$V/fx/$2.json" "$D$3"; }
for m in measured-prose-624k measured-dense-398k measured-dense-484k; do echo "$m status=$(post "m-$m" "$m" /v1/messages) estimate=$(awk -v n=$m -F'\t' '$1==n{print $3}' $V/fx/estimates.tsv) bytes_div_4=$(( $(stat -c %s $V/fx/$m.json) / 4 ))"; done
grep -o 'prompt is too long: [0-9]* tokens > [0-9]* maximum' $V/out/m-measured-dense-484k.resp
python3 -c "import json;r=json.load(open('$V/out/m-measured-prose-624k.resp'));print('prose usage', r.get('usage'))"
```
Expected: `measured-prose-624k status=200` (estimate ≈94k; `bytes_div_4` ≈156.5k is what the old gateway compared, refusing it at about 76% of the real window); `measured-dense-398k status=200` (estimate ≈101k; real ≈110k); `measured-dense-484k status=400` with `prompt is too long: <≈133.8k> tokens > 131072 maximum` (the estimate ≈123k passes the gate; Bedrock's refusal arrives recoverable, item 1). The prose usage `input_tokens` should be within 3% of its estimate (record it).

- [ ] **Step 7: Evidence, cost and secret checks**

```bash
L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; V=$L/verify
INFERPLANE_ADMIN_TOKEN=x $V/mayu audit verify --file $V/audit.jsonl > $V/out/verify.out 2>&1; echo "verify=$?"; cat $V/out/verify.out
python3 - "$V" <<'EOF'
import json, pathlib, sys
V = pathlib.Path(sys.argv[1])
recs = [json.loads(l) for l in (V / 'audit.jsonl').read_text().splitlines()]
done = [r for r in recs if r['event'] == 'request_completed']
cost = sum((r.get('cost') or {}).get('amount_usd_micros', 0) for r in done)
bridge = [r for r in recs if r['request']['ingress'] == 'responses' and (r.get('outcome') or {}).get('error') == 'context_window_exceeded']
print('records', len(recs), 'completed', len(done), 'billed_ok', sum(1 for r in done if r['outcome']['status'] == 200), 'cost_usd', cost / 1e6, 'bridge_refusals', len(bridge))
EOF
for s in key.txt admin.txt; do echo "$s found in: $(grep -lF "$(cat $V/$s)" $V/mayu.log $V/audit.jsonl $V/out/* 2>/dev/null | wc -l) files"; done
grep -c 'bedrock: upstream error status=400 code=ValidationException' $V/mayu.log
```
Expected: `verify=0` (`chain OK`), `billed_ok 2`, `cost_usd` below 0.10, `bridge_refusals 1`, both secrets found in `0 files`, and one server-log line per Bedrock refusal (the raw upstream text stays in the operator log only). Write a short results file `$V/results.md` with the Step 3–6 outputs (status lines, `N` values, ratios, cost) for the report.

- [ ] **Step 8: Stop the test gateway and delete throwaway material**

```bash
L=/home/atomoh/.tmp/inferplane-path-remediation-20261029; V=$L/verify
pkill -f "$V/mayu serve"; sleep 1; pgrep -f "$V/mayu serve" || echo stopped
rm -f $V/admin.txt $V/key.txt $V/auth.hdr $V/fx/*.json $V/out/*.resp
ls $V
```
Expected: `stopped`; only logs, configs, stores, `fx/estimates.tsv`, `calibration.tsv`, `out/metrics.txt`, `out/verify.out` and `results.md` remain. No commit (nothing in the repository changed). Report Steps 1–7 in the final message; `mayu.service` was not touched.

- [ ] **Step 9: Regenerate `AGENTS.md` (controller, after Steps 1–8)**

Task 10 changed the root `CLAUDE.md` (one Project Structure line), so the generated `AGENTS.md` marker is stale. The controller runs `/co-agent sync-context` (a host command, not a shell step) to regenerate it, never editing it by hand, then:

```bash
W=/home/atomoh/.tmp/inferplane-path-remediation-20261029/worktree
git -C $W status --short | grep -v '^??'
head -c 160 $W/AGENTS.md; echo
git -C $W add AGENTS.md
git -C $W commit -s -m "docs: regenerate AGENTS.md" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
Expected: before the commit only ` M AGENTS.md` is listed, and its first line carries a new `claude-md-sha` and today's `generated-at`. If `/co-agent sync-context` is unavailable, skip this step and say so in the final message; nothing else depends on it.

---

## Self-review notes (plan author)

- Coverage: item 1 → Task 1 (+ live Task 12 Step 4); item 2 → Task 2 (+ Step 3); item 3 → Task 3; item 4 → Task 5; item 5 → Task 4; item 6 → Tasks 8 and 9; item 7 → Task 7; item 8 → Task 6; item 9 → Tasks 10 and 11 (+ Steps 5–6); item 10 → Design decision 15 (ruling). The five Review Focus pins are in Tasks 5, 9, 10 and 11 (two).
- Verification: the spec of every task was applied in order to a copy of main `26ad568`; each task's "fail" command failed with the listed errors and its check command passed; after Task 11 the full Task 12 Step 1 gates passed (`go test ./... -race`, vet, gofmt, `go mod tidy -diff`, `tests/run-all.sh`, the docs checks, `mkdocs build --strict`, `check_docs_site.py`, both static builds). Task 12 Step 3 was dry-run against a test gateway built from that copy (no Bedrock call). Steps 4–7 need Bedrock and were not run by the plan author.
- Names used across tasks: `providers.ContextOverflowCounts`, `providers.PromptTooLongBody`, `routingtest.Spy.Err`, router reason `context_window_exceeded`, `requestedTokens` (router tests; body changes in Task 11), `ResolveModelSource`, `ObserveModelFallback` (router and metrics), `audit.WithModelFallback`, `RoutingRef.ModelFallbackFrom/ModelFallbackSource`, `writeIngressError`, `WithMaxInflightRequestBytes`, `ServerConfig.MaxInflightRequestBytes`, `tokenest.Estimate`, `tokenesttest.{Classes,ClassNamed,Dense,AnthropicBody}`.
- Judgment calls an executor may meet: none expected. If an edit's old text does not match exactly, the base moved: re-read the surrounding code and apply the same change, and record it in the task report.
