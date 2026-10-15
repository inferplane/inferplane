---
translation_source: runbooks/inferplaned-policy-store.md
translation_source_sha256: 671775cf70c2d1bbbfc703e9ef16e251d9d1384eaf8fd93b3d1ff571e64da062
---

# ECS에서 영속 예산 설정 활성화 {#enable-persisted-budget-settings-on-ecs}

콘솔의 예산 수정은 정책 API가 쓰기 가능한 저장소를 보고하고 로그인 세션에
쓰기 권한이 있을 때 활성화됩니다. 파일 기반 CP는 `writable: false`를 반환하므로
브라우저의 disabled 속성을 바꿔도 정책 쓰기를 허용하지 않습니다.
ADR-038은 PostgreSQL 정책 저장소를 지원합니다.

## 기존 사용량 CP 유지 {#preserve-the-existing-usage-cp}

메모리 기반 데모에서는 `deploy/ecs/policy-store.yaml`로 별도의 정책 CP와 비공개
PostgreSQL DB를 준비합니다. 기존 CP와 보관 중인 사용량은 계속 실행됩니다.
`scripts/prepare-policy-controlplane.py`는 정책, 동기화, fleet 상태, SSO 설정에
정확한 CloudFront 경로만 추가합니다. 사용량·추론·admin·정적 경로는 기존 origin을
유지합니다. Gateway는 검토된 CloudFront CP URL로 정책을 동기화해야 합니다.
기존 CP의 직접 주소를 사용하는 gateway는 새 정책을 받기 전에 주소 변경이 필요합니다.

이는 배포 구성으로, 새로운 거버넌스 프로파일은 아닙니다. 영속 예산 authority는
opt-in이며 정책 저장소 연결로 활성화되지 않습니다. 정책 CP에서 legacy lease
issuer는 새로 시작합니다. Legacy allowance는 영속 전역 escrow가 아니며 기존
로컬 hard cap은 계속 적용됩니다. 단일 AZ DB와 정책 replica 1개는 데모 구성으로,
HA 검증을 완료한 배포가 아닙니다. 사용량의 메모리 보관 범위와 교체 시 손실 조건은
기존과 같으며 이 DB로 사용량을 이전하지 않습니다.

## 비밀과 DB 접근 {#secrets-and-database-access}

ECS가 Secrets Manager의 정책 DSN을 주입합니다. CP execution role은 해당 DSN과
기존 machine-token 파라미터만 읽으며 DB master secret은 읽을 수 없습니다.
일회성 bootstrap 태스크는 새 DB의 생성된 자격 증명을 받아 별도의 `inferplane_cp`
로그인을 만듭니다. 안전하지 않은 비밀번호, 검증 없는 TLS를 거부하고 자격 증명이
포함될 수 있는 클라이언트 오류를 출력하지 않습니다. 배포 도구는 비밀 값이나 운영
레코드를 조회하지 않습니다.

암호화된 DB는 CP 보안 그룹의 5432 포트만 허용하며 TLS를 요구합니다.
`sslmode=verify-full`과 공식 RDS CA 묶음으로 인증서를 검증합니다.
7일 백업, 삭제 보호, DB·secret의 Retain 설정으로 상태를 보호합니다.
CP는 변경하지 않은 이미지의 정책 파일로 빈 저장소를 한 번 seed합니다.
이후 DB 정책이 authoritative이며 파일을 감시하지 않습니다.
기존 OIDC issuer·client·허용 그룹 설정을 유지해야 합니다.
Heartbeat 토큰은 읽기 전용이며 정책 쓰기 권한을 얻지 않습니다.

## 준비와 배포 {#prepare-and-deploy}

DB, Fargate, secret, 네트워크의 상시 요금이 추가됩니다. 현재 리전의 AWS 공개
단가와 신규 리소스를 검토하고 생성 승인을 받은 뒤 change set을 실행합니다.

1. 기존 배포 구성을 비공개로 저장합니다. cfn-lint와 `policy-store-security.guard`로
   저장소 템플릿을 검증합니다.
2. 저장소 change set과 `describe-events` 검증을 확인합니다. 첫 단계는 격리된
   리소스만 생성하며 `TaskDefinitionArn`은 비워 둡니다.
3. 승인 후 첫 단계를 실행합니다. ECS에서 bootstrap 이미지를 실행하고 exit code
   0을 확인합니다. 주입된 자격 증명을 조회하지 않습니다.
4. 기존 고정 runtime·이미지·구성에서 정책 CP 태스크를 등록하고 RDS CA와 DSN secret
   참조를 추가합니다. 금액 authority·프로파일 플래그는 유지합니다.
   태스크 ARN을 포함한 저장소 변경안을 검토하고 정책 서비스를 만든 뒤 정상 target을
   기다립니다.
