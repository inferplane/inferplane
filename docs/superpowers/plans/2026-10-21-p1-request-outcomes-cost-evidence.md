# P1 Request Outcomes and Cost Evidence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. 사용자 요청의 ‘담당별 구현’ 방식을 유지한다.

**Goal:** 관리자 화면에서 비용 없는 실패, 부분 응답, 비용 미확정과 bounded 오류 사유를 기존 금융 집계와 구분해 보여준다.

**Architecture:** 기존 audit JSONL을 source of truth로 유지하고 SQLite/PG read model에 동일한 관측 계약을 추가한다. Responses는 이미 확인한 오류 코드를 보존하며 관리자 API/UI는 결과와 비용 근거를 함께 표시한다. 기존 금액/토큰/requests의 대상은 변경하지 않는다.

**Tech Stack:** Go 1.26.9, pure-Go SQLite, pgx/Postgres, 정적 JavaScript/BigInt, 기존 Node DOM fixture, Python/MkDocs.

**Spec:** [P1 설계](../specs/2026-10-21-p1-request-outcomes-cost-evidence-design.md)

**Status:** 구현 전 사용자 리뷰 대기. 설계와 실행 계획을 함께 요청한 사용자에게 검토 가능한 계획 초안으로 제공한다. 설계·계획 승인을 추정하지 않는다. builder check 완료: `nothing important left`.

## Global Constraints

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
- 모든 커밋은 `git commit -s`로 DCO sign-off를 포함한다.

## Review Focus

1. 비용 없는 HTTP 오류/Outcome 없는 기록이 큐 필터 또는 Replay에서 다시 누락되는 경우: Task 1의 전체 ingestion 경로와 Task 2 PG 계약 테스트.
2. 구버전 행·원본 소실·혼합 writer를 성공/확정 비용으로 오인하는 경우: Task 1 migration, Task 2 checkpoint/rebuild, Task 4 구버전 UI 테스트.
3. Postgres correction으로 팀/모델/날짜가 바뀌어 옛 rollup에 중복 금액이 남는 경우: Task 2 old/new key correction과 stale epoch 테스트.
4. HTTP 200 이후 실패에 재시도나 이중 정산, 원문 오류 누출이 생기는 경우: Task 3 typed error, client disconnect, finish-once 테스트.
5. estimated 0과 unknown 0이 같은 UI로 보이거나 int64 금액이 반올림되는 경우: Task 4 실제 shipped JS의 DOM/API fixture 테스트.

## 실행 순서와 소유권

| 작업 | 담당 역할 / 소유 경로 | 의존성 | 검토할 독립 결과 |
|---|---|---|---|
| 1 | analytics 계약·SQLite 담당 / `internal/analytics` 직속 Go | 없음 | 비용 없는 실패까지 수집하고 기존 합계 보존 |
| 2 | PG 담당 / `internal/analytics/pgstore` | 1의 공개 타입 | migration·rollup·checkpoint parity |
| 3 | Responses 담당 / `internal/server/responsesapi` | 스펙의 코드 목록 | post-commit bounded 오류와 정산 보존 |
| 4 | 관리자 화면 담당 / `analyticsapi`, `adminui`, 필요한 admin 통합 테스트 | 1·2 | 상태·금액·구버전 API를 실제 UI에서 구분 |
| 5 | 통합·문서 담당 / 문서, 필요 시 검증 fixture | 1–4 | 회귀검사·ADR/API/운영 문서 일치 |

작업 1 계약이 고정되면 2와 3은 병렬 실행할 수 있다. 공유 파일을 두 구현 담당자에게 맡기지 않는다. 작업 4는 1의 타입과 fixture를 사용하고 2의 최종 계약 검증 후 완료한다. 각 작업은 동일 구현 담당자에게 수정 요청을 되돌리고, 별도의 fresh reviewer가 스펙과 코드 품질을 검토한다. 검토 중 컨트롤러가 담당자 코드를 몰래 수정하지 않는다.

구현 시 최초 작업 지시마다 ‘다른 담당자가 같은 저장소에서 작업 중이며 다른 변경을 되돌리지 말 것’을 명시한다. SDD ledger와 task brief는 플러그인의 절대 helper 경로를 사용하되 실행 CWD는 대상 worktree로 둔다.

---

### Task 1: 관측 계약과 SQLite projection

**Files:**
- Create: `internal/analytics/evidence.go`, `internal/analytics/evidence_test.go`, `internal/analytics/evidence_fixture_test.go`, `internal/analytics/projection_migration_test.go`.
- Modify: `internal/analytics/index.go`, `internal/analytics/sink.go`, `internal/analytics/index_test.go`, `internal/analytics/sink_test.go`, `internal/analytics/store_test.go`.

