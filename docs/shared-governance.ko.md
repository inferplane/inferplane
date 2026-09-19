---
translation_source: shared-governance.md
translation_source_sha256: 280bd5a59d4d1a0a707a365ac41a39d09ab84f0aa861e649744c7371df3e9a91
---

# 공유 키와 전역 요청률·토큰 할당량 {#shared-keys-and-global-ratetoken-quotas}

여러 게이트웨이가 같은 가상 키를 조회하고 RPM·TPM·토큰 할당량·금전 카운터를 함께 집행해야 할 때 사용합니다. 키 조회와 요청 승인에 Postgres가 필요합니다. 개별 프로세스·노드 장애를 견디려면 복제 DB 엔드포인트와 여러 게이트웨이를 운영하세요. DB 장애·네트워크 분할 시 새 공유 요청은 거부됩니다.

기본 SQLite와 ADR-045 노드별 예산 프로파일도 유지됩니다. 공유 모드는 DB 의존성을 명시적으로 가지며 오프라인 키 캐시가 아닙니다.

## 프로파일 활성화 {#enable-the-profile}

`INFERPLANED_POLICY_DSN`, `INFERPLANED_DURABLE_BUDGETS=true`, 일반적인 머신·관리 자격 증명으로 inferplaned를 시작합니다. 최초 정책에는 `examples/shared-governance/governance.yaml`을 사용하세요. 모든 제어 영역과 공유 게이트웨이는 **동일한 DB·스키마**를 가리켜야 합니다.

게이트웨이는 `examples/config.shared-governance.json`에서 시작합니다.

```json
{
  "key_store": {
    "type": "postgres",
    "dsn_ref": {"env": "INFERPLANE_PG_DSN"}
  },
  "governance_store": {"type": "postgres"},
  "budget_timezone": "UTC",
  "control_plane": {
    "url": "https://inferplaned.example.invalid",
    "token_ref": {"env": "INFERPLANE_CONTROL_TOKEN"},
    "require_sync": true
  }
}
```

예시 엔드포인트·모델·가격·컨텍스트 한도를 검증한 공급자 설정으로 교체하세요. SQLite `path`, `control_plane.authority`, 로컬 `policies`, 복제본별 `provider_store`는 설정하면 안 됩니다. 기본 식별자는 고유한 호스트명·부팅 ID이며, 명시적 `dataplane`도 복제본마다 달라야 합니다.

한도 값이 같더라도 네임스페이스·세대 검사는 다른 정책 DB를 거부합니다. `/readyz`는 최초 동기화와 DB 가용성을 포함합니다. `/healthz`는 프로세스 상태입니다. 백엔드·연결·프로파일 변경에는 재시작이 필요합니다.

## 한도와 라우팅 {#limits-and-routing}

공유 팀 기록과 가상 키 옵션이 RPM·TPM 및 기존 금전 한도를 제어합니다. 팀 `tokens_per_day`는 UTC 달력일 윈도를 사용합니다. 정책은 여러 팀에 걸친 사용자 전용 주체와 한 팀 내 팀+사용자 주체를 지원합니다.

```yaml
apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: {name: developer-limits, generation: 1}
spec:
  subject: {user: opaque-user-id}
  rules:
    - name: throughput
      failurePolicy: FailClosed
      rate: {rpm: 60, tpm: 2000000}
    - name: monthly-tokens
      failurePolicy: FailClosed
      tokenQuota: {limitTokens: 100000000, period: CalendarMonth}
```

공유 월별 토큰 한도에는 `tokenQuota`를 사용합니다. 기존 `quota.tokens_per_month`에는 기준 팀 저장소 열이 없어 이 프로파일에서 거부됩니다. 여러 규칙과 팀·키 계층이 모두 적용되며 경고 규칙이 차단 규칙을 완화할 수 없습니다.

각 공급자 시도는 선언된 컨텍스트의 토큰 종류 5개와 해당 정수 가격 상한을 보수적으로 예약합니다. 상한보다 작은 한도는 짧은 요청도 거부할 수 있습니다. 사용량이 완전하면 미사용 부분을 반환하고, 부분 스트림·누락 사용량이면 불확실성을 유지합니다. 캐시 TTL 가격을 몰라도 독립적으로 알려진 토큰 수량 정산은 가능합니다.

