# P1: 요청 결과·비용 근거·스트리밍 오류 가시성 스펙

- 작성일: 2026-10-21
- 상태: 2026-10-21 사용자 설계·계획 승인(“네 구현”). Tasks 1–5 구현·검증·독립 최종 리뷰 완료. 최종 리뷰의 I1–I3/M1–M2 수정과 범위 한정 독립 재리뷰가 완료됐으며 열린 항목과 새 결함은 없다. 통합 방식 선택 대기.
- 기준: `origin/main` `77ad9239aa444d950abb02c06e62fc839b3082c3`.
- 실행 방식: Superpowers의 스펙 → builder check → 작업계획 → 사용자 리뷰 → 담당별 TDD 구현 → 독립 리뷰.
- 사용자 요구: inferplane의 오류율·호환성·비용 가시성 문제를 개선하고 관리자 UI에서 확인한다.
- 이번 범위는 앞서 제시한 P1 세 항목이다. CLI 모델 프로필 확대와 추가 유료 장문 실험은 후속 P2다.

## 1. 문제와 원하는 결과

Astra의 자동 압축 호환성 수정과 반복 CLI 보고서는 이미 main에 반영됐다. 이번 작업은 그 테스트를 재실행하는 작업이 아니라, 운영 중 같은 실패를 관리자 화면에서 알아볼 수 있도록 하는 작업이다.

현재 `internal/analytics/index.go`의 `Billable`과 `sink.go`, Postgres `ingestBatch`는 `request_completed && cost != nil`만 집계한다. 비용을 계산하지 못한 실패는 감사 로그에 있어도 관리자 Logs와 Usage에서 누락될 수 있다. 기존 집계의 `requests`는 모든 수신 요청 수가 아니다.

`audit.UsageRef.Evidence`, `CostRef.PricingMissing`, `OutcomeRef.Partial`은 이미 존재하지만 analytics 투영에는 남지 않는다. UI `loadLogsPage`는 `cost_micros || 0`을 통화로 표시하여 미확정과 실제 0을 구분하지 못한다.

Responses `attempt.stream`은 응답 전송이 시작된 뒤 실패하면 HTTP 200을 유지하고 `partial=true`로 기록한다. 이때 `fail` 경로가 타입으로 확인한 오류 코드를 저장하지 않아, 컨텍스트 초과 같은 진단 근거가 사라질 수 있다.

관리자는 다음을 구분할 수 있어야 한다.

1. 감사 로그에서 관측한 완료 기록 중 성공·실패·부분 응답·결과 미확정 건수.
2. 산정 가능한 비용과 근거가 불완전한 기록 금액, 단가 없음, 비용 기록 없음.
3. HTTP 200 이후의 실패와 전송 이전 HTTP 오류.
4. 구버전 기록의 한계와 아직 재생하지 않은 과거 집계.

## 2. 범위와 결정의 출처

사용자가 지정한 범위는 P1부터 스펙·계획·담당별 구현을 진행하는 것이다. 아래 세부 설계는 저장소 근거를 바탕으로 작성자가 선택했으며, 사용자 리뷰 대상이다.

- 기존 Go/SQLite/Postgres/정적 JavaScript를 유지한다. 새 런타임 의존성은 추가하지 않는다.
- 기존 금액·토큰·`requests` 필드의 계산 대상은 유지하고, 운영 관측 필드를 추가한다.
- 감사 레코드 타입을 확장하지 않고 기존 근거에서 투영한다. 원본 감사 바이트는 수정하지 않는다.
- `request_completed`만 관측 분모에 포함한다. `request_started` 거부, 미인증 401, 관리자 이벤트, 클라이언트에서 전송조차 못 한 실패는 포함하지 않는다.
- 완료 기록 한 개가 집계 단위다. 재시도/폴백의 기록 단위는 ingress마다 다를 수 있으므로, 고유 사용자 요청 수·세션 성공률·전체 트래픽 오류율이라고 부르지 않는다.
- 성공은 전송 결과의 분류다. 모델 답변 품질이나 작업 완수 증명이 아니다.
- 공개 성능 보고서의 기존 실측 수치는 변경하지 않는다.

