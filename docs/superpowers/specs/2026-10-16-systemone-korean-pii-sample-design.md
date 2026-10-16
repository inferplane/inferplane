# systemone PII classifier and the Korean opt-in sample

Status: approved by the user on 2026-10-16, including the three proposals in
Decisions. This is sub-project 1 of
[the pluggable PII classifiers spec](2026-10-07-pii-classifiers-design.md)
(the umbrella). It follows the boundary rules of
[PR-08](../plans/2026-10-11-gateway-correctness-improvements.md) and settles
the inspection-scope question that PR-08 left open in the umbrella.
Implementation starts only after its implementation plan is approved.
Amendments 1–7 come from mapping this spec to the code (7 from the commit
security review; it supersedes 1), and are listed in the
plan (`docs/superpowers/plans/2026-10-16-systemone-korean-pii-sample.md`).

## Goal

The user's goal (2026-10-15): use Jev-like typed decision models
(strands-decider, Laya) to detect Korean personal data well, and ship this as an
opt-in sample of inferplane's PII protection.

Interpretation, confirmed through the decisions below:

- inferplane gains a `systemone` classifier type. It sends the protected
  request content to a self-hosted `/v1/systemone` server and turns the answer
  into a routing restriction through the existing `sensitiveData` rules.
- The repository ships a Korean sample: a fine-tuned Laya checkpoint published
  as weights and as a container image, a Helm sidecar, an example config and
  policy, a model card, and the training recipe that produced the checkpoint.
- Everything is off by default. Without `pii_classification` in the config the
  gateway behaves, and costs, exactly as today.

Success means three things. First, the gateway path is implemented and tested,
including the PR-08 completion conditions. Second, the sample deploys from the
published artifacts and records Shadow evidence. Third, an operator can promote
the classifier to Enforce on the rollout gates in this document. Accuracy on
real traffic is not something this change can establish. It is measured in
Shadow after deployment.

## Decisions

| Question | Decision | Source |
|---|---|---|
| What "PII removal" means | Routing protection. A detection restricts the route (InternalOnly or Block). The classifier never edits the body. | User, 2026-10-15 |
| First scope | The `systemone` type only, internal boundary only, file config only. | User, 2026-10-16 |
| Weights | Publish the fine-tuned weights. | User, 2026-10-16 |
| Distribution | A container image with the weights baked in, plus a Hugging Face model repository holding the same weights and the model card. | User, 2026-10-16 |
| Inspection scope | All protected content actually retransmitted, as PR-08 requires. A node-local verdict cache makes long sessions practical. | User, 2026-10-16 |
| Published checkpoint | Laya v3 seed 1 only. strands-decider stays a documented alternative in the recipe. | Proposed here |
| Shadow execution | Synchronous, inside `RouteRequest`, like Enforce. | Proposed here |
| Image variants | One CPU image (linux/amd64 and linux/arm64). A GPU build is documented, not published. | Proposed here |

The three "proposed here" rows are this spec's choices and are open to review.
The reasons are given in the sections that follow.

## Evidence that shapes the design

The fine-tuning results are recorded in the experiment plan on branch
`feat/korean-pii-classifier-finetune`
(`docs/superpowers/plans/2026-10-13-korean-pii-classifier-finetune.md`). They
are summarized here because they set defaults. All rows are synthetic. The
independent set v3 has 2,236 clean rows, written by four non-Anthropic
generator models in 14 genres that do not appear in training.

| Model | F1 | FPR | AUROC | Abstain (0.1 < p < 0.9) | CPU p50 / p95 per document |
|---|---|---|---|---|---|
| Gateway regex (`internal/sensitivity`) | 0.478 | 0.345 | 0.555 | 0 | 0.1 / 0.3 ms |
| Laya v3 seed 1 | 0.953 | 0.036 | 0.993 | 0.023 | 369 / 805 ms |
| strands-decider v3 | 0.961 | 0.038 | 0.993 | 0.025 | 2.2 / 3.9 s |

- **Why Laya seed 1.** Its F1 difference from strands-decider is within noise
  (paired bootstrap 95% CI −0.003 to +0.020). Laya is 322M parameters against
  2B, has a batch endpoint, and runs on CPU. strands-decider needs a GPU.
  - Seed 1 was chosen on the same set it is scored on, so 0.953 is optimistic.
  - The two other Laya seeds scored 0.934 and 0.939.
