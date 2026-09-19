---
translation_source: runbooks/credential-brokering.md
translation_source_sha256: cb55d833e6adfaf66cc5784dfe649ea4e6e7b5a8286dc67fda310cd1a26c0a4c
---

# 운영 절차: 자격 증명 중개 {#runbook-credential-brokering-adr-040}

개발자·노드 IAM에서 `bedrock:Invoke*` 상시 권한을 제거하는 방식입니다. `inferplaned`가 STS 중개 역할을 보유하고 `POST /v1alpha1/credentials`로 각 `mayu`에 최대 1시간 세션을 발급합니다. Bedrock 공급자에서 `auth.mode: "broker"`로 선택합니다. 사용자가 mayu 환경을 읽을 수 없는 Kubernetes 노드·전용 프록시에서는 직접 우회해도 Bedrock 자격 증명을 얻지 못합니다. 개발자 소유 장비에서는 환경의 중개 토큰을 읽을 수 있으므로 상시 IAM 권한 제거 효과만 있고 우회 방지는 아닙니다. 전체 위협 모델은 ADR-040을 참고하세요.

## 1. 양쪽 IAM 권한 연결 {#1-iam-wiring-do-this-first-it-is-two-sided}

예를 들어 `inferplane-bedrock-broker`라는 **중개 역할**을 만듭니다. 신원 정책은 Bedrock 호출만 허용하고 가능하면 네트워크 조건을 붙입니다.

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": ["bedrock:InvokeModel", "bedrock:InvokeModelWithResponseStream"],
    "Resource": "*",
    "Condition": {
      "IpAddress": { "aws:SourceIp": ["<your corporate/VPC CIDRs>"] }
    }
  }]
}
```

> **권장:** `aws:SourceIp` 또는 VPC 엔드포인트의 `aws:SourceVpce` 조건으로 유출된 임시 자격 증명이 외부 네트워크에서 쓰이지 못하도록 제한하세요. 조건이 없으면 보유자가 최대 1시간 동안 어디서나 Bedrock에 접근할 수 있습니다.

신뢰 정책은 **아래 세 작업 모두** 허용해야 합니다. `sts:TagSession`·`sts:SetSourceIdentity`를 거부하면 태그만 빠지는 것이 아니라 AssumeRole 전체가 실패합니다.

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": { "AWS": "arn:aws:iam::<account>:role/<inferplaned task role>" },
    "Action": ["sts:AssumeRole", "sts:TagSession", "sts:SetSourceIdentity"]
  }]
}
```

**inferplaned의 태스크 역할**에도 대응하는 신원 정책을 추가합니다.

```json
{
  "Effect": "Allow",
  "Action": ["sts:AssumeRole", "sts:TagSession", "sts:SetSourceIdentity"],
  "Resource": "arn:aws:iam::<account>:role/inferplane-bedrock-broker"
}
```

세션 수명은 **역할의 최대 수명과 무관하게 1시간**으로 제한됩니다. inferplaned 역할 자체가 임시 역할이므로 STS 역할 연결에 해당합니다. `MaxSessionDuration`을 늘려도 연결된 세션의 한도는 늘지 않습니다.

## 2. inferplaned 활성화 {#2-enable-it-on-inferplaned}

inferplaned는 설정 파일 대신 환경 변수를 사용합니다.

```bash
INFERPLANED_BROKER_ROLE_ARN=arn:aws:iam::<account>:role/inferplane-bedrock-broker
INFERPLANED_BROKER_TOKEN=<long random secret — NOT the value of INFERPLANED_TOKEN>
```

| 변수 | 필수 여부 | 의미 |
| --- | --- | --- |
| `INFERPLANED_BROKER_ROLE_ARN` | 활성화 스위치 | 없으면 자격 증명 엔드포인트는 404이며 기존 동작 유지 |
| `INFERPLANED_BROKER_TOKEN` | ARN 설정 시 필수 | 누락·JWT 형태·`INFERPLANED_TOKEN`과 동일하면 시작 실패. 하트비트 토큰과 공유하면 노드 하나 침해로 직접 Bedrock 권한을 얻을 수 있음. 더 좁은 범위에 보관 |

