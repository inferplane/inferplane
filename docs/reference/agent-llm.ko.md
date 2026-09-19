---
translation_source: reference/agent-llm.md
translation_source_sha256: e52485b4ae2e179da810ff5298cde6d168d16434fd513c7031f37401addbcae2
---

# 에이전트·LLM 구현 {#agent-llm}

### 1. 개요 {#1-overview}

Anthropic·Bedrock·OpenAI 호환 업스트림과 통신하는 공급자 추상화, 그리고 thinking·cache_control을 보존하며 클라이언트·업스트림을 변환하는 정규 스키마입니다.

### 2. 구성 요소 {#2-components}

| 구성 | 경로 | 역할 |
| --- | --- | --- |
| 공급자 인터페이스 | `providers/provider.go` | Name·Models·Complete·Stream, 선택적 TokenCounter |
| 레지스트리 | `providers/registry.go` | 유형 문자열별 Register·New |
| Anthropic | `providers/anthropic/` | Messages 원문과 바이트 단위 SSE |
| Bedrock | `providers/bedrock/` | Claude InvokeModel·그 외 Converse, SDK 격리 |
| OpenAI 호환 | `providers/openaicompat/` | vLLM·Ollama, 순서 보존 model 변경 |
| 네이티브 Responses | `providers/openairesponses/` | 원문 전달, 제한된 SSE·사용량 검증 |
| Responses 변환·수신 | `internal/responses/`, `internal/server/responsesapi/` | 원본 검사와 Codex 무상태 도구 어댑터 |
| 적응형 라우팅 | `internal/router/`, `internal/sensitivity/` | 3등급, 실제 성공 목적지 로컬 고정, 비용·개인정보 교집합, 완전 마스킹 |
| 가짜 공급자 | `providers/testing/mockprovider/` | 결정적인 단위 테스트 |
| 정규 스키마 | `pkg/schema/` | Anthropic 상위 집합, Extra 보존, SSE 작성 |
| 필터 | `internal/filter/` | RequestFilter와 레지스트리 |
| 기존 PII 필터 | `plugins/piimask/` | 선택적 정규식·Luhn, 형식별 자리표시자, 단방향·금고 없음, 메시지 텍스트 마스킹 |
| 트레이싱 | `internal/tracing/` | 선택적 OTLP 트레이스, GenAI와 캐시·비용·부분 결과, W3C 전파·감사 trace_id. 기본 no-op |

### 3. 주요 결정 {#3-key-decisions}

공급자는 패키지 하나와 blank import로 추가하며 내부 핵심 변경을 하지 않습니다. Anthropic 상위 집합은 thinking과 cache_control을 보존합니다. Bedrock Claude는 최상위 model만 안전하게 바꾼 InvokeModel을 사용하고 이벤트 스트림을 Anthropic SSE로 다시 구성합니다.

기존 PII 필터는 선택적입니다. ADR-044 Mask는 지원되는 요청 전체를 유한하게 변환한 후 독립적으로 재검사합니다. 모든 수신이 RawBody·Parsed를 함께 갱신합니다. 마스킹은 캐시 네임스페이스를 바꾸지만 안정적인 마스킹 접두부는 캐시할 수 있으며 고정 비용 배수를 약속하지 않습니다.

Bedrock Guardrails는 모든 InvokeModel·스트림·Converse 호출의 데이터 영역에 적용합니다. 공급자 기본 가드레일에 팀별 대체를 지원하지만 팀이 기본값을 제거하는 선택은 없습니다. Mantle에는 가드레일 인수가 없어 유효 가드레일이 있는 요청을 400으로 거부합니다. 그렇지 않으면 적용되지 않은 가드레일을 감사에서 적용했다고 기록하는 우회가 됩니다. 해당 모델은 Converse·InvokeModel로 라우팅하세요.

