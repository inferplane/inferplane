# Go inspection performance design

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status and outcome

Proposed on 2026-10-27 against `0360e81f8ddecc21ccb8cc590cf6d4f5270a31de`.
Keep Go and measure gateway overhead before changing execution. This design covers
one implementation project: reproducible measurement and separation of structural
inspection from finite deterministic PII detection. It does not implement that
separation or claim a measured speedup. Publish results only after the comparison.

PII enforcement is optional configuration; applicable privacy obligations remain
mandatory. Do not add a global `pii_enabled=false` override.

## Evidence and current behavior

| Evidence | Current behavior |
| --- | --- |
| `internal/sensitivity/inspect.go`, `text.go` | One JSON walker collects shape/token signals and invokes finite detectors over inspected values |
| `internal/router/request_routing.go` | Responses always inspects; other routing inspects for privacy, context including Shadow, or strict targets |
| `internal/server/bedrockapi/invoke.go` | Native Converse also always inspects structure before financial admission |
| `internal/sensitivity/redact.go` | Masking independently inspects original and transformed content |
| `internal/server/responsesapi/handler.go` | Canonical parsing, routing inspection and compatibility checks can revisit the same request |
| `internal/authority/pgstore/shared.go`, `shared_constraints.go` | Shared admission uses a synchronous transaction and counter/account locks |
| `internal/authority/local/store.go`, `internal/audit/wal.go` | Private journal transactions and audit fsync remain request-path work |

The existing end-to-end performance pages include upstream/network time and do
not establish JSON CPU cost or a Go/Rust comparison. Earlier review estimates
such as ms/MiB, SQL counts and requests/s are hypotheses for this work, not freshly
reproduced baselines.

## Alternatives and decision

| Alternative | Decision |
| --- | --- |
| Rewrite in Rust | Deferred; no evidence that language overhead dominates upstream wait, allocations, locks or fsync |
| Skip inspection when PII is disabled | Reject; Responses and native Converse still need structural/compatibility checks |
| Keep the walker; make detector visits selective | Recommend; retain one source of structural truth and existing full-inspection API |
| Cache whole prompts or pool decoded request trees | Defer; introduces retention and lifetime risks before profiling proves need |

## Proposed contract

Preserve `Inspector.Inspect` as full deterministic inspection. Add an optional
capability without changing injected legacy implementations:

```go
type InspectionMode uint8

const (
    DetectSensitiveData InspectionMode = iota
    StructureOnly
)

type SelectiveInspector interface {
    Inspector
    InspectWithMode(context.Context, string, []byte, InspectionMode) (Result, error)
}
```

Add `Result.SensitiveDataInspected bool` as internal evidence, never a new metric
dimension or audit field. Full successful inspection sets it to true, even when
the category set is empty; structure-only leaves it false. Invalid modes return
a content-free error. `Complete` retains its existing meaning: inspectable
content, never a universal PII-free guarantee.

The built-in inspector implements both APIs. Structure-only keeps decoding,
duplicate-key rejection, exact numeric spelling, nested JSON traversal,
cancellation, output-budget validation, decoded-byte/node/depth bounds,
shape/opacity and token/capability signals. Only detector callbacks and category
allocation are skipped. Do not skip the text walker or decode binary content.

Create a router helper:

```go
func inspectForRoute(
    ctx context.Context,
    inspector sensitivity.Inspector,
    protocol string,
    raw []byte,
    mode sensitivity.InspectionMode,
) (sensitivity.Result, error)
```

If `SelectiveInspector` is available, use it and require successful full mode to
report `SensitiveDataInspected=true`. If only the old `Inspector` is available,
call its documented full API and mark a successful result as fully inspected;
never downgrade it to an inferred clean result. Apply this evidence check before
privacy decisions or learned classification. Errors follow existing ingress
error/failure-policy behavior.

| Effective request condition | Proposed behavior |
| --- | --- |
| No rules/strict target, Messages/Chat/InvokeModel | Preserve existing routing-inspection skip |
| Responses without privacy | Structure-only routing inspection, including native opaque-content compatibility |
| Native Converse preflight | Structure-only preflight regardless of policy; privacy routing still performs full inspection when required |
| Context-only including Shadow, or strict target without privacy | Structure-only routing inspection |
| Applicable `sensitiveData`, including next to Shadow | Full deterministic inspection; configured learned classifiers keep their current semantics |
| Required Mask or enabled complete Responses legacy masking | Existing full original inspection and independent full reinspection; legacy detector coverage remains mandatory |
| Count APIs | Preserve local/authenticated-200 behavior and zero learned-classifier calls |