## 3. 공통 제약

- Go 1.26.9, `CGO_ENABLED=0`, 새 런타임 의존성 없음.
- 모든 비용은 int64 microUSD이며 기존 round-half-even 계산을 유지한다. 표시에서도 큰 정수를 반올림해 잃지 않는다.
- 금전 authority, reserve/settle/refund, quota, RBAC, identity, privacy routing의 의미를 변경하지 않는다.
- 미확정 사용량을 실제 0이나 환불 근거로 바꾸지 않는다. analytics는 재구축 가능한 읽기 모델이다.
- 감사 스키마·필드 순서·기존 줄 바이트·hash chain은 변경하지 않는다.
- 새 메트릭 라벨과 원문 오류·프롬프트·키·세션 식별자 노출은 추가하지 않는다.
- 인증 없는 401 감사는 추가하지 않는다. count API의 항상 HTTP 200 계약은 유지한다.
- HTTP 응답이 시작된 스트림은 재시도하지 않는다. 같은 프로토콜 성공 응답 바이트는 보존한다.
- 원본 dirty worktree와 미병합 Converse/평가 UI 변경은 보존한다. 구현은 별도 worktree에서 진행한다.
- 임시 파일과 테스트 산출물은 `~/.tmp` 아래에 둔다.

## 4. 투영 계약

### 4.1 포함 조건과 결과 분류

`Observable(r audit.Record) bool`은 `r.Event == "request_completed"`만 검사한다. 기존 `Billable`은 그대로 남겨 금융 보고서와 기존 집계 호환성을 유지한다.

`outcome_state`는 상호 배타적인 네 값이다. 순서대로 적용한다.

| 조건 | 값 |
|---|---|
| 원본 Outcome가 있고 Partial=true | `partial` |
| Outcome 없음, status<200 또는 status>599 | `unknown` |
| 유효한 status이고 비어 있지 않은 Outcome.Error, 또는 status>=300 | `failed` |
| 나머지 200–299 | `succeeded` |

부분 응답을 실패 건수에도 중복 가산하지 않는다. UI는 실패율을 `(failed + partial) / completed`로 계산하고 같은 자리에서 분자·분모와 unknown 건수를 보인다. `completed=0`이면 비율은 `—`다. unknown을 성공으로 취급하지 않는다. 결과 미확정이 있으면 관측 비율이 완전하지 않음을 표시한다.

### 4.2 비용 상태

Cost가 존재한다고 공급자 청구서의 확정 금액이 되는 것은 아니다. 완전한 근거를 가진 값도 산정액(estimate)으로 부른다.

`cost_state` 분류는 아래 우선순위다.

| 조건 | 값 | UI 의미 |
|---|---|---|
| Cost=nil | `unavailable` | 비용 기록 없음; 0으로 표시 금지 |
| Cost.PricingMissing=true | `unpriced` | 단가/계산 불가; 0으로 표시 금지 |
| Cost 있으나 Usage=nil 또는 Usage.Evidence=nil | `legacy_unknown` | 사용량 근거 없음; 구버전 포함 |
| Outcome.Partial 또는 Usage.Estimated 또는 Evidence의 InputKnown/OutputKnown/InputTotalKnown/CacheWriteTTLKnown 중 하나가 false 또는 AccountingUncertain=true 또는 기록 금액<0 | `uncertain` | 기록된 금액은 있으나 완전한 산정액이 아님 |
| 나머지 | `estimated` | 근거를 갖춘 산정액; 금액 0도 유효 |

부분 응답의 관측 usage가 완전해 보여도 전체 호출 비용을 안다고 단정하지 않는다. 비용 상태와 전송 성공 여부는 독립적이다. HTTP 400에도 완전한 사용량·금액 근거가 있다면 비용은 `estimated`일 수 있다.

