**게이트웨이 정확성 개선안 — 2026-10-11**

상태: 구현을 위한 제안. 이 문서의 완료 조건은 아직 달성되지 않았다.
중앙 저장소·ClickHouse 구축은 이번 범위에서 제외한다.
2026-10-12 보완: [Bedrock native 사용성 동등성 검토](2026-10-12-bedrock-native-parity.md)의
PR-11/12/13과 direct/proxy qualification을 추가한다. 기존 PR-01~10만으로
Bedrock Runtime·Mantle의 전체 native 기능이 동일해진다고 주장하지 않는다.

근거: 로컬 작업 브랜치 커밋 `46ab5ad`(main 미병합)에서 수행한 로컬 로그 분석과 12개 adversarial 재현.
상세 자료는 비판적 검토(`review/critical-direction-2026-10-11/README.ko.md`)와
재현 결과(`review/critical-direction-2026-10-11/reproductions.log`)에
있다. `review/`는 `.gitignore` 대상인 operator-local 자료로 저장소에 포함되지 않으므로 아래 완료 조건은 해당 파일 없이도
재현할 수 있도록 정식 테스트로 옮긴다.

현재 배포 바이너리는 미커밋 변경이 포함된 빌드다. 소스에서 재현한 결함과
운영 장애의 원인은 구분한다. 모든 부분 응답을 서버 오류로, 모든 400을
컨텍스트 초과로, 비용 증거 부재를 무료 호출로 간주하지 않는다.

**1. 개선의 기준**

다음 다섯 질문에 각각 답할 수 있어야 한다.

| 질문 | 답을 결정하는 증거 | 다른 증거로 대신하면 안 되는 것 |
|---|---|---|
| 응답이 정상 종료했는가? | 실제 upstream terminal 및 유효한 프로토콜 lifecycle | HTTP 200, clean TCP EOF, 합성한 stop |
| 클라이언트에게 전달됐는가? | client write 결과, commit 여부, 취소 상태 | upstream 성공만으로 전달 성공 판단 |
| 사용량·가격을 어느 정도 아는가? | 필드별 관측 여부, cache TTL, 가격 version/지원 범위 | 누락을 0으로 채우거나 unknown TTL을 실측으로 표시 |
| 다음 시도가 허용되는가? | 오류 종류와 전송 단계, 다시 확인한 권한·privacy·budget | Go error가 존재한다는 이유만으로 재시도 |
| 어떤 데이터가 어느 경계를 통과하는가? | 실제 재전송 body 전체와 허용된 destination | 마지막 user turn, 모델 이름, classifier의 Clear만으로 안전 보장 |

이 구분을 공통 판정 함수와 provider observation으로 표현한다. 네 ingress를
하나의 거대한 handler로 합치는 리팩터링은 하지 않는다. 프로토콜별 wire와
error renderer를 유지하고 완료·재시도·증거 판정만 공유한다.

같은 wire의 RawBody 보존, 검증된 Mask, 권한 재검사, most-restrictive policy,
count API의 local/200, integer microUSD, uncertainty 유지와 기존 authority의
fencing·expiry 계약은 모든 작업의 불변조건이다.
`principal`·`metrics`·`governance`·`adminauth` leaf는 server/config 의존성을
추가하지 않는다. 설정 연계 타입은 decoupled mirror로 전달한다.

**2. 1차: 응답 종료와 재시도 결함을 먼저 닫는다**

**PR-01 — 실제 종료와 사용량 관측을 보존한다**

범위: `providers/provider.go`, 기존 provider의 observation adapter,
`internal/server/requestpolicy`, 네 inference ingress,
`internal/responses`, 필요한 `pkg/schema` 관측 타입.
공통 계약 추가와 기존 adapter 교정은 core correctness 작업이다.
이후 새 provider 추가 PR은 `providers/<name>`와 blank import 경계를 지킨다.
관측용 필드를 native request/response wire에 직렬화하지 않는다.

변경:

