# Real coding-agent clients through mayu — verification record

Date: 2026-09-02. Purpose #1 (CLAUDE.md) targets Claude Code, Codex, and
OpenCode as the clients this gateway exists for. Until now their support was
pinned by wire-shape fixtures constructed from documented payloads
(`internal/server/openaiapi/agent_wire_test.go`) — this record replaces that
caveat with REAL client binaries driven through a running mayu against a
mock upstream, with the audit chain as evidence.

Setup for all three runs: `mayu serve` with an `anthropic`-type provider and
an `openai_compatible` provider both pointed at a local mock upstream
(`benchmarks/gwcompare` `upstream` subcommand — valid Anthropic Messages
JSON/SSE + `count_tokens`, valid OpenAI chat completions JSON/SSE), one team,
one virtual key minted via `POST /admin/keys`. No real provider credentials
anywhere; the client sees only the `ik_...` virtual key.

## Claude Code 2.1.258 — ✅ full turn served

```bash
ANTHROPIC_BASE_URL=http://127.0.0.1:9102 ANTHROPIC_API_KEY=$VIRTUAL_KEY \
ANTHROPIC_MODEL=claude-test ANTHROPIC_SMALL_FAST_MODEL=claude-test \
claude -p 'say ok' --model claude-test
# → exit 0, printed the upstream's reply:
#   ok: mock upstream reached through the gateway
```

- The CLI authenticated with the virtual key (`Authorization: Bearer ik_...`
  — mayu accepts both header forms), hit `POST /v1/messages?beta=true`
  (query string and beta header tolerated), and `count_tokens` answered 200
  through the gateway (the never-non-200 mandate, exercised by the real
  caller).
- The turn settled: audit `request_completed`, status 200, usage
  `{input:25, output:9}`, cost 210 µUSD at the configured test rate, and
  `mayu audit verify` reported `chain OK` over the full log afterwards.
- One real-client note: Claude Code sends `?beta=true` and warns locally
  about unrecognized model names (cosmetic; the turn completes).

## OpenCode 1.18.26 — ✅ full turn served

```bash
# ~/.config/opencode/opencode.json: provider "mayu" via @ai-sdk/openai-compatible,
# baseURL http://127.0.0.1:9102/v1, apiKey = the virtual key
opencode run 'say ok'
# → exit 0, printed the upstream's reply: benchmark response
```

- Served on the `openai` ingress; audit shows the turns completed at 200
  with 165 µUSD settled each.
- Note: OpenCode fetches its provider catalog from models.dev at startup —
  it needs outbound network once, unrelated to the gateway path.

## Codex 0.152.1 — ✅ full turn served (via the new `/v1/responses` ingress)

The first attempt EXPOSED the gap this record exists to catch: current
Codex has REMOVED the Chat Completions wire for custom providers
(openai/codex discussion #7782) —

```
Error loading config.toml: `wire_api = "chat"` is no longer supported.
How to fix: set `wire_api = "responses"` in your provider config.
```

— superseding the Chat-shape Codex fixtures in `agent_wire_test.go`, which
pin a wire current Codex no longer speaks. The gap was closed the same day:
`internal/server/responsesapi` serves `POST /v1/responses` as an adapter
over the chat ingress, built against Codex's REAL captured request (the
trimmed capture is that package's test fixture). With
`wire_api = "responses"`:

```bash
# ~/.codex/config.toml: model_provider mayu, base_url http://127.0.0.1:9102/v1,
# wire_api = "responses", env_key MAYU_API_KEY = the virtual key
codex exec --skip-git-repo-check 'say ok'
# → exit 0, zero protocol errors, printed the upstream's reply:
#   benchmark response
#   tokens used: 31   (usage carried through response.completed)
```

Codex's 9-tool agentic request (exec_command + 8 more, 17KB instructions,
`reasoning`/`include`/`prompt_cache_key` knobs) streams through the full
pipeline; untranslatable params are dropped WITH disclosure
(`x-inferplane-responses-params-dropped`), and function tools survive the
round trip (the package's tests pin the tool-call reassembly from split
SSE deltas). Non-function tools (`namespace`, `web_search`) have no chat
equivalent and are disclosed as dropped — Codex's core exec loop does not
depend on them.

## What this does and does not prove

Proves: all THREE target clients complete real turns through the full
pipeline — key auth, RBAC, governance PreCheck with budget reservation,
provider egress, settle with integer-µUSD cost, hash-chained audit — with
zero client-side patches, and the one protocol gap found was identified
from the client's own error and closed against its own captured wire, not
guessed from documentation. Does not prove:
production adoption, real-provider traffic, or performance under a real
model (see `benchmarks/gwcompare/README.md` for what the latency numbers do
and don't cover).