### 4.3 Go/API 데이터

`internal/analytics/evidence.go`에 순수 분류를 둔다. 각 저장소가 같은 분류 함수를 사용한다.

- `type OutcomeState string`, `type CostState string`.
- `type RecordEvidence struct { HasCost bool; OutcomeState OutcomeState; CostState CostState; Partial bool; ErrorCode string }`.
- `func Observable(r audit.Record) bool`.
- `func EvidenceOf(r audit.Record) RecordEvidence`.
- `func BoundedErrorCode(code string) string`: 아래 허용 목록 외 비어 있지 않은 값은 `upstream_error`, 빈 값은 빈 값.

기존 `Totals`, `TeamRow`, `ModelRow`, `DayPoint`에 `Operations OperationTotals`를 `json:"operations"`로 추가한다. `OperationTotals`는 아래 int64 필드를 갖는다. JSON 이름은 snake_case다.

| Go 필드 | 의미 |
|---|---|
| Completed, Succeeded, Failed, Partial, Unknown | 완료 기록 수와 결과 분류 |
| EstimatedRequests, UncertainRequests, UnpricedRequests, UnavailableRequests, LegacyUnknownRequests | 상호 배타적 비용 상태별 건수 |
| EstimatedCostMicros | estimated 상태의 산정액 합계 |
| UncertainRecordedCostMicros | uncertain + legacy_unknown 상태에 남은 금액 합계 |
| LegacyProjectionRecords | 원본을 아직 재생하지 못해 projection_version=0인 행 수 |

불변식: `completed = succeeded + failed + partial + unknown = estimated_requests + uncertain_requests + unpriced_requests + unavailable_requests + legacy_unknown_requests`.

`Event`에 `HasCost bool`, `OutcomeState OutcomeState`, `CostState CostState`, `Partial bool`, `ErrorCode string`, `ProjectionVersion int`를 추가한다. JSON은 각각 `has_cost`, `outcome_state`, `cost_state`, `partial`, `error_code`, `projection_version`이다. `error_code`는 빈 값이면 생략한다. 기존 `cost_micros`는 숫자형으로 유지하되 UI는 상태를 먼저 읽는다.

기존 합계의 비용·토큰·requests는 `has_cost=true` 행에서만 계산한다. 비용 없는 완료 기록만 있는 모델/팀/날짜도 새 관측 필드를 보여주기 위해 행이 생길 수 있으며, 해당 행의 기존 금액과 requests는 0이다. `Recent`는 모든 관측 완료 기록을 반환한다. 기존 keyset 페이지 크기 200과 순서는 유지한다. API route·메서드·full-admin 권한은 바꾸지 않는다.

## 5. SQLite와 Postgres 이행

`events`에 `projection_version`(기본 0), `has_cost`(기본 true/1), `outcome_state`(기본 unknown), `cost_state`(기본 legacy_unknown), `partial`(기본 false/0), `error_code`(기본 빈 문자열)을 additive migration으로 추가한다. 과거 행은 이전 필터 때문에 Cost가 있었다는 사실만 안다. 과거 HTTP 200만으로 성공/완전한 비용을 추정하지 않는다.

새 코드가 원본 감사 기록을 투영하면 version=1을 쓴다. SQLite는 기존 version=0 행을 Replay에서 1로 갱신할 수 있게 하되, 이미 version=1인 같은 ID는 기존 first-write-wins 계약을 유지한다. 기록 ID·팀·모델 등 원본 식별 데이터를 중복으로 만들지 않는다. Postgres는 기존 corrected-record upsert 계약을 유지하고 변경 전/후 rollup key를 모두 재계산한다. 팀/모델/날짜가 달라진 correction에서 옛 그룹을 남기지 않는다.

SQLite `sink.Write`, `Ingest`, `Replay`, freshness 갱신 조건을 모두 Observable로 바꾼다. 큐 용량 4096, 비차단 drop, 재생 복구, close drain은 유지한다. 불완전한 마지막 JSONL 줄은 계속 건너뛴다. 소실된 원본을 만들어 복구했다고 주장하지 않는다.

