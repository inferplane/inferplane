---
translation_source: performance/index.md
translation_source_sha256: aa7731744ea9cadac278dcea608c7c97cc1577d4edc491251c4960a944e7c888
title: 성능 결과
description: inferplane의 지연, 처리량, 캐시 비용과 네이티브 Converse 호환성 실측 결과.
---

# 성능 결과 {#performance-results}

**2026년 10월 20일 기준.** 저장된 최신 측정 결과를 보여줍니다. 아래 네이티브 Converse·UI 결과는 아직 병합되지 않은 개발 구현에서 얻었으며, 공개 main 브랜치에서 사용할 수 있는 기능을 뜻하지 않습니다. 게이트웨이·전체 모델 측정은 10월 19일, 네이티브 Converse·관리자 UI 측정은 10월 20일 자료입니다. 명시된 조건에서의 관측이며 운영 SLA를 의미하지 않습니다.

<div class="grid cards" markdown>

-   **49개 모델 포함**

    Bedrock 직접 호출에서 42개 모델이 하나 이상의 과제를 완료했습니다. Anthropic, OpenAI, xAI/Grok, Qwen, DeepSeek, Moonshot/Kimi, MiniMax, GLM을 포함합니다.

-   **26개 모델에서 입력 점유율 95–100% 성공**

    선언된 컨텍스트 한계 부근에서 삽입한 세 값을 모두 회수했습니다. 합성 입력 회수 결과이며 코딩·문서 이해 성능 점수는 아닙니다.

-   **네이티브 Invoke 48/48건 완료**

    inferplane을 경유해 동시 요청 1·4로 실행했습니다. Sonnet 4.6의 짧은 과제에서 조건별 E2E 중앙값은 795.96–814.56ms였습니다.

-   **네이티브 Converse 4/4건 완료**

    개발 빌드에서 Haiku 4.5와 Qwen3 32B를 관리자 UI·스트리밍 SDK로 각각 호출했습니다. 모델·API당 한 건입니다.

</div>

[전체 모델 결과 보기](models.md){ .md-button .md-button--primary }
[측정 데이터 다운로드](../assets/data/performance.json){ .md-button download="inferplane-performance.json" }

## 게이트웨이 지연 {#gateway-latency}

클라이언트가 inferplane의 네이티브 Bedrock Invoke 엔드포인트를 통해 Claude Sonnet 4.6(`us.anthropic.claude-sonnet-4-6`)을 호출했습니다. 조건별 12건, 최대 출력 64토큰, 고정 문구를 응답하는 짧은 과제입니다. SDK는 요청당 한 번만 시도했습니다. E2E에는 모델 실행과 네트워크 시간이 포함되므로 게이트웨이 CPU 오버헤드만을 나타내지 않습니다.

<!-- performance:gateway -->
| API | 동시 요청 | 성공/호출 | E2E p50 (ms) | E2E p95 (ms) | TTFT p50 (ms) | 성공 req/s |
| --- | --- | --- | --- | --- | --- | --- |
| InvokeModel | 1 | 12/12 | 814.56 | 913.70 | — | 1.23 |
| InvokeModelWithResponseStream | 1 | 12/12 | 797.03 | 852.62 | 792.05 | 1.26 |
| InvokeModel | 4 | 12/12 | 795.96 | 908.80 | — | 4.67 |
| InvokeModelWithResponseStream | 4 | 12/12 | 796.06 | 1,012.43 | 790.73 | 4.52 |
<!-- /performance:gateway -->

이 과제에서 관측한 응답 실패는 **0/48건**입니다. 조건별 관측이 12건이므로 nearest-rank p95는 최댓값입니다. 해당 실행의 분포이며 안정적인 꼬리 지연 추정치는 아닙니다. 최대 처리 용량, 다중 노드 확장성, 공유 Postgres 오버헤드를 입증하지 않습니다.

## 동시 요청에 따른 생성 처리량 {#generation-throughput-under-concurrency}

