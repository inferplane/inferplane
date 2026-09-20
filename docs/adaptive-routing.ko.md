---
translation_source: adaptive-routing.md
translation_source_sha256: 4aab86d568087848ef70870e04fe1cef82332367d805ea940383ae160911ac90
---

# 적응형 라우팅과 Codex {#adaptive-routing-and-codex}

승인된 목적지와 제한을 설정하면 `mayu`가 해당되는 요청에 적용하므로 사용자가 작업마다 모델을 직접 바꿀 필요가 없습니다. 컨텍스트 기반 모델 선택, PII 처리, 예산에 따른 전환은 설정된 규칙 안에서 데이터 영역이 수행합니다. 운영자는 정책을 선택하고 목적지를 검증해야 합니다. 컨텍스트는 Shadow로 시작하며 개인정보 규칙은 독립적으로 집행됩니다.

[이 방식의 강점](why-inferplane.md) · [아키텍처](architecture.md)

`examples/config.adaptive-routing.json`은 독립된 노드 로컬 예제입니다. 엔드포인트·모델 ID·컨텍스트·기능·가격을 검증한 배포 값으로 바꾸세요. Qwen·Gemma·Kimi라는 이름만으로 비용, 모델 크기, 프로토콜, 데이터 경계를 정하지 않습니다.

## 라우팅 계약 {#routing-contract}

| 요구 | 동작 |
| --- | --- |
| 약함·일반·강함 | `simpleModel`·선택적 `normalModel`·`complexModel`. 로컬 크기와 마지막 사용자 키워드 신호 |
| 잦은 전환 방지 | 명시적 `stability`. 기본 유지 5분·성공 3회·세션 TTL 30분 |
| 개인정보 전용 모델 | 명시적 승인 모델과 내부 공급자 메타데이터의 `InternalOnly` |
| 개인정보 마스킹 | 검사 가능한 탐지 내용에 `Mask`, 외부 호출 전 완전 변환·재검사 |
| 예산 전환 | `budgetTiers.enforceTargets: true`, 임계값 100 지원. 모든 재시도가 선택 모델 내에 머묾 |
| 총예산 하드 한도 | 독립적으로 유지. 저비용·내부 모델도 소진 시 거부 |
| 가용성 | 로컬 결정과 승인된 백엔드 폴백. 중앙 분류·어피니티 서비스 없음 |

비호환 목적지, 알 수 없는 경계, 거부된 모델, 빈 교집합이 외부·고비용 폴백으로 바뀌지 않습니다. 공급자 캐시는 이러한 제약 아래의 선호 조건입니다. 유지 기간은 새 컨텍스트 선호 적용을 지연할 수 있으므로 캐시 비용과 작업 품질을 함께 고려하세요.

세션 키 범위는 인증된 키·팀, 수신 프로토콜, 요청 모델입니다. `X-Inferplane-Session-ID`는 선택적 불투명 성능 힌트입니다. 없으면 대화 접두부의 안정적인 지문을 유도합니다. 둘 다 사용자 식별·권한 증명이 아니므로 헤더에 개인정보를 넣지 마세요. 고정은 로컬·유한하며 재시작·데이터 영역 이동 시 캐시 연속성이 사라질 수 있습니다.

## 예제 설정 {#example-configuration}

`auto`는 처음 일반 경로로 해석합니다. 평가 통과 후 정책이 약함·일반·강함을 고릅니다. 전환 임계값 $100, 별도 총 하드 예산 $150는 시연 값이며 권장 지출 한도가 아닙니다.

`governance.yaml`과 함께 `privacy-internal.yaml`·`privacy-mask.yaml` 중 **하나**를 로드하세요. 두 개인정보 정책의 교집합을 의도하지 않는다면 디렉터리 전체를 로드하지 마세요. 둘 다 로드하면 선택 관계가 아니라 마스킹과 내부 목적지를 모두 요구합니다.

```bash
mkdir -p /tmp/inferplane-adaptive-demo
mayu pricing check --config examples/config.adaptive-routing.json
mayu keys create --team engineering --models '*' --store /tmp/inferplane-adaptive-demo/keys.db
mayu serve --config examples/config.adaptive-routing.json
```

참조된 환경 비밀은 비밀 관리 도구로 제공합니다. 한 번 표시되는 가상 키를 설정 파일이나 커밋에 넣지 마세요.

컨텍스트는 Shadow로 시작합니다. 대표 평가 후 해당 규칙을 Enforce로 바꾸세요. `normalModel`에는 `maxNormalInputTokens > maxSimpleInputTokens`가 필요합니다. 추정은 도구 스키마를 포함한 전체 요청을 대상으로 하며 공급자 토크나이저의 정확한 수치가 아닌 보수적 값입니다.

