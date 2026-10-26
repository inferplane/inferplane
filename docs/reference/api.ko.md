---
translation_source: reference/api.md
translation_source_sha256: 2b625b481776d381694e4070c40e857bfed7ac0c122463e2ce3a9180f7680d90
---

# API 구현 {#api}

### 1. 개요 {#1-overview}

Messages·Chat Completions·Responses·Bedrock 수신과 상태·메트릭·키 관리용 관리 영역의 HTTP 구현입니다. 전체 계약은 [API 레퍼런스](../api-reference.md)를 참고하세요.

### 2. 구성 요소 {#2-components}

| 구성 | 경로 | 책임 |
| --- | --- | --- |
| 데이터·관리 mux | `internal/server/server.go` | 인증·집행·메트릭 조립 |
| Anthropic | `internal/server/anthropicapi/` | messages·count_tokens·models. 알 수 없는 모델 404·금지 모델 403에 키의 허용 모델 목록 표시 |
| OpenAI | `internal/server/openaiapi/` | chat/completions·models, 같은 허용 목록 오류 안내 |
| Responses | `internal/server/responsesapi/` | POST /v1/responses, 네이티브와 지원 무상태 텍스트·도구 어댑터, 공통 승인·라우팅 |
| Bedrock | `internal/server/bedrockapi/` | /model/{modelId}/invoke·invoke-with-response-stream의 AWS eventstream, count-tokens는 정책·저장소 거부 시 로컬 HTTP 200 추정치와 inputTokens(잘못된 자격 증명은 401 가능). Claude Code의 Bedrock 모드·기본 URL·인증 생략 설정에서 bearer를 공통 KeyAuth로 해석. AWS 형식 message·X-Amzn-ErrorType 오류, 지원 Bedrock 대상 |
| 네이티브 Converse | `internal/server/bedrockapi/`, `providers/bedrock/native_converse.go` | `/model/{modelId}/converse`, `/converse-stream`. 원본 JSON·AWS 이벤트, 공통 승인·감사, 적격 Runtime 대상만 지원(ADR-052) |
| 정책 적용 비교 | `internal/server/evaluation.go` | mayu의 `POST /admin/evaluations`. 전체 관리자와 별도 가상 키, 비스트리밍 데이터 핸들러 실행, 응답 결과·정수 비용·불확실성 근거(ADR-052) |
| 사용량 | `internal/server/usageapi/usage.go` | GET /v1/usage, 본인 키·팀의 유효 예산·할당량, 정수 microUSD, 무제한은 null. team_budget_day·key_budget_day는 calendar-day, 기존 월 필드는 calendar-month 유지. key_id 표시 없음 |
| 관리 키 | `internal/server/adminapi/keys.go` | 팀 권한과 감사가 적용되는 발급·조회·폐기 |
| 팀·사용자 | `internal/server/adminapi/teams.go` | GET /admin/teams는 관리 인증, PUT·DELETE /admin/teams/{name}은 전체 관리자. SetTeamLookup으로 다음 요청부터 집행. GET /admin/users는 owner의 읽기 전용 파생 뷰이며 사용자 테이블·사용자 지출 기능과 구분 |
| 본인 조회 | `internal/server/adminapi/whoami.go` | subject·teams·is_admin·auth_method의 비밀 없는 표현 |
| 키 콘솔 | `internal/server/adminui/` | /admin/ui/는 내장·비인증·데이터 없는 셸, 데이터는 인증 API. login_origins가 있으면 브라우저 Code+PKCE SSO |
| 인증 설정 | server.go의 AuthConfigView | GET /admin/auth/config의 비밀 없는 sso·issuer·client_id. OIDC 없으면 404, login_origins 없으면 sso=false |
| 설정 조회·쓰기 | `internal/server/configapi/` | GET /admin/config, PUT·DELETE /admin/providers/{name}·models/{name}. provider_store 없으면 405, 쓰기는 전체 관리자. base_url은 절대 http(s), aliases는 요청 내 중복·전체 모델 충돌 검사. export는 비밀 없는 Git용 설정 |
| 연결 시험 | `configapi/probe.go` | POST /admin/providers/test, 참조만 있는 초안의 HealthChecker. 전체 관리자, 메타데이터 주소 차단·선택적 allowed_hosts SSRF 보호. 클라이언트 메모리에만 상태, provider_store 없으면 405. 정제한 ok·latency_ms·detail 반환 |
| 모델 목록 | `configapi/catalog.go` | GET /admin/providers/catalog?type, 내장 자동완성 참고 목록. 모르는 유형은 빈 값이며 저장을 막지 않음 |
| 공급자 저장소 | `internal/providerstore/` | 선택적 DB 기준 토폴로지. 비밀 열 없음, 영속 시드, 이식 가능한 DDL. aliases는 타깃별이 아닌 model_aliases에 모델별 저장 |
| 감사 검증 | `internal/server/auditapi/` | GET /admin/audit/verify, 저장 대상별 완전 접두부 체인 검사, 16 MiB 상한 |
| 예산 알림 | `adminapi/alerts.go` | 전체 관리자 GET /admin/alerts/recent, 인스턴스별 최근 50회 webhook 링 |
| 공급자 상태 | `configapi/health.go` | 전체 관리자 GET /admin/providers/health, 주기적 HealthStore. health check 미설정 시 nil, 수동 시험과 별개 |
| 로그·본문 | `analyticsapi/logs.go`, `adminapi/bodies.go` | 전체 관리자 GET /admin/logs의 id-keyset 페이지·body_ref. GET·DELETE /admin/bodies/{ref}. 열람은 사용자별 5분 중복 제거 body_accessed, 삭제는 body_deleted, body_ref 대신 record_ref 감사. 삭제·정리·복호화 불가는 500 대신 410 tombstone |
| 메트릭 | `internal/server/metricsapi.go` | 비인증 Prometheus /metrics |
| OpenAI 변환 | `internal/openai/convert.go` | 정규 요청·응답·청크 상호 변환 |
| CP 사용량 | `internal/controlplane/usage.go` | POST /v1alpha1/usage: 검증 오류 400, 과대 413, 저장소 장애 503, 저장 후 ack·FIFO 재시도. GET은 team/user/model·group_by·정수 비용, 메모리 대체 시 degraded. export는 since/until 필수 CSV·JSON 스트리밍, 쓰기별 기한으로 30초 WriteTimeout 대응 |
| CP 차트 | `internal/controlplane/usage_charts.go` | 인증된 GET /v1alpha1/usage/charts (ADR-051). since·배타적 until 필수, 양수 ≤366일. 정수 total_micro_usd, UTC days[{day,spent_micro_usd}], 상위 8 models[{key,spent_micro_usd}], other_micro_usd·model_count. 보관된 텔레메트리만 집계하며 사용자·팀 ID나 승인 크레딧은 없음. 잘못된 기간 400, 커서·저장소 실패 503이며 부분 차트 성공으로 바꾸지 않음. 성공 no-store |
| CP 중개 | `controlplane/broker.go` | POST /v1alpha1/credentials, dataplane·provider=bedrock 요청 1 MiB. 임시 키·토큰·만료와 no-store 응답. ARN 있을 때만 등록. 전용 상수 시간 토큰, JWT는 미검증 403. 3600초 STS, 정제한 최대 64자 ID를 세션명·SourceIdentity·태그에 사용. SourceIdentity 거부 시 태그로 1회 재시도. 빈 ID·다른 공급자 400, STS·불완전 결과는 고정 502 |
| CP 정책 | `controlplane/policies.go` | GET /v1alpha1/policies: 하트비트·콘솔 OIDC, writable 포함. DSN 없으면 쓰기 405. PUT·DELETE는 전용 POLICY_WRITE_TOKEN 또는 콘솔 OIDC, 하트비트 쓰기는 403. 쓰기 토큰 없으면 모든 정적 bearer 쓰기 거부. actor/op/name/content-sha256 로그 |
| CP 계정 연결 | `internal/controlplane/policies.go` | 같은 인증된 정책 목록에 선언의 `identity_bindings: [{reported_ref, account_ref, kind}]`를 선택적으로 추가(ADR-050). 바인딩이 없으면 생략하며 issuer·subject·IdP 그룹 원문은 반환하지 않음. `Cache-Control: no-store`. 정확한 재무 선택자를 위한 승인된 선언 메타데이터이며 사용자 레지스트리·신원 증명·회계 계정 변경이 아님 |
| CP 내보내기/가져오기 | `controlplane/export.go`, `import.go` | GET /v1alpha1/config/export(비밀 없는 다중 GovernancePolicy YAML, 첨부 파일로 다운로드), POST /v1alpha1/config/import(Replace 방식 복원). dry_run=true로 문서별 diff와 result_generation을 미리 확인하고, 적용 시 expected_generation이 필수(불일치 시 409). 빈 번들은 allow_empty=true가 필요합니다. 번들 전체를 검증한 뒤 트랜잭션 한 번으로 반영하고, authnWrite 권한이 필요합니다(heartbeat 토큰은 403). store가 없으면 405입니다. YAML 전송 한도는 64 MiB이며, 두 sync 모드와 요청에 따른 authority 항목을 포함한 최악의 sync 응답 크기를 구성 요소별로 측정하고 한도 초과 시 즉시 중단합니다. 데이터 플레인 디코딩 한도인 8 MiB 이내여야 합니다(초과 시 400). 요청에 따른 크기 여유는 실제 데이터 플레인 클라이언트가 로컬 nonce로 생성하는 ASCII-safe/hex authority 요청 식별자를 전제로 합니다. JSON 이스케이프가 많이 필요한 식별자는 이를 보낸 데이터 플레인 자신의 디코딩 한도만 초과할 수 있습니다. pgstore 검증에서 authority 요청 식별자 문자 집합을 제한하는 작업은 후속 과제로 기록되어 있습니다. 정책만 다루며 원장·사용량·identity 설정은 대상이 아닙니다 |
| CP 콘솔 | `internal/controlplane/ui/` | /ui/ [운영 대시보드](../runbooks/inferplaned-dashboard.md): 개요·정밀 사용량과 내보내기·최근 데이터 플레인 generation과 거절·검토 후 정책 CRUD(기존 서버 쓰기 권한). 데이터 없는 공개 셸, 분리된 읽기·쓰기 자격 증명은 JS 메모리만, self CSP와 API 불가·degraded 표시. 로그인 origin 환경 변수로 Code+PKCE |
| CP 인증 설정 | `controlplane/authconfig.go` | GET /ui/auth/config의 비밀 없는 sso·issuer·client_id. issuer·client_id 있을 때만 등록, 아니면 404 |
| CLI 로그인 | `internal/server/authapi/` | 데이터 영역의 선택적 config·발급·자기 폐기. 콘솔과 별도 OIDC client_id·검증기, 만료·owner는 서버 결정, 주체별 발급 제한. 폐기는 일반 KeyAuth |