아래 배치는 inferplane의 **Messages 어댑터**를 사용했습니다. 모델 3개, 조건별 요청 8건, 최대 출력 512토큰입니다. 성공 응답의 출력 토큰 합계를 실패 요청에 소요된 시간까지 포함한 **배치 전체 경과 시간**으로 나눕니다. 실제 전달한 배치 처리량이며 개별 요청의 decode 속도가 아닙니다.

<!-- performance:concurrency -->
| 모델 | 동시 요청 | 성공/호출 | 응답 실패율 | 배치 시간 (s) | 성공 출력 tok/s |
| --- | --- | --- | --- | --- | --- |
| Claude Opus 4.8 | 1 | 5/8 | 37.5% | 41.88 | 57.50 |
| Claude Opus 4.8 | 4 | 6/8 | 25.0% | 16.35 | 166.95 |
| GPT-6.1 Sol | 1 | 8/8 | 0.0% | 71.77 | 57.07 |
| GPT-6.1 Sol | 4 | 8/8 | 0.0% | 18.46 | 221.87 |
| Qwen3 Next 80B A3B | 1 | 8/8 | 0.0% | 45.87 | 89.29 |
| Qwen3 Next 80B A3B | 4 | 8/8 | 0.0% | 13.59 | 301.38 |
<!-- /performance:concurrency -->

동시 요청을 1 → 4로 늘렸을 때 GPT-6.1 Sol은 **57.07 → 221.87 tok/s**, Qwen3 Next 80B A3B는 **89.29 → 301.38 tok/s**였습니다. 두 모델은 각 조건에서 8/8건을 완료했습니다. Opus 4.8의 실패 응답 5건은 빈 응답으로 분류되었으며 처리량에는 실패에 소요된 시간도 반영되어 있습니다. 이 작은 배치만으로 포화 처리량이나 요청률 한도를 판단할 수 없습니다.

## 관측된 프롬프트 캐시 비용 {#observed-prompt-cache-cost}

각 모델에서 동일한 prefix를 inferplane의 Messages 어댑터로 순차 재사용했습니다. 캐시 쓰기 1건, 이어서 읽기 3건이 관측되었습니다. 첫 요청이 cold였다고 가정하지 않고 보고된 캐시 토큰 수를 사용합니다.

<!-- performance:cache -->
| 모델 | 캐시 토큰 | 쓰기/읽기 호출 | 쓰기 요청 비용 | 읽기 요청 비용 중앙값 | 쓰기 TTFT (s) | 읽기 TTFT 중앙값 (s) |
| --- | --- | --- | --- | --- | --- | --- |
| Claude Opus 4.8 | 65,693 | 1 / 3 | $0.411541 | $0.033806 | 2.391 | 1.645 |
| Claude Sonnet 4.6 | 65,642 | 1 / 3 | $0.246616 | $0.020152 | 2.475 | 1.610 |
| GPT-6.1 Sol | 65,634 | 1 / 3 | $0.164409 | $0.006887 | 2.082 | 1.377 |
<!-- /performance:cache -->

캐시 읽기 요청 비용은 각 모델의 캐시 생성 요청보다 Sonnet 4.6·Opus 4.8에서 약 **91.8%**, GPT-6.1 Sol에서 약 **95.8%** 낮았습니다. 기록된 가격으로 전체 요청 비용을 비교한 수치이며 모든 작업에 적용되는 할인율이 아닙니다. 최초 쓰기, 만료, 미적중, prefix 변경, 재시도 비용은 계속 발생합니다. 기본 TTL만 실행했습니다.

컨텍스트 크기에 따른 요청 비용과 생성 지연은 [모델별 표](models.md)에서 확인하세요. 행마다 입력 토큰 수, 입력군, 캐시 상태가 다를 수 있습니다.

## 네이티브 Converse와 관리자 UI {#native-converse-and-administrator-ui}

측정한 **개발 빌드**는 지원되는 Bedrock Runtime 목적지에 대해 `/model/{configured-model}/converse`와 `/converse-stream`을 수신했습니다. 해당 빌드의 관리자 **모델 비교** 페이지는 가상 키 승인·정산 경로로 두 모델을 호출했습니다. 이 구현은 main에 병합되지 않았으며, 측정 결과 게시는 해당 엔드포인트나 UI를 활성화하지 않습니다. 공개 구현은 [API 레퍼런스](../api-reference.md)를 참고하세요.

