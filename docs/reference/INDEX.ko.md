---
translation_source: reference/INDEX.md
translation_source_sha256: 53a88446f9c097477458b9d42eb269d08bcec0a5c0abe9006fa53883cd3ad72d
---

# 구현 레퍼런스 색인 {#implementation-reference-index}

계층별 구성 요소, 주요 결정, 코드 위치를 설명합니다. 현재 구조는 [아키텍처](../architecture.md), v0.1의 역사적 배경과 구현 대비 주석은 [초기 설계](../specs/2026-06-10-inferplane-gateway-design.md)에서 확인하세요.

<!-- AUTO-MANAGED:reference-index -->
| 계층 | 문서 | 범위 |
| --- | --- | --- |
| 인프라 | [infrastructure.md](infrastructure.md) | Dockerfile, Helm, Grafana |
| API | [api.md](api.md) | 데이터·관리 영역, 수신 핸들러, 변환 |
| 데이터 | [data.md](data.md) | 키, 감사 체인, 거버넌스 저장소 |
| 보안 | [security.md](security.md) | 인증, RBAC, 비밀 분리, 메트릭 |
| 에이전트·LLM | [agent-llm.md](agent-llm.md) | 공급자 추상화, 정규 스키마 |
| Bedrock 코딩 모델 | [bedrock-coding-models.md](bedrock-coding-models.md) | 모델 관찰과 검증 한계 |
| 컨텍스트 윈도우 검증 | [context-window-validation.md](context-window-validation.md) | 게이트웨이·클라이언트 컨텍스트 검증 근거 |
| 가격 | [pricing.md](pricing.md) | 가격표, 단위, 운영자 재정의 |
<!-- /AUTO-MANAGED:reference-index -->

project-init의 `/add-reference-doc <layer>`로 계층을 추가할 수 있습니다. 아직 없는 frontend·ui·iac 데이터 파생 계층은 이 Go 게이트웨이에 해당하지 않습니다.

운영자용 라우팅 스키마·예제·한계·배포 기준은 [정책 기반 라우팅](../policy-routing.md)(ADR-043)을 참고하세요.
