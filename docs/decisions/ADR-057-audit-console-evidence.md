# ADR-057: Audit console evidence

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status

Proposed, 2026-10-29. The design was approved in review; implementation waits
for plan approval. ADR-056 is reserved by the personal-dataplane OIDC proposal
on a separate branch.

## Context

Administrators need an audit view of the gateway: what happened to a request,
what one Claude Code session did, what sensitive data was detected, and what was
refused automatically. The audit hash chain is the only trustworthy source, but
it cannot answer three of these questions today:

- It records no per-attempt timing. `outcome.fallback_used` and
  `outcome.fallback_chain` are declared but never written
  (`internal/audit/record.go:50-56`).
- It records nothing about the client request: Claude Code's
  `x-claude-code-request-class` and `X-Claude-Code-Session-Id` headers are
  ignored, so a session cannot be reconstructed.
- Claude Code auto mode classifier decisions look like ordinary model calls.

The analytics index (ADR-015, ADR-018) projects only `request_completed`
records, but every gateway denial is written as a `request_started` record with
an outcome, so denials are absent from the index today.

ADR-044 keeps scoped session hints out of audit and metrics: affinity pins use
scoped HMAC digests in a bounded local store. Grouping requests by session in
the audit chain needs an explicit exception to that rule.

## Options considered

### Group sessions in the index only, from raw headers at ingest

The index is fed from audit records, so the raw ID would have to pass through
the chain or a side channel. Rejected.

### Store the raw session ID in the audit record

Simplest, but a client-chosen identifier would become permanent tamper-evident
data and could link sessions across keys. Rejected.

### HMAC with a node secret

Resistant to dictionary attacks on low-entropy IDs, but Mode B gateways would
need a shared secret to group the same session, and a lost or rotated secret
breaks grouping of historical records. Claude Code session IDs are random
UUIDs, so a domain-separated SHA-256 bound to the key is sufficient. Rejected
for now; revisit if a client sends low-entropy IDs.

### Key-scoped, domain-separated SHA-256 digest in the audit record

Chosen. See the decision below.

### No session view

Smallest change, but the console could not show what an agent session did,
which was the main request. Rejected.

## Decision

1. Audit records gain three append-only, `omitempty` fields after
   `guardrail_version`:
   - `attempts[]{provider, model, status, http, start_ms, dur_ms}`, at most
     eight entries, provider names and closed statuses only.
   - `client{request_class, session_ref}`. `request_class` is kept only if it is
     one of `main`, `subagent`, `workflow`, `compaction`, `auxiliary`,
     `side_reply`.
   - `auto_mode{verdict, stage, signature}`, an estimate; `verdict` is
     `block`, `allow` or `unknown`; `stage` is `s1`, `s2` or `server`.
   - Terminal records also carry `latency{total_ms, ttft_ms}` on every ingress.
2. When a request makes more than one attempt, `outcome.fallback_used` and
   `outcome.fallback_chain` are written from `attempts`. Older records are
   unchanged.
3. `session_ref = "s1:" + hex(sha256("inferplane-session-v1\0" + key_id + "\0" + session_id)[:16])`.
   It is written to the audit chain and the analytics index only, never to
   metrics, logs or API output as the raw ID. It groups requests of one session
   under one key for audit; it is never used for authorization or routing. It is
   on by default and `audit.client_evidence.session_ref: "off"` disables it.
   This is a scoped exception to ADR-044's rule for audit and metrics; ADR-044
   pins are unchanged.
4. An auto mode verdict is recorded only when it parses unambiguously from an
   Anthropic-format response, and does not require the request class header
   (Claude Code 2.1.312 does not send it):
   - Block call: stop sequences contain `</block>`; after removing `<thinking>`,
     exactly one consistent `<block>yes|no`, and the reply stopped on `</block>`
     or closes it in the text; stage `s1`. Otherwise `unknown` and the counter.
   - Server safeguard: `safeguard_results[]` with a `dangerous_tool_use` entry
     (complete or streamed). `status.type` `available` with every `tool_uses`
     entry `{type: evaluated, outcome: not_flagged}` is `allow`; any other
     status or outcome is `unknown` plus the counter; empty `tool_uses` records
     nothing; stage `server`.
   - The full text rule, including stage `s2` (`<thinking>` replies with no stop
     sequence), runs only when `request_class == auxiliary`.
   - `</severity>` calls and replies ending in `<severity>` are never recorded.
   The gateway reads at most 16 KiB of text, stores no text, and counts
   non-matching classifier-like calls in one unlabelled metric. `signature`
   (`claude-code-2.1`) names the client format version. The console always
   labels these verdicts as estimates.
5. The analytics index gains an `audit_events` table and a category table that
   project every terminal request record (completed records and denied
   `request_started` records) with the exact audit line (`record_json`). The
   existing `events` table is unchanged. `kind` is derived in the index, never
   written to the audit chain.
