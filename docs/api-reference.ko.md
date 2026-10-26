---
translation_source: api-reference.md
translation_source_sha256: 9c3feca93b68fa421083252513c6605cd2609367ae838887eca550d45f109363
---

# API 레퍼런스 {#api-reference}

클라이언트 트래픽의 **데이터 영역**(`:8080`)과 운영용 **관리 영역**(`:9090`)을 제공합니다. 생성·모델 조회는 가상 키(`ik_...`)로 인증합니다. 계산 API와 선택적 로그인 초기화에는 아래 예외 계약이 적용됩니다.

## 데이터 영역 · 8080 {#data-plane-8080}

### 인증 {#authentication}

클라이언트 프로토콜에 맞게 가상 키를 전송합니다.

- Anthropic: `x-api-key: ik_...`. Claude Code는 `ANTHROPIC_API_KEY` 값을 사용합니다.
- OpenAI: `Authorization: Bearer ik_...`.

키를 팀·허용 모델이 있는 Principal로 해석합니다. 클라이언트 키를 업스트림으로 보내지 않으며 공급자 자격 증명은 서버가 주입합니다.

### Anthropic 수신 {#anthropic-ingress}

```
POST /v1/messages
POST /v1/messages/count_tokens
GET  /v1/models
```

| 엔드포인트 | 동작 |
| --- | --- |
| `/v1/messages` | Messages, stream=true이면 SSE. Anthropic 프로토콜 업스트림에 본문 원문 전달 |
| `/v1/messages/count_tokens` | 토큰 계산. **키 인증 성공 후 200**이며 라우팅·정책·준비 상태·저장소 거부에는 로컬 계산을 유지. 없거나 잘못된 키는 401 |
| `/v1/models` | 주체가 사용할 모델 조회. `anthropic-version`으로 응답 형식 협상 |

### OpenAI 수신 {#openai-ingress}

```
POST /v1/chat/completions
POST /v1/responses
GET  /v1/models
```

| 엔드포인트 | 동작 |
| --- | --- |
| `/v1/chat/completions` | Chat Completions와 SSE. 업스트림 프로토콜이 다르면 정규 스키마 변환 |
| `/v1/responses` | 네이티브 Responses 또는 지원되는 무상태 텍스트·도구 브리지 |
| `/v1/models` | 주체에게 허용된 모델 조회 |

### Bedrock 형식 수신 {#bedrock-shaped-ingress}

```text
POST /model/{modelId}/invoke
POST /model/{modelId}/invoke-with-response-stream
POST /model/{modelId}/converse
POST /model/{modelId}/converse-stream
POST /model/{modelId}/count-tokens
```

AWS SigV4 클라이언트 신원이 아니라 가상 키(`x-api-key`·Bearer)로 인증합니다. 설정된 모델 ID를 URL 인코딩하세요. 생성은 공통 승인·라우팅 제어를 사용하고 스트림은 Bedrock 응답 형식입니다. 준비 상태·라우팅·저장소 때문에 생성을 거부하는 경우 계산은 로컬 HTTP 200을 유지하며, 키 누락·무효는 401이 가능합니다. 공급자 자격 증명과 AWS 서명은 외부 호출 때 별도로 적용합니다.

네이티브 Converse는 JSON 원문과 마지막 사용량 메타데이터를 포함한 AWS 이벤트 프레임을 보존합니다. Converse 이벤트를 InvokeModel 청크로 감싸거나 Anthropic 변환기의 파라미터 보정을 적용하지 않습니다. 필수 Guardrail 삽입과 검증된 정책 마스킹은 예외이며 Guardrail 충돌은 거부합니다. 설정된 Bedrock Runtime 대상만 지원하고 Mantle·SDK 엔드포인트 재정의는 제외합니다. FIPS·dualstack 설정은 반영합니다. 별도 가격이 없는 서비스 등급·최적화 지연 옵션은 거부하며, 캐시 TTL이나 OpenAI 캐시 입력 과금 의미가 불명확하면 비용 불확실성을 유지합니다. [계약·평가 가이드](bedrock-converse-evaluation.md)를 참고하세요.

