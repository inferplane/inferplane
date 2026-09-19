---
translation_source: reference/infrastructure.md
translation_source_sha256: ddece87d82dc93383d564cb44e609a2c7a068f27b554924f4865441fdeb9a1e7
---

# 인프라 {#infrastructure}

### 1. 개요 {#1-overview}

mayu·inferplaned를 각각 정적 바이너리로 빌드해 별도의 distroless 이미지로 배포합니다. 게이트웨이 Helm 차트는 ConfigMap 설정과 선택적 Bedrock IRSA ServiceAccount를 구성합니다.

### 2. 구성 요소 {#2-components}

| 구성 | 경로 | 역할 |
| --- | --- | --- |
| Dockerfile | `Dockerfile` | CGO_ENABLED=0 다단계 빌드와 비루트 distroless |
| 제어 영역 Dockerfile | `Dockerfile.inferplaned` | 별도 정적 inferplaned 이미지 |
| Docker 제외 | `.dockerignore` | 테스트·문서·차트 빌드 컨텍스트 제외 |
| Helm 차트 | `charts/inferplane/` | Deployment, 데이터·관리 Service, 계정, ConfigMap, 선택적 정책 ConfigMap·Ingress·PVC, NOTES |
| GovernancePolicy CRD | `deploy/crd/` | v1alpha1 구조 스키마·CEL, Kubernetes 1.25+. 컨트롤러 watch는 후속 과제 |
| 차트 값 | `charts/inferplane/values.yaml` | 이미지, 로컬=1·공유 다중 복제, Secret·IRSA·Ingress·영속성·OTLP 예제 |
| Grafana | `deploy/grafana/inferplane.json` | 패널 9개의 Prometheus 대시보드 |
| 제품 문서 | `mkdocs.yml`, `docs/assets/`, `scripts/docs_hooks.py` | Material·static-i18n, 한영 가이드·메뉴·검색·저장소 링크 |
| 문서 전달 | `.github/workflows/docs.yml` | 엄격한 빌드, 번역 누락·최신성, 링크 검증. 제한된 배포 권한으로 main의 Pages 게시 |

### 3. 주요 결정 {#3-key-decisions}

- CGO를 끈 정적 바이너리여서 libc 없이 비루트 distroless 실행이 가능합니다.
- 관리 콘솔 정적 자산은 `go:embed`로 바이너리에 포함됩니다(ADR-001).
- **설정 재로드:** 설정 수정 후 `kill -HUP <pid>`로 공급자·모델·가격을 원자적으로 교체합니다. 거버넌스 카운터·키·감사는 유지하고 오류 시 이전 설정을 유지합니다. 리스너·TLS·드레인·팀 정책 한도는 재시작이 필요합니다.
- 로컬 기본은 복제본 하나입니다. ADR-046 공유 Postgres는 여러 게이트웨이를 지원하며 차트가 잘못된 로컬 복제를 거부합니다. 영속 공유는 독립 PVC의 StatefulSet·필수 안티어피니티·중단 예산입니다. [공유 거버넌스](../shared-governance.md)를 참고하세요.
- **영속성:** 로컬은 기존 Deployment·PVC 동작, 공유는 키·카운터를 Postgres에 두고 복제본별 PVC에 감사 구간을 저장합니다. WAL을 공유하지 마세요.
- 차트는 existingSecret만 참조하고 비밀을 만들지 않습니다.
- Ingress는 기본 비활성입니다. 켜더라도 관리 경로는 `ingress.admin.enabled: true`를 추가로 지정해야 합니다. 데이터 영역 활성화가 관리 노출을 자동으로 허용하지 않습니다.
- **수집 신호 세 가지:** 메트릭은 `:9090/metrics` Prometheus이며 OTLP 메트릭 내보내기는 없습니다. 중복 계측·원장 불일치를 피하기 위함입니다. `gen_ai_*` 이름도 전송은 Prometheus입니다. 트레이스는 선택적 config.otel이 HTTP 4318·gRPC 4317로 전송합니다. GenAI, 캐시 read/5m/1h, 정수 비용·가격 누락, 부분 스트림 필드를 포함합니다. HTTP 200을 이미 보낸 부분 스트림도 span은 Error입니다. 사용량 윈도는 inferplaned `/v1alpha1/usage`의 자체 프로토콜이며 OTLP 수신기가 처리하지 않습니다.
- 수집기에 메트릭·트레이스 파이프라인과 각각 batch 처리기를 구성하세요. 메트릭은 인증이 없고 비밀·key_id가 없지만 비용 정보이므로 클러스터 내부에 둡니다. 차트는 운영자 모니터링 스택의 CRD인 ServiceMonitor·PodMonitor를 제공하지 않으며 이름이 admin인 포트로 수집하면 됩니다.
- NOTES.txt는 실제 Ingress 주소 또는 port-forward, 첫 키 명령, Claude Code 환경 변수를 출력해 values에서 다시 계산하지 않고 시작할 수 있게 합니다.

