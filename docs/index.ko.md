---
translation_source: index.md
translation_source_sha256: 01b4a5a5b06981a8d759b9aaaadd1dfc641244fe1900bfffa167637b8ed1d964
title: 정책을 정하면 요청마다 자동 집행합니다
description: 정책 관리와 추론 경로를 분리하고 모델 접근, 예산 전환, PII 라우팅과 감사를 자동 집행하세요.
hide:
  - toc
  - navigation
---

<div class="ip-hero" markdown>

<p class="ip-eyebrow">정책은 중앙에서, 집행은 각 게이트웨이에서</p>

# 정책을 정하면, 요청마다 자동 집행합니다. {#give-every-request-a-policy}

누가 어떤 모델을 쓸 수 있는지, 얼마까지 사용할지, 민감 정보를 어디로 보낼지 정하세요. inferplane은 코딩 에이전트가 작업하는 동안 그 규칙을 자동 적용합니다. 승인된 모델을 선택하고, 예산 임계치에서 전환하고, 탐지된 PII를 내부 목적지로 라우팅하며, 판단 근거를 기록합니다.

전용 제어 영역이 정책과 예산 사용 권한을 관리합니다. 각 `mayu` 데이터 영역은 에이전트 가까이 또는 공유 게이트웨이에서 추론 요청마다 이를 집행합니다.

<div class="ip-actions" markdown>

[첫 요청 보내기](getting-started/quickstart.md){ .md-button .md-button--primary }
[inferplane의 강점](why-inferplane.md){ .md-button }

</div>
</div>

<div class="ip-facts">
<span>제어 영역·데이터 영역 분리</span><span>자체 호스팅 · Apache-2.0</span><span>두 개의 정적 Go 바이너리</span><span>알파</span>
</div>

## 중앙에서 관리하고 각 게이트웨이에서 집행합니다. {#your-models-one-place-to-govern-access}

<div class="ip-diagram" tabindex="0" role="region" aria-label="아키텍처 다이어그램. 작은 화면에서는 가로로 스크롤할 수 있습니다." markdown>

![운영자는 추론 경로와 분리된 inferplaned에 정책을 설정합니다. inferplaned는 mayu에 정책과 예산 사용 권한을 배포합니다. 에이전트의 프롬프트는 mayu에서 접근·개인정보·라우팅·예산 검사를 거쳐 승인된 공급자로 전달됩니다. 공유 프로파일만 승인마다 Postgres를 호출합니다.](assets/architecture.svg)

</div>

[아키텍처와 장애 경계 알아보기](architecture.md) · [다이어그램 크게 보기](assets/architecture.svg)

**`inferplaned`가 관리하고 `mayu`가 집행합니다.** 프롬프트와 응답 스트림은 에이전트·데이터 영역·공급자 사이에서 이동합니다. 제어 영역 HTTP 서비스를 통과하지 않으며 요청 검사에 원격 분류기가 필요하지 않습니다. 독립 실행하는 `mayu`는 로컬 정책을 읽을 수도 있습니다.

회계 범위는 명시적으로 선택합니다. 로컬 집행, 전역 정책 예산의 영속 권한을 이용한 로컬 승인, 또는 공유 Postgres 기반 키·요청률·토큰·금전 승인을 사용할 수 있습니다. 공유 프로파일은 승인마다 DB를 호출하며, 제어 영역 분리가 이 의존성을 없애지는 않습니다.

<div class="grid cards" markdown>

-   **거버넌스 설정을 요청마다 자동 적용**

    가상 키, 모델 접근 권한, 팀·사용자에 매칭되는 정책을 설정합니다. 각 게이트웨이가 과금 호출 전에 유효한 제한을 적용하며 폴백에도 같은 제한을 집행합니다.

    [자동 적용되는 동작 보기 →](why-inferplane.md#configure-the-rules-the-gateway-applies-them)

-   **예산에 따라 모델 경로 자동 전환**

    지출 임계치와 승인된 경제형 목적지를 지정합니다. 엄격 전환을 활성화하면 이후 시도의 목적지를 제한하고, 별도 하드 한도로 지출을 중단할 수 있습니다.

    [예산 전환 시나리오 보기 →](why-inferplane.md#one-configuration-several-automatic-decisions)

-   **PII를 승인된 내부 경계로 자동 라우팅**

    지원되는 요청 내용을 로컬에서 검사합니다. 내부 전용 라우팅, 검증된 마스킹, 차단 중에서 정책을 정하며 폴백에도 같은 개인정보 제한이 유지됩니다.

    [개인정보 보호와 라우팅 설정 →](adaptive-routing.md)

-   **자동 판단과 비용의 근거 추적**

    요청 모델, 선택 경로, 실제 시도와 사용량·거부 이유를 함께 확인합니다. 권한 프로파일은 시도 전에 예산을 예약하고 사용량이 불완전하면 미확정 지출을 유지합니다.

    [영속 회계 알아보기 →](durable-budgets.md)

</div>

## 클라이언트 사용은 간단하게, 정책은 명확하게. {#keep-the-client-simple-make-the-policy-explicit}

코딩 에이전트는 `auto` 같은 허용된 별칭을 계속 요청할 수 있습니다. 컨텍스트 라우팅을 활성화하면 mayu가 요청 크기와 설정된 키워드를 평가하고, 접근·개인정보·예산 제한 안에서 호환되는 목적지를 선택합니다. 컨텍스트는 평가를 위한 **Shadow**로 시작하지만 PII 규칙은 이미 집행됩니다. 모델 기능, 내부 경계, 가격은 운영자가 검토해야 하는 입력입니다.

예를 들어 제공된 적응형 정책은 탐지된 PII를 승인된 내부 `economy` 모델로 보냅니다. 예시의 월 $100 전환 임계치가 활성화되면 엄격 예산 라우팅이 지정된 모델 이름을 `economy`로 전환합니다. 별도의 $150 하드 한도는 승인할 수 없는 요청을 계속 차단합니다. 이 동작들은 함께 적용되며 서로를 우회하지 않습니다.

[시나리오와 설정 과정 보기](why-inferplane.md#one-configuration-several-automatic-decisions). 유한 PII 탐지기와 규칙 기반 컨텍스트 신호에는 명시된 한계가 있습니다. 자동 라우팅이 모든 개인정보 탐지, 최적 모델 선택, 실측 비용 절감을 보장하지는 않습니다.

## 작게 시작하고 필요한 집행 범위를 선택하세요. {#start-small-select-the-right-enforcement-scope}

| 다음 목표 | 시작 문서 |
| --- | --- |
| 게이트웨이 하나로 실제 요청 평가 | [로컬 빠른 시작](getting-started/quickstart.md) |
| 관리되는 개발자 장비 전체의 금전 예산 집행 | [노드별 영속 예산](durable-budgets.md) |
| 여러 게이트웨이에서 키·요청률·토큰 할당량·예산 공유 | [공유 Postgres 거버넌스](shared-governance.md) |
| 운영 환경 요구 사항 충족 여부 판단 | [상용화 준비도](operations/production-readiness.md) |

!!! note "현재 릴리스 상태를 정확히 확인하세요"
    inferplane은 알파입니다. 동작하는 구현과 회귀 테스트가 운영 환경 검증을 대신하지는 않습니다. 세분화된 관리 역할, 완전한 사용자 예산 풀 계약, 복구·부하 검증, 서명된 릴리스는 남은 과제입니다. 이 저장소는 공개 가용성 SLA나 규정 준수 인증을 제공하지 않습니다.