- stream iterator의 종료를 `terminal_observed`, `terminal_missing`,
  `terminal_invalid`로 판정한다. 합성된 client terminal이 실제 upstream 완료의
  증거를 만들 수 없도록 provenance를 보존한다.
- Converse가 MessageStop 또는 Metadata 없이 끝나면 부족한 관측을 남기고
  protocol/usage 오류로 마친다. 현재의 무조건 성공 `finish()`를 제거한다.
- SDK의 nil token field는 nil/unknown 관측으로 보존한다. nil → 0 pointer로
  바꾸어 “관측한 0”처럼 보이게 하지 않는다. 명시적인 0은 유효하다.
- 정상 terminal과 수량 관측이 있고 TTL만 불확실하면 응답 전달은 유지한다.
  TTL 부재와 terminal 부재를 같은 `AccountingUncertain` 한 비트로 판정하지 않는다.
- Messages·Chat·Bedrock의 clean EOF도 완료를 검사한다. 종료가 없거나 error
  frame이 있으면 `Partial=true` 및 실패 outcome으로 남긴다.
- commit 전에는 ingress 고유 HTTP error, commit 후에는 ingress 고유 stream
  error를 내보낸다. `[DONE]` 같은 client 종료 표식이 필요해도 실패 의미를 먼저
  전달하고 내부 성공 근거로 쓰지 않는다.
- client cancel/write error도 검사한다. 관측된 usage는 정산하되 모르는 나머지는
  uncertainty로 유지한다. 취소를 모델의 정상 완료나 upstream 장애로 오분류하지 않는다.
- client 전달 완료와 재무 관측 완료를 독립적으로 계산한다. client write가
  실패했다는 이유만으로 이미 확인된 upstream terminal/usage를 지우지 않는다.
  authority 정산·환불은 기존 검증기에 충분한 usage·가격 근거가 모두 있을 때만
  허용하며, client 취소나 HTTP 200 자체는 그 근거가 아니다.
- 정산/finalizer는 attempt당 한 번이다. success/affinity 기록과 breaker 판정은
  실제 결과를 따른다. client 취소·request validation으로 provider를 unhealthy로
  만들지 않는다.
- native Responses의 정상 `response.incomplete`와 출력 한도 도달은 보존한다.
  프로토콜이 유효하게 끝난 incomplete와 transport truncation을 구분한다.

완료 조건:

- [ ] text만/usage만/stop만 있는 Converse 세 fixture에서 `response.completed`가 없다.
- [ ] clean EOF 세 legacy ingress 재현이 부분 실패로 기록된다.
- [ ] 네 ingress 모두 정상 완료, explicit zero usage, 누락 usage, 중단,
  error frame, 취소, client write 실패, output-limit terminal을 검증한다.
- [ ] unknown TTL 정상 응답을 잘못 거부하지 않는다.
- [ ] native nonstream HTTP-200 `status: incomplete`를 불필요한 502/fallback으로
  바꾸지 않는다. stream/nonstream의 output-limit lifecycle을 일치시킨다.
- [ ] 성공/실패 모두 exactly-once settle 또는 liability retention을 검증한다.
- [ ] clean Chat EOF 허용은 ADR-047의 finish reason·완전한 usage·SSE framing
  조건을 만족하는 경우에만 유지한다.

**PR-02 — 오류 종류와 전송 단계에 따라 재시도한다**

범위: `providers/errors.go`, provider별 오류 mapping,
`internal/server/requestpolicy`, 네 ingress의 complete/stream attempt loop.
`UpstreamError`뿐 아니라 non-2xx `ProxyResponse`에도 같은 판정을 적용한다.