### 4. 코드 위치 {#4-code-pointers}

- `Dockerfile`: 빌드·런타임 단계.
- `charts/inferplane/templates/deployment.yaml`: Pod와 8080·9090 포트.
- `charts/inferplane/templates/configmap.yaml`: config.json 렌더링.
- `charts/inferplane/templates/ingress.yaml`: 선택적 데이터·관리 Ingress.
- `charts/inferplane/templates/pvc.yaml`: 로컬 키 저장소 영속 볼륨.
- `charts/inferplane/templates/NOTES.txt`: 설치 후 빠른 시작.

### 5. 관련 문서 {#5-cross-references}

- [아키텍처](../architecture.md)의 인프라 절.
- [ADR-031](../decisions/ADR-031-monorepo-control-plane-data-plane-split.md), [ADR-046](../decisions/ADR-046-shared-governance.md).
- [컨테이너·Helm](../operations/deployment.md), [복구](../operations/recovery.md), [문서 관리](../documentation.md).

### 라우팅 배포 · ADR-043 {#routing-rollout-adr-043}

sensitiveData·context 활성화 전에 모든 mayu, inferplaned, 사용하는 CRD를 업그레이드합니다. 경로·가격·검증된 경계와 기능을 먼저 설치하세요. 로컬 정책은 토폴로지 조립 후 리스너 시작 전에, CP ApplyWire는 각 데이터 영역에서 목적지를 검사합니다. 첫 요청부터 보호하려면 require_sync, 정책 나이 제한에는 max_policy_age를 사용합니다. 준비되지 않거나 오래된 상태에서도 계산은 로컬/200입니다. 빠른 시작 디렉터리 대신 독립 policy-routing 예제를 사용하세요. Shadow는 컨텍스트 선호에만 적용하고 개인정보는 이미 집행합니다. 이 라우팅 기능만으로 HA·내구성이 좋아지는 것은 아닙니다. [운영자 가이드](../policy-routing.md)를 참고하세요.

ADR-044의 결정·유한 어피니티는 로컬이며 중앙 분류기·캐시 의존성이 없습니다. 폴백은 승인된 개인정보·비용 제약 안에서만 동작합니다. CP 장애 중에도 유효 권한이 있는 동안 설치된 규칙을 쓰며 만료된 하드 리스는 거부합니다. ADR-045는 Postgres 전역 금전 권한, 교체 가능한 CP 복제본, 비공개 영속 노드 저널을 추가합니다. CP 준비 상태는 DB를 검사하고 데이터 영역은 장애 중 이미 확정된 유한 잔액만 유지합니다. 복제 DB와 안정적인 CP 주소를 운영하세요. ADR-045는 로컬 키·요청률·토큰을 전역화하지 않으며 별도 동기 공유 승인은 ADR-046입니다. [배포·실패 동작](../durable-budgets.md)을 참고하세요.
