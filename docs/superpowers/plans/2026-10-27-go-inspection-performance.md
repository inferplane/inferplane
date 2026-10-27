# Go inspection performance implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Measure gateway overhead and remove unnecessary deterministic PII detection while preserving required structural and privacy checks.

**Architecture:** Keep the existing JSON walker and full `Inspector.Inspect` contract. Add an optional selective interface, choose the mode from effective routing policy, and use structure-only native Converse preflight. Preserve full original inspection and independent reinspection for masking.

**Tech Stack:** Go 1.26.10, standard library, existing fake-provider/httptest fixtures, optional isolated Postgres test fixture.

**Spec:** [Design](../specs/2026-10-27-go-inspection-performance-design.md).

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status

Planning only, 2026-10-27, baseline `0360e81`. No task below has been implemented.
Complete Tasks 1–4 as one inspection project; create separate measured designs
for memory admission, upstream waits, SQL/journal contention and audit durability.

## Global constraints

- Keep Go 1.26.10 and `CGO_ENABLED=0`; add no production dependency or Rust component.
- Preserve full `Inspector.Inspect` behavior and the optional interface from the spec.
- Keep privacy full/fail-closed, including next to context Shadow; never add a global bypass.
- Preserve original body bytes except existing authorized transformations.
- Keep structural/numeric/nested/opaque limits and authenticated count-200 behavior.
- Keep profile-qualified admission, monetary uncertainty and single settlement unchanged.
- Never retain real prompts/credentials or add identity/text/category-value metric labels.
- Record at least five benchmark trials; cap aggregate in-flight payload bytes explicitly.
- Preserve English/Korean public document pairs and generated AI-context validation.

## Review focus

1. Duplicate keys, encoded JSON and numeric identifiers must not bypass mandatory traversal.
2. Responses stored references/opaque content and native Converse unknown fields retain compatibility and byte semantics.
3. Effective privacy plus Shadow, classifier failures and misleading selective evidence cannot look clean.
4. Masking must inspect original and transformed content with both required detector sets.
5. Cancellation/fallback must keep holds, once-only settlement and count/admission behavior.

## Task 1: Establish reproducible baseline

**Files:** Create `internal/sensitivity/inspect_bench_test.go`,
`internal/router/inspection_bench_test.go`, `cmd/mayu/inspection_bench_test.go`
and `docs/benchmarks/inspection/baseline.md`. The existing untracked benchmark
directories belong to the user; choose unused files and preserve their contents.

**Interfaces:** Produce a sensitivity-local fixture helper
`inspectionBenchmarkFixture(protocol string, bodyBytes int) []byte`,
`TestInspectionBenchmarkFixtures`, `BenchmarkInspect`,
`BenchmarkRouteRequestInspection`, and `BenchmarkGatewayInspection`.
Create payloads/setup outside timers; integrate existing gateway test helpers.
Use fake dispatch counters and fixed fake latency; disable real credentials.

- [ ] Write `TestInspectionBenchmarkFixtures` first: syntactically valid protocol bodies, declared sizes, nested tools and opaque fixtures, with no real text/account data.
- [ ] Run `go test ./internal/sensitivity -run '^TestInspectionBenchmarkFixtures$' -count=1`; expect missing helper/fixture failure before implementation.
- [ ] Add bounded synthetic fixtures and baseline benchmarks with `b.ReportAllocs()`. Cover five protocols, 20 KiB/200 KiB/1 MiB/8 MiB, concurrency 1/8/32 and policy modes from the spec. Set an explicit in-flight byte cap; run optional 60 MiB alone.
- [ ] Run `go test ./internal/sensitivity -run '^TestInspectionBenchmarkFixtures$' -count=1`; require all fixture assertions to pass.
- [ ] Run `go test ./internal/sensitivity ./internal/router ./cmd/mayu -run '^$' -bench 'Benchmark(Inspect|RouteRequestInspection|GatewayInspection)' -benchmem -count=5`; record output and complete metadata in the baseline artifact.
- [ ] Collect CPU/heap and mutex/block profiles in separate identical runs. Record raw samples, RSS, p50/p95/p99, dispatches and errors; mark unavailable journal/shared profiles unmeasured.
- [ ] Review the benchmark's actual timed scope and fixture validity; commit this measured baseline separately with DCO sign-off.