- **Remaining errors.** The model card states these:
  - Documents that hold only one side of an identifier-plus-fact pair are
    flagged 14.5% of the time. An example is an employee number with no health
    or salary fact.
  - Name-only documents are flagged 3.3% of the time.
  - Name-plus-attribute recall is 0.87.
- **Length.** Korean is 0.70 tokens per character (p95 0.79) with the Laya
  tokenizer. Evaluation documents are p50 424 and p95 974 characters. The
  multilingual checkpoint reads at most 1,024 tokens.
- **CPU cost.** On a 16-vCPU host, `POST /v1/systemone/batch` with
  Korean windows of 800–1,000 characters takes p50 448 ms for 1 window, 2.4 s for 4 and
  7.9 s for 16. Cost grows almost linearly with the number of windows. On an
  A10G GPU, one request took 24 ms.
- **Retransmission.** A coding-assistant request resends the system prompt,
  the tool definitions and the whole conversation on every turn. Coverage of
  all of it (PR-08) at about 1 KB per window would put most long-session
  requests above any practical window cap. The verdict cache (§3) is what makes
  the full scope affordable.

The cost numbers lead to two consequences. A CPU sidecar is an evaluation
profile for short sessions and small teams. A production deployment needs a
GPU-backed endpoint.

## Scope

**In scope:**
- the `internal/classifier` seam;
- the content extractor;
- the verdict cache;
- `plugins/systemone`;
- routing integration (Shadow and Enforce, fail-closed);
- audit and metrics;
- the `pii_classification` file config with SIGHUP reload;
- the Korean sample package, its publication, and the docs.

**Deferred to later sub-projects, each with its own spec and plan:**
- `presidio` and `generic` plugins;
- the `external` boundary: hosted Jev API, local-first two-phase execution and
  `allowed_boundaries`;
- console registration: classifier store, `/admin/classifiers`, probe;
- asynchronous Shadow;
- a cache shared across nodes.

## Design

### 1. Seam: leaf package `internal/classifier`

```go
type Verdict int // Clear | Flagged | Abstain
type Mode string // "Shadow" | "Enforce"

// Scores holds one verdict per window, in order; a window past the
// classifier's cap is Abstain and was not sent (amendment 7, 2026-10-16).
type Scores struct {
	Verdicts []Verdict
	Sent     int // windows sent, for metrics only
}

type Classifier interface {
	// Windows cuts segments into the deterministic windows the classifier
	// reads and the cache stores; every non-empty segment's text is in some
	// window.
	Windows(segments []string) []string
	Score(ctx context.Context, windows []string) (Scores, error)
}

type Config struct {
	Name, Type, Revision, Endpoint string
	BearerToken string                     // resolved from the api_key ref; never logged
	Threshold   float64
	Options     map[string]json.RawMessage // validated by the plugin factory
}

type Factory func(Config) (Classifier, error)
func Register(typ string, f Factory) // from plugin init(); a duplicate panics
func Types() []string

type Entry struct {
	Name, Revision string
	Mode           Mode
	Classifier     Classifier
	Global         bool
	Teams          map[string]bool
	settings       [32]byte // digest of type, endpoint, revision, threshold and canonical options
}

type Set struct {
	Entries       []Entry
	Timeout       time.Duration
	MaxInputBytes int
	cache         *verdictCache // nil when the cache is disabled
}

func (s *Set) For(team string) []Entry // nil-safe
```

- Core packages import only `internal/classifier`.
- `plugins/systemone` registers itself in `init()` and is blank-imported in
  `cmd/mayu/main.go`, like the providers.
- The resolved `*Set` is a field of `live.State`, built by `BuildStateWith`.
  A request therefore uses classifiers and a cache from the same generation as
  its routes.
- `Run(ctx, req)` is the package's entry point for the router. It asks each
  entry for its windows, filters them through the cache, scores the rest in
  parallel, stores newly clear windows and returns one result per entry.
  Identical uncached windows in one request are sent once. The router never
  touches the cache itself.

### 2. Content extraction

The classifier reads exactly the text that the regex inspector reads. A new
function `sensitivity.Segments(ctx, protocol, raw)` walks the body with the
inspector's own walker. It returns every visited string in body order, as
borrowed segments. That covers:
- every role of the conversation, including assistant history, tool calls and
  tool results;
