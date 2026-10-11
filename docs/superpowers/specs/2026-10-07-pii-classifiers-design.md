# Pluggable PII classifiers

Status: design approved in the 2026-10-07/11 brainstorming conversation. The
spec was revised to a pluggable classifier-provider structure and is awaiting
review. No implementation is authorized yet.

## Product goal

The existing "PII dedicated model" path (`sensitiveData.onDetected: InternalOnly`
plus explicit `internalModels`) is only as good as its trigger. Today the trigger
is a finite regex/validator set: email, phone, Luhn card, US SSN, IPv4 and Korean
RRN. It misses names, addresses, account and health data, and credentials
written in prose. Korean text matters especially.

This change adds **pluggable PII classifiers**, modeled on how LiteLLM
`guardrails` select a backend type in config. A classifier is a service attached
through a typed plugin. It can be self-hosted, or, when the operator's
organization permits it, an external API. Initial types:

- `systemone`: a typed decision model behind the `/v1/systemone` protocol. This
  covers self-hosted Laya (Apache-2.0) and TypeSafe's hosted Jev API.
- `presidio`: a Microsoft Presidio Analyzer (MIT). The operator can configure
  language-specific recognizers and NLP models, Korean included.
- `generic`: a small inferplane-defined HTTP contract. Any in-house model can be
  wrapped behind it, such as a Korean NER model or a small internal LLM.

Classifier results restrict routing through the SAME `sensitiveData` rules. They
never loosen a route, mutate the body or replace the regex detectors.
Per-classifier accuracy, false-positive cost and p95 latency are rollout gates
measured in Shadow mode. This implementation does not establish them as benefits.

## Decisions taken in the conversation

| Question | Decision |
|---|---|
| Backend | Internal and external backends are both supported. Whether new input may be sent to an external classifier depends on each organization's policy, so it is an explicit operator setting (`allowed_boundaries`). The default is internal only. |
| External safety | External classifiers run only on requests that local signals have left clear (local-first), over TLS only, and every audit record names the classifier boundary. |
| Shape | LiteLLM-style typed classifier plugins, several at once, each scoped and moded independently. |
| Korean | Not solved by Laya, whose multilingual accuracy is well below English. Covered by adding a `presidio` (Korean recognizers) or `generic` classifier. |
| Inspection scope | New input only: user text and tool results after the last assistant turn. |
| Enforcement | Per-classifier `mode: Shadow | Enforce`. Shadow records only. |
| Policy schema | Unchanged. There is no new rule type and no CRD change. |
| Backend versioning | An optional operator `revision` label on each classifier is recorded in audit and metrics. |
| Console registration | Classifiers can be registered from the mayu admin console using the ADR-008 pattern. The console can register only boundaries already allowed by the file setting. It cannot widen `allowed_boundaries`. |

## Differences from LiteLLM guardrails (intentional)

- **Plugin loading.** LiteLLM loads custom Python classes at runtime. inferplane
  is a static Go binary, so it uses compiled `plugins/<type>` packages plus the
  `generic` HTTP contract. This is the same isolation model as providers
  (ADR-009).
- **Failure.** Guardrails can be fail-open. A classifier that errors, times out
  or abstains in Enforce mode applies the rule's `onUninspectable`, so it fails
  closed.
- **Restriction only.** Classifiers produce a restriction signal and nothing
  else. There is no silent masking (ADR-009 rejected LiteLLM-style silent
  masking).
- **Scope.** Pre-call only. There is no during-call or post-call output scanning
  in v1.
- **Boundary.** Every classifier declares `data_boundary: internal | external`.
  - External use is off by default. Turning it on is a file-level decision.
  - Local signals gate external calls: text that local detection already found
    sensitive is never sent out for a second opinion.
- **Audit.** Records hold bounded result enums only. They never hold raw text,
  entity values or scores.

## Existing behavior and constraints

- `internal/sensitivity` runs regex inspection over the whole body. Rule
  evaluation is in `internal/router/request_routing.go:255-296`:
  - `len(Categories) > 0` triggers `onDetected` (InternalOnly, Block or Mask);
  - `!Complete` triggers `onUninspectable` (InternalOnly or Block);
  - Block wins, and all matching rules accumulate.
