---
translation_source: runbooks/audit-anchoring.md
translation_source_sha256: 3b847fb10352596c357db4d84b18c848a36be18213ce0b1276406e54d3604a69
---

# 운영 절차: S3 Object Lock 감사 앵커링 {#runbook-s3-object-lock-audit-anchoring-adr-012}

감사 해시 체인의 최신 해시를 WORM(한 번 쓰고 여러 번 읽는) S3 버킷에 주기적으로 저장합니다. 변경할 수 없는 앵커가 당시 체인 내용을 입증하므로 로컬 감사 파일을 다시 쓴 공격을 숨기기 어렵게 합니다. **변조 탐지**에 외부 증거에 의한 **변조 저항성**을 더하는 방식입니다.

## 변조 저항성은 조건부입니다 {#tamper-resistance-is-conditional}

게이트웨이는 앵커 객체를 PUT할 뿐입니다. 버킷·IAM을 올바르게 구성해야 하며 그렇지 않으면 수정 가능한 JSON을 저장하는 것에 불과합니다.

1. **버킷에 Object Lock을 활성화**하고 **COMPLIANCE 기본 보존**을 설정합니다. GOVERNANCE는 특권 사용자가 우회할 수 있습니다. 버전 관리가 필요하며 Object Lock에 수반됩니다.
2. **우회·삭제를 금지하는 IAM:** 게이트웨이는 `s3:PutObject`만 필요합니다. `s3:BypassGovernanceRetention`, `s3:DeleteObject`, `s3:DeleteObjectVersion`, Object Lock 설정 변경 권한을 주지 않습니다. 별도로 엄격히 관리하는 감사자 신원이 읽기를 담당합니다.
3. **제한된 증거 공백(RPO = 앵커 interval):** 마지막 성공 앵커 이후 기록은 다음 앵커 전까지 로컬 변조 탐지 수준입니다. 포렌식 RPO에 맞게 주기를 선택하세요.

조건이 빠지면 앵커를 삭제·덮어쓸 수 있어 저항성 강화가 성립하지 않습니다.

## 버킷 생성 · 운영자/IaC의 최초 작업 {#create-the-bucket-one-time-operatoriac}

```bash
aws s3api create-bucket --bucket my-inferplane-audit-anchors --region us-west-2 \
  --object-lock-enabled-for-bucket \
  --create-bucket-configuration LocationConstraint=us-west-2
aws s3api put-object-lock-configuration --bucket my-inferplane-audit-anchors \
  --object-lock-configuration '{"ObjectLockEnabled":"Enabled","Rule":{"DefaultRetention":{"Mode":"COMPLIANCE","Days":365}}}'
```

## 설정 활성화 {#enable-in-config}

```json
"audit": {
  "buffer": { "path": "/var/lib/inferplane/audit.wal" },
  "sinks": [ { "type": "file", "path": "/var/lib/inferplane/audit.jsonl" } ],
  "anchor": {
    "type": "s3", "bucket": "my-inferplane-audit-anchors", "prefix": "anchors",
    "region": "us-west-2", "interval": "5m", "retain_days": 365
  }
}
```

- `retain_days`는 버킷 기본값에 더해 객체별 COMPLIANCE 보존을 설정합니다. `endpoint`로 Object Lock이 있는 MinIO 같은 S3 호환 WORM 저장소를 지정할 수 있습니다. 블록이 없으면 S3 작업자도 없는 선택적 기능입니다.
- 게이트웨이는 Bedrock과 같은 표준 AWS 자격 증명 체인(IRSA·환경·프로파일)을 사용합니다.
- **불투명 instance ID**를 사용하세요. 객체 키에 ID가 들어가므로 배포 이름에 테넌트·호스트·개인정보를 인코딩하지 마세요.

## 앵커 객체 {#anchor-object}

각 앵커 주소는 `s3://<bucket>/<prefix>/<instance>/<ts>-<count>.json`입니다.

```json
{ "instance": "<host>-<ulid>", "head_hash": "sha256:…", "count": 12345,
  "ts": "2026-06-14T00:05:00.123456789Z" }
```

체인 최신 해시, 기록 수, 인스턴스 ID, 시각만 포함하며 비밀·개인정보를 넣지 않습니다. 실패는 다음 주기에 재시도하고 `inferplane_audit_anchor_failures_total`로 집계합니다. 성공했을 때만 커서를 진행합니다.

## 로컬 체인과 WORM 앵커 대조 · 감사자 절차 {#verify-a-local-chain-against-the-worm-anchors-auditor-procedure}

1. 해당 인스턴스의 **최신** 앵커를 조회합니다.

   ```bash
   aws s3 ls s3://my-inferplane-audit-anchors/anchors/<instance>/ | sort | tail -1
   aws s3 cp s3://my-inferplane-audit-anchors/anchors/<instance>/<latest>.json -
   ```

2. 로컬 체인을 다시 검증해 체인 내부 변조를 확인합니다.

   ```bash
   mayu audit verify --file /var/lib/inferplane/audit.jsonl
   ```

3. **교차 검증:** 앵커의 `count`번째 기록에서 로컬 체인 최신 해시를 다시 계산하고 `head_hash`와 **같은지** 확인합니다. 다르면 앵커 이후 로컬 체인이 바뀐 것입니다. 변경할 수 없는 WORM 앵커를 기준으로 판단합니다. count 이후 기록은 다음 앵커가 필요한 RPO 구간입니다.

> 원격 조회와 대조를 한 번에 수행하는 앵커 인식 `mayu audit verify` CLI는 후속 과제입니다. v1은 앵커 기록기와 이 수동 절차를 제공합니다.

## 장애: 앵커 실패 증가 {#incident-anchor-failures-climbing}

`inferplane_audit_anchor_failures_total` 증가 시 자격 증명·네트워크·버킷 정책을 확인하세요. 앵커링은 최선의 노력 방식이라 요청·감사 기록은 계속되지만 외부 증거 없는 구간이 늘어납니다. `audit anchor failed` 로그, `s3:PutObject`, 버킷 연결을 점검합니다. 다음 성공 주기에 현재 해시를 기록하며 매번 다시 계산하므로 앵커 실패 자체가 기록 손실을 뜻하지는 않습니다.