- the system and developer input;
- tool definitions and their argument schemas;
- encoded JSON inside strings, to the walker's existing depth limit.

The walker does not trust a role or the history position: an earlier turn is
read like a new one. Content the walker cannot read (media, encrypted or opaque
items) already makes the regex result incomplete, so the rule's
`onUninspectable` applies. The classifier does not need its own coverage
signal for it.

Extraction happens once per request and is shared by every entry. The total
extracted text is capped by `max_input_bytes`. Above the cap, every entry is
Abstain, nothing is hashed or sent, and nothing is cached. An empty extraction
marks every entry `skipped`.

### 3. Verdict cache

The cache stores which whole windows a classifier has already judged clear, so
that repeated content (system prompt, tool definitions, earlier turns) is
classified once (amendment 7). A long segment is split into deterministic
windows, so a tool result longer than `max_windows` windows is read across a
few requests instead of abstaining forever.

Amendment 1 cached units (short segments and chunks) instead. The 2026-10-16
commit security review showed that it let two parts cleared separately, such as
a name and a diagnosis, bypass the check on their combination. Whole-window
caching cannot do that, because a window is skipped only when that exact text
was judged clear.

**Key.** Each key is HMAC-SHA256 under a 32-byte key drawn from `crypto/rand`
for each Set generation. It covers, length-prefixed:
- the principal's team;
- the entry name and the entry's settings digest;
- the policy references (name, generation and rule) of the rules that apply
  to the request, which `RouteRequest` always has, unlike the snapshot-only
  policy generation string;
- the window text.

**Value.** Only the Clear mark and its insertion time. Flagged, Abstain and
error results are never cached, so flagged content is classified again on every
request that carries it.

**Bounds.**
- An LRU of `max_entries` (default 100,000, about 10 MB) with
  `ttl_seconds` (default 3,600).
- `max_entries: 0` disables the cache.

**Lifetime.** The cache belongs to one Set generation. A reload, a restart, a
revision or option change, or a change to an applicable policy's generation makes old entries
unreachable. Eviction and TTL expiry lead to classification again.

**What it stores.** No text, no raw session identifier and no principal key.
Its values cannot widen authorization: a hit means only that this classifier
already returned Clear for this exact text, for this team, under these
settings and these policy generations.

**Why team scope.** A hit is faster than a miss. A cache shared across teams
would let one team test whether another team had sent a particular clear
string. Keying by team confines that timing signal to the team's own traffic.

**Safety argument.** The verdict depends on content, not on role or position.
So forged assistant history gains nothing: the same text gets the same verdict
whether it is marked as history or as new input. A window is cached only when
its own verdict was clear, and a request's windows are judged as wholes, so
what was judged together is always judged together again.

**Known limit.** A document-level classifier judges one window at a time. A
name in one place and a health fact in another are judged together only when
they share a window, so a name in an earlier turn and a diagnosis in the
current turn may not be read together. The cache does not widen this gap: it
never joins or splits windows. Rollout gate 2 measures this class separately.

### 4. Plugin `plugins/systemone`

**Wire.** Both modes send the same question:

```json
{"type": "noul", "instructions": "<instructions>", "criteria": {"true": "...", "false": "..."}}
```

The `criteria` field is present only when configured.

- `batch` mode (default, Laya):
  - request: `POST {endpoint}/v1/systemone/batch` with
    `{"states": [w1, …, wn], "questions": {"pii": Q}, "model"?: m}`;
  - response: `{"results": [{"answers": {"pii": {"type": "noul", "noul": p}}}, …]}`.
- `single` mode (strands-decider, which has no batch route):
  - request: `POST {endpoint}/v1/systemone` with
    `{"state": w, "questions": {"pii": Q}, "model"?: m}` for each window;
  - at most `concurrency` requests run at once, under the shared deadline.

**Options.** Each one is validated at boot. An unknown option is an error.

| Option | Default | Bounds | Why |
|---|---|---|---|
| `instructions` | the trained statement: "Does this text contain personal information about a specific person, or a secret credential?" | 1–512 chars | A different statement asks a different question. |
| `criteria` | absent | `true` and `false` strings, 1–512 chars each | strands-decider calibration collapses without the training criteria. |
| `model` | absent | `[a-z0-9._-]{1,64}` | Laya routes Latin text to its English checkpoint unless `model` names the fine-tuned one. |
| `batch` | `true` | bool | |
| `concurrency` | 4 | 1–16 | single mode only |
| `window_runes` | 1000 | 200–4000 | Matches the training length distribution, and stays under 1,024 tokens for Korean at p95. |
| `window_overlap_runes` | 150 | 0 to `window_runes`/2 | |
| `max_windows` | 64 | 1–64 | 64 is Laya's batch-state cap. |
| `abstain_band` | absent | `{"low": l, "high": h}` with 0 < l < `threshold` < h < 1 | |

