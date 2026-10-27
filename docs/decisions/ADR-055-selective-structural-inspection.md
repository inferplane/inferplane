# ADR-055: Select structural inspection from effective privacy policy

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status

Accepted, 2026-10-27. The user approved the Go inspection implementation plan.
Synthetic local qualification does not establish production or fleet capacity.

## Context

Responses compatibility, native Converse preflight and context/strict-target
routing require JSON structure and capability signals. The original inspector
also ran finite sensitive-data detectors on those paths without a privacy rule.
Large textual bodies paid that detection cost before admission. Disabling JSON
traversal would weaken duplicate-key, numeric, nested-input and opaque-content
checks; disabling required privacy checks would change the security contract.

## Options considered

### Keep full detection on every inspected request

- **Pros:** Preserve one inspection entry point.
- **Cons:** Pay unnecessary detector CPU for unprotected structural checks.

### Select the detector mode inside the existing Go traversal

- **Pros:** Preserve structural limits, original bytes and full privacy detection;
  retain the old API through an optional interface.
- **Cons:** Maintain explicit evidence and safe consumers of two modes.

### Introduce a global bypass or rewrite the gateway in Rust

- **Pros:** A bypass could reduce work; a rewrite could change allocation behavior.
- **Cons:** A bypass violates required privacy; a rewrite adds migration and
  maintenance cost without evidence that language choice causes this bottleneck.

## Decision

Keep Go. Preserve `Inspector.Inspect` as full finite detection and add the optional
`SelectiveInspector.InspectWithMode` interface. Its zero mode is
`DetectSensitiveData`; `StructureOnly` skips detector callbacks and category
allocation while retaining the same walker, budgets, cancellation and limits.
Full successful results carry the internal `SensitiveDataInspected` evidence bit;
opaque content still has incomplete coverage. Never publish this bit in audit
records, response bodies or metric labels.

Derive routing mode from effective privacy on the captured policy/topology
snapshot. Applicable `sensitiveData` requires full inspection, including beside
context Shadow. Context-only and strict-target-only routing uses structural mode.
Unprotected Responses still checks structure and transport compatibility.
Native Converse always checks structure before admission; applicable routing
privacy then performs full inspection. Other unprotected legacy ingresses keep
their existing routing-inspector skip.

Require full evidence before privacy classification or a usable masked result.
Inspect the original and independently reinspect transformed content in full;
the existing redactor also retains its full checks. An old `Inspector` falls
back to its documented full contract. A selective full result without evidence
refuses. Add no public `pii_enabled` switch. Learned classifiers and legacy masking
remain separate opt-ins; counts remain local, authenticated-200 and classifier-free.

## Consequences

### Positive

- Remove unnecessary finite detection from structural-only paths.
- Preserve privacy refusal, masking, byte forwarding, compatibility and accounting.
- Measure inspection, routing and the gateway independently with synthetic fakes.

### Negative

- Maintain two-mode evidence and legacy fallback tests.
- JSON decoding and capability checks still cost CPU and memory.
- Classifier networking, upstream waits, DB/journal contention and audit durability
  remain separate bottlenecks; local synthetic timings do not qualify them.

## References

