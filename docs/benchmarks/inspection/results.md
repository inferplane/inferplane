# Selective inspection measurements

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Result

On 2026-10-27, the synthetic local comparison met the recommended inspection
performance targets. For all five protocols at C1 and 200 KiB/1 MiB,
structure-only inspection reduced median time by more than 15%. The table shows
five-trial median throughput time at C1 in milliseconds per operation.

| Stage / protocol / policy | Body | Before ms/op | After ms/op | Reduction |
| --- | --- | ---: | ---: | ---: |
| Inspector / Responses | 200 KiB | 49.557 | 1.165 | 97.6% |
| Inspector / Responses | 1 MiB | 251.946 | 6.035 | 97.6% |
| Router / Responses / no privacy | 200 KiB | 49.682 | 1.178 | 97.6% |
| Router / Responses / no privacy | 1 MiB | 252.777 | 6.094 | 97.6% |
| Gateway / Responses / no privacy | 200 KiB | 55.826 | 7.329 | 86.9% |
| Gateway / Responses / no privacy | 1 MiB | 285.404 | 35.562 | 87.5% |
| Gateway / Responses / context Shadow | 200 KiB | 56.233 | 7.787 | 86.2% |
| Gateway / Responses / context Shadow | 1 MiB | 284.684 | 38.389 | 86.5% |
| Gateway / native Converse / no privacy | 200 KiB | 58.250 | 9.853 | 83.1% |
| Gateway / native Converse / no privacy | 1 MiB | 294.575 | 48.212 | 83.6% |

These are local handler costs with zero-delay fake providers, not real model
latency or production throughput. Native Converse's preflight accounts for its
no-policy improvement; the router alone still skips unprotected legacy Converse.
Unprotected Messages/Chat/InvokeModel keep their previous routing-inspector skip.

The main matrices plus fixed concurrency validate 274 baseline and 336 after
cases, each with five trials and zero operation failures. Expected refusals have
zero dispatches; permitted gateway operations have exactly one. Requested C32
Responses comparisons use identical actual ceilings of 32 workers at 200 KiB and
16 at 1 MiB. Gateway throughput time falls 86.2% and 86.7%, respectively.

## Full inspection and evidence

Four short adaptive-concurrency full-inspection comparisons initially exceeded
5% time increase. Repeat six cases (including controls) with fixed `32x`, alternating
old/new binaries for five trials. Identical workers and interleaved medians range
from -1.76% to +1.53% change: no sustained >5% full-mode regression.
Raw repeats and `full-repeat-comparison.json` preserve the initial findings and
repeat evidence. No timing assertion was added to unit tests.

Required privacy remains full beside Shadow. Missing full evidence refuses before
classification/cache; original and transformed Mask bodies retain full checks.
Structure-only retains the same numeric/duplicate/nested/opaque/cancellation
walker. New mode, legacy fallback, cache recovery and five-ingress monetary tests
are separate from these performance samples. The differential fuzz test ran for
30 seconds and processed 912,444 inputs without a failure.

## Remaining cost and limits

Allocation reduction is small. The 1 MiB C1 no-privacy Responses gateway still
allocates about 20.2 MiB/op and native Converse about 13.2 MiB/op. `B/op` is cumulative
allocation, not resident or maximum heap. The after CPU samples point to JSON
parsing rather than regexp detection; the inspection profile has only 70 ms of
CPU samples and does not justify precise CPU-share claims. Profiles include
setup/teardown and heap summaries use `inuse_space`.

Large adaptive cases can have `b.N=1`, so their p50/p95/p99 may coincide; they do
not qualify tail latency. RSS is end-of-case process `VmRSS`, not peak RSS.
The synthetic 16 MiB in-flight body cap bounds logical payload only, not heap and
not production admission. Same fixtures, Go/GC/worker settings and binary/source
hashes are recorded in `metadata.json`, `fixture-manifest.json` and [baseline](baseline.md).
`analyze.py` validates raw completion, five-trial counts, failures and dispatches.

ADR-045/046 performance, real upstreams/clients, real classifier networking,
native learned-clear classification and isolated 60 MiB stress remain unqualified.
The existing native classifier-segment alias rejection remains fail closed and
is recorded as refusal, never as a successful classification. Correctness tests
with local Postgres do not establish shared-profile performance.

Next projects: aggregate body-memory admission and repeated parsing, then bounded
upstream waits, SQL/journal contention and audit durability. Keep Go; this
comparison provides no Go/Rust benchmark.

---

<a id="korean"></a>

# 한국어

## 결과

2026-10-27 합성 로컬 비교에서 권고 검사 성능 목표를 충족했습니다. 5개 프로토콜의
C1·200 KiB/1 MiB 구조 검사 모두 중앙값 시간이 15% 이상 줄었습니다. 표는 각 5회
표본의 C1 처리량 기준 시간이며 단위는 작업당 밀리초입니다.