정책당 규칙은 최대 256개, 티어 단계는 최대 100개입니다. CRD의 유한 컬렉션 한도는 단순 YAML 형태 검사뿐 아니라 Kubernetes 네이티브 스키마·CEL 비용 검사로 CI에서 검증합니다.

엄격한 티어가 참조하는 소프트 임계값은 승인 관점에서 계량용입니다. 더 높은 총 하드 한도를 더 낮은 차단 한도로 바꾸지 않습니다. 사용량은 계속 측정하며 실제 한도가 없어도 경고 전용 계량이 라우팅 판단에 사용됩니다. 승인된 저비용 트래픽을 포함한 실제 비용은 총한도에 합산합니다. 기존 로컬·리스 정확성 한계는 유지됩니다.

## Codex 로컬 설정 {#codex-local-configuration}

사용자 `~/.codex/config.toml`에 사용자 지정 공급자를 설정합니다. 현재 Codex의 지원 경로는 프로젝트 로컬 공급자 설정이 아닙니다. 게이트웨이 키는 `INFERPLANE_API_KEY`에 둡니다.

```toml
model = "auto"
model_provider = "inferplane"
web_search = "disabled"

[model_providers.inferplane]
name = "inferplane"
base_url = "http://127.0.0.1:8080/v1"
wire_api = "responses"
env_key = "INFERPLANE_API_KEY"
```

HTTP Responses 스트리밍을 지원하며 이 예제는 WebSocket 지원을 선언하지 않습니다. `auto`는 승인된 로컬 모델에 무상태 Chat Completions 브리지를 사용할 수 있습니다. `codex-native`는 호환 OpenAI 방식 업스트림의 네이티브 Responses 예제입니다.

브리지는 Chat Completions, Messages, Bedrock Converse·InvokeModel, Mantle Chat/Messages에서 텍스트, 함수·사용자 지정 도구, 결과, 호환 무상태 이력을 지원합니다. Bedrock 도구 이름은 결정적인 백엔드 안전 별칭으로 바꾸고 Responses 클라이언트에 전달하기 전에 복원합니다. 공급자 소유 대화 ID, 암호화 추론 전달, 비동기 응답 저장, 미지원 내장 도구를 조용히 변환하지 않습니다. 이런 기능은 네이티브 호환 목적지나 새 이식 가능한 대화를 사용하세요. 모델 자체의 코딩·도구 품질 검증도 필요합니다.

명시적으로 선택한 Responses 모델은 수신 컨텍스트 추정과 출력 예산으로 용량을 검사합니다. 자동 대안 선택, 내부 전용·엄격한 예산 목적지에는 보수적 검사 상한을 유지합니다. 이를 구분해 디코딩 바이트를 입력 토큰으로 간주했다는 이유만으로 정상적인 긴 도구 이력을 거부하지 않도록 합니다. 두 추정 모두 토크나이저가 아니며 정산은 업스트림 사용량을 따릅니다.

Chat 브리지는 함수 스키마 strict를 유지하며 strict 함수에는 `structured_output` 선언 목적지가 필요합니다. Anthropic·Bedrock 브리지는 non-strict 도구를 받습니다. 실제 Messages 봉투를 만들고 출력 한도가 없으면 `max_tokens: 4096`을 제공합니다. Responses 메시지 단계 관리는 어댑터의 Responses 쪽에 남습니다. 실패·잘린 시도의 관측 사용량을 유지하고 재시도마다 남은 하드 예산과 현재 엄격 목적지를 다시 검사합니다.

참고한 OpenAI 자료:

- https://developers.openai.com/codex/config-reference
- https://developers.openai.com/codex/config-advanced
- https://developers.openai.com/api/reference/resources/responses/methods/create
- https://developers.openai.com/api/docs/guides/streaming-responses

## Amazon Bedrock IAM 자격 증명으로 Codex 연결 {#codex-with-amazon-bedrock-iam-credentials}

`bedrock` 공급자는 이식 가능한 Responses를 설정된 Converse·InvokeModel·Mantle 경로로 연결합니다. 관리되는 브리지 선택에는 컨텍스트 한도, 코딩 클라이언트용 `tools`, 가격 선언이 필요합니다. 실제 백엔드도 요청한 도구를 지원해야 합니다.

Bedrock Mantle 네이티브 Responses에는 `bedrock_responses`를 사용합니다.

```json
{
  "providers": {
    "codex-bedrock": {
      "type": "bedrock_responses",
      "base_url": "https://bedrock-mantle.us-west-2.api.aws/openai"
    }
  },
  "models": {
    "openai.gpt-6-astra": {
      "targets": [
        {"provider": "codex-bedrock", "model": "openai.gpt-6-astra"}
      ]
    }
  }
}
```