### 3. 주요 결정 {#3-key-decisions}

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

count_tokens는 별칭도 정규화하며 항상 200입니다. 같은 프로토콜이면 원문 전달, 다르면 정규 변환을 적용합니다. 별칭은 RBAC·라우팅·감사·메트릭 전에 정규 이름으로 바꿉니다. Anthropic 원문 경로는 최상위 model만 바꾸고 중첩 cache_control과 HTML 이스케이프 상태를 보존합니다. 오류는 수신 형식이며 허용된 가용 모델 목록을 표시합니다.

**모델 폴백:** 한 단계 model_fallbacks와 기본 동일 계열 휴리스틱을 허용 검사 전에 적용합니다. 미설정 이름만 허용된 키는 다른 모델을 대신 받지 않고 거부됩니다. 이미 설정된 모델도 업스트림 404·Bedrock ValidationException이면 기존 폴백 루프에서 교차 모델을 시도하되, 나중 추가한 타깃을 FilterModelAllowed로 다시 검사합니다. 응답 x-inferplane-model-fallback은 공급자 폴백 헤더와 독립적입니다.

**기존 선택적 예산 티어:** routing의 budgetTiers는 affinity·context와 별개이고 budgetRef는 같은 문서의 숫자 한도를 가진 예산입니다. 기존 ADR-034는 CP 리스 원장으로 전역 사용률을 판단하고 윈도 안에서 단조롭게 티어를 유지합니다. 이미 라우팅된 모델에 SubstituteTier를 적용하며 원래 모델과 목적지 RBAC·경로가 모두 유효할 때만 치환합니다. 대안을 못 쓰면 원래 모델을 유지합니다. x-inferplane-substituted-model과 감사 model_substituted_from을 남깁니다. 정책 적용 단계에서도 SetRoutedAndPriced로 목적지 경로·가격을 요구하며 없으면 문서 전체를 거부합니다. 엄격 티어의 별도 거부 의미는 ADR-044를 따릅니다.

