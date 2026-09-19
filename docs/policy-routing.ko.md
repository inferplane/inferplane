---
translation_source: policy-routing.md
translation_source_sha256: 39890ecaf5a52ccaab41618cab144934b678133e43006543b2b0a2b47a07b3f8
---

# 정책 기반 라우팅 {#policy-aware-routing}

보호 대상 내용의 처리 목적지를 제한하고 컨텍스트별 모델 추천을 기록합니다. 승인된 데이터 처리 경계 안에서 총비용을 줄이며 코딩 작업을 수행하는 것이 목표입니다. 절감과 품질은 실제 워크로드로 평가해야 하며 싼 모델 선택만으로 입증되지 않습니다.

이 문서는 기존 2단계 분류 모드입니다. 일반 등급, 세션 안정성, 엄격한 예산 전환, 완전 마스킹, Codex 추가 기능은 [적응형 라우팅](adaptive-routing.md)을 참고하세요. 아래 적용 한계는 새 stability 옵션이 없을 때 해당합니다.

## 독립된 예제 설정 {#configure-an-isolated-example}

저장소 루트에서 `examples/config.policy-routing.json`과 `examples/policy-routing/governance.yaml`을 사용합니다. 정책은 의도적으로 `examples/policies/` 밖에 있습니다. 그 디렉터리는 모델 이름이 다른 빠른 시작 예제용이며 함께 로드해서 무관한 라우팅 목적지가 추가되면 안 됩니다.

`.invalid` 엔드포인트와 업스트림 모델 ID를 실제 OpenAI 호환 배포로 바꾸세요. 컨텍스트·기능을 검증하고 예시 백만 토큰당 USD 가격을 자체 비용 배부 요율로 교체합니다. 내부로 표시한 공급자는 실제 승인된 처리 경계를 충족해야 합니다. `data_boundary`는 운영자 선언이며 호스트명·모델로 추론하지 않습니다.

비밀 관리 도구로 `INFERPLANE_ADMIN_TOKEN`, `POLICY_ROUTING_PUBLIC_KEY`, `POLICY_ROUTING_PRIVATE_KEY`를 제공합니다. 환경 변수 참조만 사용하고 비밀 평문을 저장하지 않습니다. 상태 디렉터리를 만든 뒤 `engineering` 팀 키를 발급합니다.

```bash
mkdir -p /tmp/inferplane-policy-routing-demo
mayu pricing check --config examples/config.policy-routing.json
mayu keys create --team engineering --models '*' --store /tmp/inferplane-policy-routing-demo/keys.db
mayu serve --config examples/config.policy-routing.json
```

한 번 표시되는 키는 비밀로 취급하세요. `premium-coder` 또는 별칭으로 일반 Messages·Chat Completions 요청을 보냅니다. 예제의 `tools` 기능은 예시이며 비전·추론·구조화 출력 지원은 선언하지 않습니다. 지원되는 Anthropic 텍스트 본문을 사용하는 Bedrock InvokeModel·InvokeModelWithResponseStream도 이 `openai_compatible` 목적지로 라우팅할 수 있습니다. 게이트웨이가 요청을 변환하고 수신 프로토콜 응답을 만듭니다. 기존 교차 프로토콜 제한이 적용되므로 비전·추론·구조화 출력·검사 불가 내용은 이 정책 선택 경로를 사용할 수 없습니다. 기능 선언이 변환기의 제한을 덮어쓰지 않습니다.

## 정책 필드 {#policy-fields}

규칙마다 종류가 하나이고 `routing` 하위 종류도 하나입니다. affinity는 현재 거부되며 budgetTiers 또는 context를 사용합니다. 한 정책 안의 `team`·`user`는 AND 조건이고 일치하는 모든 정책이 적용됩니다.

| 필드 | 계약 |
| --- | --- |
| `sensitiveData.onDetected` | `InternalOnly` 또는 `Block` 필수 |
| `sensitiveData.onUninspectable` | `InternalOnly` 또는 `Block` 필수 |
| `sensitiveData.internalModels` | 하나라도 InternalOnly이면 명시적 모델 이름 필요. 빈 목록·와일드카드 불가, 별칭은 현재 토폴로지로 해석 |
| 민감 정보 규칙 `failurePolicy` | `FailClosed` 필수. 검사·조회 오류는 거부 |
| `routing.context.mode` | 기본 `Shadow`, 명시적 `Enforce` 가능 |
| `routing.context.fromModels` | 비어 있지 않은 명시적 이름. 예산 티어 후·개인정보 치환 전 모델에 매칭 |
| `simpleModel`, `complexModel` | 명시적 목적지 필수. 두 모델 모두 경로·가격 필요 |
| `maxSimpleInputTokens` | 양수 보수적 입력 임계값 필수. 초과 시 complex 추천 |
| `complexKeywords` | 선택적 비공백 문자열. 디코딩 텍스트의 대소문자 무시 매칭, 임계값 이하여도 complex 추천 |
| 컨텍스트 규칙 `failurePolicy` | `FailOpen` 필수. 선호 모델을 못 쓰면 안전한 경로 유지 |

