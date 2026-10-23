---
translation_source: ux/admin-workspaces.md
translation_source_sha256: b4deaf8604cb00614c86b75f8e5ac449ad2986da21c4e66aa8b75255ddf71e3c
---

# 관리자 UX 구성과 검증

컨트롤 플레인과 게이트웨이의 관리 화면을 하나의 시각 체계로 정리하고,
조회에서 조치까지 이어지는 운영 흐름을 구성한다. LiteLLM 공식 문서의
키 발급·지출 조회·모델 추가를 기본 작업으로 삼고, inferplane의 정책 배포와
예산 기반 모델 대체를 별도의 운영 흐름으로 완성한다.

## 화면과 작업

| 작업 | 진입점과 완료 동작 | 검증 근거 |
| --- | --- | --- |
| 코딩 도구 연결 | mayu 개요의 모델 등록 → 키 발급 → 클라이언트 설정 | 게이트웨이 브라우저 QA, 기존 등록·라우팅 테스트 |
| 가상 키 찾기·발급 | 키 목록 검색, 팀·모델·예산 입력, 1회 표시 | 브라우저 발급 및 검색 검증, 금액 폼 테스트 |
| 모델·프로바이더 수정 | 목록 검색 → 수정 → 저장; 기존 메타데이터 보존 | 실제 PUT/라우팅 Go 테스트, Node·브라우저 테스트 |
| 지출 확인 | 정산 분석 데이터와 명시적인 UTC 기간; 조회 불가 구분 | 안전 범위 밖 int64·소액·분석 장애 검증 |
| 운영 이상 조사 | CP Needs attention → 조건별 노드 → 거절 상세 → 관련 정책 | 실제 이벤트 핸들러 및 브라우저 연결 흐름 |
| 지출 한도 설정 | 일간·월간 예산 폼 → 변경 요약 → 게시 | 예산 Node 테스트 및 브라우저 QA |
| 모델 대체 정책 설정 | Guided rule → 같은 정책의 예산 → 임계점·원본·대상 선택 | 생성 문서의 Go 정책 검증 및 실제 브라우저 게시 |
| 접근·처리량·민감 입력 보호 | 목적별 폼 → 전체 문서 검토 → 게시 | Node 반례 및 Go 엄격 디코딩·의미 검증 |
| 게시 결과 조사 | 성공 메시지의 Inspect distribution → 노드 관측 확인 | 브라우저 QA |
| 탐색 | Ctrl/⌘ K, 페이지·조회된 데이터 검색, 키보드 선택, Esc 닫기 | 양쪽 화면의 브라우저 QA |
| 화면 적응 | 시스템 글꼴, 라이트·다크, 좁은 화면에서 표만 스크롤 | 390/768/1440px 전 화면 확인 |

컨트롤 플레인은 `/ui/`, 게이트웨이는 `/admin/ui/`에 있다. CP의 검색은
조회된 팀별 사용량·정책·최근 노드만 다룬다. 게이트웨이 검색은 조회된
키·팀·프로바이더·모델만 다루며, 새 키의 평문과 클라이언트 설정 예시는
검색 대상에 넣지 않는다. 두 검색 모두 서버에 별도 검색 요청을 보내지 않는다.

## 설계 기준

기존 메뉴를 기억하지 않아도 검색으로 작업에 접근한다. 빈 화면에서는 다음
단계가 보이며, 운영 경고에서는 관련 근거로 이동한다. 정책 변경은 수정한
필드와 전체 문서를 함께 검토하고 초안으로 돌아갈 수 있다. 게시 후에는
다음 하트비트의 배포 결과를 확인한다.

예산과 정산 지출은 정수 단위를 유지한다. 설정 한도와 남은 승인 크레딧은
다른 개념이므로 텔레메트리로 크레딧을 추정하지 않는다. 원래 모델로 돌아갈 수
있는 대체와 대상을 강제하여 요청을 거부할 수 있는 대체의 차이를 설명한다.
민감 입력 보호도 탐지·검사 범위와 불투명 입력 차단을 구분한다.