## Task 2: Add selective structural inspection

**Files:** Create `internal/sensitivity/inspection_mode.go`,
`inspection_mode_test.go`, `inspection_mode_fuzz_test.go`; modify `inspect.go`
and `inspect_bench_test.go`. Keep `redact.go` full-inspection calls unchanged.

**Interfaces:** Produce `InspectionMode`, `DetectSensitiveData=0`,
`StructureOnly=1`, `SelectiveInspector.InspectWithMode(context.Context, string,
[]byte, InspectionMode) (Result, error)` and `Result.SensitiveDataInspected bool`.
`NewInspector()` still returns `Inspector`; its concrete value implements both.

- [ ] Write `TestInspectionModesPreserveStructure`, `TestInspectionFullEvidence`, `TestInspectionInvalidMode`, `TestInspectionModesRejectMalformed`, and `FuzzInspectionModesPreserveStructure`.
- [ ] Pin Review Focus 1: duplicate keys, numeric exactness, escaped/nested tool JSON, resource limits and cancellation yield the same structural outcome. Compare all result fields except categories and the explicit evidence bit.
- [ ] Run `go test ./internal/sensitivity -run 'TestInspection(Modes|FullEvidence|InvalidMode)' -count=1`; expect missing API/evidence failure.
- [ ] Implement mode validation and one shared walker; structure-only skips detector callbacks/category allocation but retains text traversal and all limits. Successful full mode sets evidence true, even with no categories.
- [ ] Run `go test ./internal/sensitivity -race` and `go test ./internal/sensitivity -run '^$' -fuzz '^FuzzInspectionModesPreserveStructure$' -fuzztime=30s -parallel=2`; require unchanged full inspection/redaction tests.
- [ ] Extend `BenchmarkInspect` with full/structure subcases and repeat the baseline command five times. Record results rather than asserting timing in unit tests.
- [ ] Review full-API compatibility and content-free errors; commit with DCO sign-off.

## Task 3: Select inspection by effective policy

**Files:** Create `internal/router/inspection.go` and
`internal/router/inspection_mode_test.go`; modify `request_routing.go`,
`inspection_bench_test.go` and existing classifier/privacy/Responses routing tests.

**Interfaces:** Consume Task 2 and produce
`inspectForRoute(context.Context, sensitivity.Inspector, string, []byte,
sensitivity.InspectionMode) (sensitivity.Result, error)`.
Preserve old injected inspectors by safe full-API fallback. Selective full results
without evidence fail before privacy/classification; do not publish a new audit field.

- [ ] Write `TestRoutingInspectionModes`, `TestRoutingLegacyInspectorFallback`, `TestRoutingPrivacyRequiresInspectionEvidence`, and `TestRoutingShadowDoesNotLoosenPrivacy` using a mode-recording selective fake plus an old-API fake.
- [ ] Pin Review Focus 2–3: no-policy Responses still rejects malformed requests and refuses opaque bridging; strict/context-only uses structure-only; layered privacy uses full mode; failed/false evidence refuses without dispatch or a classifier-clear cache entry.
- [ ] Run `go test ./internal/router -run 'TestRouting(Inspection|LegacyInspector|PrivacyRequires|ShadowDoesNot)' -count=1`; require failure before adding selection.
- [ ] Implement the helper and derive mode from existing `hasPrivacy` on the captured request snapshot. Keep no-rule Messages/Chat/InvokeModel skips. Preserve context FailOpen/Shadow error behavior and strict-target refusal.
- [ ] Pin Review Focus 4: extend Mask/combined legacy/classifier cases to assert independent full reinspection and refusal of unsafe or incomplete changes; no clean verdict comes from structure-only evidence.
- [ ] Run `go test ./internal/router ./internal/sensitivity ./internal/classifier -race`; then repeat routing benchmarks against Task 1 baseline.
- [ ] Review generation/routing evidence and injected-inspector compatibility; commit with DCO sign-off.

