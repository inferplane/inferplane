---
translation_source: operations/observability.md
translation_source_sha256: 7d70d36ecbf1250008227558f1d44a9c12c8e81c4179011eedc6536e49935ae8
---

# 모니터링과 문제 해결 {#monitor-and-troubleshoot}

준비 상태로 요청 승인 가능 여부를, 메트릭으로 추세를, 감사·권한 기록으로 회계 증거를 확인합니다. 프로세스 상태가 정상이어도 요청을 승인할 수 있다고 단정할 수 없습니다.

## 상태와 신호 채널 {#health-and-signal-channels}

| 신호 | 엔드포인트·전송 | 용도 |
| --- | --- | --- |
| 프로세스 생존 | `GET :9090/healthz` | 프로세스가 응답하는가? |
| 준비 상태 | `GET :9090/readyz` | 현재 의존성과 조건에서 요청을 승인할 수 있는가? |
| 메트릭 | `GET :9090/metrics`, Prometheus | 지연·요청·비용 추정·거부·감사 상태 |
| 트레이스 | 선택적 `otel`, OTLP HTTP/gRPC | 요청·시도별 시간과 제한된 메타데이터 |
| 사용량 윈도 | 게이트웨이 → CP `/v1alpha1/usage` | 제어 영역 분석, OTLP와 별개 |
| 감사 | 인스턴스별 저장 대상·WAL | 요청 증거와 정확한 바이트 체인 검증 |

상태와 메트릭은 의도적으로 인증이 없습니다. 관리 네트워크 경로를 제한하세요. 정적 콘솔은 공개된 데이터 없는 셸이며 데이터 API에는 인증이 필요합니다.

[Grafana 대시보드](../../deploy/grafana/inferplane.json)에서 시작하세요. Prometheus 스크레이프·수집기와 OTLP 트레이스 수신기를 따로 구성합니다. 차트는 ServiceMonitor를 설치하지 않습니다.

## 알림 대상 신호 {#signals-worth-alerting-on}

| 신호 | 확인할 내용 |
| --- | --- |
| 지속적인 준비 상태 실패·503 | 정책 동기화, 신원 지문, DB, 권한 발급·저널, 감사 WAL 압력 |
| `inferplane_pricing_miss_total` | 경로 가격 누락·가격 메타데이터 오류 |
| `inferplane_audit_write_failures_total` | 저장 권한·용량·하위 시스템 가용성 |
| `inferplane_audit_buffer_utilization_ratio` | 보관 WAL 기록 수 / 1024. 1 이상이면 새 추론 차단. 1 미만에서도 WAL 실패가 고정될 수 있으므로 `/readyz` 확인 |
| `inferplane_audit_anchor_failures_total` | 객체 저장소·IAM·보존 설정 |
| `inferplane_audit_auto_mode_unparsed_total` | 콘솔이 읽지 못한 Claude Code 자동 모드 차단 호출·서버 안전장치(ADR-057). 증가하면 클라이언트 형식 변경을 의심 |
| `gen_ai_server_time_to_first_token_seconds` | 업스트림·요청 경로 지연 |
| `inferplane_fallback_total`·회로 차단기 상태 | 공급자 실패·비호환 |
| `inferplane_usage_windows_dropped_total` | 분석 전달 손실. 권한 회계와 구분 |

실측 트래픽과 프로파일에 따라 임계값을 정하세요. Prometheus 비용은 관측값입니다. 사용 가능한 잔액은 정수 회계 기록과 확정된 권한 예약이 결정합니다.

## 증상별 점검 {#troubleshooting-by-symptom}

| 증상 | 먼저 확인할 내용 |
| --- | --- |
| 401 | 가상 키와 공급자·관리 토큰 구분, DB 선택, 키 폐기, 신원 바인딩 |
| 403 | 모델·팀 접근, 개인정보·리전 제한, 정책 호환성 |
| 권한 프로파일의 402 | 금전 소진 또는 보수적 상한이 잔액보다 큼 |
| 429 | 요청률·토큰 할당량 소진과 집행 범위 |
| 첫 요청 전 503 | 필수 동기화, 공유 네임스페이스·바인딩, 최초 권한, DB·저널 |
| 재시도 가능한 권한 대기 | `Retry-After` 준수. 요청을 통과시키려고 회계 상한을 낮추지 않기 |
| HTTP 200 스트림 조기 종료 | 종료 프로토콜 오류, 관측 사용량, 유지된 의무 |
| 생성은 거부되는데 계산 값이 0 | 계산은 로컬/200 유지. 준비 상태·키 저장소 확인 |
| Codex 모델 누락 | 도구·Responses 기능 선언, 네이티브 카탈로그 매핑, 클라이언트 버전 |
| 캐시 적중 감소 | 프로토콜 변환, 마스킹, 모델 전환, 요청 바이트 변경 |