| 단계 / 프로토콜 / 정책 | 본문 | 변경 전 ms/op | 변경 후 ms/op | 감소 |
| --- | --- | ---: | ---: | ---: |
| Inspector / Responses | 200 KiB | 49.557 | 1.165 | 97.6% |
| Inspector / Responses | 1 MiB | 251.946 | 6.035 | 97.6% |
| Router / Responses / no privacy | 200 KiB | 49.682 | 1.178 | 97.6% |
| Router / Responses / no privacy | 1 MiB | 252.777 | 6.094 | 97.6% |
| Gateway / Responses / no privacy | 200 KiB | 55.826 | 7.329 | 86.9% |
| Gateway / Responses / no privacy | 1 MiB | 285.404 | 35.562 | 87.5% |
| Gateway / Responses / context Shadow | 200 KiB | 56.233 | 7.787 | 86.2% |
| Gateway / Responses / context Shadow | 1 MiB | 284.684 | 38.389 | 86.5% |
| Gateway / native Converse / no privacy | 200 KiB | 58.250 | 9.853 | 83.1% |
| Gateway / native Converse / no privacy | 1 MiB | 294.575 | 48.212 | 83.6% |

지연 0의 가짜 공급자를 사용한 로컬 핸들러 비용이며 실제 모델 지연이나 운영
처리량이 아닙니다. 네이티브 Converse의 무정책 개선은 사전 검사 전환 때문입니다.
라우터 자체는 무보호 기존 Converse 검사를 여전히 생략합니다. 무보호
Messages/Chat/InvokeModel도 기존 라우팅 검사 생략을 유지합니다.

기본 행렬과 고정 동시성의 기준 274개·변경 후 336개 사례를 각각 5회 검증했고
작업 실패는 0입니다. 예상 거부는 호출 0회, 허용 게이트웨이 작업은 정확히 1회입니다.
요청 C32 Responses의 실제 worker 상한은 양쪽 모두 200 KiB에서 32, 1 MiB에서
16입니다. 게이트웨이 처리량 기준 시간이 각각 86.2%, 86.7% 줄었습니다.

## 완전 검사와 근거

짧은 적응형 동시성 비교의 완전 검사 4개 사례에서 처음에는 5% 초과 시간 증가가
나왔습니다. 대조군을 포함한 6개 사례를 `32x`로 고정하고 구·신 바이너리를 교차해
5회 재측정했습니다. 같은 worker의 교차 중앙값 변화는 -1.76%~+1.53%이며
지속적인 5% 초과 회귀는 없었습니다. 원시 반복과 `full-repeat-comparison.json`에
최초 결과와 재확인 근거를 남깁니다. 시간 단위 테스트는 추가하지 않았습니다.

Shadow와 함께 적용되는 개인정보 의무는 완전 검사입니다. 완전 검사 근거가 없으면
분류기/캐시 전에 거부하고 Mask의 원문·변환 결과 완전 검사를 유지합니다. 구조
모드도 숫자·중복·중첩·불투명 내용·취소 순회를 보존합니다. 모드·기존 폴백·캐시 복구·
5개 수신 경로 정산 테스트는 성능 표본과 별개입니다. 차등 fuzz 테스트는 30초 동안
912,444개 입력을 처리했고 실패는 없었습니다.

## 남은 비용과 한계

할당 감소는 작습니다. C1·1 MiB·무개인정보 정책의 Responses 게이트웨이는 여전히
약 20.2 MiB/op, 네이티브 Converse는 약 13.2 MiB/op를 할당합니다. `B/op`는 누적
할당이며 상주 메모리나 최대 힙이 아닙니다. 변경 후 CPU 표본에서는 정규식 대신
JSON 파싱 비용이 보입니다. 검사 CPU 표본은 70ms에 불과해 정확한 점유율을
주장하지 않습니다. 프로파일에는 준비/종료가 포함되고 heap 요약은 `inuse_space`입니다.

큰 적응형 사례의 `b.N`은 1일 수 있어 p50/p95/p99가 같을 수 있으며 tail 지연을
검증하지 않습니다. RSS는 사례 종료 시 프로세스 `VmRSS`이고 최대 RSS가 아닙니다.
합성 16 MiB 처리 중 본문 상한은 논리 본문만 제한하며 힙이나 운영 admission 제한이
아닙니다. 동일 fixture·Go/GC/worker 조건과 바이너리/소스 해시는 `metadata.json`,
`fixture-manifest.json`, [기준 측정](baseline.md)에 있습니다. `analyze.py`는 원시
완료·5회 표본·실패·호출 수를 검증합니다.

ADR-045/046 성능·실제 업스트림/클라이언트·분류기 네트워킹·네이티브 학습-clear
분류·단독 60 MiB 부하는 미검증입니다. 기존 네이티브 분류기 segment 별칭 거부는
fail-closed로 유지하며 분류 성공으로 집계하지 않습니다. 로컬 Postgres 정확성
테스트가 공유 프로필의 성능을 입증하지는 않습니다.

후속 프로젝트는 전체 본문 메모리 admission·반복 파싱, 업스트림 대기 제한,
SQL/journal 경합, 감사 내구성 순서입니다. Go를 유지하며 Go/Rust 성능 비교는 없습니다.
