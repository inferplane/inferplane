# Go inspection performance execution

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status and result

Implemented and validated on 2026-10-27; final fresh-context review pending.
[Plan](2026-10-27-go-inspection-performance.md),
[design](../specs/2026-10-27-go-inspection-performance-design.md),
[measurements](../../benchmarks/inspection/results.md),
[ADR-055](../../decisions/ADR-055-selective-structural-inspection.md).

Keep Go and the full inspection API. Effective privacy, including beside Shadow,
requires full detector evidence; structural-only paths preserve the same traversal.
Native Converse preflight switches to structural mode while privacy stays full
before admission. No new public setting, provider change or production dependency.

C1 synthetic gateway time decreases about 87% for Responses and 83% for native
Converse at 200 KiB/1 MiB. Required PII/Mask coverage and monetary liabilities
remain binding. Five-trial interleaved full repeats stay within -1.8% to +1.6%.
Allocation remains a follow-up, not a resolved issue. Real upstreams, clients,
classifier networking and shared/journal performance remain unqualified.

## Verification

- Both CGO-disabled Go binaries build statically with trimpath. Temporary linked-worktree builds use `-buildvcs=false`: Go selects the synthetic `/tmp/.git` for VCS status; source/binary hashes preserve provenance.
- `go test ./... -race -json`: 62 packages, 2,343 tests and 2,758 subtests pass; no failures. Five additional packages have no tests. The only three skipped tests are opt-in real Codex CLI cases.
- Local Postgres 17 fixtures enable authority/shared and keystore tests; no live upstream/IdP/credential calls. Fixtures use separate databases and localhost-only port 55446.
- `go vet ./...`, formatting and `git diff --check` pass.
- `bash tests/run-all.sh`: 70/70 pass, zero skips.
- Differential fuzz: 30 seconds, 912,444 inputs, no failure. New API/selection/preflight tests were observed RED before implementation and GREEN afterward.
- Translation/ADR indexes: 48 pairs; documentation tests: 31; strict MkDocs/link check: 207 pages; generated performance/compatibility evidence and AI-context checks pass. Compatibility fixtures are not fresh live-client observations.
- Main/fixed benchmark comparisons: baseline 274 and after 336 cases, five trials each; six interleaved full-repeat cases, five trials each. Raw logs, profiles, summaries and hashes are under `docs/benchmarks/inspection/`.

## Decisions recorded during execution

- use a dedicated /tmp worktree with existing user materials preserved — writable sandbox roots and avoiding unrelated .gitignore changes — cost if wrong: artifacts need explicitly transferring back to the original branch.
- shared test-only synthetic fixture utility under internal/testutil/inspectionfixture avoids three divergent benchmark bodies — stdlib only, used by tests, same fixture signature retained in sensitivity — cost if wrong: helper maintenance spans benchmark packages.
- bound each benchmark sample with -benchtime=100ms, -count=5 and GOMAXPROCS=4 instead of the unbounded default matrix — preserve specified trials/coverage and compare identical runs — cost if wrong: noisy short cases require longer reruns before accepting gains.
- freeze baseline test binaries before preparing Tasks 2–4 source while measurements run — baseline remains the original runtime and only brief RED checks overlap — cost if wrong: scheduling noise requires isolated comparison reruns; binary hashes and raw trials preserve provenance.
- retain native Converse classifier alias rejection as an explicitly unqualified learned-clear case — it is an existing independent compatibility issue and must not be hidden as a successful classifier benchmark — cost if wrong: a separate compatibility fix is needed.
- create a new performance PR on the merged main tree instead of updating PR #127 — #127 is already merged and its squash tree is identical to runtime baseline 0360e81 — cost if wrong: rebase provenance must retain original benchmark binary hashes and task ranges.
- use -buildvcs=false for final temporary-worktree builds — Go VCS discovery selects the synthetic /tmp/.git rather than the linked worktree Git file; explicit commit/source/binary hashes remain recorded — cost if wrong: these test binaries lack embedded VCS stamping.

## Review

Pending the single whole-branch independent review. No final merge claim yet.

---

<a id="korean"></a>

# 한국어

## 상태와 결과

