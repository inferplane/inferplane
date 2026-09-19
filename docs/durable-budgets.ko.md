---
translation_source: durable-budgets.md
translation_source_sha256: 0d3f31dc83081deb34dfbe09b662a6aeb73a655cb7270dd6103c40ad24f0e62c
---

# 영속 전역 예산 {#durable-global-budgets}

**노드별 mayu 인스턴스 전체에 전역 GovernancePolicy 금전 예산을 적용**하는 프로파일입니다. Postgres가 권한 발급·보고·예산 윈도의 기준 저장소입니다. 추론 요청마다 중앙 DB를 호출하지 않고 이미 확정된 로컬 사용 권한을 이용합니다.

## 제어 영역 {#control-plane}

비밀 관리 도구와 환경 변수로 다음 값을 설정하세요.

```text
INFERPLANED_POLICY_DSN        = secret Postgres connection reference
INFERPLANED_TOKEN             = machine heartbeat credential
INFERPLANED_POLICY_WRITE_TOKEN = distinct policy administration credential
INFERPLANED_DURABLE_BUDGETS    = true
```

모든 제어 영역 복제본을 같은 데이터베이스·스키마로 실행합니다.

```bash
inferplaned --policies examples/durable-budgets/governance.yaml
```

파일은 최초 초기화에만 사용합니다. 이후 정책 읽기·쓰기는 Postgres를 이용합니다. 권한 트랜잭션도 동일한 정책 행을 읽으므로 오래된 복제본 캐시가 이전 정책으로 권한을 발급할 수 없습니다. 마이그레이션·DB 오류 시 초기화를 실패 처리하며 메모리로 대체하지 않습니다. `/readyz`는 실제 권한 DB 연결 상태를 반영합니다.

가용성이 필요하면 복제된 Postgres와 안정적인 부하 분산 제어 영역 주소를 운영하세요. 이 기능을 켠다고 클라우드 배포나 운영 DB가 생성되지는 않습니다. 모든 권한·정책 테이블을 일관되게 백업·복원해야 합니다. 노드가 더 최신 권한을 가진 상태에서 오래된 DB를 복원하면 안전하지 않습니다.

## 데이터 영역 {#data-plane}

`examples/config.durable-budgets.json`에서 시작하세요. 예시 엔드포인트, 모델 ID, 기능·컨텍스트 선언, 가격은 검증한 값으로 교체합니다. 각 노드에는 고유하고 안정적인 ID와 전용 영속 저널이 필요합니다.

```json
{
  "budget_timezone": "UTC",
  "control_plane": {
    "url": "https://inferplaned.example.invalid",
    "dataplane": "unique-stable-node-id",
    "token_ref": {"env": "INFERPLANE_CONTROL_TOKEN"},
    "require_sync": true,
    "authority": {
      "journal_path": "/private/node-state/budget-authority.sqlite"
    }
  }
}
```

저널과 SQLite 부속 파일은 권한 `0600`의 비공개 일반 파일이어야 합니다. 상위 디렉터리를 먼저 만드세요. 실행 중인 저널을 노드 간 공유·복사하지 마세요. 권한 모드, 노드 식별자, 저널 경로 변경에는 재시작이 필요합니다. 원격 권한 통신에는 HTTPS가 필수이며 로컬 개발에는 루프백 HTTP를 사용할 수 있습니다.

각 모델에 양수 컨텍스트 한도와 알려진 가격을 선언해야 합니다. 요청 승인 시 모든 입력·캐시 종류와 출력을 고려한 보수적 상한을 예약합니다. 작은 요청도 최종 비용보다 큰 값을 일시 예약할 수 있으며, 완전한 관측 사용량이 있으면 미사용 로컬 부분을 다시 사용할 수 있습니다. 캐시 TTL 구분을 알 수 없으면 더 싼 캐시 쓰기 가격으로 환급을 증명할 수 없어 전체 예약을 유지합니다. `n > 1` 같은 다중 생성 제어는 호출 전에 거부합니다. Bedrock 생성의 SDK 내부 재시도는 끄며 게이트웨이의 각 폴백 시도는 별도로 예약합니다.

예산이 충분하면 중앙 권한은 대기 요청의 상한 이상으로 발급됩니다. 안전한 상한을 충당할 수 없는 예산은 공급자 호출 전에 거부합니다.

권한이 처음 필요한 요청은 백그라운드 작업자가 권한을 받는 동안 `Retry-After: 1`과 함께 503을 반환할 수 있습니다. 예산 소진은 402입니다. 어떤 폴백 모델도 이 검사를 우회하지 않습니다. 소프트 전환 임계값과 총예산 하드 한도는 별개이며 적응형 라우팅 필드를 그대로 사용합니다.

## 장애 동작 {#failure-behavior}

| 상황 | 결과 |
| --- | --- |
| CP 프로세스 재시작·장애 조치 | Postgres가 모든 권한·보고·티어 유지 |
| CP·DB 접근 불가 | 유효한 로컬 권한을 기한까지 사용 |
| 권한 응답 유실 | 동일 재시도에 기존 권한 반환, 새 잔액 생성 안 함 |
| 권한 만료·노드 유실 | 중앙 자동 환급 없음 |
| 요청 정상 완료 | 실제 비용 소비, 증명된 미사용 로컬 예약 해제 |
| 부분 스트림·취소·사용량 누락 | 상한을 미확정 상태로 유지, 관측 사용량은 별도 |
| 노드·저널 재시작 | 이전 열린 권한은 보수적으로 소진 처리, 복원 권한 재사용 불가 |
| 예산 윈도 전환 | DB 소유 새 윈도 사용, 늦은 보고는 기존 윈도에 남음 |
| 잘못되거나 위조된·충돌하는 현재 부팅 보고 | 전체 트랜잭션 거부 |
| 이전 부팅 보고·계량값 복원 | 서버 최댓값 보존, 중복 환급 없음 |
| 저널 접근 불가·잘못된 권한 응답 | 실패 시 거부 |

미확정 자금을 회수하려고 권한 테이블을 지우거나 저널 JSON을 수정하면 안 됩니다. 미해결 의무와 재전송 식별자를 보존하세요. 응답 없는 오래된 요청은 보수적으로 포기할 수 있으나 서버는 확인되지 않은 권한을 유지하고 환급 요청도 받지 않습니다. 로컬 요청 이력은 크기가 제한됩니다. 잘못된 사용량, 검사된 비용 오버플로, 상한 위반은 공급자 메타데이터·가격·동작 오류를 뜻하며 로컬 승인을 막고 관련 중앙 권한을 동결합니다.

## 범위 {#scope}

설정된 가격과 검증된 공급자 상한에 대한 정확한 예약 회계이며 외부 청구서 일치를 보장하지 않습니다. 기존 키별 로컬 할당량·요청률을 전역화하거나 가상 키 저장소를 공유하지 않습니다. OIDC 신원 마이그레이션도 추가하지 않습니다. 기존 인증된 발급 경로로 안정적인 불투명 사용자 주체를 설정하세요. [ADR-045](decisions/ADR-045-durable-budget-authority.md)를 참고하세요.

## 검증 {#validation}

저장소 테스트는 일회용 로컬 Postgres와 독립 스키마를 사용합니다.

```bash
go test ./... -race
```

Postgres 테스트에는 `INFERPLANE_TEST_PG_DSN`을 반드시 일회용 서비스로 설정하세요. CI가 Postgres 17을 제공하고 실행합니다. DB 테스트를 스킵한 결과는 공유 권한의 정확성을 입증하지 않습니다.