파싱할 수 없는 Mantle 2xx는 502로 실패합니다. Parsed 없이 통과시키면 정산을 건너뛰어 비용 없는 요청처럼 기록될 수 있기 때문입니다. Bedrock 업스트림 오류는 UpstreamError로 실제 429·4xx·5xx를 보존합니다. 비스트리밍·스트림 최초 바이트 전에는 상태를 바꿀 수 있으나 출력 후 오류는 기존 부분 스트림 처리로 남깁니다.

신형 Bedrock 모델 중 thinking.go 허용 목록(Opus 4.7/4.8, Fable 5, Sonnet 5, Mythos)은 CLI의 기존 enabled/budget_tokens 형태를 거부합니다. 해당 모델에만 adaptive thinking과 output_config.effort로 바꾸며 나머지는 유지합니다. 영구적인 일반 변환이 아니라 CLI·모델 스키마 차이의 호환 처리입니다.

**Span은 정산된 수치를 기록합니다.** 표준 input/output_tokens만으로 캐시·비용을 표현할 수 없어 inferplane.usage.cache_read_input_tokens, cache_write_5m/1h_input_tokens, 정수 inferplane.cost.amount_usd_micros와 pricing_missing을 사용합니다. 0이면서 missing이면 가격 누락이고, missing이 아니면 명시적 무료나 정상적인 round-half-even 결과일 수 있습니다. 부분 스트림에는 response.partial과 Error 상태를 기록해 이미 HTTP 200인 잘린 응답을 정상 종료와 구분합니다. 비밀·key_id·원시 클라이언트 입력을 넣지 않고 라우팅 후 설정 범위 모델·공급자만 사용합니다.

OTLP는 트레이스에만 사용합니다. 메트릭은 관리 포트 Prometheus이고 사용량 윈도는 CP 자체 API입니다. [수집기 계약](infrastructure.md)을 참고하세요.

### 4. 경로별 프롬프트 캐시 보존 {#4-prompt-cache-preservation-by-path}

보존은 클라이언트의 캐시 분기점 바이트가 업스트림까지 유지된다는 뜻입니다. 보존 경로의 수정은 최상위만이며 system·messages·tools 바이트는 같습니다.

| 수신 → 외부 경로 | 표식 | 상태·방법 |
| --- | --- | --- |
| Anthropic → anthropic | cache_control | RawBody 보존, 최상위 model만 변경, HTML 이스케이프 끔 |
| Anthropic·Bedrock → Claude InvokeModel | cache_control | 최상위 model·stream 제거, anthropic_version·필수 beta 주입 |
| Anthropic·Bedrock → Mantle Messages | cache_control | toMantleAnthropicBody 최상위 변경으로 보존 |
| OpenAI → Mantle Messages | cache_control | Parsed에서 다시 렌더링해 표식 손실. OpenAI 원문을 Messages로 잘못 해석하지 않도록 원문 수정은 Anthropic 수신에만 적용 |
| Anthropic → Converse | cache_control→cachePoint | **손실**. system 평탄화와 텍스트·도구 블록만 변환, CachePoint 매핑 미구현 |
| Anthropic → Mantle Chat | cache_control | 정규 스키마에서 OpenAI로 다시 작성, 표식 없음 |
| Anthropic → openai_compatible | cache_control | 문서화된 최선의 노력 변환, 경고와 함께 무시 |
| OpenAI → openai_compatible | 업스트림 자동 캐시 | 최상위 model 값 외 원문 보존. 스트림 사용량을 요청하지 않았으면 순서 보존 include_usage 삽입. 추가 usage-only 프레임은 클라이언트 전달에서 제거 |
| 마스킹된 팀 | cache_control | 네임스페이스 변경. 기존 필터는 재직렬화하고 완전 정책 Mask는 결정적 바이트를 만들어 반복 마스킹 접두부도 캐시 가능. 실제 경로를 측정하며 고정 비용 배수 없음 |

표식이 사라지는 경로도 업스트림 자동 캐시를 위해 대화 접두부를 턴 사이 안정적으로 유지해야 합니다. Converse는 user/assistant 이외 역할의 CLI 훅 출력을 다음 사용자 메시지에 합쳐 위치를 유지합니다. 이를 system에 합쳤던 이전 동작은 매 턴 머리를 바꿔 전체 약 475k 토큰을 캐시 쓰기하는 현상이 관측되었습니다.