- Mask runs the redactor, then reinspection. The output must be Complete with
  zero categories, otherwise the result is `mask_failed`.
- There is no existing seam for this:
  - `filter.RequestFilter` is text-transform only. Filters register in `init()`
    without config (`plugins/piimask/piimask.go:118`), and they are not
    connected to routing.
  - `Router.SetRequestInspector` (`internal/router/router.go:146`) replaces the
    regex inspector wholesale. It is global, has no config and has no production
    caller.
- Go 1.25, pure-Go dependencies, `CGO_ENABLED=0`. Classifier backends (PyTorch,
  ONNX, spaCy) run out of process.
- ADR-043/044 and the 2026-09-09 spec (line 72) say there is no remote
  classifier and no network dependency, and that learned classifiers "require a
  separate design". This document is that design. It needs a new ADR, proposed
  as ADR-049, which is written only when requested.

## Design

### 1. Seam: new leaf package `internal/classifier`

```go
type Verdict int // Clear | Flagged | Abstain

type Input struct {
    Texts []string // new-input segments; borrowed, never retained
}

type Classifier interface {
    Classify(ctx context.Context, in Input) (Verdict, error)
}

type Factory func(Config) (Classifier, error)
func Register(typ string, f Factory) // called from plugin init(); duplicate panics
func Types() []string
```

- `Config` carries the typed common fields (endpoint, auth ref, threshold,
  language) and an `Options map[string]json.RawMessage` for type-specific
  settings. Plugins validate their own options at boot.
- Core packages (`router`, `server`) import only this package. Each concrete
  type lives in `plugins/<type>/` and is blank-imported in `cmd/mayu/main.go`.
  A new classifier type therefore needs zero core diff.
- `classifier.Set` is the resolved, per-team view built at assembly. It is
  nil-safe in the same way as `filter.Masking`:

```go
type Entry struct { Name string; Mode Mode; Classifier Classifier; Global bool; Teams map[string]bool }
type Set struct { Entries []Entry; Timeout time.Duration; MaxInputBytes int }
func (s *Set) For(team string) []Entry
```

### 2. Plugins

All plugins use stdlib `net/http`. Each one:
- reads the response under a size cap and decodes it strictly;
- never wraps request text or the response body into an error;
- uses bearer auth through `secret:` / `env:` / `file:` refs.

| Type | Wire | Verdict rule |
|---|---|---|
| `systemone` (`plugins/systemone`) | `POST /v1/systemone/batch`, one `noul` statement per window of about 400 tokens. The statement is configurable, and so is the window cap `max_windows`. | Any window with `P(true) >= threshold` is Flagged. A backend abstention, or a missing or invalid probability, is Abstain. Exceeding `max_windows` is Abstain. |
| `presidio` (`plugins/presidio`) | `POST /analyze` with `{text, language, score_threshold, entities?}` per segment. `language` and the optional `entities` allow-list come from config. | Any returned entity with `score >= threshold` is Flagged. An error response, or an unsupported-language response, is an error. |
| `generic` (`plugins/genericclassifier`) | `POST {endpoint}` with `{"version":"v1","texts":[...],"language":"ko"}`. The response is `{"verdict":"flagged"|"clear"|"abstain"}`. | Taken as returned. An unknown verdict or a malformed response is an error. |

- Presidio returns entity spans, but v1 uses them only for the verdict. The
  spans are discarded and never logged. Feeding spans to Mask is a possible
  later extension and is out of scope here.
- Korean coverage through `presidio` depends on the operator deploying an
  Analyzer that has Korean NLP and recognizer configuration. The plugin only
  passes `language`. It does not ship models.
- The `generic` contract is documented in `docs/reference/api.md` as an
  inferplane integration interface, versioned by `version`.

### 3. Configuration (new top-level key)

