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

## Codex 0.152.1 — ❌ blocked: requires the Responses API

```bash
# ~/.codex/config.toml: model_provider mayu, base_url http://127.0.0.1:9102/v1,
# wire_api = "chat"
codex exec 'say ok'
# → Error loading config.toml: `wire_api = "chat"` is no longer supported.
#   How to fix: set `wire_api = "responses"` in your provider config.
```

Current Codex has REMOVED the Chat Completions wire for custom providers
(openai/codex discussion #7782): a provider must serve the OpenAI
**Responses API** (`POST /v1/responses`). mayu does not serve it, so Codex
cannot use inferplane today, full stop. This finding supersedes the
Chat-shape Codex fixtures in `agent_wire_test.go` — those pin a wire current
Codex no longer speaks. Purpose #1's Codex claim therefore requires a
`/v1/responses` ingress; tracked as the top model-compatibility gap
(docs/comparison.md §6, roadmap Purpose table).

## What this does and does not prove

Proves: the two working clients complete real turns through the full
pipeline — key auth, RBAC, governance PreCheck with budget reservation,
provider egress, settle with integer-µUSD cost, hash-chained audit — with
zero client-side patches, and the failure mode for the third is precisely
identified from the client's own error, not guessed. Does not prove:
production adoption, real-provider traffic, or performance under a real
model (see `benchmarks/gwcompare/README.md` for what the latency numbers do
and don't cover).