The common `threshold` field defaults to 0.5 and must lie in (0,1).

**Windowing.** `Windows` cuts the request text: all segments, cached or not,
non-empty ones joined by a blank line in body order (amendment 9, after the
second and third commit security reviews, 2026-10-16):
- Consecutive windows share exactly `window_overlap_runes` runes, and each
  window holds at most `window_runes`. Every stretch of at most
  `window_overlap_runes` runes of the request text therefore lies wholly
  inside some window: two parts that close together are always read together,
  whatever the segment sizes and wherever the edges fall, natural or crafted.
- A window ends, in order of preference: at the end of the first segment
  whose keyed hash is divisible by 16, once the window holds at least twice
  the overlap; otherwise at the last segment end that fits with at least
  twice the overlap; otherwise at the last newline in its final fifth;
  otherwise at `window_runes`, never inside a UTF-8 sequence.
- The hash is `hash/maphash`, seeded randomly per classifier instance, once
  per generation, so boundary segments cannot be computed offline. It is not
  a cryptographic hash; the overlap guarantee does not depend on it.
- Edges at boundary segments depend on content, not position: text inserted
  earlier in a request changes the windows only up to the next such edge, and
  every window but the last stays identical when a turn is appended, so their
  cache entries keep hitting.
- A window ends at a segment only after taking in at least
  `window_overlap_runes` new runes, so the windows' total text stays within
  about three times the request text (measured: twice, for 262,144 one-rune
  boundary segments).
- When more than `max_windows` uncached windows remain, `Score` reads the
  first `max_windows` and returns Abstain for the rest without sending them.
  A request with unread windows is never Clear (PR-08). The read clear windows
  are cached, so a later request has less left to read.

**Verdict.**
- Each window is judged first:
  - with a configured band, `low < p < high` is Abstain;
  - otherwise `p >= threshold` is Flagged;
  - anything else is Clear.
- The request verdict combines the windows: any Flagged window gives Flagged;
  otherwise any Abstain or an unread remainder gives Abstain; otherwise Clear.
- The following are errors, never verdicts:
  - a non-2xx status;
  - a body that is not strict JSON, or that exceeds 256 KiB;
  - a result count that differs from the window count;
  - a missing or non-`noul` answer;
  - a probability outside [0,1] or not finite.

  An error caches nothing.

**Hygiene.**
- Uses stdlib `net/http`, with `Proxy: nil` and no redirect following, so
  request text never travels through an environment proxy or to another host
  (amendment 5).
- Sends `Authorization: Bearer` only when an `api_key` ref is configured.
- Error values carry a bounded class only: `timeout`, `transport`, `status`
  or `decode`. They never carry request text, the response body or the
  endpoint's credentials.
- Window text is built per call and is not retained after `Score` returns.

### 5. Configuration

```json
"pii_classification": {
  "timeout_ms": 3000,
  "max_input_bytes": 2097152,
  "cache": {"max_entries": 100000, "ttl_seconds": 3600},
  "classifiers": [
    {"name": "laya-ko", "type": "systemone", "mode": "Shadow",
     "revision": "laya-ko-pii-v3s1", "teams": ["pilot-ko"],
     "endpoint": "http://127.0.0.1:8095", "data_boundary": "internal",
     "threshold": 0.5,
     "options": {"model": "multilingual", "max_windows": 4,
                 "abstain_band": {"low": 0.1, "high": 0.9}}}
  ]
}
```

This is the sample's CPU profile. The defaults are `timeout_ms` 500 and
`max_windows` 64, which assume a GPU endpoint.

Boot validation fails on any of these:
- an unknown `type` (the error lists `classifier.Types()`);
- a name outside `[a-z0-9-]{1,32}`, or a duplicate name;
- a `revision` outside `[a-z0-9._-]{1,32}`;
- more than 4 classifiers;
- a `data_boundary` other than `internal`;
  - the field is required, so a future `external` value never changes the
    meaning of an existing file;
  - `external` is rejected as unsupported in this version;