개인정보 규칙은 Shadow에서도 집행됩니다. Block이 우선하고 InternalOnly는 승인된 모델 집합의 교집합 및 모든 시도의 명시적 내부 공급자를 요구합니다. 교집합이 비거나 호환되는 안전한 목적지가 없으면 수신 프로토콜 형식의 403입니다. 컨텍스트가 이 제한을 완화할 수 없습니다. 겹치는 컨텍스트 추천은 일치해야 하며 일치 규칙 중 Shadow가 있으면 강제 전환하지 않습니다.

예제는 `mode: Shadow`를 명시합니다. 워크로드 평가를 통과한 뒤 해당 규칙만 다음처럼 변경하세요.

```yaml
routing:
  context:
    mode: Enforce
    fromModels: [premium-coder]
    simpleModel: economy-coder
    complexModel: premium-coder
    maxSimpleInputTokens: 4096
    complexKeywords: [security, authentication, migration]
```

`maxSimpleInputTokens`는 simple/complex 선택 기준이지 Enforce의 길이 상한이 아닙니다. 적격 요청이 임계값을 넘으면 별도의 호환 `complexModel`로 바뀔 수 있습니다. 예제에서는 complex와 원래 모델이 같으므로 큰 `premium-coder` 요청을 컨텍스트 규칙만으로 바꾸지 않습니다.

`failurePolicy: FailOpen`을 유지하세요. Enforce는 assistant·도구 이력, 도구 스키마·호출, 미디어, 추론, 구조화 출력 요구가 없고 완전히 검사 가능한 단일 사용자 턴만 선택합니다. 여러 턴은 관측만 합니다. 개인정보 검사는 모든 턴에서 제출된 전체 이력에 적용됩니다. 클라이언트 헤더에서 영속 세션 신원이나 고정을 추론하지 않습니다.

## 검사와 기능 한계 {#inspection-and-capability-limits}

마스킹·본문 수집 전에 원본 바이트를 읽으며 바꾸지 않습니다. JSON 키·문자열, 숫자의 정확한 표기, system/developer 프롬프트, 메시지, 메타데이터, 도구 정의·결과, 중첩 JSON 도구 인수를 검사합니다. 유한한 탐지 범위는 다음과 같습니다.

- 이메일 형식과 일부 북미·한국·국제 전화번호 형식.
- Luhn 검증을 통과한 13~19자리 카드 번호, 범위 제외 조건이 있는 미국 SSN 형식.
- 유효한 IPv4 리터럴과 한국 주민등록번호 형식.

오탐·미탐이 있으며 모든 개인정보·난독화를 다루지 않습니다. 검사 가능한 추론 텍스트는 검사합니다. 불투명 이미지·오디오·문서·파일, 암호화·삭제된 thinking, 알 수 없는 콘텐츠 블록·본문 구조는 검사 불가입니다. 예제는 이를 차단합니다. 원격 파일 가져오기나 임의 바이너리 디코딩은 없습니다. JSON 오류·취소·검사 한도 오류는 개인정보 정책에서 거부하며 컨텍스트 전용 실패는 경로를 유지합니다. 자원 한도는 입력 64 MiB, 디코딩 128 MiB, 노드 262144개, JSON 깊이 64, 인코딩 JSON 중첩 깊이 8입니다. 품질 보장이 아닙니다.

`models.<name>.capabilities`는 `tools`, `vision`, `reasoning`, `structured_output`만 받습니다. 빈 값은 선언된 기능이 없다는 뜻입니다. `context_window`는 입력+출력 토큰의 음이 아닌 값이며 0은 알 수 없음입니다. 자동 대안과 교차 모델 폴백은 보수적 입력+출력 예산을 충족하는 컨텍스트, 관측된 모든 기능, 가격, RBAC·리전 허용, 호환 전송이 필요합니다. 알려진 변환 손실은 기능 선언보다 우선합니다. OpenAI 수신은 직접 Anthropic을 선택할 수 없고 네이티브 Bedrock에는 호환 경로가 필요합니다. Converse·교차 프로토콜에도 추가 제약이 있습니다. 이 기능 자체는 Responses 수신이나 기존 마스킹 범위를 확장하지 않습니다.

공급자 저장소를 켜면 콘솔의 공급자·모델 편집 폼이 선언을 채우고 교체합니다. `unknown`, 빈 컨텍스트·0, 기능 선택 해제로 명시적으로 지울 수 있습니다. 일반 편집은 기존 별칭을 보존하며 NEW PROVIDER·NEW MODEL은 새 초안을 시작합니다.