Postgres `rollup_day`에 모든 OperationTotals 대응 열을 추가하고, 최초 업그레이드의 같은 schema migration transaction 안에서 기존 events를 근거로 rollup을 다시 계산하고 영속 마이그레이션 마커를 함께 커밋한다. 현재 스키마를 다시 열 때는 마커를 확인해 재계산을 건너뛰며, 실패하면 열·backfill·마커를 함께 롤백해 재시도한다. 기존 request_count/금액/토큰 열은 has_cost 조건을 적용한다. advisory lock, 단일 connection, statement timeout 복원은 유지한다. 중간 상태를 정상 집계로 노출하지 않는다.

Postgres의 checkpoint가 이미 지나간 비용 없는 기록은 additive migration만으로 복구되지 않는다. 운영자가 기존 `POST /admin/analytics/rebuild`를 실행하면 epoch fencing 후 보관된 감사 원본을 재생한다. 이 작업은 derived events/rollup/checkpoint에만 작용하며 권한·금전 테이블을 건드리지 않는다. 자동 rebuild는 하지 않는다. 롤아웃은 모든 analytics writer 업그레이드 후 재생 순서로 문서화한다. 혼합 버전 writer 구간의 완전성은 보장하지 않는다.

`legacy_projection_records=0`은 모든 과거 감사가 수집됐다는 증명이 아니다. 원본 보관 기간, 큐 drop, 수집 지연, `request_started`만 있는 요청의 한계는 UI 도움말과 운영 문서에 유지한다.

## 6. Responses 스트리밍 오류

공급자 코드 허용 목록은 현재 `providers.ResponseErrorCode`가 인정하는 `context_length_exceeded`, `invalid_encrypted_content`다. 임의 텍스트에서 키워드를 찾아 분류하거나 새 공급자 코드를 추측하지 않는다.

게이트웨이에서 직접 확인한 원인에는 `request_canceled`, `upstream_timeout`, `stream_incomplete`, `response_incompatible`, `usage_missing`, `client_write_failed`, `upstream_error`를 사용한다. analytics allowlist는 이 집합과 위 두 공급자 코드의 합집합이다.

`responsesapi`의 `streamFailureCode(cause error) string`은 순서대로 typed UpstreamError의 허용 코드, errors.Is(context.Canceled), errors.Is(context.DeadlineExceeded), 명시적 gateway sentinel, 일반 upstream_error를 반환한다. `Error()` 문자열은 사용하지 않는다.

commit 전에는 기존 HTTP status와 retry 규칙을 유지한다. commit 후에는 HTTP 200 + partial=true를 유지하며 audit Outcome.Error와 gateway가 생성하는 SSE error의 code에 같은 bounded code를 남긴다. 클라이언트 쓰기가 실패하면 추가 SSE 쓰기를 강제하지 않고 client_write_failed를 감사에 남긴다. 사용량이 이미 관측됐으면 기존 방식으로 정산하고, 없어도 완료 관측은 남는다. 성공·실패에서 settlement/finish는 정확히 한 번이다.

native transport의 기존 이벤트 바이트를 다시 쓰거나 provider-owned 상태를 해석하지 않는다. 이미 받은 native error frame에 typed 오류 근거가 없다면 구체적 공급자 코드를 추정하지 않고 일반/불완전 스트림 코드만 남긴다. 기존 wire 호환성과 회계 경로 테스트를 유지한다.

## 7. 관리자 UI

Usage와 Overview는 새 `operations.completed`를 ‘관측 완료 기록’으로 표시한다. 성공·실패·부분 응답·결과 미확정 건수를 함께 보인다. 모델/팀/날짜 표에도 같은 의미를 사용한다.