**Interfaces:**
- Consumes: `audit.Record`, 기존 `Index.Ingest(audit.Record) error`, `Index.Replay(io.Reader) (int, error)`, `Store.Summary/SummaryQuery`, `Store.TimeSeries/TimeSeriesQuery`, `Store.Recent(int, string)`.
- Produces: `OutcomeState`, `CostState`, `RecordEvidence`, `Observable(audit.Record) bool`, `EvidenceOf(audit.Record) RecordEvidence`, `BoundedErrorCode(string) string` 및 스펙 §4.3의 `OperationTotals`/Event 필드.
- `OutcomeState` 값: `succeeded`, `failed`, `partial`, `unknown`.
- `CostState` 값: `estimated`, `uncertain`, `unpriced`, `unavailable`, `legacy_unknown`.
- `OperationTotals`와 JSON 필드는 스펙 §4.3 표 그대로 사용한다. `Billable`의 의미/서명은 그대로 둔다.

- [ ] **Step 1: 실패할 분류·집계 테스트 작성.** `TestEvidenceClassification`에서 스펙 §4.1/4.2 우선순위 전체를 table test로 고정한다. `TestObservableProjectionContract`의 A–H fixture는 스펙 §8의 아래 값을 검증한다.

```text
operations: completed=8, succeeded=4, failed=2, partial=1, unknown=1
cost counts: estimated=3, uncertain=1, unpriced=1, unavailable=2, legacy_unknown=1
estimated_cost_micros=1300, uncertain_recorded_cost_micros=2050
legacy requests=6, cost_micros=3350
Recent length=8; costless records present; started/admin events absent
```

- [ ] **Step 2: RED 확인.** `go test ./internal/analytics -run 'Test(EvidenceClassification|ObservableProjectionContract)' -count=1`. 새 타입/함수가 없거나 새 관측 수치 불일치로 실패해야 한다.
- [ ] **Step 3: 순수 분류와 저장소 변경 구현.** `EvidenceOf`에 스펙의 우선순위 적용, `index.go`에 additive 열/OperationTotals scan 및 기존 필드의 has_cost 조건, `sink.go`의 필터에 Observable 적용. 새 projection_version=1, 구버전 기본값=0. SQLite conflict update는 version=0일 때만 적용한다.
- [ ] **Step 4: 경로별 실패 테스트 추가.** `TestSinkIncludesCostlessCompletion`, `TestReplayUpgradesLegacyProjectionOnce`, `TestReplayKeepsTruncatedTailOut`, `TestUnknownErrorCodeIsBounded`, `TestZeroCostRequiresEvidence`. migration 전 행 1개는 unknown/legacy_unknown/legacy_projection_records=1이며 실제 JSONL 재생 후 근거 기반 상태와 version=1로 바뀐다. 동일 ID 재생 후 건수/금액 불변. 없는 원본 행은 계속 version=0이다. 큐는 비차단이고 Close는 drain한다.
- [ ] **Step 5: GREEN 확인.** `go test ./internal/analytics -race -count=1`; 기존 Billable/model/day/금액 테스트도 함께 통과해야 한다.
- [ ] **Step 6: 작업 커밋.** 위 소유 파일만 stage하고 `git commit -s -m "feat(analytics): project completed outcomes and cost evidence"`.

### Task 2: Postgres 동일 계약과 migration/rebuild

**Files:**
- Modify: `internal/analytics/pgstore/pgstore.go`, `internal/analytics/pgstore/pgstore_test.go`, `internal/analytics/pgstore/aggregator_test.go`.
- Create: `internal/analytics/pgstore/evidence_test.go`, `internal/analytics/pgstore/projection_migration_test.go`.

**Interfaces:**
- Consumes: Task 1의 공개 분류 함수와 OperationTotals/Event. 기존 `ingestBatch(ctx context.Context, holder string, epoch int64, records []audit.Record, checkpointUpdates map[string]int64) error`, `upsertEvent(ctx context.Context, tx pgx.Tx, r audit.Record, key rollupKey) error`.
- Produces: 같은 Summary/TimeSeries/Recent JSON 계약, 동일 lease-fenced `Rebuild(context.Context) error`. 공개 Store 서명은 변경하지 않는다.
- Correction 시 이전 key 조회는 같은 fenced transaction에서 수행하며 새/옛 key 모두 recompute한다. 빈 그룹은 rollup에서 제거한다.

