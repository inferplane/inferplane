# Synthetic inspection baseline

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Provenance

Measured on 2026-10-27 with Go 1.26.10, linux/arm64 and `GOMAXPROCS=4`.
Runtime baseline: `0360e81`; documentation snapshot: `9d1226e`.
The first benchmark commit in this directory's history contains the baseline
scaffolds. Freeze its test binaries before changing runtime code; their SHA-256
values are in `metadata.json`. Fixture hashes come from the shared Go generator
and appear in `fixture-manifest.json`.

`baseline-summary.json` validates **274 cases, five trials each** across the three
phases and the separate fixed-count concurrency cases. Raw `baseline-*.txt` files
include operation counts, allocations, actual worker counts, failures, per-request
p50/p95/p99 and end-of-case process RSS. Gateway rows require exactly one fake
dispatch per admitted operation, zero for expected privacy refusals.

## Scope and limits

Use five ingress protocols with 20 KiB/200 KiB/1 MiB/8 MiB synthetic request bodies.
Measure all protocol/body combinations without policy; cover policy modes at
200 KiB/1 MiB. Sweep routing/gateway concurrency on Responses without policy and
with context Shadow; inspection sweeps every protocol/size. Requested concurrency
is a ceiling of 1/8/32, bounded by a **16 MiB logical in-flight body cap** and the
operation count. Fixed `32x` Responses cases at 200 KiB/1 MiB remove adaptive
iteration-count differences: actual worker ceilings are respectively 32 and 16.
This cap does not bound decoded heap or total process memory.

The gateway measures its real local handler pipeline and SQLite key auth with
zero-delay fake provider I/O, explicitly free prices, a 64 Mi-token synthetic
context window, no money/rate caps, and an async local audit WAL with no required
external sink. Fixture construction, policy installation, authentication-key
creation and warmup are outside timers. Worker/sample-buffer overhead is inside.
Percentiles are sampled operation latency; `ns/op` is throughput-derived.
RSS is `VmRSS` after each case, **not peak RSS**.

The learned classifier is a local Clear fake with cache disabled, not SystemOne
or an HTTP round trip. Native Converse's existing classifier-segment alias refusal
remains fail closed: its gateway classifier cases expect 403/zero dispatch, and
its routing-only internal policy retains an internal destination. These cases
do not qualify successful learned classification.

ADR-045 journal, ADR-046 shared Postgres, real upstream/client quality, real
classifier latency and optional isolated 60 MiB stress remain **unmeasured**.
Correctness testing with Postgres is separate from this performance baseline.
Never infer Rust gains, production throughput or gateway CPU by subtracting
unrelated end-to-end model runs.

## Reproduce

Run each package separately without competing tests or benchmarks:

```bash
GOMAXPROCS=4 go test ./internal/sensitivity -run '^$' -bench BenchmarkInspect -benchmem -benchtime=100ms -count=5
GOMAXPROCS=4 go test ./internal/router -run '^$' -bench BenchmarkRouteRequestInspection -benchmem -benchtime=100ms -count=5
GOMAXPROCS=4 go test ./cmd/mayu -run '^$' -bench BenchmarkGatewayInspection -benchmem -benchtime=100ms -count=5
GOMAXPROCS=4 go test ./internal/router -run '^$' -bench 'BenchmarkRouteRequestInspection/responses/none/bytes=(204800|1048576)$/c=32$' -benchmem -benchtime=32x -count=5
python3 docs/benchmarks/inspection/analyze.py
```

Apply the fixed-count selector to the corresponding inspection/gateway cases too.
Profiles use each phase's Responses/no-policy/1 MiB/C1 case with `10x`, count 1,
and Go CPU/heap/mutex/block flags. The profile summaries include setup and teardown;
heap summaries show `inuse_space`. Raw profiles and pinned binaries are retained
in `/tmp/inferplane-go-inspection-20261027/`, outside Git.
Brief compilation/RED checks overlapped a few main-matrix samples; use the isolated
fixed-count trials and repeat uncertain full-mode comparisons before accepting gains.

---

<a id="korean"></a>

# 한국어

## 근거

2026-10-27, Go 1.26.10·linux/arm64·`GOMAXPROCS=4`에서 측정했습니다.
런타임 기준은 `0360e81`, 문서 스냅샷은 `9d1226e`입니다. 이 디렉터리 이력의
첫 벤치마크 커밋에 기준 측정 코드가 있습니다. 런타임 수정 전에 테스트 바이너리를
고정했으며 SHA-256은 `metadata.json`에 있습니다. 공통 Go 생성기의 fixture
해시는 `fixture-manifest.json`에 있습니다.

