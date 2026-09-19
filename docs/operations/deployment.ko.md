---
translation_source: operations/deployment.md
translation_source_sha256: 9fdf54ad32f3dc11856798308759c5a0344020771b47c18c9ce6c962fed7987e
---

# 컨테이너 배포 {#deploy-with-containers}

검토한 커밋에서 이미지를 빌드하고 [배포 프로파일](../getting-started/deployment-profiles.md)을 먼저 선택하세요. 이 절차는 평가용 배포를 구성합니다. 운영 용량 산정과 장애 시험은 별도의 릴리스 조건입니다.

## 이미지 빌드 {#build-an-image}

저장소 루트에서 실행합니다.

```bash
INFERPLANE_IMAGE_TAG="$(git rev-parse --short=12 HEAD)"
docker build -t "inferplane:$INFERPLANE_IMAGE_TAG" .
docker build -f Dockerfile.inferplaned \
  -t "inferplaned:$INFERPLANE_IMAGE_TAG" .
```

두 Dockerfile은 정적 Go 바이너리를 빌드하고 비루트 distroless 이미지에 넣습니다. 이 저장소에는 릴리스 이미지를 게시하는 워크플로가 없습니다. 검토된 이미지를 자체 레지스트리에 올리고 배포 시 확인된 다이제스트로 고정하세요.

## 로컬 컨테이너 하나 실행 {#run-one-local-container}

[기본 예제](../../examples/config.json)에서 시작합니다. 상태 경로는 `/var/lib/inferplane`입니다. 사용하지 않는 공급자와 경로를 제거하고 실행 환경의 비밀 관리 기능으로 자격 증명을 전달하세요. 트래픽을 보내기 전에 가격 커버리지를 확인합니다.

이미지는 UID/GID 65532로 실행됩니다. 해당 UID가 쓸 수 있는 **새 전용 디렉터리**를 준비하거나 동일한 소유권의 볼륨을 구성하세요. 실행 중인 감사 WAL을 다른 컨테이너와 공유하면 안 됩니다.

```bash
mkdir -p .local/container-state
sudo chown 65532:65532 .local/container-state
sudo chmod 700 .local/container-state

# Export only the credentials referenced by your reviewed config beforehand.
docker run --rm --name inferplane \
  -p 127.0.0.1:8080:8080 -p 127.0.0.1:9090:9090 \
  --env ANTHROPIC_API_KEY --env INFERPLANE_ADMIN_TOKEN \
  --mount type=bind,src="$PWD/examples/config.json",dst=/etc/inferplane/config.json,readonly \
  --mount type=bind,src="$PWD/.local/container-state",dst=/var/lib/inferplane \
  "inferplane:$INFERPLANE_IMAGE_TAG"
```

이 명령은 수정한 설정이 내보낸 공급자 자격 증명만 사용한다고 가정합니다. Bedrock 경로가 남아 있다면 우연히 선택된 기본 자격 증명 체인에 의존하지 말고 의도한 신원을 명시적으로 제공하세요. SELinux 호스트의 바인드 마운트에는 적절한 컨테이너 라벨이 필요할 수 있습니다.

비공개 관리 API·콘솔로 키를 발급하거나 `docker exec`에서 `mayu keys create --config /etc/inferplane/config.json`을 실행하세요. 컨테이너에는 셸이 없으므로 바이너리를 직접 호출합니다.

## Helm 차트 배포 {#deploy-the-helm-chart}

차트는 기존 Secret을 참조하고 공급자 자격 증명을 생성하지 않습니다. 비밀 관리 컨트롤러로 대상 네임스페이스에 `inferplane-secrets`를 준비하세요.

레지스트리·다이제스트에 맞는 이미지, 검토된 설정, 리소스, 영속성, 네트워크 노출을 values 파일에 작성합니다. 로컬 프로파일의 평가 예시는 다음과 같습니다.

```yaml
replicaCount: 1
image:
  repository: registry.example.invalid/platform/inferplane
  tag: reviewed-commit
persistence:
  enabled: true
  size: 10Gi
secrets:
  existingSecret: inferplane-secrets
resources:
  requests:
    cpu: 100m
    memory: 256Mi
  limits:
    memory: 1Gi
ingress:
  enabled: false
```

리소스 값은 측정의 시작점이며 용량 보장이 아닙니다. `config` 블록도 작성해야 합니다. 차트 기본값을 운영 정책으로 사용하지 마세요.

```bash
helm lint charts/inferplane -f pilot-values.yaml
helm template inferplane charts/inferplane \
  --namespace inferplane -f pilot-values.yaml > rendered.yaml
# Review the rendered configuration, mounts, secret references and ports.
helm upgrade --install inferplane charts/inferplane \
  --namespace inferplane --create-namespace -f pilot-values.yaml
```

로컬 모드는 복제본 하나만 허용합니다. 영속 로컬 모드는 Recreate 배포이므로 교체 중 중단 시간을 계획하세요.

여러 복제본에는 명시적인 [공유 프로파일](../shared-governance.md)과 [공유 Helm 예제](../../examples/helm.shared-governance.yaml)를 사용합니다. 동일한 권한 DB·스키마, 초기화된 제어 영역, 고유한 게이트웨이 식별자, 안티어피니티를 충족할 노드가 필요합니다. 영속 공유 모드는 PVC를 분리하지만 Postgres의 고가용성까지 구성하지는 않습니다.

## 사용자 트래픽 허용 전 {#before-admitting-users}

준비 상태, 금지 모델 거부, 가격 커버리지, 감사 쓰기, 비밀 교체, 백엔드 장애 시 거부 동작을 확인하세요. 관리 포트와 메트릭은 비공개로 유지하고 원격 트래픽에는 검토된 TLS·네트워크 제어를 적용합니다. [보안](security.md), [모니터링](observability.md), [복구](recovery.md)를 참고하세요.