## Task 4: Integrate native preflight and qualify all ingresses

**Files:** Modify `internal/server/bedrockapi/invoke.go`, add
`internal/server/bedrockapi/inspection_mode_test.go`, extend
`cmd/mayu/inspection_bench_test.go` and existing ingress/acceptance tests.
Update `docs/policy-routing.md`/`.ko.md`, `docs/adaptive-routing.md`/`.ko.md`,
`docs/architecture.md`/`.ko.md`, root/module instructions and benchmark artifacts.
Allocate ADR-055 only if still free; its status follows actual approval.

**Interfaces:** Consume the selective inspector while retaining an `Inspector`
reference for full compatibility fallback; native Converse preflight requests
structure-only without changing malformed rejection or forwarding.
Native privacy routing still requests full mode before admission.

- [ ] Write `TestNativeConverseStructureOnlyPreflight` and `TestNativeConversePrivacyStillFull`; prove byte preservation, unknown-field forwarding, malformed/duplicate rejection and privacy refusal before dispatch.
- [ ] Run `go test ./internal/server/bedrockapi -run 'TestNativeConverse(StructureOnly|PrivacyStill)' -count=1`; require the test to fail under unconditional detector preflight. Use an internal injectable seam only if necessary to prove the call, not a production metric.
- [ ] Switch the native preflight to structural mode with safe legacy fallback; preserve all generation/accounting gates and no hidden retries.
- [ ] Pin Review Focus 5 across Messages/Chat/Responses/InvokeModel/Converse: disconnect before usage, partial usage, missing terminal frame, unknown TTL, fallback rejection, and exactly-once settlement preserve existing liabilities. Counts remain local/authenticated-200 and classifier-free.
- [ ] Run `go test ./internal/server/... ./cmd/mayu -race`; run all five-protocol synthetic benchmark comparisons with the same metadata/sample counts as baseline.
- [ ] Apply the spec's correctness and recommended performance gates. Investigate noisy or >5% full-mode regressions; do not claim gains without raw evidence or merge a failed performance proposal.
- [ ] Update public bilingual behavior only after implementation; record measured scope/limitations, approved ADR and regenerated AI context. Preserve reference bodies under `/sync-docs` restrictions.
- [ ] Run both `CGO_ENABLED=0 go build` commands, `go test ./... -race`, `go vet ./...`, formatting, `bash tests/run-all.sh`, translation checks, strict MkDocs and site-link checks. Optional profile coverage must name the fixtures actually exercised.
- [ ] Review the final diff independently and commit with DCO sign-off; publish only within the session's authorized PR scope.

## Later projects and plan self-review

Use the design's priority table after measurement: aggregate body memory,
upstream wait cancellation, shared SQL/private journal, then audit batching.
Each needs its own design and adversarial acceptance tests; none is implicitly
authorized for implementation by this planning document.

All proposed interfaces have an owner and consumer; Tasks 2–3 preserve the old
API. Tasks 2–4 cover every Review Focus item. The full validation requirement
appears in Task 4; DB-free core measurements and optional profile qualification
are explicitly distinguished. Planning checkboxes remain unchecked.

---

<a id="korean"></a>

# 한국어

## 상태

2026-10-27, 기준 `0360e81`의 계획입니다. 아래 작업은 아직 구현하지 않았습니다.
작업 1–4를 하나의 검사 개선 프로젝트로 진행합니다. 메모리 admission·업스트림
대기·SQL/journal 경합·감사 내구성은 측정 후 별도 설계를 작성합니다.

## 공통 제약

- Go 1.26.10·`CGO_ENABLED=0` 유지; 운영 의존성·Rust 구성 요소 추가 없음.
- 명세의 완전 `Inspector.Inspect` 동작과 선택 인터페이스 유지.
- Shadow와 함께 적용되어도 개인정보 완전 검사·fail-closed 유지; 전역 우회 없음.
- 기존 허용 변환 외 원문 바이트 보존.
- 구조/숫자/중첩/불투명 내용 한계와 인증 후 count-200 유지.
- 프로필별 admission·금액 불확실성·단일 정산 유지.
- 실제 프롬프트·자격증명 보존 또는 신원/텍스트/탐지 값 메트릭 라벨 추가 없음.
- 최소 5회 측정; 전체 처리 중 본문 바이트 명시적 제한.
- 공개 영문·한국어 문서 쌍과 생성 AI 컨텍스트 검증 유지.

