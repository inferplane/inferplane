# 예산·사용자 비용·토큰 정책 UX 점검

참고: [AWS 사내 LLM Gateway 샘플](https://aws.amazon.com/ko/blogs/tech/llmgateway-on-amazon-bedrock/).
2026-10-13에 본문과 실제 예산·대시보드 이미지를 확인했다.

기존 화면에는 예산 폼과 팀 사용량이 있었지만, 요청한 세 작업이 충분히 쉽게
이어지지는 않았다. 사용자 비용 API는 화면에 없었고, 일일 예산 입력과
일간·월간 토큰 총량 설정도 빠져 있었다. 아래 흐름으로 보완한다.

| 작업 | 기존 상태 | 현재 흐름 |
| --- | --- | --- |
| 팀·키 예산 | 월간만 입력, 기간이 모호한 label | mayu에서 일일·월간 USD를 구분하여 입력·조회·편집 |
| 개인 예산 | 계정 참조와 정책 이름을 직접 알아야 함 | CP 사용자 행의 **Set budget** → 계정 선택 완료 → 금액·기간 → 검토·게시 |
| 사용자당 비용 | API만 있고 화면에 없음 | **Users & costs**에서 비용·토큰·모델 상세; 개요에 사용자 수·평균 비용 |
| 일일 팀 토큰 | 숫자 입력은 있지만 소수를 parseInt로 자름 | mayu 팀 폼에서 일일 토큰을 정수로 검증하여 저장 |
| 월간/일간 사용자 토큰 | 전체 JSON 편집 필요 | 사용자 행의 **Token limit** → 기간·총량 → shared 프로파일 확인 → 검토·게시 |
| TPM/RPM | 의미와 총량의 차이를 알아야 함 | 분당 처리량과 달력 기준 총량을 별도 label·정책 카드로 표시 |
| 모델 대체 | 한 임계점만 가이드로 작성 | 두 임계점 작성 및 원래 요청 모델 → 단계별 대상의 시각적 표시 |

## 예산 설정

mayu `/admin/ui/`의 키·팀 폼은 **Daily budget**과 **Monthly budget**을 구분한다.
USD를 최대 소수점 6자리까지 입력하고 정수 microUSD로 변환한다. 빈칸·0은
해당 범위에 추가 한도를 두지 않는 값이며, 다른 더 엄격한 한도는 계속 적용된다.
기존 팀의 일일 예산을 불러오고 월간 한도와 함께 보존한다.

CP `/ui/`에서는 **Users & costs → Set budget**으로 개인 예산을 설정한다.
이름은 자동 생성하므로 기술적인 정책 이름을 먼저 작성할 필요가 없다. 선언된
계정 연결이 있으면 정확한 `account_ref`를 채운다. 연결이 없는 managed
보고 ID는 감사·보고용 digest일 수 있으므로 재무 계정을 추측하지 않는다.
승인된 키 조회의 `account_ref`를 명시적으로 선택한다. 잘못된 사용자 정책이나
팀 전용 정책으로 개인 설정을 대체하지 않는다.

등록 사용자·서비스 계정 키 발급도 mayu 폼에서 선택할 수 있다. 이 선택은
verified-identity 선언과 전체 관리자 권한을 요구하며 기존 서버가 최종 검증한다.
Human의 issuer·subject를 브라우저에서 받아 신원을 만들어내지 않는다.

## 사용자 비용

지출, 전체 토큰, 입력·출력·캐시 읽기·5분/1시간 쓰기를 조회한다. 팀별로
필터링하거나 사용자를 검색·정렬하고, 사용자 행에서 모델 상세로 이동한다.
출력에는 이미 reasoning이 포함되며 토큰 클래스는 한 번씩 더한다.

평균 비용은 귀속된 계정의 보고 지출을 계정 수로 나눈 값이다. 서비스 계정이
포함될 수 있으며 빈 귀속의 지출은 따로 표시하고 평균에서는 제외한다. 연결
선언이 있는 과거·현재 보고 alias는 같은 재무 계정으로 집계하되, 실제 회계
행이나 권한을 바꾸지 않는다. 모든 금액은 정수 단위를 유지한다.

이 화면은 사용자 풀·직원 수·AWS 청구서·남은 승인 크레딧이 아니다. 저장된
보고 데이터에 한정되고 도착 지연이 있을 수 있다. 조회 장애, 불완전 fallback,
귀속 누락을 구분한다. API 필터의 빈 user가 모든 사용자를 의미하므로
귀속 없는 행은 모델 조회나 개인 한도 설정으로 연결하지 않는다.

## 토큰 정책과 프로파일

- **TPM**: 분당 토큰 처리량.
- **RPM**: 분당 요청 수.
- **Daily team tokens**: default/local 게이트웨이 팀의 일일 토큰 한도.
- **Token quota**: shared Postgres 프로파일의 일간·월간 총량; 사용자 또는 팀을
  선택할 수 있으며 항상 `FailClosed`.

사용자별 RPM/TPM과 tokenQuota는 shared Postgres가 필요하다. 폼은 이 조건을
확인하도록 하며 체크박스가 프로파일을 켜거나 readiness를 증명하지는 않는다.
실제 데이터 플레인이 검증하고 거절 기록을 보고한다. 예약 중인 토큰 때문에
기록된 정산 사용량이 상한에 도달하기 전에도 새 요청이 거절될 수 있다.

default/local 개인 예산 카운터는 팀·게이트웨이별이며, node-local money 및 shared
프로파일의 별도 계약을 그대로 따른다. shared의 DB 장애는 신규 승인을 거절한다.
모든 팀을 합친 보고 지출과 로컬 한도의 카운터 범위를 혼동하지 않아야 한다.

## 샘플과 다른 부분

| AWS 샘플 기능 | inferplane에서의 해석 |
| --- | --- |
| HARD_BLOCK | hard cap·차단 동작을 사용하며 다른 hard cap도 함께 적용 |
| SOFT_WARNING + 110% 상한 | 그대로 지원하지 않음; soft threshold가 110% 차단을 만들지는 않음 |
| 예산에 따라 RPM 50%씩 낮추는 THROTTLE | 자동 예산 연동 RPM 축소는 구현하지 않음 |
| CPM·CPH 비용 속도 제한 | 현재 rate 폼은 RPM·TPM; 비용은 일간·월간 한도 |
| 팀 예산 미설정이면 항상 거절 | 기본 로컬의 0/빈칸 의미를 바꾸지 않음 |
| 한도 소진율·잔액 | 텔레메트리로 실제 남은 승인 크레딧을 꾸며내지 않음 |
| 다운그레이드 체인 | 임계점별 원 요청 모델 → 대상 매핑; 재귀적 체인은 아님 |

## 재현

```bash
go test ./internal/controlplane ./internal/controlplane/ui ./internal/server/adminui -race
node internal/controlplane/ui/testdata/user_limits.cjs
node internal/server/adminui/testdata/routing_forms.cjs --money
python3 internal/controlplane/ui/testdata/browser_qa.py --screenshots /tmp/inferplane-budget-user-token-ux
python3 internal/server/adminui/testdata/browser_qa.py --screenshots /tmp/inferplane-daily-budget-ux
```

브라우저는 설치된 Playwright·Chromium을 사용하며 합성 로컬 API만 호출한다.
실제 AWS·IdP·모델·키를 사용하지 않는다. 동작 검증은 프로파일별 정책 수용·거절,
조회 권한, 정확한 계정 참조, 검토 전 쓰기 금지, 숫자 범위, 과거 응답 무시,
모바일·테마·로그아웃을 확인한다. 실제 사람의 작업 시간·오류율 비교는 별도 평가다.
