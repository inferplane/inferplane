---
translation_source: reference/data.md
translation_source_sha256: 4d4c812ec17a123cad622b193757149d426bf01c60c38335e4d3887eb7a31f42
---

# 데이터 구현 {#data}

### 1. 개요 {#1-overview}

가상 키 저장소, 디스크 감사 로그, 두 단계 집행 저장소의 영속·메모리 상태를 설명합니다. providerstore는 SQLite 전용이고 keystore는 SQLite·Postgres를 지원합니다. 기존 limiter·budget은 인스턴스 메모리이며 Redis·Valkey 의존성은 없습니다. ADR-046은 공유 Postgres 키·원자적 자원 승인을 제공하고 기본 로컬 프로파일은 단일 복제본입니다. ADR-045는 전역 Postgres 금전 권한과 비공개 SQLite 예약 저널을 추가합니다. 본문·정책·사용량·분석 Mode B에도 Postgres를 사용할 수 있습니다.

### 2. 구성 요소 {#2-components}

| 구성 | 경로 | 역할 |
| --- | --- | --- |
| 전역 금전 권한 | `internal/authority/pgstore/`, `internal/authority/local/` | Postgres의 유한 권한·UTC 윈도, 호출 전 private 저널 예약. 추론마다 중앙 DB 호출 없음. 만료·재시작 환급 없음, 불명확 결과 상한 유지 |
| 공유 승인 | `internal/authority/pgstore/shared*.go` | 전역 RPM·TPM·달력 토큰·팀/키 금전·정책 금전 원자 예약, 원래 윈도·재전송 안전 정산 |
| 공유 키 | `internal/keystore/postgres*.go`, `import.go` | 해시 키·팀, 요청별 스냅샷, 조건부 폐기, 불변 시드 지문, 원자적 SQLite 가져오기 |
| SQLite 키 | `internal/keystore/sqlite.go` | SHA-256 키와 teams. name 기본키, allowed_models, rpm/tpm, tokens_per_day, quota_on_exceeded, budget_usd_micros, budget_on_exceeded, budget_usd_micros_per_day(0=무제한), guardrail_id/version, allowed_regions, 생성·갱신 시각 |
| 저장소 인터페이스 | `internal/keystore/keystore.go` | Store·Principal·Allows와 별도 TeamStore. KeyEnsurer도 선택적 인터페이스. SQLite EnsureKey는 key_hash 충돌 시 revoked·created_at을 건드리지 않고 갱신해 선언형 참조 키의 재생성을 지원. Create 계열은 새 임의 키 생성 |
| 공급자 저장소 | `internal/providerstore/sqlite.go` | 참조만 있는 providers, 기본 guardrail_id/version, 순서 있는 model_targets, 모델 단위 model_aliases, 영속 seeded 표시 meta. 이식 가능한 TEXT/INTEGER DDL |
| 감사 기록 | `internal/audit/writer.go` | 단일 쓰기 해시 체인·WAL 정리 |
| 감사 WAL | `internal/audit/wal.go` | buffer_then_block 디스크 버퍼 |
| 감사 검증 | `internal/audit/verify.go` | 인스턴스 구간별 체인 검증 |
| 감사 앵커 | `internal/audit/s3anchor/` | 선택적 S3 Object Lock WORM 해시 앵커, 비밀·개인정보 없는 객체 |
| 요청률 저장소 | `internal/limiter/limiter.go` | 메모리 TPM·RPM 토큰 버킷과 두 단계 처리 |
| 예산 저장소 | `internal/budget/budget.go` | 메모리 microUSD. budget:day:team:acme·budget:month:team:acme처럼 윈도 태그로 분리. 일 자정·월초 기준은 budget_timezone, 기본 UTC |
| 본문 저장소 | `internal/bodystore/` | 감사 체인 외부의 선택적 bodies. ref·record_id·team·시각·만료·크기·wrapped_key_nonce/ct·req_nonce/ct·resp_nonce/ct. BLOB/BYTEA 봉투 AEAD, 스트리밍의 응답 열은 nullable. SQLite·Postgres, TTL·용량 Purge와 행별 완전 삭제. rewrap-key는 CAS로 wrapped_key만 바꾸고 req/resp는 읽지 않음 |
| 분석 색인 | `internal/analytics/` | events의 ts·body_ref, SQLite ALTER-if-missing·Postgres ADD COLUMN IF NOT EXISTS, GET /admin/logs의 파생 뷰 |
| ULID | `pkg/ulid/ulid.go` | Crockford base32 단조 기록 ID |
| 사용량 저장소 | `internal/telemetry/` | UsageBatch·Entry 정수 비용·별도 5m/1h·해석 모델. 중복 키·NUL·1e15 초과·잘못된 윈도 거부. MemoryAggregator는 24시간·잠금 스냅샷. Postgres의 usage_windows 기본키는 dataplane/window_start/team/user/model, 윈도 색인, 지연 연결·advisory lock 847003 마이그레이션·배치 교체. 오류에 DSN 없음. DurableAggregator는 PG 커밋→메모리→ack, 조회는 PG 우선·메모리 대체에 degraded |