## 배포·업그레이드·복구 {#delivery-upgrade-and-recovery}

새 규칙 활성화 전에 mayu, inferplaned, 사용하는 GovernancePolicy CRD를 업그레이드하세요. 모든 참여 데이터 영역이 스키마와 경로·가격을 갖추었는지 확인합니다. 구형 데이터 영역은 집행할 수 없으며 혼합 버전의 거부 응답을 보호로 간주하면 안 됩니다. 새 정책 없는 기존 설정은 동작을 유지합니다.

로컬 `policies`와 `control_plane` 중 하나만 선택합니다. 로컬 정책은 리스너 바인딩 전에 유효한 파일·DB 토폴로지와 검사하며 실패한 재로드는 마지막 유효 스냅샷을 유지합니다. 후속 적용은 현재 live holder를 사용합니다. 개인정보·컨텍스트·예산 치환 목적지는 `pricing.on_missing: allow`여도 가격이 필요합니다. 별칭은 허용하지만 누락 모델 폴백으로 정책 목적지를 대신할 수 없습니다. 메타데이터는 SQLite 초기화·오버레이, 관리 쓰기·조회·내보내기, 토폴로지 재로드를 거쳐 유지합니다. 나중 토폴로지 변경에도 런타임 검사를 적용하며 정책·토폴로지 게시를 분리합니다.

거부된 분산 민감 정보 문서는 유효한 교체본이 적용될 때까지 해당 주체의 라우팅을 차단합니다. 활성 정책의 잘못된 교체도 보호를 몰래 제거하지 않습니다. 컨텍스트 전용 선호 규칙의 거부는 개인정보 차단을 만들지 않습니다.

첫 요청부터 CP 보호를 받으려면 다음과 같이 설정하세요.

```json
"control_plane": {
  "url": "https://control-plane.example.invalid",
  "token_ref": {"env": "INFERPLANED_TOKEN"},
  "require_sync": true,
  "max_policy_age": "1m"
}
```

`require_sync`가 없으면 기존 시작 동작은 첫 동기화 전에 요청을 처리합니다. 설정하면 동기화 전과 승인된 정책이 오래된 경우 관리되는 생성에 503을 반환합니다. 두 토큰 계산 API는 준비되지 않은 상태·오래된 정책·개인정보 거부·과대/읽기 불가 본문에서도 업스트림 호출 없이 로컬 추정과 HTTP 200을 유지합니다. 준비 상태 예외가 외부 호출 권한을 주는 것은 아닙니다.

## 결정 확인과 배포 평가 {#read-decisions-and-evaluate-rollout}

requested 모델은 별칭·누락 모델 폴백을 해석한 예산 티어 전 모델입니다. `fromModels`는 티어 후·개인정보 치환 전 모델에 매칭됩니다. selected는 정책의 실제 선택이고 proposed는 관측값입니다. 수동·무규칙 결과는 회로 차단기 순위가 폴백을 먼저 두더라도 기존 사전 검사 동작을 위해 입력 모델을 유지합니다. 실제 시도는 감사의 actual 모델·공급자 필드로 확인하고 proposed에서 추측하지 마세요.

`x-inferplane-routing-reason`은 한정된 사유를, `x-inferplane-routed-model`은 Shadow와 함께 집행된 개인정보를 포함한 실제 정책 선택을 표시합니다. 기존 `x-inferplane-substituted-model`은 앞선 예산 티어여서 최종 모델과 다를 수 있습니다. `request.routing`은 정책명·세대, 검사 상태·종류, 계획된 공급자·경계와 완료·부분 결과의 실제 공급자·경계를 기록합니다. 선택적 필드라 기존 감사 체인을 보존합니다. `inferplane_routing_decisions_total{team,mode,reason}`에는 모델·사용자·키·세션·탐지 값 라벨이 없습니다. 결정 증거에 프롬프트·탐지 원문은 없으며 별도 선택적 본문 기록은 기존 동작을 유지합니다.

Enforce 전에 기준 대비 허용 임계값을 정하고 평가하세요.

1. 대표 작업의 테스트·사람 인수를 포함한 코딩 성공률.
2. 콜드 캐시 쓰기·여러 턴·재시도를 포함한 총 정산 비용.
3. 폴백을 포함한 p95 지연과 실패.
4. 불투명 내용, 거부·누락 목적지, 잘못된 세대, 리전·기능 충돌, 내부 실패, 외부 재시도 시도 등 개인정보 부정 사례.

단위 테스트 성공은 비용 절감·운영 준비·공유 HA·영속 예산을 입증하지 않습니다. 기존 인스턴스별 요청률·할당량·예산 한계도 남습니다. 컨텍스트 품질·비용이 악화되면 개인정보 집행을 유지하면서 Shadow로 돌아가세요. [ADR-043](decisions/ADR-043-policy-aware-routing.md)을 참고하세요.