- [ ] **Step 1: 테스트 DB 준비 및 RED 테스트 작성.** 저장소 CI의 일회용 PG 서비스 규약을 사용한다. `INFERPLANE_TEST_PG_DSN`은 이 작업용 DB만 가리키며 기존 사용 중인 DSN을 재사용하지 않는다. `TestPGObservableProjectionContract`는 Task 1의 A–H 기대값을 동일하게 검사한다. 테스트 prerequisite가 없으면 skip을 PASS로 기록하지 않는다.
- [ ] **Step 2: RED 확인.** 테스트 전용 DSN으로 `go test ./internal/analytics/pgstore -run TestPGObservableProjectionContract -count=1`. 기존 필터 때문에 completed=8 계약이 실패해야 한다.
- [ ] **Step 3: migration/ingestion/queries 구현.** events/rollup additive 열을 같은 advisory-lock migration transaction에 넣고 모든 기존 events에서 rollup을 다시 생성한다. ingestBatch는 Observable을 포함하고 순수 EvidenceOf를 쓴다. checkpoint와 epoch fence는 변경하지 않는다. 기존 aggregate 필드는 has_cost만 계산한다.
- [ ] **Step 4: 이행과 correction 테스트 작성.** `TestPGProjectionMigrationPreservesLegacyTotals`는 예전 events/rollup을 만든 뒤 migration 2회 실행해 숫자 불변과 legacy_unknown을 검사한다. `TestPGCorrectionRecomputesBothKeys`는 같은 ID를 다른 팀/모델/날짜로 correction한 뒤 옛 그룹 제거와 총액 1회 반영을 검사한다. `TestPGRebuildRecoversCostlessHistory`는 이전 checkpoint가 지나간 비용 없는 JSONL이 명시적 rebuild 후 회복되는지 검사한다. 기존 stale-epoch 테스트가 새 관측 열에도 rollback을 검증하도록 확장한다.
- [ ] **Step 5: GREEN 확인.** `go test ./internal/analytics/pgstore -race -count=1`와 `go test ./cmd/mayu -run AnalyticsModeB -count=1`. 일회용 DB integration 테스트가 실제 실행됐는지 결과를 기록한다.
- [ ] **Step 6: 작업 커밋.** `git commit -s -m "feat(analytics): preserve outcome evidence in Postgres rollups"`.

### Task 3: Responses 오류 사유와 partial 기록 보존

**Files:**
- Modify: `internal/server/responsesapi/attempt.go`, `internal/server/responsesapi/error_code_test.go`.
- Create: `internal/server/responsesapi/stream_failure.go`, `internal/server/responsesapi/stream_failure_test.go`.
- Inspect only: `observe.go`, `internal/responses`, `providers/response_error.go`, 기존 governance/authority 테스트. settlement 수정은 이 작업 범위가 아니다.

**Interfaces:**
- Consumes: `providers.UpstreamError`, `providers.ResponseErrorCode([]byte) string`, `attempt.finish(status int, u *schema.Usage, body []byte, partial bool, ttft float64)`, 기존 typed context 오류.
- Produces: `streamFailureCode(cause error) string`, 기존 `attempt.stream(http.ResponseWriter, bool) bool`의 bounded audit/SSE error code.
- Private sentinel: `errStreamIncomplete`, `errResponseIncompatible`, `errUsageMissing`, `errClientWriteFailed`; 각각 스펙의 해당 code로 매핑한다. provider 문구를 sentinel로 분류하지 않는다.

- [ ] **Step 1: RED stream fixture 작성.** fake provider가 첫 chunk 후 `*providers.UpstreamError{StatusCode:400, Body:...context_length_exceeded...}`를 반환하게 한다. `TestCommittedStreamPreservesTypedFailure`는 HTTP 200, SSE code=context_length_exceeded, audit.partial=true, audit.error 동일, provider attempts=1, completed/settlement 각 1회를 검사한다. usage 있는/없는 경우를 모두 실행한다.
- [ ] **Step 2: RED 확인.** `go test ./internal/server/responsesapi -run TestCommittedStreamPreservesTypedFailure -count=1`. 현재 generic code/비어 있는 audit error 때문에 실패해야 한다.
- [ ] **Step 3: 오류 mapping 구현.** `streamFailureCode`는 허용 upstream code → errors.Is(context.Canceled/DeadlineExceeded) → sentinel → upstream_error 순서다. `fail`이 commit 여부 분기 전에 a.errorCode를 설정하게 한다. gateway SSE envelope는 JSON encoding을 사용한다. write error는 후속 write 없이 client_write_failed를 남긴다. no-usage와 renderer 실패 지점은 명시적 sentinel로 표시한다.
- [ ] **Step 4: 적대적·회귀 테스트 작성.** `TestStreamFailureDoesNotClassifyRawText`에서 원문에 알려진 code·가짜 키·프롬프트를 넣고 출력/감사에 미노출을 확인한다. `TestCommittedStreamNeverRetries`, `TestClientDisconnectFinishesOnce`, `TestStreamDeadlineCode`, `TestNativeStreamBytesRemainUnchanged`, `TestIncompleteStreamKeepsObservedUsage`를 추가한다. precommit 429/5xx retry, nonretryable 400, 기존 compaction 회귀를 유지한다.
- [ ] **Step 5: GREEN 확인.** `go test ./internal/server/responsesapi ./internal/responses ./internal/server/requestpolicy -race -count=1`. Cost nil에서도 완료 감사 이벤트가 남는지 확인한다.
- [ ] **Step 6: 작업 커밋.** `git commit -s -m "fix(responses): retain bounded failures after stream commit"`.