- an endpoint that is not an absolute `http(s)` URL, or that carries
  userinfo, a query or a fragment;
- an inline `api_key`; the token is `api_key_ref: {"env": NAME}` or
  `{"file": "/abs"}`, the repository's `SecretRef` convention (amendment 2);
- any unknown field in the block, which is decoded strictly (amendment 3);
- an invalid `mode`;
- a threshold outside (0,1);
- `timeout_ms` outside 1–30000;
- `max_input_bytes` outside 1 KiB–16 MiB;
- `cache.max_entries` outside 0–1,000,000, or `cache.ttl_seconds` outside
  60–86,400;
- a plugin option error.

Further behavior:
- Without `teams`, the entry applies to every team.
- A SIGHUP reload rebuilds the Set, with a new cache and a new HMAC key, along
  with the new `live.State`. A failed validation keeps the current generation,
  which is the existing never-fatal reload posture.
- Enabling any classifier logs a warning that names the endpoints receiving
  request text.
- Classifiers are node-local file config, including on a gateway attached to
  a control plane. Control-plane distribution is out of scope.

### 6. Routing integration

The umbrella's single-phase §5, with the scope of §2 above and one
simplification. `RouteRequest` reads
`in.State.Classifiers.For(in.Principal.Team)` itself, so ingress handlers do
not change and the generation always matches.

**When it runs.** Classification runs after the regex inspection and before the
rule loop, only when all of the following hold:
- a `sensitiveData` rule applies (`hasPrivacy`);
- the request is not `CountOnly`, which keeps the count_tokens HTTP-200
  invariant and adds no network dependency to counts. A count still applies
  the rule's `onUninspectable` when the team has an Enforce entry, because the
  body was not read (Ruling 13, after the Task 8 review);
- the entry list is non-empty.

Teams without both a privacy rule and an entry pay nothing.

**Execution.**
- All entries run in parallel under one child context with `timeout_ms`.
- There are no retries.
- An entry still pending at the deadline is an error. Its completed windows
  are not cached, because they were not reported back.

**Combination.** Only Enforce entries count:
1. Any Enforce Flagged makes the request classified.
2. Otherwise, any Enforce Abstain or error makes it classifier-uninspectable.
3. Otherwise the request is clear.

This signal is router-local and is not added to `sensitivity.Result`. As a
result, Mask reinspection never sees it, and it can still mask regex spans in a
classified body.

**Mapping to rules.** The rule loop's trigger list gains two entries:

| Signal | Rule `onDetected` | Applied action |
|---|---|---|
| classified | InternalOnly or Block | that action |
| classified | Mask | `onUninspectable`, because the classifier gives Mask nothing to redact |
| classifier-uninspectable | any | `onUninspectable` |

- A classified request adds the bounded category `model_pii` to
  `Decision.Categories`. `boundedCategories` gains `model_pii`.
- Block precedence, the internal-model intersection, the remote-tool boundary
  refusal, rule accumulation and routing reasons are unchanged.

**Shadow is synchronous (proposed).** Shadow entries run in the same call as
Enforce entries and add the same latency. The reasons:
- the measured added latency is the latency Enforce would add;
- the routing record is complete when it is written;
- request text is never retained past the request.

Asynchronous Shadow would remove the latency. In exchange it needs a copy of
the content that outlives the request, a bounded queue that drops work, and a
second audit record that is linked to the first. It is deferred.

### 7. Observability

- **Audit.** `audit.RoutingRef` gains an append-only, `omitempty` field:
  `Classifiers []ClassifierRef`. Each entry is
  `{Name, Revision, Boundary, Mode, Result, WouldRestrict}`.
  - `Result` is one of `flagged`, `clear`, `abstain`, `error` or `skipped`.
  - `Boundary` is always `internal` in this version. It is recorded now so
    that adding `external` later does not change the record format.
  - `WouldRestrict` is set only in Shadow, and only when the result is
    `flagged`, `abstain` or `error`.
  - The list holds at most 4 entries.
  - The field never carries text, windows, probabilities, digests, endpoints
    or tokens.
  - Records without classifiers keep their exact bytes.
