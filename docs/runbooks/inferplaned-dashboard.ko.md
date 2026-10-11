---
translation_source: runbooks/inferplaned-dashboard.md
translation_source_sha256: 0cb9896d7ec95ca06a34c8365d569f8d8fbe7d4fa09dbea0c81f4778e7a25075
---

# Control plane 운영 대시보드 {#control-plane-operations-dashboard}

## 진입점과 범위 {#entry-point-and-scope}

**inferplaned**의 `/ui/`에서 엽니다. mayu의 `/admin/ui/`와 다릅니다. 정적 Go
바이너리에 내장되어 npm 빌드나 외부 자산이 필요하지 않습니다. 배포되는
GovernancePolicy 문서를 관리하고 사용량·하트비트 관측을 읽습니다. 가상 키 관리,
업스트림 모델 설정, 추론 전달, OTel 내보내기 활성화는 이 콘솔의 범위가 아닙니다.

[콘솔 SSO](inferplaned-console-sso.md)로 로그인하거나 읽기 자격 증명으로
`INFERPLANED_TOKEN`을 입력합니다. 빈 자격 증명은 서버가 루프백 개발 모드인
경우에만 사용합니다. 인증 실패 시 작업 공간은 열리지 않습니다. Disconnect는
자격 증명·조회 데이터·초안을 지우고 늦게 도착한 응답을 무시합니다. IdP 세션을
종료하지는 않습니다.

## 작업 공간 화면 {#workspace-views}

| 화면 | 근거와 작업 |
| --- | --- |
| Overview | 보고된 지출, 사용량이 있는 팀(등록 팀 수 아님), 입력·출력·캐시 토큰, 최근 노드, 지출 상위 5개 팀, generation 차이와 기록된 거절 |
| Usage & costs | 팀 검색·정렬, 팀별 모델 상세, 분리된 캐시 클래스, 가장 세밀한 JSON 내보내기 |
| Budgets | 팀·사용자 예산 검색, 일간·월간 필터, 새 정책 또는 기존 정책에 예산 추가, USD 한도 편집, 검토 후 게시 |
| Data planes | 현재 generation, 지원 API 버전, 최근 노드 관측, generation 비교, 마지막 하트비트, 기록된 정책 거절 상세 |
| Policies | 모든 규칙 조회, 정책·subject 검색, 기존 예산·rate·모델 접근 필드 편집, 전체 JSON 문서 생성·편집, 게시 전 검토, 정확한 이름 입력 후 삭제 |

화면의 날짜는 **양 끝을 포함하는 UTC 달력 날짜**입니다. 브라우저는 To에 하루를
더해 API의 배타적 `until`로 전달합니다. 초기 범위는 이달 1일부터 오늘까지입니다.
Apply range 또는 Refresh 후에만 새 범위를 적용합니다. 모델 상세와 내보내기는
미제출 날짜 입력이 아니라 적용된 범위를 사용합니다. 인벤토리와 정책은 현재
관측이며 사용량 기간으로 필터링되지 않습니다.

JSON 내보내기는 선택한 기간의 **모든 팀**을 포함합니다. 검색·정렬은 내보내기
범위를 바꾸지 않습니다. 화면의 집계 표가 아니라 명시적 날짜 경계를 사용하는
`/v1alpha1/usage/export`를 이용합니다. 저장소 장애나 불완전한 JSON 스트림을
정상 다운로드로 바꾸지 않습니다. 현재 브라우저에서 결과를 버퍼링하므로 매우
큰 데이터는 날짜 범위를 좁혀 내보내세요.

## 지출과 배포 상태 해석 {#reading-spend-and-distribution-honestly}

- JSON 파싱·합산·표시·내보내기 내내 정수 microUSD를 유지합니다. JavaScript의
  안전한 숫자 범위를 넘는 int64도 보존합니다. 소수점 6자리를 모두 표시하며
  부동소수점 비용 계산을 하지 않습니다.
- 총 토큰은 입력·출력·캐시 읽기·5분 쓰기·1시간 쓰기를 각각 한 번 더합니다.
  출력에는 이미 reasoning이 포함됩니다. 보고된 집계 사용량이며 요청별 컨텍스트
  점유량이나 AWS 청구서가 아닙니다.
- degraded 응답은 내구 저장소 이력이 불완전하고 최근 메모리로 대체되었다는
  경고를 표시합니다. 없거나 사용할 수 없는 API를 지출 0이나 정상적인 빈
  fleet으로 표시하지 않습니다. fleet·정책 API가 없는 telemetry-only 배포도 지원합니다.