### Task 4: 관리자 API와 비용·결과 표시

**Files:**
- Modify: `internal/server/analyticsapi/analyticsapi_test.go`, `internal/server/analyticsapi/logs_test.go`.
- Modify: `internal/server/adminui/static/app.js`, `internal/server/adminui/static/index.html`, `internal/server/adminui/static/i18n.js`, `internal/server/adminui/static/style.css`.
- Create: `internal/server/adminui/analytics_evidence_test.go`, `internal/server/adminui/testdata/analytics_evidence.cjs`.
- Extend as needed: 기존 `internal/server` admin analytics 권한 테스트. 새 권한 체계는 만들지 않는다.

**Interfaces:**
- Consumes: `GET /admin/analytics/summary`, `/admin/analytics/timeseries`, `/admin/logs`의 기존 응답 + Task 1 `operations` 및 Event 신규 필드.
- Produces: `costEvidenceText(event) -> string`, `outcomeEvidenceText(event) -> string`, `operationRateText(operations) -> string`; 기존 `usd(value)`/정수 JSON parser를 사용한다.
- `costEvidenceText`는 스펙 §7 상태별 표시를 따른다. `operationRateText`는 `(failed+partial)/completed`의 백분율과 정확한 분자/분모를 표시하며 분모 0 또는 누락이면 `—`다. BigInt를 먼저 유지하고 퍼센트 표시만 소수 1자리로 만든다.

- [ ] **Step 1: RED API/JS 행동 테스트 작성.** 새 cjs는 실제 app.js/i18n.js를 Node vm + 작은 DOM/fetch fixture로 실행한다. A–H JSON으로 새 fields 직렬화, Logs 8행, Usage completed=8와 3/8 (37.5%), unknown=1을 검사한다. 하드코딩된 source 문자열 검사가 아니라 DOM 출력/실제 handler 동작을 검증한다.
- [ ] **Step 2: RED 확인.** `go test ./internal/server/analyticsapi ./internal/server/adminui -run 'Test.*Evidence' -count=1`. 현재 모든 cost를 usd로 표시하는 동작과 새 지표 없음 때문에 실패해야 한다.
- [ ] **Step 3: 화면 연결 구현.** Usage·Overview·모델/팀·날짜 표에 관측 건수와 결과 breakdown, 산정 가능한 비용, 근거 불완전 기록 금액을 연결한다. Logs에 HTTP status + outcome + cost state + bounded error code를 표시한다. 기존 raw cost를 남기는 곳은 ‘기록된 금액’으로 이름을 명확히 한다. 모든 신규 문구에 en/ko 키를 넣는다.
- [ ] **Step 4: 상태/하위호환 회귀 추가.** `TestAnalyticsEvidenceBehavior`에서 estimated zero와 unavailable/unpriced zero의 화면 차이, partial HTTP200의 경고, missing operations/cost_state의 구버전 안내, legacy_projection_records 표시를 검사한다. 금액 `9007199254740993`의 문자열 정밀도와 empty denominator, HTML처럼 생긴 unknown 오류의 escape를 검사한다. 기존 full-admin 권한 테스트에 새로운 응답 필드가 401/403으로 차단되는지 포함한다.
- [ ] **Step 5: GREEN 확인.** `go test ./internal/server/analyticsapi ./internal/server/adminui ./internal/server -race -count=1`와 `node internal/server/adminui/testdata/analytics_evidence.cjs`. 로컬 fake-data UI로 한국어/영어 렌더링·폭 좁은 화면·네트워크 실패 상태를 확인하고 screenshot은 ~/.tmp에 둔다. 외부 모델 호출은 없다.
- [ ] **Step 6: 작업 커밋.** `git commit -s -m "feat(admin): distinguish request outcomes and uncertain cost"`.