## 검토 초점

1. 중복 키·인코딩 JSON·숫자 식별자가 필수 순회를 우회하지 않습니다.
2. Responses 저장 참조/불투명 내용·네이티브 Converse 미지 필드의 호환성과 바이트 의미를 유지합니다.
3. Shadow 옆 개인정보·분류기 오류·잘못된 선택 검사 근거를 깨끗한 결과로 처리하지 않습니다.
4. 마스킹은 필요한 두 탐지기 집합으로 원문과 변환 결과를 검사합니다.
5. 취소/폴백 시 예약·단일 정산·count/admission 동작을 유지합니다.

## 작업 1: 재현 가능한 기준 측정

**파일:** `internal/sensitivity/inspect_bench_test.go`,
`internal/router/inspection_bench_test.go`, `cmd/mayu/inspection_bench_test.go`,
`docs/benchmarks/inspection/baseline.md`를 만듭니다. 기존 untracked benchmark
디렉터리는 사용자의 자료이므로 사용하지 않은 파일을 선택하고 내용을 보존합니다.

**인터페이스:** sensitivity 안의
`inspectionBenchmarkFixture(protocol string, bodyBytes int) []byte`,
`TestInspectionBenchmarkFixtures`, `BenchmarkInspect`,
`BenchmarkRouteRequestInspection`, `BenchmarkGatewayInspection`을 제공합니다.
본문/설정 생성은 타이머 밖에서 하고 기존 게이트웨이 테스트 보조 코드를 활용합니다.
가짜 호출 카운터·고정 지연을 쓰고 실제 자격증명을 끕니다.

- [ ] `TestInspectionBenchmarkFixtures`를 먼저 작성합니다. 유효한 프로토콜 본문·선언 크기·중첩 도구·불투명 fixture를 검증하고 실제 텍스트/계정을 넣지 않습니다.
- [ ] `go test ./internal/sensitivity -run '^TestInspectionBenchmarkFixtures$' -count=1`을 실행합니다. 구현 전에는 helper/fixture 누락으로 실패해야 합니다.
- [ ] 제한된 합성 fixture와 `b.ReportAllocs()` 기준 벤치마크를 구현합니다. 5개 프로토콜·20 KiB/200 KiB/1 MiB/8 MiB·동시성 1/8/32·명세 정책 모드를 포함합니다. 전체 처리 바이트를 제한하고 선택적 60 MiB는 단독 실행합니다.
- [ ] `go test ./internal/sensitivity -run '^TestInspectionBenchmarkFixtures$' -count=1`로 fixture 검증 통과를 확인합니다.
- [ ] `go test ./internal/sensitivity ./internal/router ./cmd/mayu -run '^$' -bench 'Benchmark(Inspect|RouteRequestInspection|GatewayInspection)' -benchmem -count=5`로 결과와 전체 메타데이터를 기록합니다.
- [ ] 동일한 별도 실행으로 CPU/heap·mutex/block 프로파일을 수집합니다. 원시 표본·RSS·p50/p95/p99·호출·오류를 기록하고 미사용 journal/shared는 미측정으로 표시합니다.
- [ ] 측정 구간과 fixture 유효성을 검토하고 DCO 서명으로 기준 측정을 별도 커밋합니다.

## 작업 2: 선택적 구조 검사

**파일:** `internal/sensitivity/inspection_mode.go`,
`inspection_mode_test.go`, `inspection_mode_fuzz_test.go`를 만들고 `inspect.go`와
`inspect_bench_test.go`를 수정합니다. `redact.go`의 완전 검사 호출은 유지합니다.