6. A read-only audit API (`/admin/audit/events`, `/events/{id}`, `/sessions`,
   `/sessions/{ref}`, `/summary`) is full-admin only and mounted only with the
   analytics index.

## Consequences

### Positive

- A fallback is explained by the chain itself: which target failed, how, and
  after how long.
- Sessions, subagent work and denials become reviewable without enabling body
  capture.
- `fallback_used` and `fallback_chain` finally carry the values their names
  promise.

### Negative

- Audit lines grow by roughly 100–300 bytes for requests with attempts and
  client headers.
- Within one key, requests of one session become linkable in long-term audit
  data. Turning `session_ref` off stops new links but does not remove old ones.
- Auto mode verdicts depend on Claude Code's response format. A format change
  yields no verdicts (and a rising unparsed count), not wrong ones.
- Records no longer in any file sink are not in the console table; the
  console shows the time its coverage starts.
- Governance denials still carry no policy name; the console shows their code
  and scope only.

## References

- [ADR-015](ADR-015-analytics-mode-b-shared-store.md), [ADR-018](ADR-018-opt-in-body-logging.md),
  [ADR-044](ADR-044-adaptive-coding-gateway.md)
- Design: `docs/superpowers/specs/2026-10-29-audit-console-design.md`

---

<a id="korean"></a>

# 한국어

## 상태

제안, 2026-10-29. 설계는 검토에서 승인되었고 구현은 계획 승인을 기다립니다.
ADR-056은 다른 브랜치의 개인 dataplane OIDC 제안이 쓰고 있습니다.

## 배경

관리자에게는 gateway 감사 화면이 필요합니다. 요청 하나에 무슨 일이 있었는지,
Claude Code 세션 하나가 무엇을 했는지, 어떤 민감 정보가 탐지되었는지, 무엇이
자동으로 거부되었는지를 봐야 합니다. 믿을 수 있는 근거는 감사 hash chain뿐인데,
지금은 이 가운데 세 가지에 답하지 못합니다.

- 시도별 시간이 없습니다. `outcome.fallback_used`와 `outcome.fallback_chain`은
  선언만 있고 한 번도 채워지지 않습니다(`internal/audit/record.go:50-56`).
- 클라이언트 요청 정보가 없습니다. Claude Code의
  `x-claude-code-request-class`와 `X-Claude-Code-Session-Id` 헤더를 무시하므로
  세션을 다시 구성할 수 없습니다.
- Claude Code auto mode 분류기 결정은 일반 모델 호출처럼 보입니다.

analytics 인덱스(ADR-015, ADR-018)는 `request_completed`만 투영하는데, gateway
거부는 모두 outcome이 붙은 `request_started`로 기록됩니다. 그래서 지금 인덱스에는
거부가 없습니다.

ADR-044는 범위가 정해진 세션 hint를 감사와 metrics 밖에 둡니다. affinity pin은
제한된 로컬 저장소의 scoped HMAC digest를 씁니다. 감사 체인에서 요청을 세션으로
묶으려면 이 규칙에 명시적인 예외가 필요합니다.

## 검토한 옵션

### 수집 시점의 원본 헤더로 인덱스에서만 세션 묶기

인덱스는 감사 기록에서 만들어지므로, 원본 ID가 체인이나 별도 경로를 거쳐야
합니다. 채택하지 않음.

### 감사 기록에 세션 ID 원문 저장

가장 단순하지만, 클라이언트가 정한 식별자가 영구적인 위변조 감지 데이터가 되고
키를 넘어 세션을 연결할 수 있습니다. 채택하지 않음.

### 노드 비밀을 쓰는 HMAC

엔트로피가 낮은 ID에 대한 사전 공격에 강하지만, Mode B gateway들이 같은 세션을
묶으려면 비밀을 공유해야 하고, 비밀을 잃거나 바꾸면 과거 기록의 묶음이 끊깁니다.
Claude Code 세션 ID는 무작위 UUID이므로, 키에 묶인 도메인 분리 SHA-256으로
충분합니다. 지금은 채택하지 않고, 엔트로피가 낮은 ID를 보내는 클라이언트가 생기면
다시 검토합니다.

### 감사 기록에 키 범위·도메인 분리 SHA-256 digest

채택. 아래 결정을 보세요.

### 세션 화면 없음

변경이 가장 작지만, 주요 요구였던 agent 세션의 동작을 보여줄 수 없습니다.
채택하지 않음.

## 결정

1. 감사 기록 `guardrail_version` 뒤에 append-only, `omitempty` 필드 세 개를
   추가합니다.
   - `attempts[]{provider, model, status, http, start_ms, dur_ms}`. 최대 8개,
     provider 이름과 닫힌 상태값만 씁니다.
   - `client{request_class, session_ref}`. `request_class`는 `main`, `subagent`,
     `workflow`, `compaction`, `auxiliary`, `side_reply` 중 하나일 때만 남깁니다.
   - `auto_mode{verdict, stage, signature}`. 추정값이고, `verdict`는 `block`,
     `allow`, `unknown` 중 하나이며 `stage`는 `s1`, `s2`, `server`입니다.
   - 요청의 마지막 기록에는 모든 ingress에서 `latency{total_ms, ttft_ms}`도 붙습니다.
