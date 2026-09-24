---
translation_source: getting-started/clients.md
translation_source_sha256: 2142c1b5221a300fbb528a7e6029f3ed45386a594f421d5d76fcc5395e1d1756
---

# 클라이언트와 공급자 연결 {#connect-clients-and-providers}

클라이언트는 가상 키로 `mayu`에 인증합니다. 공급자는 별도의 서버 자격 증명을 사용합니다. 프로토콜이 호환된다고 모든 모델이 도구, 추론, 미디어, 세션 상태를 지원하는 것은 아닙니다.

| 클라이언트·연동 | 수신 프로토콜 | 설정 |
| --- | --- | --- |
| Claude Code | Anthropic Messages | [빠른 시작](quickstart.md)에 따라 게이트웨이 기본 URL과 가상 키 사용 |
| Codex | Responses | [모델별 실행 도구](../codex-launcher.md)와 명시된 호환성 한계 확인 |
| OpenCode·Chat Completions 클라이언트 | OpenAI 호환 Chat Completions | 호환 공급자 URL을 게이트웨이 `/v1`로 설정하고 가상 키와 설정된 공개 모델 선택 |
| Bedrock 호환 도구 | Bedrock InvokeModel 경로 | 변경 전 [HTTP 레퍼런스](../api-reference.md)의 요청·인증 형식 확인 |

설치된 클라이언트 버전과 선택한 백엔드의 조합으로 검증해야 합니다. Codex에는 로컬 픽스처와 설치된 클라이언트 테스트가 있지만 모든 실제 공급자의 호환성을 증명하지는 않습니다.

## 공급자 선택 {#provider-selection}

| 공급자 유형 | 서버 설정 | 주의할 경계 |
| --- | --- | --- |
| `anthropic` | `api_key_ref`와 Anthropic 기본 URL | 활성화한 변환이 없으면 동일 프로토콜 전달 시 요청 원문 바이트 유지 |
| `bedrock` | 리전과 AWS 인증 모드 | 경로별 모델 API·가드레일·리전 지원 차이 |
| `bedrock_responses` | 네이티브 Bedrock Responses 설정과 AWS 자격 증명 | 네이티브 기능과 모델 가용성 별도 검증 |
| `openai_compatible` | 호환 기본 URL과 선택적 키 참조 | 배포 모델에 맞는 기능·가격 선언 필요 |
| `openai_responses` | 네이티브 Responses 엔드포인트와 키 참조 | 네이티브 기능을 명시하고 검증해야 함 |

[Anthropic·Bedrock](../../examples/config.json), [자체 호스팅](../../examples/config.selfhosted.json), [적응형 라우팅](../../examples/config.adaptive-routing.json) 예제를 참고하세요. 예시 공급자·모델·가격 선언은 실제 제공을 보장하는 상품 목록이 아닙니다.

## 연동 검증 {#validate-the-integration}

1. 클라이언트 키로 `GET /v1/models`를 호출해 공개 모델이 보이는지 확인합니다.
2. 비스트리밍 요청을 보내 사용량과 회계를 확인합니다.
3. 스트리밍 요청과 취소를 시험합니다.
4. 도구를 사용하는 클라이언트이면 도구 호출 왕복을 시험합니다.
5. 금지 모델이 거부되고, 폴백이 접근·개인정보·예산 제약을 유지하는지 확인합니다.

## Claude Code MCP 도구 검색 {#claude-code-mcp-tool-search}

Claude Code는 `ANTHROPIC_BASE_URL`이 Anthropic이 아닌 호스트이면 MCP 도구 검색(지연 로딩)을
끄고 모든 MCP 도구 정의를 처음부터 보냅니다. `mayu`를 통해 쓰려면 클라이언트 환경에
`ENABLE_TOOL_SEARCH=true`를 설정하세요. Anthropic과 Bedrock `invoke_model` 경로는
`defer_loading`, 검색 도구, `tool_reference` 이력을 그대로 전달하고 개인정보 검사도 이
블록을 인식합니다. Bedrock Converse는 이를 표현할 수 없어 검색 도구를 버리므로 지연 도구가
처음부터 로드됩니다. 2026-09-24 Bedrock InvokeModel(Claude Haiku 4.5)에서 서버 측 검색,
클라이언트 측 `tool_reference` 결과, 스트리밍을 `sensitiveData` 정책 유무별로 검증했습니다.
모든 MCP 서버·모델에 대한 검증은 아닙니다.

Responses의 네이티브 모드와 무상태 브리지를 구분하세요. 불투명한 네이티브 이력과 호스팅·네이티브 전용 도구를 임의의 Chat Completions 백엔드로 옮길 수는 없습니다. [실행 도구 계약](../codex-launcher.md)에서 요구하면 새 세션을 시작하세요.

가격을 포함한 Kimi K3·Fable 5.1 예제와 날짜별 검증 범위는
[Bedrock 코딩 모델](../reference/bedrock-coding-models.md)을 참고하세요.