소프트 티어 전환은 진행 중 예약이 아닌 정산·미확정 소비를 기준으로 합니다. 기존 컨텍스트 안정성, 개인정보 라우팅·마스킹, 엄격한 예산 목적지 제한도 적용됩니다. 공유 게이트웨이와 ADR-045 로컬 권한 전체에서 모든 금전 하드 한도를 유지합니다.

## 키와 마이그레이션 {#keys-and-migration}

복제본 시작 시 새 행만 삽입합니다. 원래 시드 지문 덕분에 같은 설정으로 재시작해도 관리자의 변경을 덮어쓰지 않습니다. 충돌하는 선언은 시작을 거부합니다. 후속 변경에는 관리 API를 사용하세요. 승인되는 모든 키에는 현재 공유 팀 기록이 필요하며 팀 삭제 시 해당 키의 공유 승인이 중지됩니다.

키 CLI는 기존 SQLite `--store` 또는 `--config`를 받습니다.

```bash
mayu keys create --config config.json --team engineering --models auto,weak,normal,strong,economy
mayu keys list --config config.json
mayu keys revoke --config config.json --id KEY_ID
```

이 명령은 `key_store` 자격 증명만 해석하므로 공급자·제어 영역·콘솔 자격 증명이 필요 없습니다. 새 키를 만들 때만 평문을 표시합니다.

마이그레이션 절차:

1. SQLite를 백업하고 이전 배포의 요청 및 키·팀 변경을 드레인·동결합니다.
2. 대상 제어 영역의 정책 권한 저장소를 초기화합니다.
3. `mayu keys import --config shared-config.json --sqlite old-keys.db`를 실행합니다.
4. 같은 DB에 공유 게이트웨이를 시작한 뒤 인증, 폐기, 준비 상태, 전역 한도를 검증하고 트래픽을 전환합니다.

가져오기는 해시와 폐기 이력을 보존합니다. 원자적이며 재시도는 멱등적입니다. 대상 충돌 시 거부합니다. 배치 제한 시간은 5초이므로 대규모 배포는 준비가 필요할 수 있습니다. 새 권한이 유효한 동안 오래된 SQLite·DB로 되돌리면 신원이나 예산이 되살아날 수 있으므로 금지합니다.

## 가용성과 운영 {#availability-and-operations}

`examples/helm.shared-governance.yaml`은 복제본 2개를 렌더링합니다. ADR-046 구현이 포함된 이미지를 먼저 준비하고 기존 Secret 및 실제 공급자 설정을 제공하세요. 영속성을 켜면 StatefulSet, 독립 PVC, 헤드리스 Service, 필수 노드 안티어피니티, 중단 예산을 구성합니다. 스케줄 가능한 노드가 2개 이상 필요하며 DB·제어 영역도 적절한 장애 영역에 분산하세요. 감사 WAL 충돌 방지를 위해 공용 기존 PVC는 거부합니다.

모든 복제본의 감사 구간을 수집·검증합니다. 콘솔에서 단일 공유 로그 색인이 필요하면 기존 분석 Mode B를 사용하세요. 기본 로컬 색인은 로컬에 남습니다. `/v1/usage`는 `enforcement_mode: shared`와 주체별 `shared_limits`를 반환합니다. `used`에는 확정 예약·불확실성이 포함되고 `reserved`는 유지된 부분입니다. 요청률 사용량은 버킷 부채를 나타냅니다.

요청률·토큰 소진은 429, 금전 소진은 402, 저장소 접근 불가·스냅샷 변경은 503입니다. 공유 프로파일의 토큰 계산은 항상 로컬이며 신원 저장소 장애 시 0 추정치를 반환합니다. 저비용 모델이나 실패한 요청도 하드 예약을 우회하지 않습니다.

과거 회계·허가 식별자는 유지됩니다. 충돌·불명확한 커밋 이후 열린 허가는 예약 상태로 남고 자동으로 소비 확정·환급되거나 소프트 전환 티어에 포함되지 않습니다. 전체 예약은 원래 할당량 윈도와 하드 예산을 계속 제약합니다. 자동 정리나 동결 계정 조정 API는 제공하지 않습니다. 보존 정책은 의무와 재전송 안전성을 유지해야 하며 서비스 복구를 위해 카운터를 잘라내면 안 됩니다. 공급자 상한 위반은 조사 대상이지 자동 환급 근거가 아닙니다.
