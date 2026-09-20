---
translation_source: reference/bedrock-coding-models.md
translation_source_sha256: 2ba552f6c37e8f6ad918e3fd0953a7eed40440dfe86fa35f8796a3660266d921
---

# Bedrock 코딩 모델: Kimi K3와 Fable 5.1

이 예제는 기존 모델 라우팅을 확장합니다. 별도의 Responses 수신 경로나 main의
정책·신원 파이프라인 대체 구현을 추가하지 않습니다. 공급자·모델·허용 목록·가격
항목을 기존 설정에 병합하세요. 예제로 운영 설정 전체를 덮어쓰지 마세요.

| 예제 | 공개 모델명 | 업스트림 프로파일 | API | 컨텍스트 |
| --- | --- | --- | --- | --- |
| [Kimi K3](../../examples/config.bedrock-kimi-k3.json) | `kimi-k3` | `global.moonshotai.kimi-k3` | Converse | 1,000,000 |
| [Fable 5.1](../../examples/config.bedrock-fable-5-1.json) | `global.anthropic.claude-fable-5-1` | 공개 모델명과 동일 | InvokeModel | 1,000,000 |

두 예제는 서울을 요청 리전으로 사용하고 `tools`를 선언하며 가격 누락 시 차단합니다.
Global 프로파일은 한국 밖에서 처리할 수 있습니다. 엔드포인트 리전은 데이터
상주 보장이 아닙니다. SDK 기본 자격 증명 체인으로 EC2 인스턴스 역할을 사용할 수
있으며 samples 프로파일이나 추가 AssumeRole은 필요하지 않습니다. 인스턴스 역할이
목표라면 의도하지 않은 자격 증명·프로파일 override를 제거하세요.

## 가격과 기능 경계

2026-09-20 확인한 Standard Global 가격이며 단위는 100만 토큰당 USD입니다.

| 모델 | 입력 | 출력 | 캐시 읽기 | 캐시 쓰기 |
| --- | --- | --- | --- | --- |
| Kimi K3 | 3 | 15 | 0.30 | 3.75 |
| Fable 5.1 | 10 | 50 | 0.25 | 12.50 (5m), 20 (1h) |

가격 키는 정확한 global 프로파일입니다. US CRIS에는 별도 가격을 설정해야 합니다.
K3의 TTL 미분류 쓰기는 기존 5m 회계 버킷을 사용하지만 실제 TTL이 5분이라는 뜻은
아닙니다. 예제는 두 쓰기 버킷에 공개 단가를 설정합니다. Converse 어댑터에는 K3의
Chat/Responses 명시적 캐시 제어가 구현돼 있지 않습니다.

100만 컨텍스트는 공식 용량이며 실제 100만 토큰 부하 테스트 결과가 아닙니다.
Fable의 공개 최대 출력은 128K이고 클라이언트·게이트웨이 출력 예산과 다릅니다.
런처는 더 작은 출력·압축 예산을 사용할 수 있습니다. K3 모델 카드에는 최대 출력
토큰 수가 명시돼 있지 않습니다.

Fable 5.1은 adaptive thinking이 항상 켜져 있습니다. 미지원 sampling 필드를 생략하세요.
기존 Fable legacy-thinking 어댑터는 `fable-5-1`에도 적용됩니다. AWS는 `aws_review`
보존 동의를 요구하지만 예제는 계정 설정을 변경하지 않습니다. 활성화 전 조직의
명시적 승인을 받으세요. refusal은 HTTP 200으로 올 수 있으며 작업 성공을 뜻하지 않습니다.

## 클라이언트 연결과 검증 상태

main의 [클라이언트 연결](../getting-started/clients.md)과
[Codex 런처](../codex-launcher.md)를 사용하세요. 클라이언트 요청은 모두 mayu를 거치고
AWS 자격 증명은 데이터 영역에만 둡니다. Claude Code는 AWS 엔드포인트 대신 mayu의
Bedrock 수신 경로를 지정한 네이티브 Bedrock 모드도 사용할 수 있습니다. 모드 변경만으로
호환성이 검증되지는 않습니다.

2026-09-20 로컬 검증은 배포 소스에 외부 thinking 출력 핫픽스를 적용한 빌드를
사용했습니다. 짧은 파일 읽기 도구 왕복이며 모든 플러그인·도구·장시간 세션 검증은 아닙니다.

| 클라이언트 | Fable 5 | Fable 5.1 | Kimi K3 |
| --- | --- | --- | --- |
| OpenCode, Responses 런처 | 통과 | 통과 | 통과 |
| Codex, Responses 런처 | 통과 | 통과 | 통과 |
| Claude Code, Anthropic Messages 모드 | 재시도 후 통과 | 실패 | 앞선 실패로 미실행 |

Claude Code의 Fable 5.1 요청은 Bedrock의
`messages.1.output_config: Extra inputs are not permitted` 오류로 실패했으며
Fable 5 fallback도 같은 필드로 실패했습니다. Fable 5는 초기에 `safeguards` 거부도
겪었습니다. 네이티브 Bedrock 모드 재검증은 남아 있습니다. 안전 관련 필드를 무조건
삭제하거나 세 클라이언트가 모두 완전히 검증됐다고 주장하지 마세요.

이후 긴 Codex/Fable 5.1 세션에서 4096 출력 예산 소진과 무진행 반복이 관측됐습니다.
빈 응답·미완료 응답 처리, thinking/출력 예산과 제한된 재시도가 남은 과제입니다.
thinking 핫픽스는 미지원 블록 오류를 막을 뿐 이 후속 문제까지 해결하지 않습니다.

## 출처

- [Kimi K3 모델 카드](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-moonshot-ai-kimi-k3.html)
- [Fable 5.1 모델 카드](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-fable-5-1.html)
- [AWS FoundationModels 서울 가격](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonBedrockFoundationModels/current/ap-northeast-2/index.json)