- **Metrics.** Labels are limited to config names, revisions and bounded
  enums, and never include key_id:
  - `inferplane_pii_classifier_decisions_total{classifier,revision,mode,result}`;
  - `inferplane_pii_classifier_duration_seconds{classifier,revision,result}`;
  - `inferplane_pii_classifier_windows{classifier}`, a histogram of windows
    sent per request. It is the sizing signal for choosing between CPU and
    GPU;
  - `inferplane_pii_classifier_cache_windows_total{classifier,result}`, with
    `result` one of `hit` or `miss`.

  Series for a replaced revision are deleted on a generation swap.

### 8. The Korean sample

**Layout:**

```
examples/config.pii-classifier-ko.json   a complete mayu config: Shadow, team pilot-ko, CPU profile
examples/helm.pii-classifier-ko.yaml     chart values that enable the Laya sidecar
examples/pii-classifier-ko/
  README.md, README.ko.md     rollout steps, CPU and GPU profiles, evidence, limits
  governance.yaml             GovernancePolicy: sensitiveData InternalOnly
                              to a self-hosted internal model, onUninspectable InternalOnly
  laya-serve/Dockerfile       the published CPU image
  laya-serve/requirements.txt hash-pinned
  laya-serve/SHA256SUMS       digests of the published weight files
  model-card.md               source of the Hugging Face model card
  training/                   data generation, SageMaker launch, evaluation
```

**Image** (`laya-serve/Dockerfile`):
- Python 3.12 slim base, pinned by digest.
- Dependencies installed with `--require-hashes`: `laya[serve]==0.4.6` and
  CPU torch wheels for both architectures.
- A build stage fetches the weights from the Hugging Face repository at a
  pinned commit (`WEIGHTS=hf`) or takes them from a local build context
  (`WEIGHTS=local`, for acceptance before publication). Both verify them
  against `SHA256SUMS` (amendment 6).
- Only safetensors, tokenizer and config files are copied into
  `/models/laya-ko-pii`. The optimizer state (`checkpoint_latest`, 647 MB) is
  never published.
- The image runs as non-root and is compatible with a read-only root
  filesystem (writable `/tmp` only).
- It starts with `HF_HUB_OFFLINE=1`, so it needs no egress. Verified on
  2026-10-16: `laya-serve` 0.4.6 loaded exactly the publishable file set
  (`model.safetensors`, `encoder/`, `tokenizer/`, `questions.json`,
  `rl_agent_config.json`) with an empty Hugging Face cache and the offline
  flags set. It attempted no download. A Korean positive probe scored 0.991,
  and a phone-masking function scored 0.004. The plan repeats this check
  inside the built image.
- Environment defaults:
  - `LAYA_HOST=127.0.0.1`, `LAYA_PORT=8095`, `LAYA_DEVICE=cpu`;
  - `LAYA_MODELS=multilingual`, `LAYA_DEFAULT_MODEL=multilingual`;
  - `LAYA_EXTRA_MODELS={"multilingual": "/models/laya-ko-pii"}`.
- Published as `ghcr.io/<org>/laya-ko-pii:v3s1-laya0.4.6` for linux/amd64 and
  linux/arm64, and referenced by digest. The weights and card go to
  `<org>/laya-ko-pii` on Hugging Face. `<org>` is confirmed at publication
  gate 4.
- A GPU variant is documented as a build argument (CUDA torch, amd64). It is
  not published.

**Helm:**
- New values `piiClassifier.laya.enabled` (default `false`), plus
  `image.repository` and `image.digest`, `resources` (requests 2 CPU and
  2 Gi), `threads`, `port` and an optional `apiKeySecret`.
- When enabled, the chart renders a second container in the gateway pod. The
  container:
  - binds `127.0.0.1`, with no Service port and no containerPort exposure;
  - has startup and readiness probes on `/health`, run inside the container
    because the server binds `127.0.0.1`, so the pod is not Ready, and
    receives no traffic, until the model has loaded;
  - runs non-root with a read-only root filesystem and an emptyDir `/tmp`.
- The chart does not edit the gateway config. `examples/helm.pii-classifier-ko.yaml`
  shows the matching `pii_classification` block (amendment 4).
- The GPU profile is documented as an in-namespace ClusterIP Service on a GPU
  node pool, with a NetworkPolicy that admits only gateway pods. It is never a
  LoadBalancer or a public endpoint. Its suggested settings are
  `max_windows: 64` and `timeout_ms` above the measured p95.