## 결과와 비용 근거 읽기 {#read-outcome-and-cost-evidence}

게이트웨이 Overview·Usage·Logs는 관측 완료 기록을 사용합니다(ADR-053).
이전 분석에서 빠졌던 비용 없는 실패도 포함합니다. 분모는 `request_completed`이며
전체 수신 트래픽이나 고유 요청/세션이 아닙니다. `request_started` 거부,
미인증 401, 관리자 이벤트, 클라이언트 내부 실패는 제외합니다. 재시도/폴백의
기록 단위는 ingress마다 다를 수 있습니다. succeeded는 전송 결과이지 모델 답변
품질이나 사용자의 작업 완수 증명이 아닙니다.

`(failed + partial) / completed`는 분자·분모·unknown 건수와 함께 조사합니다.
HTTP 200이어도 partial이 우선하고, 없거나 유효하지 않은 결과는 unknown입니다.
완료 기록이 없으면 비율은 `—`입니다. unknown은 성공이 아니라 관측 비율의
불완전성을 뜻합니다. 메트릭 트래픽과 이 분모는 서로 다른 관측입니다.

금액 전에 비용 상태를 읽습니다. `estimated`는 사용량·단가 기반 산정액(0도 유효),
`uncertain`은 근거 불완전 기록 금액, `legacy_unknown`은 사용량 근거 없음,
`unpriced`는 가격 없음, `unavailable`은 Cost 기록 없음입니다. unpriced/unavailable의
숫자 0은 알려진 달러 금액이 아닙니다. 산정액과 uncertain/legacy 기록 금액은
분리하며 기존 Cost-present 금융 합계는 유지합니다. 어느 상태도 공급자 청구서나
미확정 의무의 환급 허가가 아닙니다.

Responses 스트림의 제한된 원인은 `context_length_exceeded`,
`invalid_encrypted_content`, `request_canceled`, `upstream_timeout`,
`stream_incomplete`, `response_incompatible`, `usage_missing`,
`client_write_failed`, `upstream_error`입니다. 공급자 코드는 타입으로 검증한 근거가
필요합니다. 취소·기한·게이트웨이 원인은 명시적 runtime 근거가 필요하며,
원문 오류나 native 공급자 소유 프레임에서 코드를 추측하지 않습니다. 알 수 없는
비어 있지 않은 분석 코드는 `upstream_error`로 제한합니다.

스트림 commit 전에는 기존 HTTP·재시도 규칙을 적용합니다. 이후에는 HTTP 200과
partial=true를 유지하고 감사 및 쓰기 가능한 게이트웨이 SSE 오류에 같은 코드를
남기며 재시도하지 않습니다. 클라이언트 쓰기 실패는 추가 SSE 쓰기 없이
`client_write_failed`로 기록합니다. Native 성공 바이트는 보존합니다. 관측 사용량은
기존의 정확히 한 번 finish·정산 경로를 따르며 누락 사용량을 0으로 만들지 않습니다.

`legacy_projection_records`, `/admin/analytics/health`, 큐 진단, 보관 원본을 함께
확인합니다. Version 0은 원본 근거 재생 전을 뜻하며 원본에 사용량 근거가 없으면
version 1도 `legacy_unknown`일 수 있습니다. 과거 행 0은 전체 수집 증거가 아닙니다.
보관 공백, 로컬 비차단 4096-entry 큐 drop, 수집 지연, 불완전 JSONL 마지막 줄,
`request_started`만 있는 요청은 범위를 제한합니다. 구버전 API의 필드 누락은
근거 정보 없음으로 표시해야 합니다.