OpenAI 스트림은 Anthropic의 시작·끝·블록 닫힘이 없어 ReadChatSSE가 정규 소비자용 수명주기를 합성합니다. 첫 파싱 청크 전에 공개 모델명의 message_start, 열리지 않은 인덱스의 텍스트 시작, message_delta 전 열린 블록 종료, DONE의 message_stop을 만듭니다. Chunk만 만들고 Raw는 nil이어서 OpenAI 수신에는 가짜 줄을 전달하지 않습니다. finish_reason과 usage-only 메시지가 모두 없이 끝나면 DONE에서 end_turn message_delta 하나를 합성합니다. 어떤 메시지 수준 프레임이라도 있으면 중복 종료로 해석되지 않도록 합성을 억제합니다.

캐시 사용량이 있는 모든 경로는 티어별 정산입니다. Anthropic·Invoke는 message_start와 delta를 MergeUsage로 합치고, Converse는 CacheReadInputTokens·CacheDetails를 정규 분리에 매핑합니다. 5분·1시간 쓰기를 끝까지 별도 가격으로 처리합니다. GPT-5.6 계열의 Converse inputTokens는 2026-08-29~31 실측에서 캐시를 포함해 해당 허용 목록에만 읽기·쓰기를 빼고 0으로 제한합니다. 그렇지 않으면 컨텍스트·청구를 이중 계산합니다. 벤더 전체가 아닌 계열별 예외입니다. GPT-6 Astra는 2026-09-09 실측에서 input=2·cacheWrite=4008처럼 이미 분리해 보고하므로 목록에 넣으면 실제 입력을 0으로 만드는 오류가 됩니다.

### 5. 모델별 Converse·Mantle 추론 인수 처리 {#5-per-model-conversemantle-inference-param-strip-rules}

일부 Bedrock 모델은 CLI가 보내는 temperature·stop_sequences 등을 값과 무관하게 거부합니다. 허용 목록은 2026-08-28 등의 명시된 리전 실측을 근거로 하며 목록에 없는 모델은 모든 인수를 유지합니다. Anthropic은 InvokeModel로 가므로 이 목록에서 제외합니다.

Converse의 converseUnsupportedInference는 다음 필드를 제거합니다.

| 업스트림 부분 문자열 | 제거 |
| --- | --- |
| openai.gpt-5.6 · luna/sol/terra | temperature, topP, stopSequences. gpt-oss·Mantle 전용 5.4/5.5와 구분 |
| openai.gpt-6 · Astra | temperature, topP, stopSequences. 2026-09-09 서울 리전 실측 |
| xai. · grok-4.6 | temperature, topP, stopSequences |
| openai.gpt-oss | stopSequences |
| deepseek.v · v3.x | stopSequences. r1은 세 인수 허용 |
| google.gemma- | stopSequences |
| minimax. | stopSequences |
| moonshot · 두 네임스페이스 | stopSequences |
| qwen. | stopSequences |
| zai. | stopSequences |

converseMinMaxTokens는 제거가 아닌 **최소 출력 한도**입니다. 아래 모델은 maxTokens<16을 거부하는데 CLI가 모델 전환 때 max_tokens=1을 시험합니다. 16으로 올려 전환 실패를 피하며 출력 한도를 줄이지 않고 모델별 한 번 로그합니다. 2026-09-04 실측의 Mantle 5.4/5.5·GLM-5는 1을 받아 목록에 없습니다.

| 업스트림 | 최소 maxTokens |
| --- | --- |
| openai.gpt-5.6 | 16 |
| openai.gpt-6 | 16 |
| xai. | 16 |

Mantle Chat의 mantleChatStripParams는 OpenAI 필드명을 사용합니다.

| 업스트림 | 제거 |
| --- | --- |
| openai.gpt-5.6 | temperature, top_p, stop |

