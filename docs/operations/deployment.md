# Deploy with containers

Build an image from a reviewed commit and select the [deployment profile](../getting-started/deployment-profiles.md) first. These procedures establish an evaluation deployment; production sizing and failure testing remain your release gates.

## Build an image

From the repository root:

```bash
INFERPLANE_IMAGE_TAG="$(git rev-parse --short=12 HEAD)"
docker build -t "inferplane:$INFERPLANE_IMAGE_TAG" .
docker build -f Dockerfile.inferplaned \
  -t "inferplaned:$INFERPLANE_IMAGE_TAG" .
```

The two Dockerfiles build static Go binaries into non-root distroless images. There is no release-image publication workflow in this repository; push reviewed images to your own registry and pin the resolved digest for deployment.

## Run one local container

Start from [the main example](../../examples/config.json). It uses `/var/lib/inferplane` for state. Remove unused providers and routes, supply the configured secrets through your runtime secret mechanism, and verify pricing before sending traffic.

The image runs as UID/GID 65532. Prepare a **new dedicated** state directory writable by that UID, or provision a volume with equivalent ownership. Do not share a live audit WAL with another container.

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

This command assumes your edited config uses only the exported provider credentials. If a Bedrock route remains enabled, explicitly supply its intended identity instead of relying on an accidental default credential chain. Bind mounts on SELinux hosts may need an appropriate container label.

Issue keys through the private admin API/console, or use `docker exec` with `mayu keys create --config /etc/inferplane/config.json`. The container has no shell; invoke the binary directly.

## Deploy the Helm chart

The chart expects an existing Secret; it never creates provider credentials. Your secret controller should provision `inferplane-secrets` in the selected namespace.

Prepare a values file with your registry/digest-compatible image selection, reviewed config, resources, persistence, and network exposure. For a local-profile evaluation:

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

The resource values are an initial measurement point, not a sizing guarantee. Supply the `config` block as well; chart defaults are not a production policy.

```bash
helm lint charts/inferplane -f pilot-values.yaml
helm template inferplane charts/inferplane \
  --namespace inferplane -f pilot-values.yaml > rendered.yaml
# Review the rendered configuration, mounts, secret references and ports.
helm upgrade --install inferplane charts/inferplane \
  --namespace inferplane --create-namespace -f pilot-values.yaml
```

Local mode allows one replica. Persistent local mode uses a Recreate deployment, so plan downtime during replacement.

For multiple replicas, use the explicit [shared profile](../shared-governance.md) and [shared Helm example](../../examples/helm.shared-governance.yaml). It needs the same authority database/schema, initialized control planes, unique gateway identities, and enough schedulable nodes for anti-affinity. Persistent shared mode uses separate PVCs; it does not make Postgres highly available.

## Before admitting users

Confirm readiness, denied-model behavior, pricing coverage, audit writes, secret rotation, and configured refusal on backend failure. Keep the admin port and metrics private; terminate remote traffic with reviewed TLS/network controls. See [security](security.md), [monitoring](observability.md), and [recovery](recovery.md).