**예산 윈도:** period는 CalendarDay·CalendarMonth이고 생략하면 기존 의미인 CalendarMonth입니다. unlimited=true와 결합할 수 없습니다. 일·월 한도는 별도 규칙이며 각각 hardCap·failurePolicy·lease·adminContact를 가집니다.

### 4. 코드 위치 {#4-code-pointers}

- `internal/server/anthropicapi/messages.go`: Messages, 스트리밍 전달, 제한된 라벨.
- `internal/server/openaiapi/chat.go`: Chat Completions.
- `internal/server/auth.go`: 가상 키 해석.

### 5. 관련 문서 {#5-cross-references}

router·tier·governance·alert·bodystore·providers와 ADR-016/017/018/021/028/029/040/041/042를 참고하세요. [운영 절차](../runbooks/)와 [CLI 로그인](../runbooks/cli-login.md)이 실무 흐름을 설명합니다.

### 정책 라우팅 스키마와 증거 · ADR-043 {#policy-routing-schema-and-evidence-adr-043}

sensitiveData는 onDetected·onUninspectable(InternalOnly·Block), 내부 전용일 때 명시적 internalModels와 FailClosed가 필요합니다. 와일드카드는 금지입니다. context는 FailOpen, fromModels, simpleModel, complexModel, 양수 maxSimpleInputTokens, 선택적 비공백 complexKeywords, 기본 Shadow·명시적 Enforce이며 다른 routing 종류와 함께 둘 수 없습니다. 일치하는 개인정보 규칙은 교집합·Block 우선입니다. [전체 계약](../policy-routing.md#policy-fields)을 참고하세요.

공급자 DTO의 data_boundary는 internal·external·unknown, 생략은 unknown입니다. 모델 DTO는 음이 아닌 컨텍스트(0=불명)와 tools·vision·reasoning·structured_output을 받습니다. 조회·쓰기·내보내기가 보존하고 모르는 enum은 거부합니다.

모든 생성은 공통 안전 체인과 수신 형식 403을 사용합니다. 계산은 개인정보·준비 상태·오래된 정책·본문 크기 실패에서 업스트림 호출 없이 로컬 200입니다. 라우팅 사유·선택 모델과 앞선 예산 치환 헤더를 구분하고 감사 requested·selected·proposed·actual의 의미도 구분합니다. Responses는 ADR-044에서 추가했습니다. 새 규칙 전 바이너리·CRD 업그레이드, 최초 CP 보호에는 require_sync가 필요합니다.

ADR-044는 Mask, normalModel·maxNormalInputTokens·stability, enforceTargets를 추가합니다. 엄격 티어는 100%를 지원하며 참조 소프트 예산은 두 번째 승인 리스가 아닌 계량 임계값입니다. 세션 힌트가 권한을 부여하지 않으며 매 고정·시도를 재검사합니다. [필드·Codex 설정](../adaptive-routing.md)을 참고하세요.

Responses는 Bedrock Converse·InvokeModel·Mantle, Anthropic·Chat 어댑터의 이식 가능한 텍스트·도구 프로파일을 받습니다. none effort는 백엔드 추론 제어 요구가 아닙니다. strict 도구와 불투명 추론 항목은 해당 네이티브 요구를 유지합니다. 저장 상태 참조(`previous_response_id`, `conversation`, `item_reference` 입력 항목)는 모든 대상에서 라우팅 전에 400 `stored_reference_unsupported`로 거부합니다. upstream은 gateway의 공유 자격 증명으로 이를 해석하고 gateway에는 사용자별 소유권 근거가 없기 때문입니다. client는 전체 입력을 다시 보냅니다.

| 모델 조회 메타데이터 | 의미 |
| --- | --- |
| capabilities | 설정된 기능, 도구가 검증된 경로의 tools 포함 |
| responses_mode | 전체 해석된 체인을 보수적으로 판단한 native·bridge·unsupported |
| codex_model | 공개 이름에서 유도한 네이티브 클라이언트 카탈로그 바인딩, 비공개 배포 ID 아님 |
| context_window·max_model_len | 선언된 상한, 미선언 시 생략 |

인증된 키 권한으로 조회 결과를 제한하고 추론 시에도 기능·개인정보·예산·호환성을 재검사합니다.

### 영속 예산 하트비트 · ADR-045 {#durable-budget-heartbeat-adr-045}

DURABLE_BUDGETS=true이면 POST /v1alpha1/sync가 authority.protocol=escrow-v1을 협상합니다. 비-JWT 머신 bearer만 허용하고 콘솔 OIDC는 권한을 발급할 수 없습니다. 전체 정책, DB 소유 윈도, 권한, 보고·계량 ack를 반환합니다. 기존 요청은 409이고 영속 클라이언트는 구형·무효 응답을 거부합니다. ActiveTier의 팀·사용자 범위를 유지하며 readyz는 실제 DB를 검사합니다. [전송 계약](../../internal/authority/pgstore/README.md)과 [설정](../durable-budgets.md)을 참고하세요.

### 공유 게이트웨이 · ADR-046 {#shared-gateway-profile-adr-046}

key_store.type과 governance_store.type을 postgres로 설정하면 키 공유·원자적 자원 승인을 활성화합니다. 하트비트의 권한 네임스페이스를 검증하고 로컬 권한은 요청하지 않습니다. 사용자 요청률·일/월 tokenQuota는 이 프로파일이 필요합니다. 사용량은 enforcement_mode=shared와 주체별 shared_limits를 반환합니다. 키 CLI는 --config를 받고 가져오기는 원래 신원·폐기 이력을 보존합니다. [공유 거버넌스](../shared-governance.md)를 참고하세요.

## Responses 업스트림 오류 코드

네이티브 Responses HTTP 실패는 업스트림 상태와 정확히 일치하는 허용 코드
`context_length_exceeded`, `invalid_encrypted_content`만 보존합니다.
`error.code` 및 기존 감사 `outcome.error`에 기록하고 원문 메시지·param·알 수 없는
코드는 숨깁니다. 오류 해석은 64 KiB로 제한하며 복구 코드만을 이유로 재시도하지
않습니다. SSE 전송 시작 전과 비스트리밍 HTTP 실패에 적용하며 이미 전송 중인
스트림의 중단 처리는 바뀌지 않습니다.

코드가 포함된 HTTP 실패에서 두 허용 복구 코드의 `error.type`은
`invalid_request_error`입니다. ADR-053은 SSE 헤더 전송 전 발생한 게이트웨이
스트림 실패에 `upstream_timeout`, `stream_incomplete`, `response_incompatible`,
`usage_missing` 같은 제한된 코드를 추가합니다. 기존 게이트웨이 오류의
`error.type="upstream_error"`는 유지하며, 이전에는 같은 실패 경로에서
`error.code`를 생략했습니다. 클라이언트는 게이트웨이 실패를 잘못된 요청으로
취급하지 않으면서 새 코드로 더 구체적인 원인을 표시할 수 있습니다.
실패 코드만을 이유로 요청을 다시 실행하지 않습니다.

Responses의 컨텍스트 사전 검사 거부도 HTTP 400과 `error.code="context_length_exceeded"`를 반환합니다. 기존 검사 기준과 인증 순서는 유지합니다.

## 분석 근거 (ADR-053) {#analytics-evidence-adr-053}

`internal/server/analyticsapi/`는 full-admin summary·timeseries·logs·health와
기존 Mode-B rebuild 경로를 제공합니다. `internal/analytics/evidence.go`는
SQLite와 PostgreSQL 투영이 공유하는 분류기입니다. Summary `Totals`, 팀/모델 행,
일별 지점에 `operations`를 추가하고, 이벤트에는 `has_cost`, `outcome_state`,
`cost_state`, `partial`, 제한된 `error_code`, `projection_version`을 추가합니다.
경로·메서드·권한 등급은 바뀌지 않습니다.

`operations.completed`는 Cost=nil도 포함한 관측 `request_completed` 기록 수입니다.
기존 requests·토큰·금액은 Cost가 있는 기록만 포함합니다. 결과 네 가지와 비용 상태
다섯 가지는 각 분류 안에서 배타적이며 전송 성공과 비용 근거는 독립적입니다.
API의 정수 산정액은 청구서·작업 품질 지표·승인 크레딧이 아닙니다.

[필드 계약과 추가 예제](../api-reference.md#analytics-outcome-and-cost-evidence-adr-053),
[운영 분모](../operations/observability.md#read-outcome-and-cost-evidence),
[원본 복구](../operations/recovery.md#recover-analytics-evidence)를 확인하세요.
Mode-B 마이그레이션·backfill은 트랜잭션으로 수행하지만 기존 checkpoint 이후
건너뛴 원본을 재생하지는 않습니다. 과거 투영 건수 0도 완전한 이력을 증명하지
않습니다. 어느 프로파일에서도 분석 변경은 금전 권한을 변경하지 않습니다.