- 데이터 플레인 API는 **10분 이상** 하트비트가 없는 노드를 생략합니다. 콘솔은
  생략된 노드를 식별하거나 offline이라고 단정할 수 없습니다. generation 일치는
  보고된 식별자 비교일 뿐 readiness·승인 권한·DB 상태·fleet HA를 증명하지 않습니다.
  거절은 한도가 있는 과거 기록으로 현재 실패 상태가 아닐 수도 있습니다.
- 갱신은 수동이며 각 스냅샷의 조회 시간을 표시합니다. API 읽기는 별도 요청이므로
  트랜잭션으로 일관된 fleet 스냅샷이 아닙니다. 실시간 예산 원장, 리스 취소, 노드
  재시작, 노드 전체 금융 권한 제어 기능은 없습니다.

## 정책 쓰기 권한과 검토 {#policy-write-authority-and-review}

### 예산 설정 {#setting-a-budget}

**Budgets → New budget**에서 **Create a new policy**를 선택하고 정책 이름과
팀 또는 사용자를 입력합니다. 이어 예산 이름, 일간·월간 윈도우, USD 한도를
설정합니다. 검증된 사용자는 선언된 재무 계정 참조를 사용합니다. 콘솔이 이메일이나
감사 digest로 신원을 추론하지 않습니다. 기존 정책을 선택하면 그 정책의 subject를
바꾸지 않고 규칙을 추가합니다. 예산 이름은 해당 정책의 다른 규칙 이름과 달라야
합니다. 일간·월간 한도를 함께 적용하려면 두 규칙을 추가합니다. 두 한도와 다른
일치하는 한도가 모두 독립적으로 적용됩니다.

금액은 정책의 milliUSD 정밀도에 맞춰 소수점 세 자리까지 받습니다. 십진 문자열을
정수 연산으로 변환하므로 `$12.345`는 `limitMilliUSD: 12345`가 됩니다.
0·음수·지수 표기·초과 소수 자릿수·내부 microUSD 범위를 넘는 값은 검토 전에
거절합니다. 새 예산의 기본값은 hard cap과 `FailClosed`입니다. hard cap을 해제하면
soft threshold를 생성하며, 기존 soft 예산을 편집할 때는 failurePolicy를 보존합니다.

**Review budget**에서 변경된 전체 정책을 확인하고 **Publish policy**로 기존
정책 PUT API에 저장한 뒤 목록을 다시 읽습니다. **Edit budget**은 다른 규칙,
lease 설정, 수정하지 않은 기본값과 추가 필드를 보존합니다. hard cap을 켜면
`FailClosed`를 강제합니다. unlimited 선언과 고급 규칙·lease 변경은 **Policies**의
전체 문서 편집기를 사용합니다. Budgets에는 설정된 한도를 표시하며, 남은 승인
크레딧이나 보고 지출로 추정한 예산 사용률은 표시하지 않습니다.

**New budget**이 비활성화되면 API 부재·파일 기준 저장소·쓰기 자격 증명 부재를
안내 문구로 구분합니다. **Policy access & advanced settings**에서 자격 증명
설정을 엽니다. 쓰기 토큰을 입력해도 파일 기준 배포가 쓰기 가능한 상태로 바뀌지는
않습니다.

`writable`은 저장소 연결 여부이며 **조회자의 쓰기 권한이 아닙니다**. 정적 토큰
배포에서는 Policy authority에 별도 `INFERPLANED_POLICY_WRITE_TOKEN`을
입력합니다. 메모리에서 선택할 뿐 시험 쓰기로 검증하지 않습니다. SSO는 콘솔
OIDC 자격 증명을 사용합니다. 기존 서버 미들웨어만 쓰기 권한을 결정하며 하트비트
읽기 토큰은 쓰기 권한을 얻지 않습니다. 게시가 거절되면 검토 창과 초안을 유지하여
재시도하거나 취소할 수 있습니다.

빠른 편집은 조회한 전체 문서를 복제합니다. 예산 규칙별로 편집하여 일·월 한도가
공존할 수 있고, 생략된 기본값·period·화면에 없는 필드·다른 규칙을 보존합니다.
일반 폼 저장으로 기존 FailClosed를 약화시키지 않으며 hard cap을 켜면 FailClosed를
강제합니다. 명시적 unlimited 선언은 전체 문서 편집기로 변경합니다. 새 빠른 편집
숫자는 음수가 아닌 안전한 정수여야 합니다. 전체 문서 편집기는 int64 정밀도를
보존하며 소수·지수 표기 대신 일반 JSON 정수 리터럴을 사용합니다.
hard cap·failurePolicy 호환성과 identity binding 등 의미 검증은 서버에 맡깁니다.

