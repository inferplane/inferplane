# Documentation synchronization report

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Outcome and scope

Run `project-init:sync-docs` against `0360e81` on 2026-10-27. Synchronize verified
behavior and write an improvement design/plan; do not change runtime code.

Document the distinction between mandatory ingress JSON parsing, conditional
routing inspection, optional privacy enforcement, optional learned classifiers
and optional legacy masking. Responses and native Converse currently run finite
PII detectors during unconditional inspection. Context Shadow can also incur
that cost. Detector separation is proposed, never described as implemented.

Use the [design](../specs/2026-10-27-go-inspection-performance-design.md) and
[implementation plan](2026-10-27-go-inspection-performance.md). Prioritize
measurement/selective inspection, then separately design aggregate body memory,
upstream wait cancellation, SQL/journal contention and audit durability.
Retain Go; no Rust comparison or new speedup has been measured in this task.

## Synchronized documents

| Document group | Verified correction |
| --- | --- |
| Root/module instructions | `mayu` CLI and config validation, gateway assembly, two static binaries, explicit Postgres profiles, conditional inspection |
| Provider/plugin instructions | Bounded error normalization and authorized byte changes, distinct classifier registration, qualified cache-cost claims |
| Governance/audit instructions | Process-local per-attempt monetary reservations, unknown usage liability, hard-cap independence, audit latch and repair before restart |
| README, architecture, adaptive/policy routing | Required structure versus optional privacy behavior; current unconditional Responses/Converse inspection cost |
| CHANGELOG Unreleased | Native Converse/evaluation, atomic policy import, strict configuration, monetary/client/audit/operator fixes; new entries include Korean |
| Reference INDEX | Add three existing operator references inside the established managed block |
| AGENTS | Regenerate from canonical CLAUDE, validate source hash/size/secrets; preserve existing Kiro bridge |

Review four changed English/Korean document pairs and update their source hashes.
Preserve the existing paired MkDocs layout rather than moving public documents
to same-file translations. Preserve the handwritten README and released
CHANGELOG structure; new changelog entries are bilingual. New archived planning
documents use the skill's English-first, same-file bilingual layout.

## Quality and review

Use the same instruction-quality rubric before and after changes: commands/20,
architecture/20, non-obvious patterns/15, concision/15, currency/15, actionability/15.
This is a documentation assessment, not a performance or security certification.

| Instruction | Before | After |
| --- | ---: | ---: |
| Root | 88 | 95 |
| cmd | 81 | 95 |
| internal | 80 | 88 |
| pkg | 96 | 96 |
| plugins | 85 | 97 |
| providers | 84 | 92 |
| Average | 85.7 | 93.8 |

The read-only final review found no important incorrect current-behavior claims
or current/proposed confusion. Internal instructions still contain dense legacy
paragraphs; later editing can improve concision without dropping contracts.
A fresh Go/security builder check reported “nothing important left” for the
written design. The plan self-review maps every interface and five failure
classes to owning tasks and tests.

## Preserved boundaries and remaining work

Keep all reference-layer bodies unchanged under the invoked skill's restriction.
The five applicable layers and their code pointers exist; `openaiapi/chat.go` in
the security reference is resolvable shorthand for its internal/server path,
not missing implementation. A later reference-specific edit can spell it out.
Keep reference INDEX unmanaged text and released CHANGELOG bytes unchanged.

The 77 source subdirectories inherit existing parent instructions; do not create
77 duplicate files. Preserve the user's existing untracked benchmark materials.

ADR-013 remains Proposed with partial supersession and open fleet/HA
qualification; its last change on 2026-09-13 warrants a decision review, not
automatic acceptance. Existing evaluation, policy-store and recovery documents
already cover shipped behavior; do not create redundant ADRs/runbooks.
No accepted performance ADR or runtime optimization is introduced.

## Validation

| Check | Result |
| --- | --- |
| English/Korean pairs and ADR indexes | 48 pairs valid |
| Documentation unit tests | 31 passed |
| Strict MkDocs build and site links/assets | Passed; 205 HTML pages |
| Performance/client compatibility generated-data checks | Passed; 49 models, 335 API records, 454 CLI records are existing data |
| Both static binaries, vet, formatting | Passed; 705 tracked Go files formatted |
| Bash harness | Passed |
| Full Go race suite | Passed; 62 packages, 2329 top-level and 2674 subtests; three opt-in CLI tests skipped |
| Example offline config validation | `configuration valid` |
| Generated AI context, reference/history preservation, diff whitespace | Passed |

Use a temporary Postgres 17 fixture bound to loopback for DB-backed tests and
local fake upstreams only. The three live Codex CLI acceptance tests remain
opt-in and are not used to claim current live-client behavior.
Raw verification/review artifacts are under
`/tmp/inferplane-doc-sync-20261027/`; this report preserves the conclusions.
No commit, push, merge, deployment or performance implementation is part of this sync.

---

<a id="korean"></a>

# 한국어

## 결과와 범위

2026-10-27, 기준 `0360e81`에서 `project-init:sync-docs`를 실행했습니다.
검증한 현재 동작을 동기화하고 개선 설계·계획을 작성하며 런타임 코드는 변경하지 않습니다.

필수 수신 JSON 파싱·조건부 라우팅 검사·선택적 개인정보 집행·선택적 학습 분류기·
선택적 기존 마스킹을 구분했습니다. Responses와 네이티브 Converse는 현재 상시 검사
중 유한 PII 탐지기도 실행합니다. 컨텍스트 Shadow도 이 비용을 발생시킬 수 있습니다.
탐지 분리는 제안이며 구현 완료로 서술하지 않았습니다.