| 원인 | 기본 동작 | 예외 또는 추가 조건 |
|---|---|---|
| 컨텍스트 초과 | 원래 client에게 recovery error; 같은 body 재시도 금지 | 별도 기능으로 승인된 compatible target 선택은 명시적 계약 필요 |
| 잘못된 schema/옵션/지원하지 않는 의미 | compatibility/request error 반환 | 필드를 삭제하여 retry하지 않음 |
| privacy·identity·RBAC·region·hard budget 거부 | 종료 | 어떤 fallback도 제한을 완화하지 않음 |
| 401/403 또는 원인 불명 400 | 종료 | 권한 문제를 model-not-found로 승격하지 않음 |
| model-not-found | 승인된 fallback 후보 검토 | 구조화된 upstream code 또는 명시적 모델 조회 증거 필요 |
| 429·확인된 일시적 unavailable/overload | bounded fallback 가능 | commit 전, request가 취소되지 않음, 새 attempt admission 성공 |
| transport timeout/connection failure | 전송 단계와 원인에 따른 보수적 판정 | 이미 처리됐을 가능성이 있으면 이전 비용은 unresolved로 남음 |
| client 취소·이미 commit된 stream·출력 변환 실패 | 자동 retry 금지 | 알려진 usage 정산 또는 uncertainty 유지 |

변경:

- 모든 `400 + ValidationException`을 model-not-found로 취급하는 matcher를
  제거한다. 원인 정보가 없으면 `validation_unknown`으로 남긴다.
- 오류 분류는 bounded upstream 코드·SDK 타입·local lifecycle·HTTP 상태를
  사용한다. 임의의 upstream 메시지/param에 정규식을 적용해 recovery code를
  만들어 내거나 그 원문을 audit/metric/client에 넣지 않는다.
- Bedrock이 세부 원인을 구조화하여 제공하지 않는 경우 이를 해결된 context
  진단이라고 주장하지 않는다. local preflight와 수동 compaction 안내를 제공한다.
- 모든 실패 attempt를 다음 target 호출 전에 기록한다. 앞선 usage가 있으면
  정산한 후 새로운 PreCheck/authority reservation을 수행한다.
- 새 attempt는 같은 topology/pricing snapshot을 사용하며 기존 runtime policy,
  identity/readiness, privacy, strict-target, RBAC/region 제한을 다시 확인한다.
- hidden SDK/transport retry를 추가하지 않는다. finite chain/attempt limit을
  유지하고 최종 성공만으로 이전 실패 attempt를 덮어쓰지 않는다.

완료 조건:

- [ ] 현재 재현의 complete/stream 여섯 경로에서 일반 validation 400이
  다음 target에 그대로 재전송되지 않는다.
- [ ] 400과 취소는 retry 0회, transient fixture는 허용된 후보 안에서만 retry한다.
- [ ] 실제 model-not-found fixture와 generic ValidationException fixture를 분리한다.
- [ ] 앞선 유료 실패가 후속 budget/strict-target 거부로 이어지는 경우를 검증한다.
- [ ] commit 후 provider 변경·권한 확대·재정산이 없다.

**PR-03 — 중앙 저장소 없이 원인을 재구성한다**

범위: `internal/audit`, `internal/server/requestpolicy`, ingress observation,
`internal/metrics`, `internal/tracing`, 기존 로컬 CLI 진단/console read-model.

추가 정보:

- 논리 request correlation ID, attempt ID/ordinal. event의 기존 `ID`는 그대로
  유지한다. client 재시도를 동일 작업으로 자동 묶거나 raw session ID를 기록하지 않는다.
- bounded `terminal_state`, `failure_stage`, `failure_cause`, commit 여부,
  실제 upstream status, 전송 단계.
- 실제 provider API/target, topology·가격 version, 활성 build reference.

상태 예:
`completed`, `output_limited`, `provider_failed`, `protocol_truncated`,
`client_cancelled`, `client_write_failed`, `conversion_failed`, `unknown`.
예외 원문은 상태 이름에 넣지 않는다.

correlation은 tracing이 꺼져 있어도 존재한다. metric에는 ID·user·key·session·
본문 값을 넣지 않고 bounded outcome/cause만 사용한다. audit에는 append-only/
omitempty를 적용하여 과거 레코드의 바이트와 hash 의미를 보존한다.