게시는 편집·검토와 명시적 PUT의 2단계입니다. 삭제는 정확한 이름 입력 후 명시적
DELETE가 필요합니다. **서버측 revision 잠금은 없습니다**. 동시 편집자는 별도로
조율해야 하며 검토 창은 compare-and-swap이 아닙니다. 게시는 CP에 영속화·즉시
적용한 뒤 노드가 하트비트로 가져갑니다. 이후 generation과 거절 기록을 확인하세요.
성공 후 재조회는 다른 빠른 편집 초안을 버리며 검토 창에 이 사실을 알립니다.
파일 기준 배포는 읽기 전용입니다. `INFERPLANED_POLICY_DSN`을 켜거나
`--policies` 파일을 수정하고 다시 배포하세요.

## 보안과 검증 {#security-and-verification}

공개 셸에는 fleet·팀·지출·자격 증명이 없습니다. 연결 시에만 보호된 데이터 읽기를
시작합니다. 읽기·쓰기 토큰은 별도 클로저 변수이고 영속화하지 않습니다. SSO
리다이렉트 동안에는 ADR-037의 짧은 수명 PKCE 값 3개만 유지합니다. 요청은 같은
origin으로 보내며 리다이렉트를 거부하고 브라우저 쿠키를 보내지 않습니다. API
값은 HTML이 아니라 DOM 텍스트로 렌더링합니다. `default-src 'self'` CSP와
인라인 스크립트·스타일·외부 폰트 금지는 유지합니다. 테마 선택도 페이지 안에서만
유효합니다. Go API·인증·readiness·정책·리스·금융 권한·토큰 카운트·라우팅 계약은
변경하지 않습니다.

소스는 `internal/controlplane/ui/static/{index.html,style.css,app.js}`입니다.
선택적인 [CloudFront 자산 배포](inferplaned-cloudfront.md)는 데이터 없는 파일만
별도로 게시하며 CP를 재시작하거나 API를 변경하지 않습니다.
같은 패키지에 핸들러와 Node 표준 라이브러리만 사용하는 동작 테스트가 있습니다.
선택적인 실제 브라우저 QA는 로컬 합성 API만 사용하며 운영 서비스나 AWS를 호출하지 않습니다.

```bash
go test ./internal/controlplane/ui ./internal/controlplane -race
node internal/controlplane/ui/testdata/dashboard.cjs
node internal/controlplane/ui/testdata/budgets.cjs
python3 internal/controlplane/ui/testdata/browser_qa.py --screenshots /tmp/inferplane-cp-dashboard-qa
```

브라우저 검증은 이미 설치된 Playwright와 Chromium이 필요합니다. 로그인·토큰
지우기·정확한 지출·탐색·검색·내보내기·정책 CRUD와 검토·거절 텍스트 안전성·CSP·
degraded 이력·테마·모바일 너비·Disconnect를 확인합니다. Go 테스트는 브라우저나
npm 설치가 필요하지 않습니다.

## SSO 요청 순서 {#sso-request-scheduling}

콘솔은 초기 조회·갱신·모델 상세·내보내기를 포함해 OIDC 인증 API 읽기와 쓰기를
순서대로 실행합니다. 현재 배포된 CP의 JWKS miss limiter는 서명 검증이 진행 중이면
다른 서명 검증을 거절합니다. 기존 초기 조회 3개의 병렬 실행 때문에 유효한 로그인도
401로 끝날 수 있었습니다. JWT 검증·Cognito·그룹 설정을 완화하거나 거절된 요청을
재시도하지 않고, CP 재시작 없이 콘솔 자체의 동시 요청을 피합니다. Disconnect는
진행 중 요청을 중단하고 이전 세션의 대기 요청을 차단합니다. 정적 bearer 요청은
독립적으로 동작합니다. 서로 다른 탭·클라이언트의 서버측 경합은 여전히 가능하며
전역 검증기 재설계가 아닌 클라이언트 측 완화책입니다. 진단용으로 ID 토큰을 공유하지 마세요.

`TestSSOBootstrapWithColdRealVerifier`는 느린 로컬 가짜 IdP와 실제 Go 서명·JWKS·
그룹 검증을 통해 배포되는 SSO 콜백·조회 코드를 실행합니다. 초기 조회와 갱신의
6개 읽기가 동시 서명 검증 없이 모두 200인지 확인합니다. Node 회귀는 잘못된
state·nonce, 실제 401 거절, 인증 재시도 없음, PKCE 정리와 대기 요청 중 연결 해제를
검증합니다. 격리된 테스트이며 실제 Cognito 사람 로그인을 완료했다는 증거는 아닙니다.