**Model card** (`model-card.md`, published to Hugging Face). It covers:
- **Lineage and licenses.** `jhu-clsp/mmBERT-base` (MIT) →
  `convaiinnovations/laya` multilingual (Apache-2.0) → this checkpoint
  (Apache-2.0, with a NOTICE that keeps the MIT attribution).
- **Intended use.** A routing signal for a gateway, not a guarantee of PII
  detection.
- **Required request settings.** `model: multilingual` and the exact
  `instructions` string.
- **Label policy.** The written policy, including rule B.
- **Training data.** Synthetic slot templates from Claude Sonnet 5.5 on Amazon
  Bedrock, filled with locally generated values. The data contains no real
  person's data and is not released.
- **Evaluation.** The independent set table above, the per-kind errors, seed
  variance, the CPU and GPU latency, and the statement that all evaluation
  data is synthetic and document-level.
- **Windowed-use limits.** The known limit from §3.
- **Revision.** `laya-ko-pii-v3s1`.

**Training recipe** (`training/`). It holds the generator, converter, launcher
and evaluation scripts from the experiment, with these changes:
- account IDs, bucket names and regions become parameters;
- data, virtual environments and model caches stay git-ignored.

The README gives the steps, the label policy and the measured GPU time. Laya
seed 1 trained in about 43 minutes on `ml.g6.2xlarge` (one NVIDIA L4).

**Publication gates.** Each one must pass before the first push to Hugging
Face or GHCR:
1. The organization completes its own review of the generation-model terms for
   training data produced with Claude Sonnet 5.5 on Amazon Bedrock.
   inferplane makes no legal determination. The independent evaluation set,
   which other providers' models wrote, was used only for evaluation and is not
   released.
2. The license files are present: Apache-2.0, plus a NOTICE with the
   mmBERT-base MIT attribution.
3. The model card has been reviewed.
4. The user explicitly confirms each publication, including the organization
   and repository names. Creating a Hugging Face repository and pushing an
   image are separate confirmations.

## Error handling summary

| Condition | Shadow | Enforce |
|---|---|---|
| Flagged | recorded, `WouldRestrict` | rule `onDetected`; Mask becomes `onUninspectable` |
| Abstain (band, unread remainder over `max_windows`, `max_input_bytes`) | recorded, `WouldRestrict` | `onUninspectable` |
| Error (timeout, transport, status, decode, extraction) | recorded, `WouldRestrict` | `onUninspectable` |
| Clear, including all segments from cache | recorded | none |
| Skipped (nothing extracted) | recorded | none |
| No privacy rule, CountOnly, or no entries | not run | not run |

## Testing

- **Registry and Set.** A duplicate registration panics. An unknown type fails
  the boot. `For(team)` scopes entries, including on a nil Set.
- **Extraction.**
  - The extracted segments equal the strings the regex walker visits, for all
    four ingress formats.
  - Segments include system and developer input, tool definitions and schemas,
    tool calls, tool results and assistant history.
  - The `max_input_bytes` cap gives Abstain with no calls.
- **Verdict cache:**
  - a second identical request makes zero classifier calls and is Clear;
  - Flagged, Abstain and error results are never cached;
  - a different team, entry, settings digest or applicable policy generation
    misses;
  - a reload or a revision change misses;
  - TTL expiry and LRU eviction lead to classification again;
  - `max_entries: 0` disables the cache;
  - no cache key or value is derivable without the per-generation HMAC key;
  - a window is cached only when its own verdict was Clear, and parts cleared
    separately never clear their combination (amendment 7);
  - windows stay identical after a turn is appended, and nearly all survive an
    insertion earlier in the request.
- **systemone plugin**, against `httptest` fakes of the batch and single wires:
  - threshold and band boundaries, including equality at `threshold`, `low`
    and `high`;
  - window packing across segments, splitting on a newline or a rune boundary,
    overlap, and multi-byte Korean text cut at every offset;
  - above `max_windows`: exactly `max_windows` windows sent, the unsent ones
    are Abstain, and only the read clear windows are cached;
  - the request carries `instructions`, `criteria` and `model` verbatim when
    configured, and omits them when not;
  - result count mismatch, non-`noul` answers, NaN, values outside [0,1],
    malformed JSON, a body over the size cap, non-2xx, timeout, and the
    concurrency bound in single mode;
  - a leak test: no request text and no token appears in any error or log.