```json
"pii_classification": {
  "timeout_ms": 500,
  "max_input_bytes": 262144,
  "allowed_boundaries": ["internal", "external"],
  "classifiers": [
    {"name": "laya-ko", "type": "systemone", "mode": "Shadow", "revision": "ko-ft-2026-10",
     "endpoint": "http://127.0.0.1:8095", "data_boundary": "internal",
     "api_key": "secret:laya-token", "threshold": 0.5,
     "options": {"max_windows": 16}},
    {"name": "ko-pii", "type": "presidio", "mode": "Shadow", "teams": ["kr-dev"],
     "endpoint": "http://127.0.0.1:3000", "data_boundary": "internal",
     "threshold": 0.6, "language": "ko"},
    {"name": "inhouse", "type": "generic", "mode": "Shadow",
     "endpoint": "http://pii-svc.internal:8080/classify", "data_boundary": "internal",
     "api_key": "secret:pii-svc-token", "language": "ko"},
    {"name": "jev", "type": "systemone", "mode": "Shadow", "teams": ["pilot"],
     "endpoint": "https://<jev-api-host>", "data_boundary": "external",
     "api_key": "secret:jev-token", "threshold": 0.5}
  ]
}
```

`allowed_boundaries` is the organization's decision about where new input may be
sent for classification:
- It is absent by default, which means `["internal"]`.
- Adding `external` is the explicit opt-in. Only the config file can set it.
  Console writes and Git-export imports cannot set it.
- inferplane does not judge whether an external processor is acceptable.
  Contracts, data-processing agreements and residency are the operator's
  responsibility.

Boot validation fails the boot on:
- an unknown `type` (the error lists `classifier.Types()`);
- a name that does not match `[a-z0-9-]{1,32}` or is duplicated;
- a `revision` that does not match `[a-z0-9._-]{1,32}`;
- more than 4 classifiers;
- a `data_boundary` that is empty, `unknown`, or not in `allowed_boundaries`.
  An unattested endpoint is never used;
- an `external` classifier whose endpoint is not `https://`, or that has no
  `api_key` ref;
- an inline secret;
- an invalid `mode`;
- a threshold outside `(0,1)` for types that use one;
- a non-positive or oversized `timeout_ms` or `max_input_bytes`;
- type-specific option errors.

`revision` is optional. It is an operator-declared label for the deployed
backend version, such as a fine-tuned checkpoint, a recognizer set or an
in-house model release. inferplane does not verify it against the backend; it
is an attestation, like `data_boundary`. Its purpose is to keep Shadow and
Enforce evidence attributable when a backend is retrained under the same
classifier name. Like any other classifier setting, it changes only through a
validated generation swap: a SIGHUP reload or a console write (§7).

Enabling any classifier logs a warning that request text is sent to the named
endpoints. When any external classifier is enabled, a separate, louder warning
names those classifiers and their teams. The key is absent by default, which
means zero overhead.

### 4. Deployment

- Helm gains opt-in sidecar or in-namespace Service values for Laya
  (`laya-serve`) and Presidio Analyzer. Model weights come from an image or a
  PVC.
- There is no central shared classifier dependency by default, so the
  adaptive-routing "Availability" contract still holds. An operator may point
  `generic` at an in-cluster Service. That Service is then their availability
  boundary.
- External classifiers are an egress dependency. Their availability and latency
  are outside the operator's cluster. A failure in Enforce mode fails closed, so
  an outage of the external API restricts the scoped teams' requests.
- ADR-049 amends ADR-044's "no network dependency" wording to cover opt-in
  internal classifiers and the opt-in external boundary.

### 5. Routing integration

**Where it runs.** `RequestRoutingInput` gains `Classifiers []classifier.Entry`.
The ingress supplies it through `classifierSet.For(team)`, the same way as
`Redactor`. Classification runs inside `RouteRequest` after the regex
inspection, and only when all of these hold:
- a `sensitiveData` rule applies (`hasPrivacy`);
- `!CountOnly` (this protects the count_tokens HTTP-200 invariant);
- the list is non-empty.

Teams without privacy rules or classifiers pay no latency.

**Input: new input only.** One shared extractor reuses the `context.go` walker
and `instructionText`. It collects the user text and tool-result text after
the last assistant turn, for anthropic, openai, responses and bedrock.
- More than `max_input_bytes` of new input counts as Abstain for every
  classifier.
- Empty new input is `skipped` with no calls.
- The regex Inspector still covers the whole body.

**Execution (local-first).** All phases share one child context with
`timeout_ms`. There are no retries. A timeout counts as an error for every
entry still pending.