### 사용량 {#usage}

`GET /v1/usage`는 인증된 주체의 사용량을 반환합니다. 공유 모드는 `enforcement_mode: shared`와 주체별 `shared_limits`를 포함합니다. used에는 예약·불확실성이 포함되고 reserved는 유지되는 부분입니다. 제어 영역 텔레메트리 수신과는 다른 API입니다.

### CLI 로그인 · 선택적 ADR-028 {#cli-login-opt-in-adr-028}

`oidc.cli_login.enabled`를 설정해야 등록되며 아니면 404입니다. 콘솔 키를 복사하는 대신 `mayu login`으로 단기 키를 받습니다. [CLI 로그인](runbooks/cli-login.md)을 참고하세요.

```
GET    /v1/auth/config   # unauthenticated; {cli, issuer?, client_id?}
POST   /v1/auth/key      # Authorization: Bearer <IdP ID token>; {"team"?: "..."} -> {key, key_id, team, expires_at}
DELETE /v1/auth/key      # x-api-key: <the minted key>; self-revoke, used by `mayu logout`
```

`expires_at`·`owner`는 항상 서버가 정하며 클라이언트가 더 긴 수명을 요청할 수 없습니다.

## 관리 영역 · 9090 {#admin-plane-9090}

### 인증 없는 경로 {#unauthenticated}

```
GET /healthz      # liveness
GET /readyz       # readiness
GET /metrics      # Prometheus exposition (no secret/key_id labels)
```

### 토큰 인증 · admin/keys {#token-authenticated-adminkeys}

`Authorization: Bearer <INFERPLANE_ADMIN_TOKEN>`을 사용합니다.

```
POST   /admin/keys        # issue a virtual key (plaintext returned once)
GET    /admin/keys        # list key metadata (never plaintext)
DELETE /admin/keys/{id}   # revoke a key
```

### 분석 결과와 비용 근거 (ADR-053) {#analytics-outcome-and-cost-evidence-adr-053}

기존 경로는 **full-admin** 권한이 필요합니다. 토큰이 없으면 401, 인증된 팀
매핑 일반 관리자는 403을 받습니다. 정적 콘솔은 데이터 없이 공개됩니다.
분석을 비활성화하면 이 경로를 사용할 수 없습니다.

```text
GET  /admin/analytics/summary?since=2026-10-21&until=2026-10-21
GET  /admin/analytics/timeseries?days=30
GET  /admin/logs?limit=200&before=<event-id>
GET  /admin/analytics/health
POST /admin/analytics/rebuild
```

Summary는 `YYYY-MM-DD` 형식의 `since`/`until` 날짜 양 끝을 포함합니다.
잘못된 날짜·역순 기간·양 끝의 차이가 366일을 초과하는 기간은 400을 반환합니다.
차이가 366일이면 포함되는 날짜는 367개입니다. `since`를 생략하면 UTC 오늘에서
30일 전이 기본값이므로 오늘까지 포함하면 31개 날짜입니다. `until`을 생략하면
검증에는 UTC 오늘을 쓰지만 저장소에는 빈 상한을 전달하므로 **실제 날짜 상한은
적용하지 않습니다**. 재현 가능한 기간 보고서에는 두 날짜를 모두 지정하세요.
2026-10-21까지 30개 날짜를 포함하려면 `since=2026-09-22&until=2026-10-21`을 씁니다.