금액은 ‘산정 가능한 비용’과 ‘근거 불완전 기록 금액’을 구분한다. 기존 총 cost_micros를 보여주는 곳에는 ‘기록된 금액’이라고 붙인다. 확정 청구액 또는 전체 실제 비용이라고 쓰지 않는다. unknown 종류별 건수와 과거 투영 미갱신 건수를 보여준다.

Logs는 HTTP status와 outcome_state를 함께 표시한다. HTTP 200 + partial은 정상 성공처럼 보이지 않아야 한다. 비용 표시 규칙은 `estimated → usd(amount) + 산정액`, `uncertain/legacy_unknown → 기록 금액 + 근거 불완전/근거 없음`, `unpriced → 단가 없음`, `unavailable → 미확정`이다. 금액 0도 이 상태 규칙을 따른다. 허용된 error_code만 텍스트 노드로 표시한다.

구버전 API가 operations 또는 cost_state를 제공하지 않으면 ‘구버전 집계/근거 정보 없음’을 표시하고 기존 기록 금액만 표시한다. 값을 0/성공으로 만들어 넣지 않는다. 한국어·영어 문구를 함께 갱신한다. 기존 BigInt 처리와 usd 함수를 재사용한다.

## 8. 수락 테스트

동일한 8개 완료 기록을 SQLite/PG/API/UI의 공유 계약 예제로 사용한다.

| ID | 결과 | 비용 근거 | 기록 금액 µUSD |
|---|---|---|---:|
| A | succeeded | estimated | 1000 |
| B | failed | unavailable | 없음 |
| C | partial, HTTP 200 | uncertain | 2000 |
| D | succeeded | unpriced | 0 |
| E | succeeded | legacy_unknown | 50 |
| F | succeeded | estimated | 0 |
| G | unknown | unavailable | 없음 |
| H | failed, HTTP 400 | estimated | 300 |

기대: completed=8, succeeded=4, failed=2, partial=1, unknown=1. 비용 상태 건수는 estimated=3, uncertain=1, unpriced=1, unavailable=2, legacy_unknown=1. estimated_cost=1300, uncertain_recorded_cost=2050. 기존 requests=6, cost_micros=3350. 오류/부분 비율은 3/8이며 unknown=1이 함께 표시된다. 같은 ID 재생 후 값은 변하지 않는다.

추가 필수 사례: nil Outcome, 빈 Error/알 수 없는 Error, 완전한 0 사용량, missing evidence, partial+missing price 우선순위, truncated JSONL, migrated version=0, PG key correction, stale lease epoch, full-admin 401/403, int64>2^53의 UI 표시, 운영 원본 접근 불가 시 미복구 표기, typed 오류와 원문 비노출, post-commit no retry, client disconnect, settlement once.

기본 검사는 네트워크·자격증명·실제 IdP 없이 실행한다. PG 통합 검사는 별도의 일회용 로컬/CI 테스트 DB에서만 실행하며 production DSN을 사용하지 않는다. PG 검사가 skip되면 통과로 보고하지 않는다.

## 9. 단계와 완료 기준

- P1-A: 공통 관측 계약 + SQLite/PG 투영. 오래된 집계와의 수치 호환성, 이행, 재생/fencing 검증.
- P1-B: 관리자 API/UI의 상태·비율·비용 가시성. 실제 shipped JS의 동작 테스트와 한국어/영어 표시 확인.
- P1-C: Responses 오류 코드 보존. typed error, partial, no retry, 기존 정산 회귀 검증.
- 통합: ADR-053(번호 재확인), API/운영/콘솔 문서, 전체 필수 검사와 독립 리뷰. 런타임 배포·DB rebuild는 별도 구체적 변경 검토 대상으로 남긴다.

원본 현재 변경사항을 덮어쓰지 않고 각 담당자의 파일 소유권을 계획에 지정한다. P1-A의 계약 확정 후 저장소와 UI 작업을 나눌 수 있고 P1-C는 별도 파일에서 진행할 수 있다. 공통 통합 검증 전까지 완료라고 부르지 않는다.