팀 가드레일·리전·일 예산 열은 ALTER 마이그레이션입니다. 팀 테이블은 ADR-016에서 새로 생성되었고 기존 금전 열은 CREATE TABLE에 포함되었습니다. 일 예산은 첫 예산 관련 ALTER입니다. ensureSchema는 keys·teams의 기존 열 조회와 마이그레이션 함수를 공유합니다. 공급자 가드레일 열은 auth_header와 같은 ALTER 방식이고 model_aliases는 새 테이블입니다.

### 3. 주요 결정 {#3-key-decisions}

- cgo 없는 modernc SQLite가 기본이어서 정적 바이너리로 빠르게 시작할 수 있습니다.
- 인스턴스별 감사 체인이 정상 재시작을 변조와 구분합니다.
- 관리 이벤트 admin_key_created·revoked·denied는 이메일 아닌 불투명 sub와 auth_method(oidc·break_glass)를 기록합니다. auth_method는 PrincipalRef 끝에 덧붙여 혼합 버전 바이트 검증을 유지합니다.
- 검사 후 차감하는 두 단계 처리로 거부된 요청이 팀을 과금하지 않게 합니다.
- 프롬프트·응답 원문은 감사 체인에 넣지 않습니다. 선택적 audit.log_bodies가 별도의 암호화·삭제 가능 저장소에 보관하고 체인에는 불투명 body_ref만 넣습니다. body_ref·record_ref도 omitempty로 구조체 끝에 추가합니다.
- 예산 치환의 model_substituted_from에는 원래 요청 모델을 보관합니다. model_requested에는 이미 처리 모델이 들어 있습니다. 이 필드는 분석 읽기 모델에 아직 없으므로 감사 체인이 기준입니다.
- 같은 팀의 설정과 DB 기록이 있으면 DB가 우선합니다. SetTeamLookup으로 매 요청 조회해 콘솔 변경이 재시작 없이 다음 요청에 적용됩니다.
- AllowedRegions가 있는 팀은 해당 라벨의 공급자만 사용할 수 있고 라벨이 없으면 거부합니다. DB 팀이 없을 때만 설정에서 합성한 팀의 지역 제한을 사용하며 DB 기록이 있으면 팀 설정 전체를 대체합니다.

### 4. 코드 위치 {#4-code-pointers}

- `internal/keystore/sqlite.go`: 스키마·SHA-256 조회.
- `internal/audit/writer.go`: 단일 쓰기와 대기 기록 기준 WAL 정리.
- `internal/audit/verify.go`: 감사 CLI 체인 검사.

### 가격표 · ADR-030 {#pricing-rate-table-adr-030}

비용은 부동소수 누적이 아닌 정수 microUSD입니다. 가격 키는 **설정 공급자 이름과 업스트림 모델 ID**입니다. models의 targets.model이며 공개 수신 모델 이름이 아닙니다. 잘못 매칭하면 비용이 0으로 기록될 수 있습니다.