한도·키·정책 권한은 기존 서버가 결정한다. 이 화면은 미구현 조직 역할이나
사용자 풀을 구현한 것으로 표시하지 않는다. generation 일치나 `/healthz`는
fleet HA 또는 추론 승인 readiness의 증거가 아니다.

## 요청 결과와 비용 근거 {#request-outcomes-and-cost-evidence}

게이트웨이 Overview·Usage는 관측 완료 기록·결과 건수·`(failed + partial) / completed`와
unknown 한계를 표시합니다. 팀·모델·일별 행도 같은 근거를 담습니다. Logs는 HTTP status와
결과·제한된 오류 원인을 함께 표시하며 HTTP 200 + partial은 경고입니다. 전송 성공은
모델 품질이나 사용자의 작업 완수 지표가 아닙니다.

산정액은 근거 불완전 기록 금액과 분리합니다. 다섯 상태는 estimated(유효한 0 포함),
uncertain, legacy_unknown, unpriced, unavailable을 구분하며 unpriced/unavailable을
달러 0으로 표시하지 않습니다. `operations`/`cost_state`가 없는 구버전 API는
구버전/근거 정보 없음과 기록 금액을 표시하고 완료·성공을 만들어 넣지 않습니다.
정수 문자열·BigInt로 JavaScript 안전 정수 범위를 넘는 값도 보존합니다.

관측 범위 도움말은 원본 보관·큐 drop·lag·version-0 투영을 설명합니다. 과거 투영 건수
0도 전체 이력 증거가 아닙니다. 단위는 감사 완료 기록이며 시작만 있는 거부·미인증
401·관리자 이벤트·클라이언트가 전송하지 못한 실패는 제외합니다. Full-admin 권한과
프로파일별 승인·금전 권한은 유지합니다. [API 계약](../api-reference.md#analytics-outcome-and-cost-evidence-adr-053)을
확인하세요.

`TestAnalyticsEvidenceBehavior`는 배포 JavaScript를 실행해 렌더링 DOM·3/8 (37.5%)·
모든 비용 상태·구버전 API·HTML 형태 오류 제한·큰 정수·refresh 및 페이지 실패를
검증합니다. 2026-10-21 합성 브라우저 실행은 영어/한국어·라이트/다크·390/768/1440px의
54개 스크린샷을 저장했으며 스크롤한 모바일 근거와 네트워크 실패도 포함합니다.
로컬 가짜 API를 사용했으므로 실제 트래픽 범위나 모델 품질을 증명하지 않습니다.

## 재현

```bash
go test ./internal/controlplane/ui ./internal/server/adminui ./internal/server/configapi -race
node internal/controlplane/ui/testdata/workspace.cjs
node internal/server/adminui/testdata/routing_forms.cjs --money
python3 internal/controlplane/ui/testdata/browser_qa.py --screenshots /tmp/inferplane-cp-ux
python3 internal/server/adminui/testdata/browser_qa.py --screenshots /tmp/inferplane-gateway-ux
```

브라우저 검증은 설치된 Playwright·Chromium이 필요하다. 합성 데이터와 로컬
가짜 API만 사용한다. 운영 서비스·실제 키·AWS·실제 IdP를 호출하지 않는다.
전체 저장소의 빌드·레이스 테스트·vet·포맷·harness 검사도 AGENTS.md에 따라
실행한다.

LiteLLM보다 빠른 작업 완료나 낮은 오류율을 수치로 주장하려면 동일한 작업,
참가자 조건과 데이터로 사용자 평가가 추가로 필요하다. 현재 검증은 위의 작업을
완료하는 UX의 구현·동작·화면 품질을 증명하며, 경쟁 제품 전체 기능의 동등성이나
실측 우위를 주장하지 않는다.