1. **Phase 1.** All `internal` entries are called in parallel.
2. **Phase 2.** `external` entries are called in parallel, but only when local
   signals leave the request clear:
   - the regex inspection found no categories and the request is Complete;
   - no Enforce internal entry returned Flagged, Abstain or an error.

   Otherwise every external entry is recorded as `skipped`. Sending text that
   is already known or suspected to be sensitive to an external party adds no
   restriction, because the request is already restricted, and it only adds
   exposure.

Shadow internal entries never gate phase 2, so Shadow cannot change egress.
Without any external entry, there is a single phase and no added latency.

**Combination.** Only Enforce entries count:
1. Any Enforce entry returns Flagged: the request is `Classified`.
2. Otherwise, any Enforce entry returns Abstain or an error: the request is
   classifier-uninspectable.
3. Otherwise the request is clear.

Shadow entries are recorded and never affect routing.

**Mapping to rules.** `sensitivity.Result` gains `Classified` and
`ClassifierUninspectable` bools. The rule loop adds two triggers:

| Signal | Rule `onDetected` | Applied action |
|---|---|---|
| Classified | InternalOnly / Block | That action |
| Classified | Mask | `onUninspectable`, because a classifier provides nothing that Mask can redact |
| ClassifierUninspectable | any | `onUninspectable` (fail closed) |
| clear / skipped | any | none (only the regex results apply) |

- `Classified` adds the bounded category `model_pii` to `Decision.Categories`.
- Redactor reinspection ignores `model_pii`, so it can still mask regex spans in
  a body that also carries a classifier flag. The classifier's InternalOnly or
  Block obligation still applies.
- Block precedence and rule accumulation are unchanged.
- There are no new routing reasons. `internal_only`, `sensitive_blocked` and
  `inspection_failed` cover every outcome.

**Generations.** The resolved `classifier.Set` lives in `live.State`, next to the
provider topology. A request captures one State, so the classifiers it uses
come from the same generation as its routes. The Set changes only through a
validated swap: either a SIGHUP reload, or a console write (§7).

### 6. Observability

- `audit.RoutingRef` gains an append-only, `omitempty` field
  `Classifiers []ClassifierRef{Name, Revision, Boundary, Mode, Result, WouldRestrict}`.
  - `Boundary` is `internal` or `external`. It lets an auditor tell, from the
    record alone, whether new input left the organization for classification.
    `skipped` means the input was not sent.
  - `Revision` is omitted when it is not configured.
  - `Result` is one of `flagged`, `clear`, `abstain`, `error` or `skipped`.
  - `WouldRestrict` is set only in Shadow mode.
  - The list is bounded at 4 entries, and names are operator config.
  - The record never contains text, entity values, spans, scores or endpoints.
  - Existing records keep their exact bytes.
- Metrics:
  - `inferplane_pii_classifier_decisions_total{classifier,revision,mode,result}`
  - `inferplane_pii_classifier_duration_seconds{classifier,revision,result}`
  - Label values are limited to the validated config names, the revisions and
    the bounded enums, and never include key_id.
  - A process carries only one revision per classifier, so the number of series
    per generation stays fixed. Series for replaced revisions are deleted from
    the collector on swap, and their history ages out of Prometheus retention.
  - An unset revision is exported as the empty label value.
- `boundedCategories` gains `model_pii`.

### 7. Console registration (ADR-008 pattern)

**Store.** When `provider_store` is configured, classifiers become
DB-authoritative.
- The store gains a `classifiers` table with TEXT/INTEGER columns and
  Postgres-portable DDL. Each row holds `name`, `type`, `mode`, `revision`,
  `endpoint`, `data_boundary`, `api_key_ref`, `threshold`, `language`, `teams`
  and `options_json`.
- **No column can hold a secret.**
- On first boot, the file's `pii_classification.classifiers` are seeded once,
  under the existing durable `meta` marker transaction, keyed separately as
  `classifiers_seeded`.
- After seeding, the DB is authoritative. A divergence from the file is logged.
- `timeout_ms` and `max_input_bytes` stay file-sourced.
- Without a store, the file is the only source, and the write endpoints return
  `405`. This is the same posture as ADR-008.