5. Main stack 경로 변경안을 준비합니다. 생성된 VPC origin ID를 `PolicyVpcOriginId`로
   전달하고 기존 모든 파라미터를 유지합니다. Distribution만 교체 없이 변경되는지
   확인한 뒤 실행·전파를 기다립니다. 정책·SSO·config 경로 6개를 invalidate합니다.
6. 공개 경로·인증 거부, 기존 사용량 서비스 유지, 새 서비스 readiness, SSO 설정,
   테스트 응답을 사용하는 브라우저의 쓰기 버튼을 확인합니다.
   실제 사람의 SSO 완료나 운영 정책 변경은 별도로 보고해야 합니다.

```bash
python3 scripts/prepare-policy-controlplane.py \
  --template "$PRIVATE_DIR/main.template.json" \
  --output "$PRIVATE_DIR/main.candidate.json"
python3 -m unittest discover -s tests/deployment -v
cfn-lint --regions ap-northeast-2 -t deploy/ecs/policy-store.yaml
```

첫 쓰기 전에는 유지한 기존 경로로 rollback할 수 있습니다. DB 정책 수정이
시작된 뒤에는 authoritative DSN과 문서를 유지해야 합니다.
이미지 파일로 돌아가면 설정한 제한을 완화하거나 잃을 수 있습니다.

## 정책 백업과 복원

policy store에는 GovernancePolicy 문서만 저장됩니다. 정책 내보내기에는 예산 authority 잔액, 사용량 텔레메트리, identity 설정이 포함되지 않으며, 가져오기도 이것들을 복원하지 않습니다. 잔액을 복원하면 중앙 authority를 다시 쓰는 셈이 되기 때문입니다. 데이터베이스 자체는 위에서 설명한 RDS/pg_dump 백업을 계속 사용하세요.

가져오기는 정책 CP 태스크 하나만 서비스 중일 때 실행하고, 롤아웃 중에는 실행하지 마세요. `deploy/ecs/policy-store.yaml`은 `MaximumPercent: 200`을 사용하므로 배포 중 inferplaned 태스크 두 개가 겹칠 수 있습니다. `writeMu`는 프로세스 로컬이어서 PUT/DELETE와 같은 기존 동시성 공백이 있습니다. DB 수준 compare-and-swap은 후속 작업으로 기록되어 있습니다.

읽기 토큰으로 백업합니다(예: cron).

```bash
curl -fsS -H "Authorization: Bearer $INFERPLANED_TOKEN" \
  "$CP/v1alpha1/config/export" -o "policies-$(date -u +%Y%m%dT%H%M%SZ).yaml"
```

복원할 때는 먼저 미리보기를 실행하고, 미리보기에서 받은 generation으로 적용합니다. 정책 쓰기 토큰을 사용합니다.

```bash
curl -fsS -X POST -H "Authorization: Bearer $INFERPLANED_POLICY_WRITE_TOKEN" \
  --data-binary @policies.yaml "$CP/v1alpha1/config/import?dry_run=true" | tee preview.json
GEN=$(jq -r .current_generation preview.json)
curl -fsS -X POST -H "Authorization: Bearer $INFERPLANED_POLICY_WRITE_TOKEN" \
  --data-binary @policies.yaml "$CP/v1alpha1/config/import?expected_generation=$GEN"
```

가져오기는 정책 세트 전체를 교체합니다. 파일에 없는 문서는 삭제됩니다. `409`가 나오면 미리보기 이후 정책이 바뀐 것이니 미리보기를 다시 실행하세요. 빈 파일은 `allow_empty=true`가 필요합니다. 파일은 최대 64 MiB이며, 전체 sync 응답의 추정 크기(`세트 JSON 바이트 × 2 + 전체 규칙 수 × 1024 + 512 KiB`)가 데이터 플레인 디코딩 한도인 8 MiB를 초과하면 `400`으로 거부됩니다. "committed but not applied" `500`이 나오면 store에는 이미 새 세트가 반영된 것이니, 미리보기를 다시 실행해 실제로 배포 중인 세트를 확인하세요. 콘솔의 Policies → Export YAML / Import (restore)에서도 같은 흐름을 제공합니다.

이 변경 이전에 준비한 배포에서는 `/v1alpha1/config/*` 요청이 여전히 기존 CP origin으로 갑니다. `prepare-policy-controlplane.py`는 이미 policy origin이 있으면 실행을 거부하므로, `/v1alpha1/config/*` behavior(policy-cp origin, `forward-auth` origin request policy)를 검토를 거쳐 수동으로 추가하세요. 그다음 policy/SSO/config 경로 6개를 무효화합니다.