**인터페이스:** `InspectionMode`, `DetectSensitiveData=0`, `StructureOnly=1`,
`SelectiveInspector.InspectWithMode(context.Context, string, []byte,
InspectionMode) (Result, error)`, `Result.SensitiveDataInspected bool`을 제공합니다.
`NewInspector()`는 여전히 `Inspector`를 반환하고 실제 값은 두 API를 구현합니다.

- [ ] `TestInspectionModesPreserveStructure`, `TestInspectionFullEvidence`, `TestInspectionInvalidMode`, `TestInspectionModesRejectMalformed`, `FuzzInspectionModesPreserveStructure`를 작성합니다.
- [ ] 검토 초점 1을 고정합니다. 중복 키·정확한 숫자·이스케이프/중첩 도구 JSON·자원 한계·취소의 구조 결과가 같아야 합니다. 종류와 근거 비트 외 모든 결과 필드를 비교합니다.
- [ ] `go test ./internal/sensitivity -run 'TestInspection(Modes|FullEvidence|InvalidMode)' -count=1`을 실행해 API/근거 누락 실패를 확인합니다.
- [ ] 모드 검증과 단일 순회를 구현합니다. 구조 모드는 탐지 콜백/종류 할당만 생략하고 문자열 순회와 한계를 유지합니다. 완전 성공은 종류가 없어도 근거 true입니다.
- [ ] `go test ./internal/sensitivity -race`와 `go test ./internal/sensitivity -run '^$' -fuzz '^FuzzInspectionModesPreserveStructure$' -fuzztime=30s -parallel=2`를 실행하고 기존 완전 검사/마스킹 테스트 통과를 확인합니다.
- [ ] `BenchmarkInspect`에 완전/구조 하위 사례를 추가해 기준 명령을 5회 반복합니다. 시간 단위 테스트 대신 측정 결과를 기록합니다.
- [ ] 완전 API 호환성과 내용을 포함하지 않는 오류를 검토하고 DCO 서명으로 커밋합니다.

## 작업 3: 유효 정책에 따른 검사 선택

**파일:** `internal/router/inspection.go`,
`internal/router/inspection_mode_test.go`를 만들고 `request_routing.go`,
`inspection_bench_test.go`, 기존 분류기/개인정보/Responses 라우팅 테스트를 수정합니다.

**인터페이스:** 작업 2를 사용하고
`inspectForRoute(context.Context, sensitivity.Inspector, string, []byte,
sensitivity.InspectionMode) (sensitivity.Result, error)`를 제공합니다.
기존 주입 검사기는 안전한 완전 API 폴백으로 유지합니다. 선택 검사기의 완전 결과에
근거가 없으면 개인정보/분류 전에 실패하며 새 감사 필드는 게시하지 않습니다.

- [ ] 모드를 기록하는 선택 fake와 기존 API fake로 `TestRoutingInspectionModes`, `TestRoutingLegacyInspectorFallback`, `TestRoutingPrivacyRequiresInspectionEvidence`, `TestRoutingShadowDoesNotLoosenPrivacy`를 작성합니다.
- [ ] 검토 초점 2–3을 고정합니다. 무정책 Responses의 malformed 거부·불투명 변환 차단, 엄격/컨텍스트 전용 구조 모드, 계층 개인정보 완전 모드, 오류/false 근거 시 호출·분류기-clear 캐시 금지를 검증합니다.
- [ ] `go test ./internal/router -run 'TestRouting(Inspection|LegacyInspector|PrivacyRequires|ShadowDoesNot)' -count=1`로 선택 구현 전 실패를 확인합니다.
- [ ] 보조 함수를 구현하고 요청 스냅샷의 기존 `hasPrivacy`로 모드를 정합니다. 무규칙 Messages/Chat/InvokeModel 생략과 컨텍스트 FailOpen/Shadow 오류 동작·엄격 목적지 거부를 유지합니다.
- [ ] 검토 초점 4를 고정합니다. Mask/기존 결합/분류기 사례에 독립 완전 재검사와 안전하지 않거나 불완전한 변경 거부를 추가합니다. 구조 근거에서 clean 판정을 만들지 않습니다.
- [ ] `go test ./internal/router ./internal/sensitivity ./internal/classifier -race` 후 작업 1 기준으로 라우팅 벤치마크를 반복합니다.
- [ ] 세대/라우팅 근거와 주입 검사기 호환성을 검토하고 DCO 서명으로 커밋합니다.