기존 게이트웨이 설정에 병합하고 `pricing.overrides.codex-bedrock`에 검증된 가격을 선언하세요. 선택 리전의 모델 접근을 확인합니다. 엔드포인트가 서명 리전을 결정하며 명시적인 상용 Bedrock Mantle HTTPS 주소만 허용합니다. Astra 예제는 문서에 명시된 Mantle 리전 `us-west-2`를 사용합니다. [AWS 모델 카드](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-openai-gpt-6-astra.html)를 참고하세요.

mayu가 자체 표준 AWS 자격 증명 체인으로 `bedrock` 서비스 요청에 서명합니다. 이 공급자는 IAM을 사용하며 `api_key_ref`나 자격 증명 중개를 사용하지 않습니다. 클라이언트 가상 키는 mayu에서 종료됩니다. 네이티브 Responses 전송이 스트리밍·사용량 회계를 제공하고 서명 전송은 본문 바이트를 보존합니다. Converse 변환은 없습니다. Bedrock Guardrails를 적용할 수 없어 가드레일 요구 요청은 거부합니다.

위 사용자 지정 공급자에 `model = "openai.gpt-6-astra"`와 실제 로컬 포트를 사용하세요. 별도로 지원 검색 경로를 구성하지 않았다면 `supports_websockets = false`, `web_search = "disabled"`를 유지합니다. Codex의 `amazon-bedrock` 공급자는 AWS에 직접 연결합니다. 게이트웨이 인증·라우팅·회계를 사용하려면 `inferplane`을 선택하세요.

`GET /v1/models`는 `capabilities`와 `responses_mode`(`native`, `bridge`, `unsupported`)를 제공합니다. 네이티브 항목의 `codex_model`은 공개 이름에서 유도해 설치된 메타데이터와 매칭하며 비공개 업스트림 배포 이름을 공개하지 않습니다. 네이티브·브리지가 섞인 체인은 이식 가능한 브리지 계약을 표시합니다. 메타데이터는 설정된 기능이며 요청 시 정책·공급자 검사가 최종 기준입니다.

이식 가능한 클라이언트는 상속된 네이티브 추론 선호를 해제하려고 `reasoning: {"effort": "none"}`을 보낼 수 있습니다. 백엔드 추론 강도 제어를 요구하지 않으며 브리지는 텍스트·도구 결과를 전달하고 생성 동작은 공급자가 정합니다. 명시적 추론 요구와 암호화 이력에는 호환 네이티브 목적지가 필요합니다.

## 개인정보와 캐시 한계 {#privacy-and-cache-limits}

활성화된 기존 PII 필터도 완전 요청 변환기를 통해 Responses에 적용됩니다. 검사 가능한 깨끗한 요청은 사용할 수 있습니다. 보호 텍스트는 두 탐지기 집합을 모두 통과해야 하며 불투명·안전하지 않은 변환은 거부합니다.

유한 탐지 범위는 이메일, 지원 전화 형식, Luhn 카드, SSN, IPv4, 주민등록번호 형식입니다. 모든 개인정보 인식이 아닙니다. 알 수 없거나 불투명한 내용은 내부·차단 설정을 따릅니다. 구조 키·식별자·숫자 값을 안전하게 형식을 유지하며 변환할 수 없으면 마스킹을 거부합니다.

동일한 변환 요청은 동일 바이트를 만들어 이후 마스킹 접두부도 공급자 캐시를 사용할 수 있습니다. 모델·공급자 전환이나 마스킹 도입은 캐시 네임스페이스를 바꿉니다. 측정 없는 캐시 적중률·절감률은 보장하지 않습니다.

## 가용성과 검증 {#availability-and-verification}

분류·정책 검사·세션 고정 조회는 mayu 로컬에서 수행합니다. 백엔드 폴백도 같은 개인정보·기능·리전·비용 한도를 지킵니다. 승인된 백엔드를 복제하고 제어 영역 관리를 추론 경로 밖에 두세요. 전역 금전 권한에는 선택적 Postgres 원장·영속 노드 저널이 있습니다. [영속 예산](durable-budgets.md), [ADR-045](decisions/ADR-045-durable-budget-authority.md)를 참고하세요. 공유 키·전역 요청률·토큰 할당량에는 가용한 HA Postgres가 필요한 [공유 게이트웨이](shared-governance.md)를 사용합니다. 노드 로컬 프로파일의 기존 요청률·키 한계는 유지합니다.

일반 테스트는 가짜 공급자와 일회용 저장소를 사용합니다. 선택적인 설치 클라이언트 시험은 격리 설정과 로컬 가짜 업스트림으로 실제 Codex CLI 도구 왕복을 실행합니다.

```bash
INFERPLANE_TEST_CODEX=1 go test ./cmd/mayu -run TestE2ECodexCLI -v -count=1
```

프로토콜과 게이트웨이 경로를 확인하며 실제 모델을 호출하지 않습니다. 운영 모델 품질이나 여러 복제본의 예산 정확성을 인증하지 않습니다.
