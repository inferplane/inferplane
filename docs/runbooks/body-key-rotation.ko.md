---
translation_source: runbooks/body-key-rotation.md
translation_source_sha256: fe9fa07a65b9d77c33df6f605dbeb9e955ca2b09d9697ade8c99bdff2d311cd5
---

# 운영 절차: 본문 저장소 키 교체 {#runbook-body-store-key-rotation-adr-018-deferred-item}

`mayu bodies rewrap-key`는 수집된 본문별 데이터 키를 감싸는 AES-256 마스터 키(`audit.log_bodies.key_ref`)를 교체합니다. `wrapped_key_nonce`·`wrapped_key_ct`만 수정하고 실제 요청·응답 암호문 `req_*`·`resp_*`는 읽거나 다시 쓰지 않습니다. 본문 크기와 무관하게 비교적 빠르게 처리합니다.

## 교체 시점 {#when-to-rotate}

- 장기 대칭 키의 정기 교체 정책.
- 현재 `audit.log_bodies.key_ref` 값의 유출 의심.
- 정해진 교체 주기를 요구하는 규정.

## 준비 사항 {#prerequisites}

- 현재 참조가 가리키는 이전 키와 새 64자리 hex(32바이트) 키. 예: `openssl rand -hex 32`.
- CLI가 읽을 환경 변수 또는 파일. **키 값을 플래그로 직접 전달하지 말고** 환경 변수명·파일 경로만 전달합니다.

## 실행 {#running-it}

```bash
mayu bodies rewrap-key \
  --store /var/lib/inferplane/bodies.db \
  --old-key-env BODY_MASTER_KEY_OLD \
  --new-key-env BODY_MASTER_KEY_NEW
```

Postgres는 `--store <path>` 대신 DSN 비밀 참조 `--postgres-dsn-env <VAR>`를 사용합니다. 키마다 환경 변수 대신 `--old-key-file`·`--new-key-file`도 사용할 수 있습니다.

**본문 저장소 자체는 전체 인스턴스 중지가 필요하지 않습니다.** 공유 Postgres 본문 저장소의 각 mayu는 독립적으로 씁니다. 교체 도중 다른 인스턴스가 만든 행은 이번 목록에 없으므로 같은 이전·새 키로 다시 실행해 처리합니다. 이것이 게이트웨이 HA 검증을 뜻하지는 않습니다. 로컬 프로파일은 단일 복제본이며 여러 게이트웨이는 명시적 ADR-046 공유 프로파일과 가용한 Postgres가 필요합니다. [배포 프로파일](../getting-started/deployment-profiles.md)을 참고하세요.

## 출력 해석 {#reading-the-output}

```
rewrapped=N skipped=M raced=K failed=P
```

- `rewrapped`: 이전 키에서 새 키로 성공적으로 바꾼 행.
- `skipped`: 제공한 이전 키로 열 수 없는 행. 다른 키·변조·잘못된 길이 등이며 실행상의 오류 카운터와는 구분합니다.
- `raced`: 목록 조회와 수정 사이 감싼 키 바이트가 바뀐 행. 다른 교체·Purge·삭제가 원인일 수 있고 이번 실행에서는 건드리지 않습니다.
- `failed`: **무시하면 안 되는** 운영 오류. 새 키로 감싸기·DB 수정 실패 등입니다. 행별 표준 오류를 출력하며 하나라도 있으면 성공 행 수와 무관하게 종료 1입니다. 이전 키로만 읽을 수 있으므로 **failed가 있으면 이전 키를 폐기하지 마세요.**

## 종료 코드 {#exit-codes}

| 코드 | 의미 |
| --- | --- |
| `0` | failed=0이고 한 행 이상 교체했거나 저장소가 비어 있음 |
| `1` | 운영 실패가 있거나, 일부 스킵되었는데 교체 행이 0. 이전 키 폐기 금지 |
| `2` | 플래그 누락·오류, 잘못된 hex 키·참조 형태 |

### 종료 1은 두 경우를 구분하지 못합니다 {#exit-1-is-ambiguous-by-design-read-this-before-treating-it-as-a-failure}

스키마에 키 버전 열이 없으므로 다음 두 경우를 도구가 구분할 수 없습니다.

1. **잘못된** 이전 키를 전달한 경우.
2. 저장소가 이미 새 키로 **완전히 교체된** 경우. 행은 남아 있으나 이제 다른 키로 감싸져 있어 같은 명령의 이전 키로 열리지 않습니다.

**완료된 교체를 다시 실행하면 `rewrapped=0 skipped=N`, 종료 1이 정상적으로 나올 수 있습니다.** 이것만으로 호출 알림을 만들지 마세요. 이전 키가 정확하고 행이 원래 있었다는 근거가 있으면 이미 교체되었을 가능성이 큽니다. 키가 정확한지 모르면 종료 1은 우연한 무작업을 성공으로 오해하지 않도록 하는 실패 시 거부 신호입니다.

## 수행하지 않는 작업 {#what-this-does-not-do}

- `req_*`·`resp_*` 암호문 마이그레이션. 감싼 데이터 키만 바꿉니다.
- 실행 중 `audit.log_bodies.key_ref` 자동 변경. 교체 후 참조와 비밀을 새 키로 바꾸고 게이트웨이를 재시작·지원되는 방식으로 재로드해야 합니다. 그렇지 않으면 새 본문이 계속 이전 키로 감싸집니다.
- 자동·예약 교체. 운영자가 실행하는 도구입니다.