No request-spanning cache, new setting or provider implementation change is
needed. Select the mode from the same effective policy/topology snapshot that
governs routing. A generation change must not reuse evidence from another request.

## Measurement and acceptance

Use local fake upstreams and synthetic payloads. Cover Messages, Chat, Responses,
InvokeModel and native Converse; plain text, nested tool JSON, escaped strings,
numeric scalars, opaque media and malformed input. Measure 20 KiB, 200 KiB,
1 MiB and 8 MiB bodies at concurrency 1, 8 and 32, with an explicitly capped
total in-flight body size. A 60 MiB stress case is optional and runs alone.

Compare no privacy, context Shadow/Enforce, strict targets, privacy Block/
InternalOnly/Mask, the legacy masker and a fake learned classifier. Separate
local, node-local journal and shared Postgres results. Run core measurement
without a DB; qualify optional profiles only with their fixture and record an
unavailable profile as unmeasured, never as passing.

Collect `ns/op`, `B/op`, `allocs/op`, CPU/heap/mutex/block profiles, process RSS,
gateway latency p50/p95/p99, dispatch counts and failures. Use at least five
trials, identical payloads and fake upstream delay. Record commit, Go/tooling
versions, OS/CPU, memory/GC settings, body/concurrency limits, protocol/profile,
fixture hash and sample counts. Use real account-free fakes; do not retain prompts
or credentials. Do not infer gateway CPU by subtracting unrelated E2E runs.

Correctness is the merge gate: preserve privacy refusal/masking, byte forwarding,
opaque compatibility, routing/RBAC, count behavior, monetary uncertainty and
single settlement under cancellation/fallback. Recommended performance target:
at least 15% lower median `ns/op` or `B/op` for 200 KiB/1 MiB inspection without
PII, with no sustained >5% regression on full inspection. These are engineering
targets, not promised product gains; repeat noisy comparisons. If the target
fails, retain the baseline and refine the design before proposing a performance
merge.

## Follow-up priorities

| Order | Separate project after measurement | Contract to preserve |
| --- | --- | --- |
| 1 | Selective inspection, then duplicate parsing if profiles justify it | Same structural validation, full required PII coverage, request-scoped evidence |
| 2 | Aggregate body-memory admission; tune per-request limits and Go memory envelope | Bounded total bytes, timely release on every exit, streaming responses remain usable |
| 3 | Upstream header/idle/total-wait cancellation policy | No hidden generation retry; dispatched unknown usage retains liability; no fixed streaming write deadline |
| 4 | Shared SQL/account contention and private journal serialization | Atomic reservations, DB-time windows, lock ordering, identity/generation fencing |
| 5 | Audit batching only under a separately reviewed durability design | Durable acknowledgement, exact chain bytes, required-sink pressure and crash recovery |

Do not blame compatible SHARE table locks alone for same-team serialization:
measure row-lock contention, SQL round trips and transaction duration separately.
Adding replicas does not eliminate contention on a shared account. Increasing
pools or removing fsync is not an acceptance criterion.

## Delivery and review

Use the [implementation plan](../plans/2026-10-27-go-inspection-performance.md).
Each task must have adversarial tests and its own reviewable outcome. The next
ADR number is 055 at this baseline; confirm the current index before allocating
one during implementation. Do not mark a planned ADR Accepted or rewrite
ADR-013's remaining fleet/HA qualification.

---

<a id="korean"></a>

# 한국어

## 상태와 목표

2026-10-27, 기준 `0360e81f8ddecc21ccb8cc590cf6d4f5270a31de`의 제안입니다.
Go를 유지하고 실행 경로 변경 전에 게이트웨이 비용을 측정합니다. 이 설계의 구현
범위는 재현 가능한 측정과 구조 검사·유한 결정적 PII 탐지 분리입니다. 분리는 아직
구현하지 않았으며 성능 향상을 측정했다고 주장하지 않습니다. 비교 완료 후 결과를 게시합니다.

PII 집행은 선택적 설정이지만 적용되는 개인정보 의무는 필수입니다.
전역 `pii_enabled=false` 우회 설정을 추가하지 않습니다.

## 근거와 현재 동작

| 근거 | 현재 동작 |
| --- | --- |
| `internal/sensitivity/inspect.go`, `text.go` | 하나의 JSON 순회가 구조·토큰 신호를 수집하고 값에 유한 탐지기를 적용 |
| `internal/router/request_routing.go` | Responses는 항상 검사; 나머지 라우팅은 개인정보·Shadow 포함 컨텍스트·엄격 목적지에 따라 검사 |
| `internal/server/bedrockapi/invoke.go` | 네이티브 Converse는 금액 admission 전에 항상 구조 검사 |
| `internal/sensitivity/redact.go` | 마스킹 전 원문과 변환 후 내용을 독립 검사 |
| `internal/server/responsesapi/handler.go` | 정규 파싱·라우팅 검사·호환성 검사에서 같은 요청 재방문 가능 |
| `internal/authority/pgstore/shared.go`, `shared_constraints.go` | 공유 admission은 동기 트랜잭션과 카운터·계정 잠금 사용 |
| `internal/authority/local/store.go`, `internal/audit/wal.go` | 개인 journal 트랜잭션과 감사 fsync도 요청 경로 비용 |

