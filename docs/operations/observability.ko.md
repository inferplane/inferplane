---
translation_source: operations/observability.md
translation_source_sha256: 29e28a6de13ed59c5d95121b5621c0625d4c80b6e3855c92542ca7bdb4bcaac6
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
| 지속적인 준비 상태 실패·503 | 정책 동기화, 신원 지문, DB, 권한 발급·저널 |
| `inferplane_pricing_miss_total` | 경로 가격 누락·가격 메타데이터 오류 |
| `inferplane_audit_write_failures_total` | 저장 권한·용량·하위 시스템 가용성 |
| `inferplane_audit_buffer_utilization_ratio` | WAL 누적과 저장 실패 조사. 자동 요청 차단의 증거는 아님 |
| `inferplane_audit_anchor_failures_total` | 객체 저장소·IAM·보존 설정 |
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

## 기록 검증 {#verify-records}

```bash
bin/mayu audit verify --file /path/to/instance-audit.jsonl
bin/mayu report --file /path/to/instance-audit.jsonl --by team,model
```

인스턴스·재시작 구간을 구분해 보관하세요. 로컬 체인이 유효해도 호스트의 이력 재작성에 대한 저항성을 증명하지는 않습니다. 필요한 경우 [외부 앵커링](../runbooks/audit-anchoring.md)을 구성하고 검증하세요.

문제 보고에는 커밋·이미지, 프로파일, 클라이언트 버전, 상태·오류 종류, 비밀을 제거한 설정, 외부 호출 전인지 스트리밍 중인지 포함합니다. 키·자격 증명·원문 프롬프트·신원 선언·DSN은 제외하세요. 보안 문제는 [비공개 보안 신고 정책](../../SECURITY.md)을 따릅니다.

## 감사 내구성 한계 {#audit-durability-limits}

기본 차트는 감사 기록을 컨테이너 stdout(최선 노력 저장 대상)으로 내보내고, WAL은 데이터
볼륨에 두며 로컬 분석 인덱스는 끕니다. 따라서 파드 볼륨에서 계속 커지는 파일이 없습니다.
[감사 기록 수집](#collect-audit-records)을 참고하세요. 다른 배포는 저장 대상을 명시적으로 설정하세요.
필수 저장 대상 실패는 진단 지표를 증가시키지만 현재 자동 fail-closed 요청 차단을
보장하지 않습니다. WAL 자동 재생·복구도 미완성이므로 준비 상태나 로컬 체인 검증을
모든 과거 기록의 보존 증거로 취급하지 마세요. 저장 실패 시 증거를 보존하고 영향을
받는 트래픽을 중지하세요.

## 감사 기록 수집 {#collect-audit-records}

[`examples/otel-collector/audit-filelog.yaml`](../../examples/otel-collector/audit-filelog.yaml)은
노드 DaemonSet용 OpenTelemetry Collector(contrib) 설정입니다. `/var/log/pods/*/inferplane/*.log`를
읽고, `{"schema_version"`으로 시작하는 줄만 남기며(mayu는 시작 메시지도 출력합니다), 각 본문을
그대로 JSONL로 S3에 내보냅니다. 읽은 위치와 전송 큐는 `file_storage`에 영속됩니다.

- 기록 본문을 파싱해 다시 직렬화하지 마세요. 각 `prev_hash`는 앞 줄의 정확한 바이트를
  기준으로 하므로, 다시 쓴 줄은 `mayu audit verify`에서 실패합니다.
- stdout은 필수 저장 대상이 아니므로 collector가 전송하기 전까지는 순환되는 컨테이너 로그가
  유일한 로컬 사본입니다. kubelet 순환은 크기 기준(`containerLogMaxSize` × `containerLogMaxFiles`)이니
  견뎌야 할 가장 긴 collector 장애 시간에 맞춰 정하세요(요청당 약 1.3KB).
- 수집한 인스턴스별 기록은 목적지에서 [외부 앵커링](../runbooks/audit-anchoring.md)과 함께
  검증하세요. 앵커가 없으면 잘리거나 새로 시작된 체인도 검증을 통과합니다.

2026-09-25 otelcol-contrib 0.119.0으로, 분할(partial) 줄을 포함한 CRI 형식 로그에서 검증했습니다.
기록 41건이 바이트 단위로 동일하게 내보내졌고 수집 파일이 `mayu audit verify`를 통과했습니다.
S3 업로드 자체는 실행하지 않았습니다.