**Admin API.** The endpoints sit behind the existing `AdminAuth`, reuse
`configapi.WriteHandler`, and take the same `reloadMu`:
- `GET /admin/classifiers` lists classifiers as a secret-free view with refs
  only.
- `PUT /admin/classifiers/{name}` and `DELETE /admin/classifiers/{name}`
  register, replace and delete.
  - Each write is validate → persist → swap. It builds a candidate `live.State`
    with the same boot validation as §3, including the `allowed_boundaries`
    check and the plugin type's option validation. The console can register
    an `external` classifier only when the file already allows `external`.
  - On failure the response is a sanitized `400`, and the DB is left untouched.
  - The DTO has no secret field. An inline `api_key` is a `400`.
  - Ref-shape validation follows ADR-008: an env ref must match
    `^[A-Za-z_][A-Za-z0-9_]*$`, and a file ref must be an absolute path that
    resolves.
- `POST /admin/classifiers/{name}/probe` sends a fixed, built-in synthetic text
  and reports `ok` or a sanitized error class, plus the latency.
  - It never takes caller text, so the console cannot be used to send arbitrary
    content to an endpoint.
  - The probe result is a connectivity check, not accuracy evidence.

**Mode safety.**
- A new registration is created in `Shadow` regardless of the submitted mode.
- Promotion to `Enforce` is a separate explicit `PUT`.
- Changing `endpoint`, `type` or `revision` demotes the classifier to `Shadow`
  unless the request explicitly re-states `mode: Enforce`. This keeps evidence
  attributable, as described under rollout gate 4.

**Audit.** Writes are secret-free admin-action events:
`classifier_registered`, `classifier_updated`, `classifier_deleted` and
`classifier_mode_changed`. They carry the name, type, mode, revision and
boundary. They never carry the endpoint secret, and never carry probe text.

**Export.** `GET /admin/config/export` includes `pii_classification.classifiers`
as a secret-free fragment, so the configuration can be reviewed in Git.

**Console.** The mayu admin console gains a "PII classifiers" tab with:
- a list showing name, type, mode, revision, boundary, teams and the last probe
  result;
- register and edit forms. The type picker comes from `classifier.Types()`, and
  the type-specific options form is driven by a static schema per type;
- a probe button and a Shadow/Enforce toggle with a confirmation step;
- an `external` badge on external classifiers. Registering one, or promoting it
  to Enforce, requires a second confirmation that names the endpoint host and
  the scoped teams. The boundary picker offers only the boundaries in
  `allowed_boundaries`.

The form collects ref names, never secret values. It follows the ADR-001 CSP
discipline: `default-src 'self'`, no inline handlers or styles, values set via
DOM properties, and the admin token kept in memory only.

**Fleet note.** As with ADR-008, console writes change only the node, or the
shared store, that they reach. In control-plane-attached fleets, operators
should manage classifiers through the file or Git export until control-plane
distribution exists. That distribution is out of scope here.

## Testing

- **Registry.** Duplicate registration panics. An unknown type fails the boot.
  `For(team)` scopes entries correctly, including a nil Set.
- **Each plugin**, against an `httptest` fake of its wire:
  - verdict and threshold boundaries;
  - abstention (systemone) and unsupported language (presidio);
  - malformed JSON, an oversized body, non-2xx responses and a timeout;
  - a leak test: no request text appears in errors.
- **Combination:**
  - any Enforce Flagged wins;
  - an Enforce error or abstain gives uninspectable;
  - Shadow entries never change routing;
  - a shared timeout cancels pending calls.
- **Local-first external gating:**
  - external entries are `skipped`, and the fake server receives zero calls,
    when regex finds categories, when the request is incomplete, or when an
    Enforce internal entry returns flagged, abstain or error;
  - external entries are called when every local signal is clear;
  - Shadow internal entries never gate phase 2;
  - with no external entries there is a single phase;
  - `Boundary` is recorded on every `ClassifierRef`.
- **External config:**
  - `external` is rejected unless it is in `allowed_boundaries`;
  - external `http://` endpoints and external entries without `api_key` are
    rejected;
  - the louder boot warning is emitted.