로컬 진단은 원문 입력 없이 HTTP 200 중 실제 완료/중단/취소 비율, attempt별
순서, 사용량 없는 실패, 누적 unresolved 수를 보여 준다. 운영자가 큰 raw
journal을 뒤지지 않아도 한 요청의 실패 단계를 설명할 수 있어야 한다.

완료 조건:

- [ ] tracing disabled에서도 started/attempt/finished의 연결이 유지된다.
- [ ] HTTP 200 부분 실패가 metrics·trace·audit에서 성공과 구분된다.
- [ ] authenticated 거부는 관측되며 기존 “401 audit 없음” 계약을 유지한다.
- [ ] mixed-version audit fixture가 바이트 동일·tamper 검증을 통과한다.
- [ ] 기존 로그의 unknown을 근거 없이 새 원인이나 성공 상태로 backfill하지 않는다.

**3. 2차: 비용 증거와 사용 가능한 예산을 개선한다**

**PR-04 — 실패 비용과 불확실성을 로컬 화면에 남긴다**

범위: `pkg/schema/usage.go`, `internal/audit/usage.go`, `internal/pricing/usage.go`,
provider usage adapter, `internal/analytics`, `internal/server/analyticsapi`,
로컬 console, `cmd/mayu/report.go`와 구현을 담당할 내부 report 패키지.
`cmd/mayu`에는 CLI 조립만 두고 새 집계 로직을 쌓지 않는다.

비용 증거를 다음처럼 분리한다.

| 항목 | 표현 | 회계상 의미 |
|---|---|---|
| observed usage | input/output/cache의 관측 수량·known 여부 | 누락은 unknown, 명시적인 0과 다름 |
| configured-price amount | integer microUSD + immutable pricing version + quality | 현재 가격표 기반 금액; invoice 확정과 다름 |
| retained liability | authority에 남은 예약과 상태 | 지출 추정액에 더하여 이중 합산하지 않음 |
| unresolved | 수량/단가/TTL/terminal 근거가 부족한 attempt와 사유 | 0원 또는 환불로 처리하지 않음 |
| reconciled | 검증된 provider 사용량/청구 근거가 연결된 상태 | 별도 대사 근거와 절차가 있을 때만 표시 |

TTL만 불확실하다고 알려진 input total을 unknown으로 덮지 않는다. 반대로
token total이 known이라는 이유로 cache TTL이나 actual cost를 확정하지 않는다.
기존 authority의 exact-usage 검증은 따로 유지한다.

`Billable` 조건을 무작정 바꾸어 legacy 금액 합계에 unknown을 0으로 섞지 않는다.
기존 비용 집계와 별도로 모든 attempt/거부를 조회하는 read-model을 만들고,
로컬 SQLite에 idempotent ingestion과 migration/replay를 적용한다.
analytics 비활성 profile은 읽기 전용 audit 분석으로 같은 상태를 확인할 수 있다.
Mode B의 기존 Store consumer도 호환되게 갱신한다; 신규 중앙 저장소를 요구하지 않는다.
hosted search 등 token 가격표가 포함하지 않는 과금은 coverage 밖이라고 표시한다.
token-cost ledger를 전체 공급자 청구액으로 표시하지 않는다.

완료 조건:

- [ ] usage 없는 실패가 요청 목록·미확정 건수에 보인다.
- [ ] total known / TTL unknown fixture가 독립적으로 표시된다.
- [ ] retry 비용은 각 attempt를 한 번만 합산한다.
- [ ] retained liability와 observed cost가 중복 집계되지 않는다.
- [ ] negative·overflow·모순 cache split은 invalid로 유지하며 debit/refund를 만들지 않는다.
- [ ] 구버전 audit replay는 unknown 증거를 그대로 남긴다.

**PR-05 — authority 예약 상한은 증거가 있는 범위에서 줄인다**

범위: `internal/server/requestpolicy/authority.go`, `internal/pricing/authority.go`,
shared/durable permit 경로, provider API별 요청 한도 계약.
ADR-045/046의 grant·window·expiry·restart·fencing 구조는 재설계하지 않는다.