- **PR-08 completion conditions** (router-level tests with a fake classifier):
  - a request with a name and address in an earlier turn and none in the new
    question is protected;
  - forged assistant history is read like any other content, and a cached
    verdict for the same text is identical regardless of its role;
  - a long tool result over the window cap restricts in Enforce and never
    returns Clear;
  - content in system and tool-schema positions is classified;
  - an entity across a window cut is read whole when shorter than the
    overlap;
  - a policy change, a reload, a restart and an eviction each lead to
    classification again;
  - unknown, timeout, abstain and over-limit apply the rule's restriction in
    Enforce;
  - count requests make no classifier call and stay HTTP 200;
  - Block precedence, the internal-model intersection, Mask combined with
    InternalOnly, and the remote-tool boundary refusal are preserved.
- **Combination.** Any Enforce Flagged wins. Enforce error or abstain gives
  uninspectable. Shadow never changes routing. The shared deadline cancels
  pending calls.
- **Config.** Every validation rule above. A rejected reload keeps the current
  Set.
- **Audit and metrics.**
  - `ClassifierRef` fields are present, the revision is omitted when unset,
    and records without classifiers keep their exact bytes.
  - Metric labels are bounded and carry no key_id.
  - Series are deleted on a revision swap.
- **Sample.**
  - A `cmd/mayu` test loads `examples/pii-classifier-ko/config.json` and its
    policy against a fake endpoint, as the existing example tests do.
  - A chart render test shows the sidecar off by default, `127.0.0.1` binding
    when on, and no Service port.
- **Acceptance (manual, recorded in the PR).**
  - Run the published image next to `mayu` locally.
  - Send a multi-turn Korean conversation as the pilot team. Confirm that the
    audit record shows `ClassifierRef`, that the cache hit metric rises on
    later turns, and that the window histogram moves.
  - Switch to Enforce and confirm that a flagged request routes only to the
    internal model.
- **Gates.** `go test ./... -race`, `go vet ./...`, `gofmt -l .`,
  `CGO_ENABLED=0` builds of both binaries, and `bash tests/run-all.sh`.

## Rollout gates (operational)

1. Run Shadow on a pilot team. Compare `WouldRestrict` against a labeled
   sample of that team's traffic, which needs review outside inferplane
   because audit holds no text.
2. Declare and fix the PR-09 gate values before Enforce, for each sensitive
   type separately:
   - false-negative rate, including a cross-turn class (a name in one turn and
     an attribute in another);
   - false-positive and abstention rates on normal coding requests;
   - p95 added latency and resource use per deployment profile;
   - the fallback destination and availability contract during a classifier
     outage.
3. Watch `inferplane_pii_classifier_windows` and the cache hit ratio. A cold
   cache abstains on long first requests, and every gateway process warms its
   own cache. If requests keep exceeding `max_windows` after warm-up, move to
   the GPU profile before drawing accuracy conclusions.
4. Promote to Enforce only after the declared gates are met. Set `timeout_ms`
   above the measured p95. A timeout in Enforce fails closed.
5. A retrained checkpoint gets a new `revision`, starts in Shadow again, and
   carries no evidence over from the previous revision.

## Out of scope

- Everything listed as deferred under Scope.
- Classifier-driven span masking, output-side scanning, and any cache of
  flagged verdicts.
- Publishing the training or evaluation data, or the strands-decider weights.
- Policy schema or CRD changes.

## Documentation (same change as the implementation)

- `docs/reference/security.md` and `docs/reference/agent-llm.md`, each with
  its `.ko.md` copy.
- `docs/policy-routing.md` and its `.ko.md` copy.
- The "PII dedicated model" row in `docs/adaptive-routing.md`.
- The matching `internal/` and `plugins/` `CLAUDE.md` files.
- The chart values comments.
- `CHANGELOG.md`.
- The umbrella records that it needs an ADR amending ADR-044's "no network
  dependency" wording. ADR-049 is now taken, so it becomes ADR-052. It is
  written only when requested.

## Changes to the umbrella spec (same commit as this spec)

- The status line links this sub-project.
- The superseded-in-part note records that this sub-project settles the
  inspection scope as all retransmitted content, with a verdict cache.
- The Korean row is replaced: fine-tuned Laya and strands-decider detect
  synthetic Korean personal data (independent-set F1 0.95–0.96). Real-traffic
  accuracy is a Shadow gate.
- The reference to ADR-049 becomes ADR-052.
