---
translation_source: index.md
translation_source_sha256: ad59f98e9218c337a44438399713ae359af4047f82a003cc61857e466df727fe
title: 코딩 에이전트 트래픽 거버넌스
description: 자체 호스팅 거버넌스 플랫폼으로 모델을 라우팅하고 비용과 사용 기록을 관리하세요.
hide:
  - toc
  - navigation
---

<div class="ip-hero" markdown>

<p class="ip-eyebrow">코딩 에이전트를 위한 거버넌스 플랫폼</p>

# 모든 요청에 정책을 적용하세요. {#give-every-request-a-policy}

inferplane을 통해 코딩 에이전트를 모델에 연결하세요. 접근 권한, 예산, 개인정보 보호 정책을 적용하고 요청부터 감사 기록까지 사용 내역을 추적합니다.

<div class="ip-actions" markdown>

[첫 요청 보내기](getting-started/quickstart.md){ .md-button .md-button--primary }
[배포 방식 선택](getting-started/deployment-profiles.md){ .md-button }

</div>
</div>

<div class="ip-facts">
<span>자체 호스팅</span><span>Apache-2.0</span><span>두 개의 정적 Go 바이너리</span><span>알파 · 명시된 운영 한계</span>
</div>

## 원하는 모델을 하나의 정책 체계로 관리하세요. {#your-models-one-place-to-govern-access}

<div class="ip-flow" role="img" aria-label="코딩 에이전트가 mayu에 요청을 보내면 접근 권한, 라우팅, 예산, 개인정보 보호를 검사한 뒤 설정된 공급자를 호출합니다.">
  <div><b>코딩 에이전트</b><small>Claude Code · Codex · OpenCode</small></div>
  <span class="ip-arrow" aria-hidden="true">→</span>
  <div class="ip-gateway"><b>mayu</b><small>접근 제어 · 라우팅 · 예산 · 감사</small></div>
  <span class="ip-arrow" aria-hidden="true">→</span>
  <div><b>모델 공급자</b><small>Anthropic · Bedrock · 호환 API</small></div>
</div>

`mayu`가 추론 트래픽을 처리합니다. 선택적으로 연결하는 `inferplaned`는 프롬프트나 응답 스트림을 전달하지 않고 정책과 예산 사용 권한을 배포합니다. 로컬 집행, 노드별 영속 금전 예산, 동기식 공유 Postgres 집행 중에서 선택하세요. 각각의 [가용성 조건](getting-started/deployment-profiles.md)은 다릅니다.

<div class="grid cards" markdown>

-   **누가 어떤 모델을 사용하는지 제어**

    가상 키를 발급하고 모델을 제한합니다. 과거 회계 계정을 교체하지 않고 검증된 사용자·서비스 신원을 적용할 수 있습니다.

    [사용자 식별 알아보기 →](verified-identity.md)

-   **외부 호출 전에 예산 판단**

    금전 사용 권한을 예약하고 한도를 집행합니다. 지출이 늘어나면 승인된 저비용 모델로 전환하도록 설정할 수 있습니다.

    [영속 예산 알아보기 →](durable-budgets.md)

-   **개인정보 보호 조건을 지키는 라우팅**

    모델 접근 권한, 민감 정보 규칙, 컨텍스트 분류, 폴백 제한을 각 공급자 호출에 함께 적용합니다.

    [라우팅 설정 →](policy-routing.md)

-   **사용량과 실패 원인 확인**

    요청 기록과 감사 체인을 확인하고 지연 시간, 비용, 거부 신호를 모니터링합니다.

    [게이트웨이 운영 →](operations/observability.md)

</div>

## 작게 시작하고 필요한 집행 범위를 선택하세요. {#start-small-select-the-right-enforcement-scope}

| 다음 목표 | 시작 문서 |
| --- | --- |
| 게이트웨이 하나로 실제 요청 평가 | [로컬 빠른 시작](getting-started/quickstart.md) |
| 관리되는 개발자 장비 전체의 금전 예산 집행 | [노드별 영속 예산](durable-budgets.md) |
| 여러 게이트웨이에서 키·요청률·토큰 할당량·예산 공유 | [공유 Postgres 거버넌스](shared-governance.md) |
| 운영 환경 요구 사항 충족 여부 판단 | [상용화 준비도](operations/production-readiness.md) |

!!! note "현재 릴리스 상태를 정확히 확인하세요"
    inferplane은 알파입니다. 동작하는 구현과 회귀 테스트가 운영 환경 검증을 대신하지는 않습니다. 세분화된 관리 역할, 완전한 사용자 예산 풀 계약, 복구·부하 검증, 서명된 릴리스는 남은 과제입니다. 이 저장소는 공개 가용성 SLA나 규정 준수 인증을 제공하지 않습니다.