- [Policy routing inspection conditions](../policy-routing.md#when-inspection-runs)
- [Adaptive routing](../adaptive-routing.md)
- [ADR-043](ADR-043-policy-aware-routing.md)
- [ADR-054](ADR-054-local-attempt-liability.md)
- Design, plan and measurements: `docs/superpowers/specs/2026-10-27-go-inspection-performance-design.md`,
  `docs/superpowers/plans/2026-10-27-go-inspection-performance.md`,
  `docs/benchmarks/inspection/`.

---

<a id="korean"></a>

# 한국어

## 상태

2026-10-27 승인됨. 사용자가 Go 검사 개선 구현 계획을 승인했습니다.
합성 로컬 검증이 운영 환경이나 전체 노드의 처리 용량을 입증하지는 않습니다.

## 배경

Responses 호환성, 네이티브 Converse 사전검사, 컨텍스트·엄격 목적지 라우팅은
JSON 구조와 기능 신호가 필요합니다. 기존 검사기는 개인정보 규칙이 없어도
이 경로에서 유한한 민감 정보 탐지기를 실행했습니다. 큰 텍스트 본문은 입장
전에 탐지 비용을 지불했습니다. JSON 순회를 끄면 중복 키·숫자·중첩 입력·
불투명 내용 검사가 약해지고, 필수 개인정보 검사를 끄면 보안 계약이 바뀝니다.

## 검토한 옵션

### 검사되는 모든 요청에서 완전 탐지 유지

- **장점:** 하나의 검사 진입점을 유지합니다.
- **단점:** 보호 정책이 없는 구조 검사에도 불필요한 탐지 CPU를 사용합니다.

### 기존 Go 순회 안에서 탐지 모드 선택

- **장점:** 구조 한계·원문 바이트·완전 개인정보 탐지를 유지하고 선택적
  인터페이스로 기존 API를 보존합니다.
- **단점:** 명시적 근거와 두 모드를 안전하게 사용하는 호출부를 유지해야 합니다.

### 전역 우회 또는 Rust로 게이트웨이 재작성

- **장점:** 우회는 작업량을 줄이고 재작성은 메모리 할당 동작을 바꿀 수 있습니다.
- **단점:** 우회는 필수 개인정보 보호를 위반하며, 언어 선택이 병목의 원인이라는
  근거 없이 재작성하면 이전·관리 비용이 늘어납니다.

## 결정

Go를 유지합니다. `Inspector.Inspect`의 완전 유한 탐지를 유지하고 선택적
`SelectiveInspector.InspectWithMode`를 추가합니다. 기본값 0은
`DetectSensitiveData`이며 `StructureOnly`는 탐지 콜백과 종류 할당만 생략합니다.
같은 순회·예산·취소·한계를 유지합니다. 완전 검사가 성공하면 내부 근거 비트
`SensitiveDataInspected`를 설정하지만 불투명 내용의 검사 범위는 여전히
불완전합니다. 이 비트를 감사 레코드·응답 본문·메트릭 라벨에 게시하지 않습니다.

캡처한 정책·토폴로지 스냅샷의 적용 개인정보 규칙에서 라우팅 모드를 정합니다.
`sensitiveData`가 적용되면 컨텍스트 Shadow와 함께 있어도 완전 검사합니다.
컨텍스트만 있거나 엄격 목적지만 있으면 구조 모드를 사용합니다. 보호 정책이
없는 Responses도 구조와 전송 호환성을 검사합니다. 네이티브 Converse는 입장
전에 항상 구조 검사하고 라우팅 개인정보가 적용되면 이어서 완전 검사합니다.
다른 무보호 기존 수신 경로는 라우팅 검사기를 생략하는 동작을 유지합니다.

개인정보 분류와 사용 가능한 마스킹 결과에는 완전 검사 근거를 요구합니다.
원문 완전 검사와 변환 후 독립 완전 재검사를 수행하고 기존 redactor의 완전
검사도 유지합니다. 기존 `Inspector`는 문서화된 완전 검사 계약으로 폴백합니다.
선택적 완전 검사 결과에 근거가 없으면 거부합니다. 공개 `pii_enabled` 설정은
추가하지 않습니다. 학습 분류기와 기존 마스킹은 별도 옵트인이고 count는
로컬·인증 후 200·분류기 호출 없음 동작을 유지합니다.

## 영향

### 긍정적

- 구조 전용 경로에서 불필요한 유한 탐지를 제거합니다.
- 개인정보 거부·마스킹·바이트 전달·호환성·정산을 유지합니다.
- 합성 가짜 업스트림으로 검사·라우팅·게이트웨이를 각각 측정합니다.

### 부정적

- 두 모드의 근거와 기존 API 폴백 테스트를 유지해야 합니다.
- JSON 디코딩과 기능 검사는 계속 CPU와 메모리를 사용합니다.
- 분류기 네트워크·업스트림 대기·DB/journal 경합·감사 내구성은 별도 병목이며
  합성 로컬 시간으로 검증할 수 없습니다.

## 참고 자료

- [정책 라우팅 검사 조건](../policy-routing.md#when-inspection-runs)
- [적응형 라우팅](../adaptive-routing.md)
- [ADR-043](ADR-043-policy-aware-routing.md)
- [ADR-054](ADR-054-local-attempt-liability.md)
- 설계·계획·측정: `docs/superpowers/specs/2026-10-27-go-inspection-performance-design.md`,
  `docs/superpowers/plans/2026-10-27-go-inspection-performance.md`,
  `docs/benchmarks/inspection/`.