`baseline-summary.json`은 세 단계와 별도 고정 반복 동시성의 **274개 사례를
각 5회** 검증합니다. 원시 `baseline-*.txt`에는 작업 수·할당·실제 worker 수·
실패·요청별 p50/p95/p99·사례 종료 시 프로세스 RSS가 있습니다. 게이트웨이는
허용 작업당 가짜 호출 정확히 1회, 예상 개인정보 거부에서는 0회를 요구합니다.

## 범위와 한계

5개 수신 프로토콜에 20 KiB/200 KiB/1 MiB/8 MiB 합성 본문을 사용합니다.
무정책은 모든 프로토콜·크기 조합을, 정책 모드는 200 KiB/1 MiB를 측정합니다.
라우팅·게이트웨이 동시성은 무정책과 컨텍스트 Shadow의 Responses에서 변화시키고,
검사 동시성은 모든 프로토콜·크기에서 변화시킵니다. 요청 동시성 1/8/32는 상한이며
**처리 중 논리적 본문 16 MiB**와 작업 수로 제한합니다. 200 KiB/1 MiB Responses의
고정 `32x` 사례는 적응형 반복 수 차이를 제거하며 실제 worker 상한은 각각
32와 16입니다. 이 제한은 디코딩 힙이나 전체 프로세스 메모리의 제한이 아닙니다.

게이트웨이는 실제 로컬 핸들러 파이프라인·SQLite 키 인증을 사용하고 공급자 I/O는
지연 0의 가짜 구현입니다. 명시적 무료 가격, 합성 64 Mi 토큰 컨텍스트, 금액·속도
상한 없음, 필수 외부 sink 없는 비동기 로컬 감사 WAL을 사용합니다. fixture 생성·
정책 설치·인증 키 생성·워밍업은 타이머 밖이고 worker·표본 버퍼 비용은 포함합니다.
백분위는 표본 작업 지연이고 `ns/op`는 처리량에서 계산합니다. RSS는 사례 종료
후 `VmRSS`이며 **최대 RSS가 아닙니다**.

학습 분류기는 캐시를 끈 로컬 Clear 가짜 구현이며 SystemOne이나 HTTP 왕복이
아닙니다. 네이티브 Converse의 기존 분류기 segment 별칭 거부는 fail-closed로
유지합니다. 해당 게이트웨이 사례는 403·호출 0회를 기대하고 라우팅 전용 내부
정책은 내부 목적지를 유지합니다. 이 사례로 학습 분류 성공을 검증하지 않습니다.

ADR-045 저널·ADR-046 shared Postgres·실제 업스트림/클라이언트 품질·실제
분류기 지연·선택적 단독 60 MiB 부하는 **미측정**입니다. Postgres 정확성 테스트는
이 성능 기준과 별개입니다. Rust 이점·운영 처리량을 추론하거나 무관한 모델
전체 응답 시간의 차이로 게이트웨이 CPU를 계산하지 않습니다.

## 재현

경쟁하는 테스트·벤치마크 없이 패키지를 각각 실행합니다.

```bash
GOMAXPROCS=4 go test ./internal/sensitivity -run '^$' -bench BenchmarkInspect -benchmem -benchtime=100ms -count=5
GOMAXPROCS=4 go test ./internal/router -run '^$' -bench BenchmarkRouteRequestInspection -benchmem -benchtime=100ms -count=5
GOMAXPROCS=4 go test ./cmd/mayu -run '^$' -bench BenchmarkGatewayInspection -benchmem -benchtime=100ms -count=5
GOMAXPROCS=4 go test ./internal/router -run '^$' -bench 'BenchmarkRouteRequestInspection/responses/none/bytes=(204800|1048576)$/c=32$' -benchmem -benchtime=32x -count=5
python3 docs/benchmarks/inspection/analyze.py
```

대응하는 검사·게이트웨이에도 고정 반복 선택자를 적용합니다. 프로파일은 각 단계의
Responses·무정책·1 MiB·C1 사례를 `10x`, 1회, Go CPU/heap/mutex/block
플래그로 실행합니다. 프로파일 요약에는 준비·종료 작업이 포함되고 heap은
`inuse_space`를 보여 줍니다. 원시 프로파일과 고정 바이너리는 Git 밖의
`/tmp/inferplane-go-inspection-20261027/`에 보존합니다. 짧은 컴파일·RED 검사가
일부 기본 측정 표본과 겹쳤으므로 독립 고정 반복 표본을 사용하고 불확실한
완전 모드 비교를 반복한 뒤 개선을 판정합니다.
