# ADR-035: Kubernetes policy channel — CRD manifest + ConfigMap-mounted documents

## Status

Accepted (2026-07-30)

## Context

ADR-031 fixed the hybrid stance: CRD-style schema, no Kubernetes dependency
in the code. ADR-033/034 delivered the local-file and control-plane
channels. This ADR lands the Kubernetes channel without adding client-go to
either binary.

## Decision

### 1. CRD manifest, no controller yet

`deploy/crd/inferplane.dev_governancepolicies.yaml` registers
`GovernancePolicy` (group `inferplane.dev`, version `v1alpha1` — the same
string as `v1alpha1.APIVersion`, guarded by a repo test) with a structural
schema plus CEL rules mirroring `internal/policy` validation: exactly one
rule kind, hard-cap ⇒ FailClosed, subject requires team and/or user, rate
needs a positive dimension (CEL needs K8s 1.25+). `kubectl apply -f` a
policy document and the API server validates it exactly as the data plane
would — authoring and review become kubectl-native.

### 2. Enforcement today flows through the Helm chart's policies ConfigMap

`.Values.policies` (filename → document body) renders into a ConfigMap
mounted read-only at `/etc/inferplane/policies`; the operator adds
`"policies": ["/etc/inferplane/policies"]` to the chart's `config`. A
`helm upgrade` or `kubectl edit configmap` reaches the pod via kubelet sync
(≈1 min) and mayu's file watcher (ADR-033) applies it within seconds — the
full kubectl-driven live-update loop with zero new dependencies. This is
the same mechanism for sidecar/GitOps flows that project CR objects to
files.

### 3. The CRD-watch controller is a named follow-up

inferplaned consuming CR objects directly (watch → distribute over the
ADR-034 sync channel) requires client-go and a controller lifecycle; it
lands as its own milestone. Until then CRD objects are validated storage —
the manifest exists now so the schema contract is public and any GitOps
bridge has a typed object to project.

## Consequences

- Both binaries stay pure of Kubernetes machinery (ADR-031 invariant).
- One document body, three working channels: local file, control-plane
  push, ConfigMap mount — plus CRD-validated authoring.
- `internal/policy/crd_test.go` pins the CRD's group/version/kind and key
  field names to the Go schema so the manifest cannot drift silently.