현재는 입력과 출력에 모두 model context window를 넣는다. 첫 최적화 후보는
**API가 실제로 강제하는 출력 상한**이다. 구현 순서는 다음과 같다.

1. request 출력 한도, API의 최소값/default, reasoning·hidden output 포함 여부,
   단일 generation 계약을 provider/API별로 확인한다.
2. 그 상한이 실제 dispatch payload와 같고 upstream에 강제된다는 offline
   fixture를 작성한다. client의 작은 숫자만 읽어 신뢰하지 않는다.
3. 증명된 경우에만 더 작은 output bound를 예약에 사용한다.
   증거가 없거나 opaque/unsupported control이면 기존 최대 bound를 유지하거나
   해당 strict profile에서 거부한다.
4. 입력은 초기 단계에서 기존 conservative bound를 유지한다.
   `len(raw)/4`를 재무 상한으로 사용하지 않는다.
5. 이후 cache class의 disjoint semantics와 전체 입력 한도가 검증된 profile만
   더 작은 입력 bound를 검토한다. 변경은 별도 ADR·adversarial 테스트 대상으로 둔다.

같은 protocol의 `max_tokens`를 몰래 줄이거나 context를 잘라 예약에 맞추지 않는다.
요청이 검증된 지원 범위를 넘으면 명확하게 거부한다.
unknown usage/TTL/가격/terminal은 현 계약대로 liability를 유지한다.
새 정보 없이 timeout·expiry·restart만으로 예약을 환불하지 않는다.

profile UI의 문구도 구분한다:
local은 누적 지출 기반 threshold이며 동시/큰 요청에서 overshoot 가능,
node-local authority는 전역 정책 money의 유한 credit,
shared는 synchronous DB admission이며 DB loss에서 거부한다.

완료 조건:

- [ ] 작은 출력 요청의 예약이 실제 enforced cap 증거로만 줄어든다.
- [ ] 출력 cap 누락·min clamp·default·reasoning·중복/모호한 control·다중 generation을 검증한다.
- [ ] short/long 가격은 각 class의 최대 단가와 upward rounding을 유지한다.
- [ ] 실제 사용량이 선언 상한을 위반해도 환불 근거가 되지 않는다.
- [ ] 부분/unknown, 동시 admission, late settle, restart, rollover, replay에 대해
  durable/shared 통합 테스트가 통과한다.
- [ ] cached 정상 요청이 많은 workload에서 retained liability와 실제 거부율을
  측정한다. 가짜 usage fixture의 성공만으로 운영 예산 가용성을 주장하지 않는다.

**4. 3차: 긴 context와 프로토콜 변환의 지원 범위를 검증한다**

**PR-06 — context 추정값과 client recovery를 분리한다**

범위: `internal/server/requestpolicy/context.go`, ingress error renderer,
`internal/router` 용량 판정, model metadata, `scripts/inferplane-codex`,
launcher tests, `docs/reference/context-window-validation`.

표시할 값은 declared context, 추정 방식과 결과, 관측 total input,
검증한 client usable/compaction profile, 요청/유효 출력 budget이다.
정확한 수량이 없는 곳에는 estimated/unknown을 표시한다.

초기 구현에서 preflight text heuristic와 보수적 routing/financial bound를
같은 값으로 통합하지 않는다. inline image base64를 text로 잘못 세는 교정은
유지하되 visual token을 0이라고 주장하지 않는다. 큰 이미지나 opaque 입력의
privacy·quota·authority 거부를 preflight 교정으로 우회시키지 않는다.

context overflow recovery는 ingress별로 고정한다. Messages의 검증된
`prompt is too long` 형태와 Responses의 bounded `context_length_exceeded`를
보존한다. gateway estimate를 upstream 실측으로 꾸미지 않는다.
upstream이 구조화된 recovery code를 주지 않으면 일반 validation/unknown으로
남기며, raw error text를 client로 내보내지 않는다.

클라이언트가 compact/새 세션을 선택한다. gateway가 자동 transcript 절단·요약,
tool result 삭제, 보안/strict budget에 위배되는 큰 모델 변경을 수행하지 않는다.