## 작업 4: 네이티브 사전 검사와 전체 수신 경로 검증

**파일:** `internal/server/bedrockapi/invoke.go`를 수정하고
`internal/server/bedrockapi/inspection_mode_test.go`를 추가합니다.
`cmd/mayu/inspection_bench_test.go`와 기존 수신/수용 테스트를 확장합니다.
`docs/policy-routing.md`/`.ko.md`, `docs/adaptive-routing.md`/`.ko.md`,
`docs/architecture.md`/`.ko.md`, 루트/모듈 지침과 측정 자료를 갱신합니다.
ADR-055는 비어 있을 때만 배정하고 실제 승인 상태를 적용합니다.

**인터페이스:** 선택 검사기를 사용하되 완전 호환 폴백용 `Inspector` 참조를 유지합니다.
네이티브 Converse 사전 검사는 malformed 거부/전달을 유지한 구조 모드입니다.
개인정보 라우팅은 admission 전 완전 모드를 유지합니다.

- [ ] `TestNativeConverseStructureOnlyPreflight`, `TestNativeConversePrivacyStillFull`로 바이트 보존·미지 필드 전달·malformed/중복 거부·호출 전 개인정보 거부를 검증합니다.
- [ ] `go test ./internal/server/bedrockapi -run 'TestNativeConverse(StructureOnly|PrivacyStill)' -count=1`로 기존 상시 탐지 사전 검사에서 실패를 확인합니다. 호출 증명에 필요할 때만 내부 주입 지점을 쓰고 운영 메트릭은 추가하지 않습니다.
- [ ] 안전한 기존 폴백을 포함한 구조 모드로 사전 검사를 전환합니다. 생성/정산 게이트와 숨은 재시도 금지를 유지합니다.
- [ ] 검토 초점 5를 5개 수신 경로에 고정합니다. usage 전 연결 종료·부분 usage·마지막 프레임 누락·알 수 없는 TTL·폴백 거부·단일 정산의 기존 책임, 로컬/인증 후 count-200·분류기 미호출을 검증합니다.
- [ ] `go test ./internal/server/... ./cmd/mayu -race`와 기준과 동일한 메타데이터/표본 수의 5개 프로토콜 합성 비교를 실행합니다.
- [ ] 명세의 정확성·권고 성능 기준을 적용합니다. 잡음·완전 모드 5% 초과 회귀를 조사하고 원시 근거 없이 향상을 주장하거나 목표 미달 성능안을 병합하지 않습니다.
- [ ] 구현 후 공개 양언어 동작을 갱신하고 측정 범위/한계·승인 ADR·재생성 AI 컨텍스트를 기록합니다. `/sync-docs`의 reference 본문 수정 금지 범위를 지킵니다.
- [ ] 두 `CGO_ENABLED=0 go build`, `go test ./... -race`, `go vet ./...`, 포맷, `bash tests/run-all.sh`, 번역 검사·strict MkDocs·사이트 링크 검사를 실행합니다. 실제 fixture가 있는 선택 프로필만 검증했다고 명시합니다.
- [ ] 최종 diff를 독립 검토하고 DCO 서명으로 커밋합니다. 게시 시 세션에서 허용된 PR 범위를 따릅니다.

## 후속 프로젝트와 계획 자체 점검

측정 후 설계의 우선순위 표에 따라 전체 본문 메모리·업스트림 대기 취소·공유
SQL/개인 journal·감사 배치를 진행합니다. 각각 별도 설계와 적대적 수용 테스트가
필요하며 이 계획만으로 구현이 자동 허용되는 것은 아닙니다.

제안한 인터페이스의 제공/사용 작업이 정의되어 있고 작업 2–3은 기존 API를
보존합니다. 작업 2–4가 모든 검토 초점을 다룹니다. 전체 검증은 작업 4에 포함되며
DB 없는 핵심 측정과 선택 프로필 검증을 구분합니다. 구현 체크박스는 미완료로 유지합니다.
