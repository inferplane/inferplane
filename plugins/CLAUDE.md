# plugins Module

## Role
Request-transform filters and optional learned PII classifiers. This is an
**extension surface** mirroring `providers/`: a new filter is **one package**
under `plugins/<name>/` plus a blank import in `cmd/mayu/main.go`, with
**zero core diff**. Core packages (`internal/server`, `router`) import only the
  `internal/filter` or `internal/classifier` interface, never a concrete plugin.

## Key Packages
- `piimask/` — opt-in PII masking (ADR-009). Regex + Luhn detectors (email,
  phone, credit-card, SSN, IPv4) → typed placeholders (`‹EMAIL›` …). One-way: no
  vault, no un-masking, **no PII stored**. Registers as `pii-mask` in `init()`.
- `systemone/` — PII classifier for `/v1/systemone` servers (Laya, strands-decider); registers as `systemone` via `classifier.Register` in `init()`; overlapping windows over the joined request text with per-instance keyed boundaries, at most `max_windows` sent per request, bounded error classes; a window the server reports as cut (`usage.truncated`/`state_tokens_dropped`) is never Clear, and under the default `truncation: "reported"` a result without `usage.truncated` is Abstain (the plugin never estimates tokens); never edits a body or logs text.

## Rules
- A transform filter registers itself with `filter.Register(...)` in `init()`; it is
  activated by the `plugins` config block (per-team or global). Registration
  happens at process start (blank import), before config validation, so an
  unknown plugin name is rejected at load.
- Classifiers register with `classifier.Register(...)` and activate through
  `pii_classification`, not the transform `plugins` block. Counts never invoke them;
  the concrete SystemOne implementation is an HTTP sidecar dependency.
- **Masking is opt-in and its cost is explicit.** Mutating protected content changes the upstream cache prefix: the first
  transformed request may miss an existing cache, while identical transformed
  prefixes can be reused later. Measure cold-cache, retries and settled cost;
  there is no universal 10× multiplier. Enabling a masking filter must log a warning + emit a metric/audit; it
  must never be silent (ADR-009, spec §308).
- **Never store PII / secrets.** Filters are one-way transforms; there is no
  original↔placeholder map at rest (that would make the gateway a PII vault).
- **Preserve structural semantics.** The legacy text-filter API handles message
  text only. Policy-required masking and the complete Responses transformer
  inspect original content and independently reinspect transformed content;
  unsafe structural/numeric changes or incomplete coverage refuse.
- **Fail closed for security filters.** Masker errors refuse generation before
  dispatch. Authenticated counts use local HTTP 200 results, never a learned
  classifier. Legacy OpenAI Chat masking remains unsupported/refused; do not
  confuse that limitation with the complete Responses transformer.

Run `go test ./plugins/... -race`; classifier tests use local fake servers only.