완료 조건:

- [ ] 한국어·JSON·system/tool schema·여러 turn·이미지·reasoning·출력 budget을
  포함한 fixture에서 false refusal과 recovery 동작을 구분한다.
- [ ] 설치된 client는 loopback fake에서 overflow → compaction 또는 명시적
  recovery → 재요청 → 정상 terminal을 끝까지 검증한다.
- [ ] 잘못된 context 선언의 operator profile은 거부한다.
- [ ] 실제 upstream 최대가 미확정이면 observed lower bound로만 문서화한다.
- [ ] 두 count 경로(`/v1/messages/count_tokens`,
  `/model/{modelId}/count-tokens`)의 local/200 및 pre-provider refusal 계약을 유지한다.

**PR-07 — 변환할 수 없는 의미를 dispatch 전에 거부한다**

범위: `internal/responses`, `internal/openai`, 기존 provider 변환기,
`internal/router`의 request compatibility predicate, discovery/launcher metadata.

client/version × ingress × provider API × model revision별 지원 표를 만든다.
text/tools/history/strict/reasoning/media/hosted tools/terminal/usage를 독립적으로
기록한다. 선언된 capability는 operator metadata이며 모델 품질의 증거는 아니다.

- Responses의 기존 strict/opaque state/unknown tool refusal과 이름 복원을 재사용한다.
  다른 ingress에서도 필요한 tool이나 content를 조용히 삭제한 채 성공시키지 않는다.
- legacy budget-tier의 제안 target이 호환되지 않으면, 여전히 허용되는 original을
  같은 privacy·권한·hard budget 조건으로 검토한다. legacy substitution만으로
  새로운 denial을 만들지 않는다. enforceTargets는 호환 target 부재 시 거부하며
  original로 복구하여 strict 제한을 완화하지 않는다.
- 긴 tool 이름은 검증된 deterministic alias·충돌 거부·history/choice/응답 복원으로
  처리하거나 dispatch 전에 명시적으로 거부한다. 임의 truncation은 하지 않는다.
- 요청자의 실행 의미를 가진 옵션은 보존 또는 거부한다. 명시적으로 허용된
  output preference 생략과 foreign thinking 출력 제거는 기존 bridge 계약으로 구분한다.
- 변환 실패 뒤에도 이미 관측한 usage를 보존한다. 같은 취소 가능한 request
  안에서 가능한 terminal accounting을 읽고, client 취소 뒤에 새 billable
  요청이나 무제한 background drain을 만들지 않는다.
- 바뀐 의미가 없는 native path는 byte-preserving contract를 유지한다.

완료 조건:

- [ ] 각 지원 조합에 parallel tool call/result, 긴 이름·hyphen·alias collision,
  custom tool, 중단된 JSON args, strict/opaque/hosted tool negative fixture가 있다.
- [ ] 어떤 unsupported feature도 도구/본문을 삭제한 HTTP 200으로 바뀌지 않는다.
- [ ] upstream 처리 후 rendering 실패의 usage 정산과 retry 금지를 검증한다.
- [ ] 성공한 한 wire의 fixture를 다른 API/model의 qualification으로 재사용하지 않는다.

**5. 4차: PII 설계의 경계를 먼저 고치고 Shadow로 평가한다**

**PR-08 — classifier 설계를 보완한다**

범위: 기존 `2026-10-07-pii-classifiers-design.md`의 후속 설계,
필요한 새 ADR, `internal/classifier` seam과 후속 `plugins/<type>` 구현.
현재 승인된 설계와 accepted ADR을 이 문서만으로 변경된 것으로 취급하지 않는다.

기본 검사 범위를 “마지막 assistant 이후”에서 **실제로 재전송될 보호 대상
전체**로 바꾼다. 사용자·assistant·tool history, system/developer 입력과 필요한
정의/인자 영역을 포함하며, opaque/media는 coverage 부족으로 다룬다.
client role이나 이미 존재하는 history라는 이유만으로 안전한 segment로 간주하지 않는다.

