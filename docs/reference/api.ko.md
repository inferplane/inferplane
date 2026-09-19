---
translation_source: reference/api.md
translation_source_sha256: 369072923bf98c381a62bd337dfc6d0e71f5d17cd1dd42049db5d262e8ba809f
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
| Bedrock | `internal/server/bedrockapi/` | /model/{modelId}/invoke·invoke-with-response-stream의 AWS eventstream, count-tokens의 200·inputTokens. Claude Code의 Bedrock 모드·기본 URL·인증 생략 설정에서 bearer를 공통 KeyAuth로 해석. AWS 형식 message·X-Amzn-ErrorType 오류, 지원 Bedrock 대상 |
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
| CP 중개 | `controlplane/broker.go` | POST /v1alpha1/credentials, dataplane·provider=bedrock 요청 1 MiB. 임시 키·토큰·만료와 no-store 응답. ARN 있을 때만 등록. 전용 상수 시간 토큰, JWT는 미검증 403. 3600초 STS, 정제한 최대 64자 ID를 세션명·SourceIdentity·태그에 사용. SourceIdentity 거부 시 태그로 1회 재시도. 빈 ID·다른 공급자 400, STS·불완전 결과는 고정 502 |
| CP 정책 | `controlplane/policies.go` | GET /v1alpha1/policies: 하트비트·콘솔 OIDC, writable 포함. DSN 없으면 쓰기 405. PUT·DELETE는 전용 POLICY_WRITE_TOKEN 또는 콘솔 OIDC, 하트비트 쓰기는 403. 쓰기 토큰 없으면 모든 정적 bearer 쓰기 거부. actor/op/name/content-sha256 로그 |
| CP 내보내기 | `controlplane/export.go` | GET /v1alpha1/config/export, 비밀 없는 다중 GovernancePolicy YAML·로더 왕복. 가져오기 기능과 구분 |
| CP 콘솔 | `internal/controlplane/ui/` | /ui/ 사용량 읽기 콘솔, 데이터 없는 셸, 토큰 메모리, self CSP·degraded 배너. 로그인 origin 환경 변수로 Code+PKCE |
| CP 인증 설정 | `controlplane/authconfig.go` | GET /ui/auth/config의 비밀 없는 sso·issuer·client_id. issuer·client_id 있을 때만 등록, 아니면 404 |
| CLI 로그인 | `internal/server/authapi/` | 데이터 영역의 선택적 config·발급·자기 폐기. 콘솔과 별도 OIDC client_id·검증기, 만료·owner는 서버 결정, 주체별 발급 제한. 폐기는 일반 KeyAuth |

### 3. 주요 결정 {#3-key-decisions}

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

Responses는 Bedrock Converse·InvokeModel·Mantle, Anthropic·Chat 어댑터의 이식 가능한 텍스트·도구 프로파일을 받습니다. none effort는 백엔드 추론 제어 요구가 아닙니다. strict 도구와 불투명·공급자 소유 상태는 해당 네이티브 요구를 유지합니다.

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