기존 전체 성능 페이지에는 업스트림·네트워크 시간이 포함되며 JSON CPU 비용이나
Go/Rust 비교를 입증하지 않습니다. 이전 리뷰의 ms/MiB·SQL 수·초당 요청 수는
이번 작업의 검증 가설이며 새로 재현한 기준값이 아닙니다.

## 대안과 결정

| 대안 | 결정 |
| --- | --- |
| Rust 재작성 | 보류; 업스트림 대기·할당·잠금·fsync보다 언어 비용이 지배적이라는 근거 없음 |
| PII가 꺼졌을 때 검사 생략 | 거부; Responses·네이티브 Converse의 구조·호환성 검사는 필요 |
| 순회를 유지하고 탐지 콜백만 선택 실행 | 권고; 구조 검증의 단일 구현과 기존 완전 검사 API 유지 |
| 프롬프트 전체 캐시·해석 트리 풀 | 보류; 프로파일 근거 전에 보존·수명 위험을 추가하지 않음 |

## 제안하는 계약

`Inspector.Inspect`는 완전한 결정적 검사로 유지합니다. 기존 주입 구현을 바꾸지
않도록 다음 선택적 기능을 추가합니다.

```go
type InspectionMode uint8

const (
    DetectSensitiveData InspectionMode = iota
    StructureOnly
)

type SelectiveInspector interface {
    Inspector
    InspectWithMode(context.Context, string, []byte, InspectionMode) (Result, error)
}
```

`Result.SensitiveDataInspected bool`을 내부 근거로 추가하되 새 메트릭 차원이나 감사
필드로 내보내지 않습니다. 완전 검사가 성공하면 종류가 없어도 true이고, 구조만 검사하면
false입니다. 잘못된 모드는 내용을 포함하지 않는 오류를 반환합니다. `Complete`는
현재의 검사 가능 여부이며 모든 PII가 없다는 보장이 아닙니다.

기본 검사기는 두 API를 구현합니다. 구조 모드도 디코딩·중복 키 거부·정확한 숫자
표기·중첩 JSON 순회·취소·출력 예산 검증·디코딩 바이트/노드/깊이 한계·구조/불투명
내용·토큰/기능 신호를 유지합니다. 탐지 콜백과 종류 할당만 생략합니다.
문자열 순회를 건너뛰거나 바이너리를 디코딩하지 않습니다.

라우터에 다음 보조 함수를 만듭니다.

```go
func inspectForRoute(
    ctx context.Context,
    inspector sensitivity.Inspector,
    protocol string,
    raw []byte,
    mode sensitivity.InspectionMode,
) (sensitivity.Result, error)
```

`SelectiveInspector`가 있으면 사용하고 완전 모드 성공 결과의
`SensitiveDataInspected=true`를 요구합니다. 기존 `Inspector`만 있으면 문서화된
완전 API를 호출하고 성공 결과를 완전 검사로 표시합니다. 추정상 깨끗하다는 결과로
낮추지 않습니다. 개인정보 결정·학습 분류 전에 이 근거를 확인합니다.
오류는 기존 수신 프로토콜·실패 정책을 따릅니다.

| 유효한 요청 조건 | 제안 동작 |
| --- | --- |
| 규칙·엄격 목적지가 없는 Messages/Chat/InvokeModel | 기존 라우팅 검사 생략 유지 |
| 개인정보 규칙 없는 Responses | 네이티브 불투명 내용 호환성을 포함한 구조 전용 라우팅 검사 |
| 네이티브 Converse 사전 검사 | 정책과 무관하게 구조 전용; 개인정보 규칙이 있으면 이후 라우팅에서 완전 검사 |
| Shadow 포함 컨텍스트 전용 또는 개인정보 없는 엄격 목적지 | 구조 전용 라우팅 검사 |
| Shadow와 함께 적용되는 경우를 포함한 `sensitiveData` | 완전 결정적 검사; 설정된 학습 분류기의 기존 의미 유지 |
| 필수 Mask 또는 완전 Responses 기존 마스킹 | 기존 원문 완전 검사·독립 완전 재검사; 기존 탐지기 범위도 필수 |
| Count API | 로컬·인증 후 200 동작 및 학습 분류기 호출 0회 유지 |