incremental 검사는 후순위 최적화다. 검증된 segment digest·검사 scope·classifier
revision·정책/egress boundary가 맞는 경우에만 재사용한다. TTL/capacity eviction,
restart, 정책/모델 변경, forged history에서 재검사 또는 제한으로 돌아간다.
원문이나 raw session ID를 저장하지 않고 digest 캐시가 authorization을 확장하지 않는다.

외부 classifier는 계속 file-level opt-in과 허용 boundary를 요구한다.
local-first는 이미 민감하다고 알려진 입력을 보내지 않는 최적화이며, 미검출
PII의 외부 전송 방지 보장은 아니다. Shadow에도 외부 원문 전송이 발생함을
configuration·audit·operator 문서에 명시한다. 내부 제한을 받은 입력은 그
제한을 위반하는 외부 classifier에도 보내지 않는다.

Mask는 verdict만으로 구현하지 않는다. span 없는 Flagged는 현 설계대로
onUninspectable의 InternalOnly/Block에 매핑한다. span 기반 확장은 구조 안전성,
완전 redaction, 독립 reinspection을 갖춘 별도 작업으로 둔다.

언어 지원 이름이나 `language: ko`만으로 한국어 품질을 승인하지 않는다.
classifier revision별 threshold·abstention·input/window/time budget을 평가한다.
window cap을 넘은 나머지를 버린 뒤 Clear를 반환하는 경로는 허용하지 않는다.

완료 조건:

- [ ] 과거 turn에 이름/주소가 있고 새 질문에는 없는 요청도 보호한다.
- [ ] forged assistant history, 긴 tool 결과, system/schema 위치, boundary를
  걸친 entity, policy 변경·restart·eviction의 재검사를 검증한다.
- [ ] unknown/timeout/abstain/over-limit은 Enforce에서 rule의 제한을 적용한다.
- [ ] classifier 때문에 count 요청에 network 의존성이나 non-200이 생기지 않는다.
- [ ] Block 우선, internal set 교집합, Mask와 InternalOnly의 조합, remote-tool
  boundary 거부를 유지한다.

**PR-09 — 한국어와 장기 세션의 release gate를 명시한다**

실제 개인정보 없는 합성/비식별 corpus를 사용한다. 이름·주소·계좌·건강·
credential·전화와 source code·버전·IP·가짜 entity·mixed-language·tool history를
유형별로 분리한다. window 경계와 긴 입력도 포함한다.

Enforce 전에 다음 gate 값을 선언하고 고정한다. corpus/모델/revision이 바뀌면
결과도 다시 낸다. 현재 증거가 없는 수치를 “충족”으로 채우지 않는다.

- 보호 유형별 허용 false-negative rate와 표본 수/신뢰구간.
- 정상 코딩 요청의 허용 false-positive·abstention·거부율.
- 배포 환경별 허용 p95 추가 지연·resource 사용.
- classifier outage 때 허용되는 fallback destination과 가용성 계약.
- native/bridge 및 기존 경로 대비 작업 성공률·총 attempt 비용·cold-cache/retry
  비용·지연의 허용 회귀.

모든 민감 유형을 하나의 평균 accuracy로 합쳐 낮은 recall을 숨기지 않는다.
regex·classifier·mask·destination enforcement의 성공을 각각 평가한다.
작업 성공은 별도 평가 harness에서 확인하며 gateway가 tool-call 횟수만으로
사용자 작업 완료를 추정하지 않는다.

**6. 배포와 검증을 소스 결과에 연결한다**

**PR-10 — build/generation 진단을 먼저 제공한다**

범위: `internal/live`와 내부 진단 패키지, `cmd/mayu`의 thin assembly,
기존 admin 인증 경로, release/operations 문서.