[API 예제](../api-reference.md#analytics-outcome-and-cost-evidence-adr-053)와
[복구 절차](recovery.md#recover-analytics-evidence)를 확인하세요. 이 투영은 메트릭
라벨·원문 프롬프트/오류·키/세션 식별자·금전 권한을 추가하지 않습니다. 기본/로컬,
노드 로컬 권한, 공유 거버넌스의 기존 프로파일별 의존성과 한계를 유지합니다.

## 기록 검증 {#verify-records}

```bash
bin/mayu audit verify --file /path/to/instance-audit.jsonl
bin/mayu report --file /path/to/instance-audit.jsonl --by team,model
```

인스턴스·재시작 구간을 구분해 보관하세요. 로컬 체인이 유효해도 호스트의 이력 재작성에 대한 저항성을 증명하지는 않습니다. 필요한 경우 [외부 앵커링](../runbooks/audit-anchoring.md)을 구성하고 검증하세요.

문제 보고에는 커밋·이미지, 프로파일, 클라이언트 버전, 상태·오류 종류, 비밀을 제거한 설정, 외부 호출 전인지 스트리밍 중인지 포함합니다. 키·자격 증명·원문 프롬프트·신원 선언·DSN은 제외하세요. 보안 문제는 [비공개 보안 신고 정책](../../SECURITY.md)을 따릅니다.

## 감사 내구성 한계 {#audit-durability-limits}

차트 기본 설정은 컨테이너 stdout(최선 노력 저장 대상)으로 감사 기록을 내보내고,
WAL은 데이터 볼륨에 두며 로컬 분석 인덱스는 비활성화합니다. WAL이 정상 동작하면
해당 볼륨에 데이터가 누적되지 않습니다. [감사 기록 수집](#collect-audit-records)을
참고하세요. 다른 배포는 저장 대상을 명시적으로 구성해야 합니다.

`audit.failure_mode` 기본값은 유일하게 지원하는 `buffer_then_block`입니다.
필수 저장 대상이 실패하면 해당 기록과 이후 기록을 WAL에 보관합니다. 보관 기록이
1024개에 도달하면 준비되지 않은 상태로 고정됩니다. 인증된 새 추론 요청은 프로토콜에
맞는 503과 `Retry-After`를 받고 `/readyz`도 503을 반환합니다. WAL 추가·정리 실패는
즉시 이 상태로 고정합니다. 최선 노력 저장 대상 실패는 요청 승인을 닫지 않습니다.
토큰 계산 API는 로컬/200을 유지하고 모델 목록도 사용할 수 있습니다. 생존 검사는
계속 프로세스 응답 여부를 표시합니다.

이 상태는 자동으로 해제되지 않습니다. 이미 승인된 요청, 관리 이벤트와 계산 요청은
계속 기록을 추가할 수 있으므로 1024는 새 요청 승인 임계값이며 엄격한 기록 수·바이트
상한이 아닙니다. 시작 시 보관 WAL을 검증하고 기록 수를 읽습니다. 재시작만으로 가득 찬
버퍼를 해제할 수 없으며 손상되거나 잘린 WAL은 시작을 거부합니다. 과거 일부 저장 대상에
이미 전달된 기록이 있을 수 있으므로 자동 저장 대상 재생은 하지 않습니다.
[감사 승인 복구 절차](recovery.md#recover-audit-admission)에 따라 증거를 보존하고
대조한 뒤 재시작하세요. 준비 상태와 로컬 체인 검증은 전체 이력 전달의 증거가 아닙니다.

## 감사 기록 수집 {#collect-audit-records}

[`examples/otel-collector/audit-filelog.yaml`](https://github.com/inferplane/inferplane/blob/main/examples/otel-collector/audit-filelog.yaml)은
노드 DaemonSet용 OpenTelemetry Collector(contrib) 설정입니다. `/var/log/pods/*/inferplane/*.log`를
읽고, `{"schema_version"`으로 시작하는 줄만 남기며(mayu는 시작 메시지도 출력합니다), 각 본문을
그대로 JSONL로 S3에 내보냅니다. 읽은 위치와 전송 큐는 `file_storage`에 영속됩니다.

- 기록 본문을 파싱해 다시 직렬화하지 마세요. 각 `prev_hash`는 앞 줄의 정확한 바이트를
  기준으로 하므로, 다시 쓴 줄은 `mayu audit verify`에서 실패합니다.
- stdout은 필수 저장 대상이 아니므로 collector가 기록을 영속 큐에 넣기 전까지는
  컨테이너 로그가 게이트웨이의 남은 로컬 사본입니다. collector의 영속 큐는 내보내기가
  끝날 때까지 별도 로컬 사본을 보관합니다. kubelet 순환은 크기 기준
  (`containerLogMaxSize` × `containerLogMaxFiles`)이니 collector가 읽지 못하는 가장 긴
  시간에 맞춰 정하세요(요청당 약 1.3KB).
- 수집한 인스턴스별 기록은 목적지에서 [외부 앵커링](../runbooks/audit-anchoring.md)과 함께
  검증하세요. 앵커가 없으면 잘리거나 새로 시작된 체인도 검증을 통과합니다.

2026-09-25 otelcol-contrib 0.119.0으로, 분할(partial) 줄을 포함한 CRI 형식 로그에서 검증했습니다.
기록 41건이 바이트 단위로 동일하게 내보내졌고 수집 파일이 `mayu audit verify`를 통과했습니다.
S3 업로드 자체는 실행하지 않았습니다.