- **Router table tests:**
  - every signal × action pair, including the Mask→onUninspectable escalation;
  - no call when the request is CountOnly, has no privacy rule, or has an empty
    list;
  - Block precedence across rules;
  - a mixed regex PII plus classifier flag under a Mask rule.
- **New-input extraction.** The boundary after the last assistant turn for all
  four protocols, plus the `max_input_bytes` cap.
- **Config.** Every boot-validation rule listed above.
- **Console and store:**
  - seeding happens exactly once under the marker, and a deleted classifier is
    not resurrected;
  - writes return `405` without a store;
  - inline secrets and pasted-secret-as-ref inputs are rejected with sanitized
    errors;
  - a boundary outside `allowed_boundaries` returns `400` with the DB
    untouched, and no console write can change `allowed_boundaries`;
  - validate→persist→swap produces one generation, and no request sees a
    half-applied Set;
  - a new registration is forced to Shadow, and a change to endpoint or
    revision demotes to Shadow;
  - probe uses only the fixed synthetic text, and its errors are sanitized;
  - admin audit events are emitted and are secret-free;
  - export round-trips;
  - console JS passes the existing CSP tests in `internal/server/adminui`.
- **Revision.**
  - A configured revision appears in `ClassifierRef` and in the metric labels.
  - An unset revision is omitted from audit.
  - Records without `Classifiers` keep their exact bytes.
- **Gates.** `go test ./... -race`, `go vet ./...`, `gofmt -l .`, and
  `CGO_ENABLED=0` builds of both binaries.

## Rollout gates (operational, not unit-tested)

1. Run each classifier in Shadow on scoped teams. Compare `WouldRestrict`
   against a labeled sample that includes Korean traffic, separately for each
   classifier.
2. Promote a classifier to Enforce only after it meets the agreed
   false-positive rate, false-negative rate and p95 added latency.
3. If a classifier falls short, tune it (Laya RLCD fine-tuning, Presidio
   recognizers or the in-house model) or keep it in Shadow.
5. **External classifiers.** Before enabling one, even in Shadow, the
   organization completes its own review of the external processor: contract
   or data-processing agreement, residency and retention. Shadow still sends
   text out, so Shadow is not a pre-approval stage for external backends.
4. Retraining the backend means a new `revision`. A new revision starts again in
   Shadow, and its evidence is compared against the previous revision before it
   returns to Enforce. Evidence from one revision is never carried over to
   another.

### Laya fine-tuning notes (Korean)

These notes are operator guidance and are not implemented by inferplane:

- **Starting checkpoint.** Start from `laya-multilingual` (mmBERT-base, 1,024
  tokens). Training uses `laya.train.finetune` with JSONL
  `{state, questions, answers}`, using the same `noul` statement that the
  plugin sends. Laya's README reports no Korean fine-tuning results, so the
  benefit is measured only through the Shadow gates above.
- **Training data.** inferplane audit never stores request text, so production
  traffic is not a training source. Use synthetic Korean coding conversations
  that contain fake identifiers.
- **Negative examples.** Include enough PII-shaped non-PII to control false
  positives on coding traffic: UUIDs, hashes, test fixtures, `example.com`
  addresses and sample phone numbers.
- **What fine-tuning cannot change.** Fine-tuning does not add span output and
  does not lift the context window limit.

## Out of scope

- Classifier-driven span masking.
- Output-side (post-call) scanning.
- Verdict caching.
- Control-plane (`inferplaned`) distribution of classifier config.
- Sending already-flagged or masked text to external classifiers.
- Any per-request or per-policy override of `allowed_boundaries`.
- Runtime-loaded plugins.
- Changes to the policy schema or CRD.

## Documentation (same change as implementation)

- `docs/reference/security.md`, `agent-llm.md`, `api.md` (the generic contract
  and the `/admin/classifiers` endpoints) and `data.md` (the `classifiers`
  table), each with its `.ko.md` copy.
- `docs/policy-routing.md` and its `.ko.md` copy.
- The "PII dedicated model" row in `docs/adaptive-routing.md`.
- The matching `internal/` module `CLAUDE.md` files.
- The Helm chart README.
- ADR-049, proposed separately.