요청 간 캐시·새 설정·공급자 구현 변경은 필요하지 않습니다. 라우팅과 같은 유효
정책·토폴로지 스냅샷에서 모드를 선택합니다. 세대 변경 시 다른 요청의 근거를
재사용하지 않습니다.

## 측정과 수용 기준

로컬 가짜 업스트림과 합성 본문을 사용합니다. Messages·Chat·Responses·InvokeModel·
네이티브 Converse, 일반 텍스트·중첩 도구 JSON·이스케이프 문자열·숫자·불투명
미디어·잘못된 입력을 포함합니다. 20 KiB·200 KiB·1 MiB·8 MiB 본문과 동시성
1·8·32를 측정하되 전체 처리 중 본문 크기를 명시적으로 제한합니다.
선택적 60 MiB 스트레스 측정은 단독 실행합니다.

개인정보 없음·컨텍스트 Shadow/Enforce·엄격 목적지·개인정보 Block/InternalOnly/Mask·
기존 마스커·가짜 학습 분류기를 비교합니다. 로컬·노드 journal·공유 Postgres 결과를
분리합니다. 핵심 측정은 DB 없이 실행하고 선택 프로필은 전용 fixture로 검증합니다.
사용 불가한 프로필은 미측정으로 기록하며 통과로 처리하지 않습니다.

`ns/op`·`B/op`·`allocs/op`, CPU/heap/mutex/block 프로파일, 프로세스 RSS,
게이트웨이 지연 p50/p95/p99, 호출 수·실패를 수집합니다. 같은 본문·가짜 업스트림
지연으로 최소 5회 반복합니다. 커밋·Go/도구 버전·OS/CPU·메모리/GC 설정·본문/동시성
한계·프로토콜/프로필·fixture 해시·표본 수를 기록합니다. 실제 계정이 없는 가짜 환경을
쓰고 프롬프트·자격증명을 보존하지 않습니다. 별개 전체 실행을 빼서 CPU 비용을 추정하지 않습니다.

병합 기준은 정확성입니다. 개인정보 거부/마스킹·바이트 전달·불투명 내용 호환성·
라우팅/RBAC·count·금액 불확실성·취소/폴백 시 단일 정산을 보존합니다.
권고 성능 목표는 PII 없는 200 KiB/1 MiB 검사에서 중앙값 `ns/op` 또는 `B/op` 15% 이상
감소이며 완전 검사에서 지속적인 5% 초과 회귀가 없어야 합니다. 이는 엔지니어링 목표이고
제품 향상 약속이 아닙니다. 잡음이 크면 반복하고 목표 미달이면 기준 측정을 유지한 채
설계를 보완한 후 성능 병합을 제안합니다.

## 후속 우선순위

| 순서 | 측정 후 별도 프로젝트 | 보존할 계약 |
| --- | --- | --- |
| 1 | 선택 검사, 근거가 있으면 중복 파싱 개선 | 같은 구조 검증·필수 PII 범위·요청 단위 근거 |
| 2 | 전체 본문 메모리 admission·요청 한계·Go 메모리 범위 | 총 바이트 제한·모든 종료 경로 해제·스트림 사용 가능 |
| 3 | 업스트림 헤더/idle/전체 대기 취소 정책 | 숨은 생성 재시도 없음·호출 후 불명 사용량 책임 유지·고정 스트림 쓰기 마감 없음 |
| 4 | 공유 SQL/계정 경합·개인 journal 직렬화 | 원자 예약·DB 시간 윈도우·잠금 순서·신원/세대 fencing |
| 5 | 별도 내구성 설계 검토 후 감사 배치 | 내구성 확인·정확한 체인 바이트·필수 sink 압력·장애 복구 |

서로 호환되는 SHARE 테이블 잠금만으로 같은 팀 직렬화를 설명하지 않습니다.
행 잠금 경합·SQL 왕복·트랜잭션 시간을 따로 측정합니다. 복제본 추가가 공유 계정
경합을 제거하지 않습니다. 풀 증가나 fsync 제거 자체는 수용 기준이 아닙니다.

## 전달과 검토

[구현 계획](../plans/2026-10-27-go-inspection-performance.md)을 따릅니다.
각 작업은 적대적 테스트와 독립 검토 가능한 결과를 가져야 합니다.
기준 시점의 다음 ADR은 055이지만 구현 중 배정 전에 현재 색인을 확인합니다.
계획 ADR을 Accepted로 표시하거나 ADR-013의 남은 fleet/HA 검증을 다시 쓰지 않습니다.