### Task 5: 운영 문서와 통합 검증

**Files:**
- Create: `docs/decisions/ADR-053-request-outcomes-cost-evidence.md` (구현 시작 시 번호 충돌 확인).
- Modify: `docs/decisions/index.md`, `docs/decisions/index.ko.md`, `docs/api-reference.md`, `docs/api-reference.ko.md`, `docs/reference/api.md`, `docs/reference/api.ko.md`, `docs/operations/observability.md`, `docs/operations/observability.ko.md`, `docs/operations/recovery.md`, `docs/operations/recovery.ko.md`, `docs/ux/admin-workspaces.md`, `docs/ux/admin-workspaces.ko.md`.

**Interfaces:**
- Consumes: Tasks 1–4의 최종 API/SQL/UI 동작과 검사 결과.
- Produces: ADR, 필드/분모/미확정 의미, SQLite 재생과 PG 명시적 rebuild 절차, 혼합 버전 rollout 한계, 실제 검증 결과. 신규 API나 runtime interface는 추가하지 않는다.

- [ ] **Step 1: 최종 동작과 문서 대조.** 스펙 수락 기준마다 담당 테스트와 실제 PASS를 연결한다. 구버전 Cost nil, 보관 원본 없음, PG checkpoint, 오류 분모 제외 항목을 문서 예제에 포함한다. 신규 부가 필드를 기존 API 예제에 추가한다.
- [ ] **Step 2: ADR/한영 문서 갱신.** 왜 기존 금융 합계와 운영 관측 분모를 나누었는지, 비용이 청구서 확정액이 아닌지, runtime failure code 허용 집합을 명시한다. live DB rebuild나 재배포를 검증 없이 완료로 쓰지 않는다.
- [ ] **Step 3: 필수 검사.** 아래를 모두 실행하며 임시/빌드 결과는 ~/.tmp에 둔다.

```bash
export TMPDIR="$HOME/.tmp"
CGO_ENABLED=0 go build -trimpath -o "$TMPDIR/inferplane-p1-mayu" ./cmd/mayu
CGO_ENABLED=0 go build -trimpath -o "$TMPDIR/inferplane-p1-inferplaned" ./cmd/inferplaned
go test ./... -race
go vet ./...
gofmt -l .
bash tests/run-all.sh
python3 -B scripts/check_docs_translations.py
python3 -B scripts/build_performance_docs.py --check
python3 -B scripts/build_client_compat_docs.py --check
python3 -B -m unittest discover -s tests/docs -v
```

기존 MkDocs venv로 `mkdocs build --strict --site-dir "$TMPDIR/inferplane-p1-site"`를 실행한다. PostgreSQL integration suite는 별도 일회용 DB에서 실제 실행 기록을 남긴다. gofmt 출력은 비어야 하고 다른 명령은 exit=0이어야 한다. 검증 명령과 수정 대상 문서 경로는 기준 worktree에서 확인했다.

- [ ] **Step 4: 독립 전체 diff 리뷰.** 별도 reviewer에게 스펙/계획/전체 diff/테스트 결과를 넘긴다. 경계 위반, 원문 노출, 금융 의미 변경, migration/rollback, repeated replay, partial no-retry를 집중 검토한다. SDD의 제한된 fix/review 규칙을 따른다.
- [ ] **Step 5: 문서 커밋 및 완료 보고.** `git commit -s -m "docs: explain outcome and cost evidence operations"`. 구현/로컬 검사/실제 DB 검사/미실행 rollout을 구분해 보고한다. Pages 배포 시 content-review 점수 85 이상과 기존 docs 배포 CI를 적용한다.

## 계획 자체 검토

- 스펙 §1–5 → Tasks 1/2, §6 → Task 3, §7 → Task 4, §8/9 → Task 5와 각 작업 테스트에 연결된다.
- 공개 타입/JSON 이름과 기존 Store/settlement 서명은 일관된다. 테스트의 정확한 수치가 A–H 표와 일치한다.
- Review Focus 5개는 각각 RED/GREEN 테스트와 소유 작업을 갖는다.
- 작업은 테스트 가능한 결과로 나눴고, 구현 함수 본문을 계획에 복제하지 않았다.
- 계획은 구현 승인·배포 승인·새 유료 모델 실험을 대체하지 않는다.