Timeseries는 `days`만 읽고 `since`와 `until`은 무시합니다. `days`가 없거나
잘못된 값 또는 0 이하이면 기본값 30을 쓰고, 366보다 크면 366으로 제한합니다.
오늘에서 `days`일 전인 하한 날짜를 포함하고, 존재하는 날짜를 최신순으로 정렬해
최대 `days`개 행을 반환합니다. 날짜 상한이나 빈 날짜의 0 채움은 없으므로 기록이
드문 경우 추가 하한 날짜가 포함될 수 있습니다. Mode A의 하한은 UTC 기준이며,
Mode B는 데이터베이스 세션 시간대에 따라 달라지는 `current_date`를 사용합니다.
따라서 `days=30`은 명시적으로 30개 날짜를 지정한 summary와 같은 기간을 보장하지 않습니다.

Logs의 최신 ID 우선 키셋 순서와 페이지 최대 200건은 유지하며, Cost가 없는 기록도
포함한 모든 관측 `request_completed`를 반환합니다. Rebuild는 PostgreSQL Mode B에서
지원합니다. 204는 파생 상태 초기화의 응답이며, 이후 수집기가 보관 원본을
재생합니다. Mode A는 405입니다. [복구 절차](operations/recovery.md#recover-analytics-evidence)를 확인하세요.

`operations`는 summary 합계·팀/모델 행·일별 지점에 추가됩니다. 단위는
**관측 완료 기록**이며 전체 트래픽·고유 사용자 요청·세션·모델의 작업 품질이
아닙니다. `request_started` 거부, 미인증 401, 관리자 이벤트, 클라이언트가
전송하지 못한 요청은 제외됩니다. 재시도/폴백 기록 단위는 ingress마다 다를 수 있습니다.

결과 우선순위는 Partial=true → `partial`, Outcome 없음 또는 status가 200–599
밖이면 `unknown`, 유효한 status에서 Outcome.Error가 비어 있지 않거나 status≥300이면
`failed`, 나머지 2xx는 `succeeded`입니다. Partial은 failed에 중복 가산하지
않습니다. 관측 실패 비율은 `(failed + partial) / completed`입니다. completed=0이면
`—`를 표시하고 결과 미확정 건수를 비율 옆에 표시합니다.

| 비용 상태 (우선순위 순) | 의미 |
| --- | --- |
| `unavailable` | Cost=nil. 구버전의 비용 없는 원본도 포함하며 금액은 알 수 없음 |
| `unpriced` | Cost.PricingMissing. 숫자 0은 무료 산정액이 아님 |
| `legacy_unknown` | Cost는 있으나 Usage 또는 Usage.Evidence가 없음 |
| `uncertain` | Partial, Usage.Estimated, InputKnown/OutputKnown/InputTotalKnown/CacheWriteTTLKnown 중 false, AccountingUncertain 또는 음수 금액 |
| `estimated` | 나머지 사용량·가격 근거가 있는 기록. 산정액 0도 유효함 |

비용 상태는 결과와 독립적이므로 HTTP 400도 산정액을 가질 수 있습니다.
`estimated`도 단가 기반 산정액이지 공급자 최종 청구액이 아닙니다.
`estimated_cost_micros`는 estimated 합계,
`uncertain_recorded_cost_micros`는 uncertain + legacy_unknown의 기록 금액 합계입니다.
금액은 모두 정수 microUSD입니다. 기존 `requests`·토큰·`cost_micros` 합계는
Cost 존재(`has_cost=true`) 조건과 계산법을 유지합니다. 새 관측 건수는 비용 배분이나
예산 정산을 재정의하지 않습니다.

8개 기록의 수락 fixture는 아래 합계 발췌와 같습니다. 동일한 `operations`가
하나의 팀 행·모델 행·일별 지점에 나타납니다.

```json
{
  "requests": 6,
  "cost_micros": 3350,
  "operations": {
    "completed": 8,
    "succeeded": 4,
    "failed": 2,
    "partial": 1,
    "unknown": 1,
    "estimated_requests": 3,
    "uncertain_requests": 1,
    "unpriced_requests": 1,
    "unavailable_requests": 2,
    "legacy_unknown_requests": 1,
    "estimated_cost_micros": 1300,
    "uncertain_recorded_cost_micros": 2050,
    "legacy_projection_records": 0
  }
}
```

`completed`는 결과 네 가지 합계 및 비용 상태 다섯 가지 합계와 같습니다.
비용 없는 기록만 있는 팀/모델/날짜는 기존 requests와 금액이 0이어도 operations는
0이 아닐 수 있습니다. `legacy_projection_records`는 원본 재생을 기다리는
version-0 행 수입니다. 0도 모든 과거 기록 수집을 증명하지 않습니다. 보관 기간,
큐 drop, 수집 지연, 완료 기록 누락은 계속 범위를 제한합니다.

`GET /admin/logs`는 `{"events":[...]}`를 반환합니다. 기존 필드 이름은 유지하고
`has_cost`, `outcome_state`, `cost_state`, `partial`, `error_code`,
`projection_version`을 추가합니다. 빈 `error_code`는 생략됩니다. 아래 이벤트
발췌는 HTTP-200 부분 응답입니다.

```json
{
  "status": 200,
  "cost_micros": 2000,
  "has_cost": true,
  "outcome_state": "partial",
  "cost_state": "uncertain",
  "partial": true,
  "error_code": "stream_incomplete",
  "projection_version": 1
}
```

unavailable/unpriced에서 숫자 0을 알려진 달러 금액으로 표시하지 마세요.
구버전 API에 `operations` 또는 `cost_state`가 없으면 근거 정보 없음이지 성공·비용 0이
아닙니다. 제한된 스트림 원인과 [관측 한계](operations/observability.md#read-outcome-and-cost-evidence)를
적용하며 원문 오류 텍스트는 이 API에 추가하지 않습니다.

### 정책이 적용되는 모델 비교 · mayu 전용 ADR-052 {#governed-model-comparison-mayu-only-adr-052}

`POST /admin/evaluations`는 전체 관리자 권한과 별도의 `X-Inferplane-Virtual-Key`를 요구합니다. 본문은 `{"protocol":"bedrock-converse","model":"configured-name","body":{"messages":[...]}}` 형식입니다. 프로토콜은 `bedrock-converse`, `anthropic`, `openai`, `responses`이며, 뒤의 세 가지는 `body.model`과 `model`이 같아야 합니다. URL·공급자 자격 증명은 받지 않습니다. 일반 데이터 영역 핸들러를 통과하므로 가상 키 권한·라우팅·예산 승인·감사가 그대로 적용됩니다.

비스트리밍 요청만 지원하며 stream·background는 거부합니다. 브리지 본문 상한은 64 MiB이고 데이터 영역의 설정된 본문 제한도 적용됩니다. 브라우저 응답 보관은 4 MiB로 제한됩니다. 바깥 HTTP 상태는 브리지 인증·검증 결과이며, 결과의 `http_status`가 실제 추론 상태입니다. 응답에는 `outcome`, `response_success`, `stop_reason`, `duration_ms`, 출력, 사용량 근거, 가격 버전·비용, `accounting_known`, `attempts`, `unsettled_attempts`가 포함됩니다.

`ttft_ms`는 null입니다. 출력 토큰/E2E 초는 디코딩 처리량이 아닙니다. HTTP 200에서도 거절·출력 중단은 응답 실패로 집계합니다. 비용 확인에는 단일 시도·완전한 정산·사용량 근거·가격이 필요하며 AWS 청구서 대조를 뜻하지 않습니다. 재시도 후 마지막 정산 비용만 있으면 전체 비용 확인으로 표시하지 않습니다. 응답은 `no-store`이며 제어 영역 바이너리는 추론을 처리하지 않습니다.

## 오류 코드 {#error-codes}

| 코드 | 의미 |
| --- | --- |
| 400 | 잘못된 요청·본문 |
| 401 | 가상 키 또는 관리 토큰 누락·무효 |
| 402 | 영속·공유 승인에서 금전 권한 소진 |
| 403 | 접근·개인정보·리전·정책 거부, 일부 기존 거버넌스 거부 |
| 404 | 모델 해석·설정된 폴백 후에도 적격 경로 없음 |
| 429 | 적용되는 요청률·토큰 할당량 소진 |
| 503 | 필수 정책·신원 준비 상태, 권한·공유 저장소 사용 불가 |
| 5xx | 업스트림 오류 또는 게이트웨이 실패 |

오류는 수신 프로토콜에 맞는 Anthropic·OpenAI 오류 형식으로 반환합니다.

## CLI {#cli}

```
mayu serve  --config <path>
mayu keys   create --team <t> --models <csv> --store <path>
mayu keys   list   --store <path>
mayu keys   revoke --id <key_id> --store <path>
mayu keys   create --team <t> --models <csv> --config <path>  # select the configured backend
mayu keys   list --config <path>
mayu keys   revoke --id <key_id> --config <path>
mayu keys   import --config <shared-config> --sqlite <old-db>
mayu audit  verify --file <path>
mayu report --file <path> --by team,model
mayu pricing check --config <path>                                  # ADR-030, CI guard: exit 1 if any route has no rate
mayu login  --gateway <url> [--team <t>] [--id-token-command <cmd>]  # ADR-028
mayu token  [--export] [--raw]                                      # ADR-028, meant to run as apiKeyHelper
mayu logout                                                         # ADR-028
```

## 정책 기반 요청 라우팅 · ADR-043 {#policy-aware-request-routing-adr-043}

Messages, Chat Completions, 네이티브 Bedrock 생성은 같은 개인정보 제한 시도 체인을 사용하고 보안 거부는 프로토콜 형식의 403입니다. Anthropic·Bedrock 계산은 라우팅 거부, 준비되지 않거나 오래된 정책, 과대·읽기 불가 본문에서 업스트림 호출 없이 로컬 200 추정치를 반환합니다. Responses는 별도 ADR-044에서 추가했습니다.

`x-inferplane-routing-reason`은 제한된 사유, `x-inferplane-routed-model`은 적용된 개인정보·컨텍스트 선택입니다. Shadow에서도 개인정보를 집행합니다. 앞선 예산의 `x-inferplane-substituted-model`과 최종 모델은 다를 수 있습니다. 감사 `request.routing`은 요청(해석된 티어 전 모델), 선택, 제안, 실제 시도와 공급자·경계를 구분합니다. 활성화 전에 [스키마·업그레이드·한계](policy-routing.md)를 확인하세요.

## Responses와 적응형 라우팅 · ADR-044 {#responses-and-adaptive-routing-adr-044}

도구 없는 Codex 압축 요청은 `parallel_tool_calls: false`를 보낼 수 있습니다. 무상태 브리지는 `tools`가 없거나 null 또는 빈 배열인 경우에만 이 중립 설정을 허용합니다. 도구가 하나라도 선언되면 직렬 실행 요구는 계속 미지원으로 거절합니다. 원격 대화 상태나 암호화된 압축 항목을 지원하도록 확장하는 변경은 아닙니다. [클라이언트 호환성 측정](performance/clients.md)을 참고하세요.

`POST /v1/responses`는 인증된 HTTP Responses를 받습니다. 네이티브 openai_responses는 전송 형식을 유지하고 지원되는 무상태 텍스트·도구는 정규 어댑터를 사용합니다. Responses 이벤트 수명주기로 스트리밍하며 중단된 스트림도 관측 사용량을 정산합니다. 미지원 교차 프로토콜 상태는 외부 호출 전에 거부합니다. 본문·준비 상태·RBAC·리전·개인정보·엄격 예산·총승인 조건을 공유합니다.

추가 필드는 `sensitiveData.onDetected: Mask`, 컨텍스트의 `normalModel`, `maxNormalInputTokens`, `stability`, 예산 티어의 `enforceTargets`입니다. [적응형 설정과 한계](adaptive-routing.md)를 참고하세요.