<!-- performance:native -->
| 모델 | API | 완료/호출 | E2E (s) | TTFT (s) | 요청 비용 |
| --- | --- | --- | --- | --- | --- |
| Claude Haiku 4.5 | Converse · UI | 1/1 | 1.035 | — | $0.000041 |
| Qwen3 32B | Converse · UI | 1/1 | 0.344 | — | $0.000004 |
| Claude Haiku 4.5 | ConverseStream · SDK | 1/1 | 0.900 | 0.890 | $0.000041 |
| Qwen3 32B | ConverseStream · SDK | 1/1 | 0.289 | 0.289 | $0.000004 |
<!-- /performance:native -->

짧은 `READY` 요청 4건 모두 응답이 완료되고 사용량·정산 근거가 확인되었습니다. UI는 비스트리밍이므로 TTFT를 측정하지 않으며, E2E는 브라우저 왕복을 제외한 내부 추론 핸들러 시간입니다. SDK E2E·TTFT는 클라이언트에서 측정했습니다. **모델·API당 n=1**이므로 모델 순위나 운영 오류율을 추론할 수 없습니다. 설치된 가격 스냅샷 기준 네 건의 비용 합계는 **$0.000090**입니다.

측정한 UI는 순차 A/B·B/A 실행, 응답 실패 집계, E2E 출력 tok/s와 JSON 내보내기를 제공합니다. 텍스트 프롬프트와 최대 4,096 출력 토큰을 지원합니다. 두 모델을 비교하는 도구이며, 위의 전체 컨텍스트·동시성 결과는 API 클라이언트에서 얻었습니다.

## 비용 가시성과 근거 {#cost-visibility-and-evidence}

| 표시 항목 | 해석 |
| --- | --- |
| 사용량·캐시 토큰 분류 | 공급자가 보고한 수량이며 evidence 필드가 완전성을 나타냄 |
| 정수 microUSD 비용 | 설치된 가격 스냅샷 × 해석된 사용량, half-even 정산 |
| 회계 확인됨 | 완전한 정산 근거와 설정 가격을 확인했다는 의미이며 청구서 대사는 아님 |
| 비용 미확정 | 가격·사용량 누락, 취소, 통신 단절, 불완전한 시도 근거 등을 의미하며 0으로 처리하지 않음 |
| 응답 성공 | 응답 완료를 의미하며 합성 회수 과제는 삽입한 세 값의 회수도 요구 |

이 자료의 가격은 **10월 19일 측정 당시 스냅샷**이며 실시간 가격이 아닙니다. 인프라, 세금, 개별 할인, 설정되지 않은 요금은 제외합니다. 기록된 추정값을 보존하며 사후 가격으로 다시 계산하지 않았습니다. 요청별 가격 구간, 캐시 해석, 관측 필드는 [컨텍스트 가격](../reference/pricing.md)에서 확인하세요. 실패한 요청에도 비용이 발생할 수 있습니다.

## 범위와 재현 근거 {#scope-and-reproducibility}

다운로드 JSON에는 모델 ID, 리전, 선언 창 크기, 기록 가격, 정제한 요청별 수치, 배치별 분모와 원본 SHA-256이 들어 있습니다. 출처 경로는 로컬 기록을 식별하며 원시 기록 파일은 게시하지 않습니다. 프롬프트, 응답 본문, 업스트림 오류 본문, 자격 증명과 개인 예산 정보는 제외합니다. 네이티브 Invoke, Messages 변환, 네이티브 Converse, 모델 직접 호출을 구분합니다.

모델·컨텍스트·생성 조건 대부분은 **한 번의 관측**입니다. 지연은 리전, 전송 방식, 캐시와 공급자의 기본 추론 설정에 영향을 받습니다. 품질 순위, 정확한 최대 컨텍스트, 운영 오류율, HA 보장, 가용성 SLA를 입증하지 않습니다. [배포 프로파일](../getting-started/deployment-profiles.md)과 [상용화 준비도](../operations/production-readiness.md)의 요구 사항을 함께 확인하세요.
