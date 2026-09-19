---
translation_source: api-reference.md
translation_source_sha256: 64b7068a985a79efec68a739101f7641d09ccb6d9d95cf9a56f0032a7ec5265f
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
| `/v1/messages/count_tokens` | 토큰 계산. **항상 200**. non-200은 Claude Code를 중단시킬 수 있음 |
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
POST /model/{modelId}/count-tokens
```

AWS SigV4 클라이언트 신원이 아니라 가상 키(`x-api-key`·Bearer)로 인증합니다. 설정된 모델 ID를 URL 인코딩하세요. 생성은 공통 승인·라우팅 제어를 사용하고 스트림은 Bedrock 응답 형식입니다. 인증·준비 상태·라우팅·저장소 때문에 생성을 거부하는 경우에도 계산은 로컬 HTTP 200을 유지합니다. 공급자 자격 증명과 AWS 서명은 외부 호출 때 별도로 적용합니다.

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

`POST /v1/responses`는 인증된 HTTP Responses를 받습니다. 네이티브 openai_responses는 전송 형식을 유지하고 지원되는 무상태 텍스트·도구는 정규 어댑터를 사용합니다. Responses 이벤트 수명주기로 스트리밍하며 중단된 스트림도 관측 사용량을 정산합니다. 미지원 교차 프로토콜 상태는 외부 호출 전에 거부합니다. 본문·준비 상태·RBAC·리전·개인정보·엄격 예산·총승인 조건을 공유합니다.

추가 필드는 `sensitiveData.onDetected: Mask`, 컨텍스트의 `normalModel`, `maxNormalInputTokens`, `stability`, 예산 티어의 `enforceTargets`입니다. [적응형 설정과 한계](adaptive-routing.md)를 참고하세요.