로컬 `mayu doctor --json`의 기본 동작은 read-only이며 billable probe를 호출하지 않는다.
build version/revision/dirty 상태, 실제 적용된 topology·가격·policy generation,
identity/readiness profile, pricing coverage, rejected reload 여부와 bounded cause를
보여 준다. secret 값·DSN·원문·raw identity는 반환하지 않는다.
진단 fingerprint가 machine 인증이나 authority binding을 대신하지 않는다.

정식 release artifact는 clean source reference와 binary digest로 식별한다.
dirty 개발 빌드는 진단에서 구분하되 운영 artifact와 같은 qualification을 붙이지 않는다.
doctor 결과와 offline qualification의 build reference가 맞아야 staged 운영 검증을 시작한다.
자체 업데이트나 자동 deployment 기능은 이 작업에 넣지 않는다.

구현 PR마다 필요한 기본 검증:

```bash
CGO_ENABLED=0 go build -trimpath -o /tmp/inferplane-check/mayu ./cmd/mayu
CGO_ENABLED=0 go build -trimpath -o /tmp/inferplane-check/inferplaned ./cmd/inferplaned
go test ./... -race
go vet ./...
gofmt -l .
bash tests/run-all.sh
```

기존 서비스가 참조하는 바이너리를 검증 build로 덮어쓰지 않는다.
Go/CLI 테스트는 credentials·network·real IdP 없이 fakes로 실행한다.
durable/shared money 변경은 추가로 ephemeral Postgres의 required integration
suite를 실행하고 skip을 성공 증거로 취급하지 않는다.
공급자 실측은 별도 승인된 synthetic 캠페인에서 budget ledger·no hidden retry·
분류된 실패 보존으로 수행한다.

완료 조건:

- [ ] doctor가 현재 source/build와 실제 적용 generation의 차이를 설명한다.
- [ ] rejected reload 후 이전 generation이 계속 적용되는 것을 보여 준다.
- [ ] 개별 PR의 테스트·ADR·API/schema 문서·영문/한국어 product 문서가 동기화된다.
- [ ] 작성 시점에 확인된 `docs/adaptive-routing.ko.md`와
  `docs/reference/pricing.ko.md`의 source hash/예제 불일치를 내용 검토로 해소한다.
  hash만 갱신하여 검사를 통과시키지 않는다.
- [ ] DCO sign-off가 있는 검토 가능한 commit으로 제출한다.

**7. 실행 순서와 완료 판정**

| 순서 | 작업 | 의존성과 산출물 |
|---|---|---|
| 1 | PR-01·02 | 재현 12개를 정식 실패 테스트로 먼저 추가하고 종료/재시도 결함 수정 |
| 2 | PR-03·10 | 로컬 audit correlation·원인·build/generation 진단 확보 |
| 3 | PR-04 | 실패·unknown·cost evidence를 기존 로컬 화면과 report에 표시 |
| 4 | PR-11·13 → PR-06·07 | Runtime/Mantle native wire와 direct/proxy fixture를 먼저 확보하고 context·bridge 의미를 검증 |
| 5 | PR-05 | 위 증거를 바탕으로 검증된 출력 상한부터 예약 최적화 |
| 6 | PR-08·09 | PII 설계 보완 → Shadow corpus/장기 세션 평가 → gate를 만족한 조합의 Enforce |
| 7 | PR-12·13 | 검증된 resource ownership·durable accounting·privacy profile에서만 native state/background 기능 확대 |

PR-08의 설계 보완과 합성 corpus 작성은 앞 단계와 독립적으로 준비할 수 있다.
전체 input 검사 비용, classifier 장애와 운영 예산 가용성은 평가 결과로 결정한다.
gateway correctness 작업과 기존 identity/roles/fleet qualification 작업을 하나의
대형 PR로 합치지 않는다.

첫 완료 목표는 “검증된 client/API/model 조합에서 정상 종료·재시도·사용량의
알려진/모르는 부분을 설명하고 정책을 유지하는 것”이다. 중앙 저장소 구축,
임의 모델의 완전 호환, 범용 PII 검출, 실제 청구서 완전 일치, enterprise/HA 완료는
이 목표의 자동 결과로 주장하지 않는다.