[설계](../specs/2026-10-27-go-inspection-performance-design.md)와
[구현 계획](2026-10-27-go-inspection-performance.md)을 따릅니다. 측정·선택 검사
분리를 먼저 진행하고 전체 본문 메모리·업스트림 대기 취소·SQL/journal 경합·
감사 내구성은 별도 설계합니다. Go를 유지하며 이번 작업에서 Rust 비교나 새 성능
향상을 측정하지 않았습니다.

## 동기화한 문서

| 문서 묶음 | 확인한 수정 |
| --- | --- |
| 루트/모듈 지침 | `mayu` CLI·설정 검증, gateway assembly, 두 정적 바이너리, 명시적 Postgres 프로필, 조건부 검사 |
| 공급자/플러그인 지침 | 제한된 오류 정규화·허용 바이트 변경, 별도 분류기 등록, 조건을 명시한 캐시 비용 |
| 거버넌스/감사 지침 | 프로세스 로컬 시도별 금액 예약, 불명 usage 책임, 독립 hard cap, 감사 latch·재시작 전 복구 |
| README·아키텍처·적응/정책 라우팅 | 필수 구조와 선택 개인정보 구분, 현재 Responses/Converse 상시 검사 비용 |
| CHANGELOG Unreleased | Converse/평가·원자 정책 가져오기·엄격 설정·금액/클라이언트/감사/운영 수정; 새 항목 한국어 병기 |
| Reference INDEX | 기존 관리 영역 안에 운영자 레퍼런스 3개 추가 |
| AGENTS | 정본 CLAUDE에서 재생성·해시/크기/비밀 검증, 기존 Kiro bridge 보존 |

변경한 영문·한국어 4쌍을 검토하고 원문 해시를 갱신했습니다.
공개 문서는 같은 파일 번역으로 옮기지 않고 기존 MkDocs 쌍 구조를 보존했습니다.
수작업 README와 릴리스된 CHANGELOG 구조를 유지하며 새 변경 항목은 양언어로
작성했습니다. 새 보관용 계획 문서는 스킬의 영문 우선·동일 파일 양언어 구조를 적용했습니다.

## 품질과 검토

변경 전후 같은 지침 평가 기준을 사용했습니다. 명령/20·아키텍처/20·비자명 패턴/15·
간결성/15·최신성/15·실행 가능성/15입니다. 문서 평가이며 성능·보안 인증이 아닙니다.

| 지침 | 이전 | 이후 |
| --- | ---: | ---: |
| Root | 88 | 95 |
| cmd | 81 | 95 |
| internal | 80 | 88 |
| pkg | 96 | 96 |
| plugins | 85 | 97 |
| providers | 84 | 92 |
| 평균 | 85.7 | 93.8 |

읽기 전용 최종 검토에서 중요한 현재 동작 오류나 현재/제안 단계 혼동을 발견하지
않았습니다. Internal 지침에는 기존 밀집 문단이 남아 있으므로 이후 계약을 삭제하지
않는 간결화가 가능합니다. 새 Go/보안 구현자 설계 검토는 “nothing important left”로
끝났습니다. 계획 자체 점검에서 모든 인터페이스와 5개 실패 유형의 작업·테스트를 연결했습니다.

## 보존한 경계와 남은 작업

호출한 스킬의 제한에 따라 reference 계층 본문을 수정하지 않았습니다.
해당되는 5개 계층과 코드 포인터가 존재합니다. 보안 reference의 `openaiapi/chat.go`는
internal/server 경로의 확인 가능한 축약이며 구현 누락이 아닙니다.
이후 reference 전용 수정에서 정확한 경로로 표기할 수 있습니다.
INDEX 비관리 영역과 릴리스된 CHANGELOG 바이트는 보존했습니다.

소스 하위 디렉터리 77개는 기존 상위 지침을 상속하므로 중복 지침 77개를 만들지
않았습니다. 사용자의 기존 untracked benchmark 자료를 보존했습니다.

ADR-013은 부분 대체와 미완료 fleet/HA 검증을 가진 Proposed 상태입니다.
마지막 변경 2026-09-13은 결정 재검토 대상이지 자동 수용 사유가 아닙니다.
기존 평가·정책 저장소·복구 문서가 출시 동작을 다루므로 중복 ADR/runbook을 만들지
않았습니다. 승인된 성능 ADR이나 런타임 최적화를 이번 작업에 추가하지 않았습니다.

## 검증

| 검사 | 결과 |
| --- | --- |
| 영문·한국어 쌍과 ADR 색인 | 48쌍 유효 |
| 문서 단위 테스트 | 31개 통과 |
| Strict MkDocs·사이트 링크/자산 | 통과, HTML 205페이지 |
| 성능/클라이언트 호환성 생성 자료 검사 | 통과, 모델 49개·API 기록 335개·CLI 기록 454개는 기존 자료 |
| 두 정적 바이너리·vet·포맷 | 통과, 추적 Go 파일 705개 포맷 정상 |
| Bash 하네스 | 통과 |
| 전체 Go race 테스트 | 62패키지·상위 테스트 2329개·서브테스트 2674개 통과; opt-in CLI 3개 제외 |
| 예제 오프라인 설정 검증 | `configuration valid` |
| 생성 AI 컨텍스트·reference/이력 보존·diff 공백 | 통과 |

DB 테스트에는 loopback에만 열린 임시 Postgres 17 fixture를, 업스트림에는 로컬
가짜 서버만 사용합니다. 실제 Codex CLI 수용 테스트 3개는 opt-in 상태를 유지하며
현재 실제 클라이언트 동작을 검증했다고 주장하지 않습니다.
원시 검증/검토 자료는 `/tmp/inferplane-doc-sync-20261027/`에 있고 이 보고서는
결론을 보존합니다. 이번 동기화에는 커밋·push·병합·배포·성능 구현을 포함하지 않습니다.