| 규칙 | 동작 |
| --- | --- |
| 키 | provider·upstream 정확 매칭 우선 |
| Bedrock 리전 간 이름 | 미매칭 시 global/us/eu/apac/us-gov 접두사 하나 제거. 실제 별도 요금이 있을 때만 접두사 키로 덮어쓰기 |
| 모델 버전 | 합치지 않음. 현재 요금이 같아도 버전별 가격 필요 |
| 공급자 | 합치지 않음. 공급자·계정별 확인하며 같은 가격·가산 요금을 가정하지 않음 |
| 캐시 | 입력 기준 읽기 0.1배, 5분 쓰기 1.25배, 1시간 쓰기 2배. 명시값 우선, 0은 유도 |
| on_missing | block이면 미가격 경로 시작 거부와 런타임 402 pricing_missing. 기본 allow는 경고·0 청구. 모르는 값은 로드 오류 |
| version | 감사 cost.pricing_version에 들어가는 자유 라벨. 요금 근거 추적용 |
| free | 입력·출력 0/0은 명시적 free=true가 있어야 무료 선언 |
| 0/0 | free 없이 0/0이면 로드 오류. 한쪽 0은 허용. 파일 로드와 UI/재로드의 BuildState 경로를 위해 테이블 빌드 때도 검사 |
| pricing sync | AWS Price List Query API에서 Bedrock 경로의 pricing.overrides 생성. 운영자가 별도로 실행하며 요청 경로 아님. pricing:GetProducts 필요. 미해결 경로는 이름과 종료 1, 임의 가격 없음. openai_compatible은 건너뜀 |

새 모델에는 공개 요금을 확인해 input_per_mtok·output_per_mtok을 추가하세요. on_missing=block이면 누락을 이름과 함께 시작 단계에서 잡습니다. Bedrock pricing sync는 API가 제공하는 요금을 조회합니다. 문서에 기록된 2026-08-26 조사에서는 us-east-1의 Claude 항목이 2.0·2.1·Instant·3 Haiku·3 Sonnet의 기존 입력 전용 다섯 행뿐이고 출력이 없어 해당 Claude-on-Bedrock 가격은 수동 관리했습니다. 현재 적용 요금은 별도로 확인해야 합니다.

### 5. 관련 문서 {#5-cross-references}

governance와 server 인증 구현, 저장소 설계 기록, 감사 검증·백업 운영 절차를 참고하세요.

### 정책 라우팅 메타데이터·세대 · ADR-043 {#policy-routing-metadata-and-generations-adr-043}

providerstore의 providers.data_boundary는 TEXT이며 누락·unknown을 신뢰하지 않습니다. model_metadata는 모델별 context_window 정수·capabilities JSON입니다. 기존 DB는 unknown·0·미선언으로 마이그레이션합니다. 초기화·오버레이·관리 쓰기·조회·내보내기·live 스냅샷에서 보존하고 슬라이스를 복사합니다. 이 메타데이터 추가 자체가 공유 집행 백엔드는 아닙니다.

MatchingRoutingPolicies는 하나의 원자 스냅샷에서 호출자 소유 규칙을 반환합니다. 거부된 분산 민감 문서는 유효한 교체 전까지 주체를 차단하고, 로컬 재로드 실패는 마지막 유효 스냅샷을 유지합니다. 적용 시 현재 RoutedAndPriced를 사용하고 토폴로지 변경 후 요청 시에도 후보를 검사합니다. 정책·토폴로지는 별도 게시이며 새 규칙 전에 전체 참여자를 업그레이드하세요.

감사 request.routing은 omitempty로 추가하고 혼합 버전 정확 바이트를 시험합니다. 요청·선택·제안 모델, 제한된 판단 필드, 정책·세대, 계획·실제 공급자와 경계를 기록해 같은 모델의 다른 공급자 재시도를 구분합니다. 탐지 원문·프롬프트·키 ID·클라이언트 세션 값을 추가하지 않습니다. 본문 기록은 별도이며 규칙이 없으면 필드를 생략합니다. [판단 의미](../policy-routing.md#read-decisions-and-evaluate-rollout)를 참고하세요.