2. 요청이 두 번 이상 시도되면 `attempts`로 `outcome.fallback_used`와
   `outcome.fallback_chain`을 채웁니다. 옛 기록은 바뀌지 않습니다.
3. `session_ref = "s1:" + hex(sha256("inferplane-session-v1\0" + key_id + "\0" + session_id)[:16])`.
   감사 체인과 analytics 인덱스에만 쓰고, metrics·로그·API 출력에 원본 ID로
   나가지 않습니다. 한 키 아래 한 세션의 요청을 감사 목적으로 묶을 뿐 권한
   판단이나 라우팅에는 쓰지 않습니다. 기본으로 켜고,
   `audit.client_evidence.session_ref: "off"`로 끕니다. ADR-044 규칙에 대한
   감사·metrics 범위의 예외이며, ADR-044 pin은 그대로입니다.
4. auto mode 판정은 Anthropic 형식 응답에서 모호하지 않게 해석될 때만
   기록하며, class 헤더를 요구하지 않습니다(Claude Code 2.1.312는 보내지
   않습니다).
   - block 호출: stop sequence에 `</block>`이 있고, `<thinking>`을 지운 뒤
     일관된 `<block>yes|no`가 정확히 하나이며, 응답이 `</block>`에서 멈췄거나
     텍스트가 닫습니다. 단계 `s1`. 그 외는 `unknown`과 카운터.
   - 서버 safeguard: `dangerous_tool_use` 항목이 있는 `safeguard_results[]`(완전
     응답 또는 stream). `status.type`이 `available`이고 모든 `tool_uses`가
     `{type: evaluated, outcome: not_flagged}`이면 `allow`, 그 밖의 status·outcome은
     `unknown`과 카운터, 빈 `tool_uses`는 기록 없음. 단계 `server`.
   - 단계 `s2`(stop sequence 없는 `<thinking>` 응답)를 포함한 전체 텍스트 규칙은
     `request_class == auxiliary`일 때만 동작합니다.
   - `</severity>` 호출과 `<severity>`로 끝나는 응답은 기록하지 않습니다.
   gateway는 텍스트를 최대 16 KiB만 읽고 저장하지 않으며, 맞지 않는 분류기 형태
   호출은 label 없는 metric 하나로 셉니다. `signature`(`claude-code-2.1`)는
   클라이언트 형식 버전입니다. 콘솔은 이 판정을 항상 추정으로 표시합니다.
5. analytics 인덱스에 `audit_events` 표와 category 표를 추가합니다. 요청의 마지막
   기록(완료 기록과 거부된 `request_started` 기록)을 감사 줄 원본(`record_json`)과
   함께 투영합니다. 기존 `events` 표는 바뀌지 않습니다. `kind`는 인덱스에서 파생하고
   감사 체인에는 쓰지 않습니다.
6. 읽기 전용 감사 API(`/admin/audit/events`, `/events/{id}`, `/sessions`,
   `/sessions/{ref}`, `/summary`)는 full-admin 전용이고 analytics 인덱스가 있을
   때만 마운트합니다.

## 영향

### 긍정적

- fallback을 체인 자체로 설명할 수 있습니다. 어느 대상이 어떻게, 얼마 만에
  실패했는지 남습니다.
- 본문 캡처를 켜지 않아도 세션, subagent 작업, 거부를 검토할 수 있습니다.
- `fallback_used`와 `fallback_chain`이 이름대로의 값을 갖게 됩니다.

### 부정적

- 시도와 클라이언트 헤더가 있는 요청의 감사 줄이 약 100–300바이트 늘어납니다.
- 한 키 안에서 한 세션의 요청이 장기 감사 데이터에서 연결됩니다.
  `session_ref`를 꺼도 새 연결만 멈추고 과거 연결은 남습니다.
- auto mode 판정은 Claude Code 응답 형식에 의존합니다. 형식이 바뀌면 잘못된
  판정이 아니라 판정 없음(판정 불가 수 증가)이 됩니다.
- 어느 file sink에도 남지 않은 기록은 콘솔 표에 없습니다. 콘솔은 기록 범위가
  시작되는 시각을 보여줍니다.
- 거버넌스 거부에는 여전히 정책 이름이 없습니다. 콘솔은 코드와 범위만
  보여줍니다.

## 참고 자료

- [ADR-015](ADR-015-analytics-mode-b-shared-store.md), [ADR-018](ADR-018-opt-in-body-logging.md),
  [ADR-044](ADR-044-adaptive-coding-gateway.md)
- 설계: `docs/superpowers/specs/2026-10-29-audit-console-design.md`