Astra는 Converse에서만 실측했으므로 Mantle 목록에는 의도적으로 없습니다. grok과 마찬가지로 검증하지 않은 경로의 인수를 유지합니다. Mantle Chat에서는 모든 해당 경로의 max_tokens를 max_completion_tokens로 바꾸고 스트림 include_usage를 설정합니다. GPT-5.6은 이전 이름을 거부하며 시험한 모델은 새 이름을 허용했습니다.

경로는 벤더 구간 기준으로 Anthropic은 /anthropic/v1/messages, OpenAI·xAI는 /openai/v1/chat/completions, 나머지는 /v1/chat/completions입니다. 모델별 예외 google.gemma-4-*는 2026-09-02 실측에서 openai 경로만 받았고 gemma-3-*는 기본 경로를 유지합니다.

### 6. 코드 위치 {#6-code-pointers}

- tracing.go: GenAI 요청·응답, 캐시·비용·부분 결과·상태.
- providers/provider.go: 인터페이스와 팀 가드레일을 전달하는 좁은 예외 필드.
- bedrock/invoke.go: 본문 구성·SSE 재작성.
- bedrock/client.go: Guardrail 및 스트림 설정.
- bedrock/errors.go: SDK 오류의 HTTP 분류와 Anthropic 오류 본문.
- bedrock/thinking.go: 허용 목록의 legacy→adaptive·effort.
- bedrock/converse.go: 변환, 제거 목록, usageWithCache.
- bedrock/mantle.go: 경로 구분, 캐시 안전 Messages 본문, Chat 인수.
- pkg/schema/usage.go: 스트리밍 병합·캐시 쓰기 TTL 구분.
- pkg/schema/extra.go: 미지 필드 보존·대소문자 충돌 거부.

### 7. 관련 문서 {#7-cross-references}

router의 해석·폴백, openai 변환, keystore의 팀 가드레일과 ADR-019·ADR-022, 운영 절차를 참고하세요.

### 정책 기반 라우팅 · ADR-043 {#policy-aware-routing-adr-043}

sensitivity는 Anthropic·OpenAI·디코딩된 Bedrock JSON 원본을 변경하지 않고 검사합니다. 중첩 도구 JSON·숫자 표기까지 유한한 이메일·전화·카드·SSN·IPv4·주민번호를 확인합니다. 불투명 미디어·삭제된 thinking·모르는 블록은 검사 불가입니다. 원격 분류기·보편적인 PII 보장은 없습니다. RouteRequest는 컨텍스트 선호·마스킹·승인·본문 수집 전에 모든 시도에 개인정보를 적용합니다.

기존 context는 Shadow 기본·FailOpen이고 Enforce는 이력·도구·미디어·추론·구조화 출력 없는 완전 검사 가능한 단일 사용자 턴만 적용합니다. 입력 임계값은 큰 요청 제외가 아니라 simple/complex 선택입니다. 적격 큰 요청도 다른 호환 complex 모델을 선택할 수 있습니다. Shadow도 개인정보는 집행합니다. 자동 대안은 선언된 컨텍스트·관측 기능·가격·RBAC·리전·호환 전송이 필요합니다. 기능은 tools/vision/reasoning/structured_output, 경계는 internal/external/unknown입니다. 운영자 메타데이터이며 실제 신뢰·변환기 지원의 증거가 아닙니다. OpenAI 직접 Anthropic 선택, 네이티브 Bedrock, Converse·교차 형식 손실의 기존 제한을 유지합니다. ADR-043 자체는 Responses·세션 고정을 추가하지 않았습니다.

requested는 해석된 티어 전 모델, 컨텍스트 source는 티어 후·개인정보 전 모델입니다. proposed는 관측값이며 수동 결과는 입력 사전 검사 모델을 유지하고 실제 시도는 별도 기록합니다. 같은 live.State에서 시도·컨텍스트·가격을 얻습니다. [라우팅 가이드](../policy-routing.md)를 참고하세요.
