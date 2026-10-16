# Korean PII classifier sample (opt-in)

This sample adds a learned Korean PII signal to inferplane's `sensitiveData`
rules. A fine-tuned Laya checkpoint runs next to the gateway, and the
`systemone` classifier asks it whether a request contains personal data. The
classifier only restricts routing: an Enforce flag keeps the request on an
internal model, and it never edits the request.

| File | Purpose |
|---|---|
| `../config.pii-classifier-ko.json` | A complete gateway config: classifier in Shadow for team `pilot-ko` |
| `governance.yaml` | The privacy rule: `InternalOnly` to `private-coder` on detection or when uninspectable |
| `../helm.pii-classifier-ko.yaml` | Helm values: the Laya sidecar and the same classifier |
| `laya-serve/` | The image: Laya 0.4.6 on CPU with the checkpoint baked in, offline, non-root |
| `model-card.md` | The model card published with the weights |
| `training/` | The recipe that produced the checkpoint |

## Run it locally

```bash
docker run -d --rm --name laya-ko -p 127.0.0.1:8095:8095 -e LAYA_HOST=0.0.0.0 \
  ghcr.io/inferplane/laya-ko-pii@<digest>
INFERPLANE_ADMIN_TOKEN=<admin token> PII_KO_PUBLIC_KEY=<provider key> \
  go run ./cmd/mayu serve --config examples/config.pii-classifier-ko.json
```

The container port is published on the host loopback only. Replace the
illustrative providers in the config first.

## Kubernetes

```bash
helm install inferplane charts/inferplane -f examples/helm.pii-classifier-ko.yaml \
  --set piiClassifier.laya.image.digest=<digest>
```

The sidecar listens on `127.0.0.1` inside the gateway pod. It has no Service
port, and its probes run inside the container.

## Profiles

- **CPU (as shipped).** About 0.5 s per 1,000-character Korean window, and a
  ready time of about 7 to 14 s. The sample therefore sets `max_windows: 4`,
  `timeout_ms: 3000` and Shadow mode for one pilot team. A request with more
  uncached windows than that reads the first four and counts the rest as
  `abstain`, so it is never Clear; later requests read the rest once the
  earlier windows are cached.
- **GPU (for real traffic).** Build `laya-serve/Dockerfile` with a CUDA torch
  wheel on amd64, and run it as an in-namespace ClusterIP Service on a GPU
  node pool. Use a NetworkPolicy that admits only the gateway pods; never a
  LoadBalancer or a public endpoint. Set `max_windows: 64` and `timeout_ms`
  above the measured p95.

## Evidence

- **Document level.** The checkpoint scores F1 0.953, FPR 0.036 and AUROC
  0.993 on an independent synthetic Korean set of 2,236 rows; the gateway's
  regex detectors score F1 0.478 on the same set. See `model-card.md`.
- **Inside a coding-assistant request** (measured 2026-10-16 against the image built from `laya-serve/` with the weights in `SHA256SUMS`):
  wrapped-body F1 `0.951`, FPR `0.032`, abstain or error `1`.

All evaluation data is synthetic. Accuracy on your traffic is unknown until
you measure it in Shadow.

## Rollout

1. Run Shadow for a pilot team, and compare `would_restrict` in the audit
   records against a labeled sample of that team's traffic.
2. Before Enforce, declare the gates per sensitive type: the false-negative
   rate (including a name in one turn and an attribute in another), the
   false-positive and abstention rates on normal coding requests, the p95
   added latency and resource use, and the fallback during a classifier
   outage.
3. Watch `inferplane_pii_classifier_windows` and the cache hit ratio. A cold
   cache abstains on long first requests, and each gateway process warms its
   own cache.
4. Promote to Enforce only when the gates are met, with `timeout_ms` above the
   measured p95. In Enforce, a timeout restricts the request.
5. A retrained checkpoint gets a new `revision` and starts in Shadow again.

## Limits

- The evaluation is synthetic and document-level.
- The gateway reads long requests in windows. A name and an attribute in
  different windows or turns are not read together. The verdict cache stores
  whole windows only, so it never joins or splits what was judged together.
- The CPU profile does not cover long coding sessions.
- No span masking for classifier flags: a Mask rule applies its `onUninspectable` action instead (regex detections still mask).
- Only internal, self-hosted classifiers are supported.

Weights: `<namespace>/laya-ko-pii` at revision `<revision>` on Hugging Face (Apache-2.0).