2026-10-27 구현·검증을 마쳤으며 새로운 컨텍스트의 최종 독립 리뷰가 남았습니다.
[계획](2026-10-27-go-inspection-performance.md),
[설계](../specs/2026-10-27-go-inspection-performance-design.md),
[측정](../../benchmarks/inspection/results.md),
[ADR-055](../../decisions/ADR-055-selective-structural-inspection.md).

Go와 기존 완전 검사 API를 유지합니다. Shadow와 함께 적용되는 개인정보 의무도
완전 탐지 근거를 요구하며 구조 모드는 같은 순회를 보존합니다. 네이티브 Converse
사전검사를 구조 모드로 바꾸되 개인정보는 admission 전 완전 검사입니다.
새 공개 설정·공급자 변경·운영 의존성은 없습니다.

C1·200 KiB/1 MiB 합성 게이트웨이 시간이 Responses 약 87%, 네이티브 Converse
약 83% 줄었습니다. 필수 PII/Mask와 금액 책임은 유지합니다. 5회 교차 완전 검사
변화는 -1.8%~+1.6%입니다. 할당은 해결한 문제가 아닌 후속 작업입니다.
실제 업스트림·클라이언트·분류기 네트워크·shared/journal 성능은 미검증입니다.

## 검증

- CGO를 끈 Go 바이너리 2개를 trimpath로 정적 빌드했습니다. 임시 linked worktree에서는 Go가 `/tmp/.git`을 선택하는 문제로 `-buildvcs=false`를 사용하며 소스/바이너리 해시로 근거를 보존합니다.
- 전체 race 테스트: 62개 패키지·테스트 2,343개·서브테스트 2,758개 통과, 실패 0. 추가 5개 패키지는 테스트가 없고 실제 Codex CLI opt-in 3개만 건너뜁니다.
- 로컬 Postgres 17의 분리 DB·localhost 포트 55446으로 authority/shared와 keystore 테스트를 켰습니다. 실제 업스트림·IdP·자격증명을 사용하지 않습니다.
- vet·포맷·diff 검사가 통과했고 하네스 70/70, skip 0입니다.
- 차등 fuzz 30초·912,444 입력에서 실패가 없고 API/선택/사전검사 테스트는 구현 전 RED·구현 후 GREEN을 확인했습니다.
- 번역/ADR 인덱스 48쌍·문서 테스트 31개·strict MkDocs/링크 207페이지·생성 성능/호환성 근거·AI 컨텍스트 검사가 통과했습니다. 기존 호환성 자료를 새 실제 클라이언트 관찰로 취급하지 않습니다.
- 기본/고정 비교 기준 274개·변경 후 336개와 교차 완전 재측정 6개를 각각 5회 검증했습니다. 원시 로그·프로파일·요약·해시는 `docs/benchmarks/inspection/`에 있습니다.

## 실행 판단

- `/tmp`의 분리 worktree로 기존 사용자 자료를 보존했습니다. 저장소로 자료를 옮기는 절차가 추가됩니다.
- 테스트용 공통 fixture를 사용해 세 벤치마크의 본문 차이를 막았습니다. 공통 도구 유지보수가 필요합니다.
- 100ms·5회·GOMAXPROCS=4로 표본 시간을 제한했습니다. 잡음이 있는 비교는 긴 고정 반복으로 재확인합니다.
- 기준 바이너리를 먼저 고정하고 측정 중 후속 코드를 준비했습니다. 일부 짧은 RED/컴파일 간섭은 별도 고정 반복과 교차 재측정으로 확인합니다.
- 네이티브 Converse 분류기 별칭 오류는 기존 fail-closed 거부로 기록했습니다. 분류 호환성 수정은 별도 작업입니다.
- 병합된 #127 대신 동일한 main 트리에서 새 성능 PR을 만듭니다. rebase 후에도 원래 해시·측정 근거를 보존해야 합니다.
- 임시 worktree 빌드의 Git 탐색 문제에 `-buildvcs=false`를 사용했습니다. 이 검증 바이너리에는 내장 VCS 정보가 없습니다.

## 리뷰

전체 브랜치의 독립 리뷰가 남아 있으며 최종 병합 완료를 주장하지 않습니다.