엔드포인트는 중개 토큰으로만 인증합니다. 콘솔 SSO의 OIDC bearer는 403으로 거부하며 브라우저가 AWS 자격 증명을 발급할 수 없어야 합니다.

## 3. mayu 활성화 {#3-enable-it-on-mayu}

```json
{
  "control_plane": {
    "url": "https://inferplaned.example.com",
    "token_ref": {"env": "INFERPLANED_TOKEN"},
    "broker_token_ref": {"env": "INFERPLANE_BROKER_TOKEN"},
    "dataplane": "build-farm-node-07"
  },
  "providers": {
    "bedrock-global": {
      "type": "bedrock",
      "region": "ap-northeast-2",
      "auth": {"mode": "broker"}
    }
  }
}
```

다음 설정은 명확한 오류와 함께 로드 단계에서 거부됩니다.

- `auth.mode`가 `default|irsa|pod_identity|profile|static|broker` 밖의 값. 오타를 기본 노드 IAM으로 대체하지 않습니다.
- `broker`인데 `control_plane` 또는 `broker_token_ref`가 없음.
- `broker_token_ref`와 `token_ref`가 같은 값으로 해석됨.
- `broker`의 CP 주소가 **루프백이 아닌 평문 HTTP**. 자격 증명을 평문 전송할 수 없습니다.

**`control_plane.dataplane`을 명시하세요.** 기본 ID는 부팅별 `hostname-ULID`이므로 재시작마다 CloudTrail 세션 이름이 달라집니다. 안정적인 ID는 연속된 감사 추적을 제공합니다. AssumeRole의 `RoleSessionName`, `SourceIdentity`, `dataplane` 태그에는 정제된 ID를 사용합니다.

## 4. 실제 우회 방지 구성 {#4-make-bypass-prevention-real-the-part-inferplane-cannot-do-for-you}

기존 노드·개발자 역할의 `bedrock:InvokeModel*`를 제거하세요. 제거 전에는 중개가 경로 하나를 추가할 뿐 기존 직접 호출로 mayu를 우회할 수 있습니다. 제거하면 남는 자격 증명은 최대 1시간 중개 세션이며 역할 하나를 비활성화해 전체 사용을 차단할 수 있습니다.

## 5. 장애 동작 {#5-failure-modes}

| 증상 | 의미·대응 |
| --- | --- |
| 시작·SIGHUP의 `credential source ... Retrieve` 실패 | 최초 조회 실패. 중개 연결·토큰·ARN·권한 점검. 노드 IAM으로 대체하지 않음. SIGHUP 실패 시 기존 토폴로지 유지 |
| CP 장애 약 1시간 후 호출 실패 | 캐시 세션 만료·갱신 불가. inferplaned 복구 후 다음 서명에서 회복 |
| 중개 토큰 누락·동일·JWT 형태로 시작 실패 | 2절 조건 확인 |
| 자격 증명 엔드포인트 502, 고정된 본문 | 서버의 AssumeRole 실패. 실제 오류는 inferplaned 로그에만 표시. 중개 역할 신뢰 정책의 TagSession·SetSourceIdentity 누락 확인 |
| CloudTrail에 SourceIdentity 없음 | 이전 태스크 역할 세션의 SourceIdentity가 상속되어 변경 불가. 해당 값 없이 재시도했으며 dataplane 태그로 추적 가능 |
| 모르는 ID의 CloudTrail 세션 | v1의 ID는 호출자 주장 값. 토큰 보유자는 어떤 ID도 주장 가능. 기계 식별 증거가 아님. 오용 의심 시 중개 토큰 교체 |

## 지원하지 않는 범위 {#what-this-does-not-do}

- 직접 Anthropic API 키 중개: 임시 토큰 메커니즘이 없어 해당 경로는 로컬 `env:`/`file:` 참조를 유지합니다.
- 중개 세션의 모델 제한: v1은 중개 역할의 전체 `bedrock:Invoke*`를 받습니다. mayu를 거치는 트래픽은 모델 정책을 집행하지만 IAM의 팀별 세션 정책은 v2 후보입니다.
- 실제 호출 장비의 증명: 호출자 주장 ID와 장비 신원은 다릅니다.
