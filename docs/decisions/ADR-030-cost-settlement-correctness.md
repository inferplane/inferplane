# ADR-030: Cost settlement correctness — streaming usage, cache tiers, and unpriced routes

**Date:** 2026-07-28
**Status:** Accepted
**Related:** ADR-003 (governance differentiation — chargeback is the headline
claim), ADR-007 (chargeback report), ADR-006 (one pricing table per topology
generation), ADR-018 (audit `cost` ref), ADR-029 (model-level fallback — a new
source of unpriced targets)

## Context

Cost came out **0 µUSD for every request** on the live demo deployment. Measured
directly against `GET /admin/logs`:

| model | input_tokens | output_tokens | cost_micros |
|---|---|---|---|
| `global.anthropic.claude-opus-5` | 96 | 79 | **0** |
| `global.anthropic.claude-opus-5` | 1 | 1896 | **0** |
| `global.anthropic.claude-sonnet-5` | 158 | 7 | **0** |

Tokens were counted correctly; only the money was wrong. The arithmetic in
`internal/pricing` (integer µUSD, round-half-even via `math/big`) turned out to
be correct — every defect was in the *inputs* to it. Five independent ones:

1. **Streaming billed output tokens only.** The settlement path kept the last
   usage-bearing frame's top-level `usage`, which on the Anthropic wire is
   `message_delta` — and a `message_delta` commonly carries `output_tokens`
   alone. Input and prompt-cache counts ride `message_start`, nested under
   `message.usage`, and were never read. Measured on the repo's own streaming
   fixture: **5 µUSD billed where 52 was correct — a 10.4x under-bill.** Claude
   Code traffic is effectively all streaming, so this was the dominant error.
2. **Cache writes always billed at the 5-minute tier.** `schema.CacheCreation`
   already carried the TTL split and no settlement path read it, so the 1-hour
   tier (2x the input rate against 5m's 1.25x) was unreachable in production.
   The code comment claiming the schema "does not yet split cache_creation by
   TTL" was false.
3. **Cache rates had to be declared per model, or they were zero.** A config
   setting only `input_per_mtok`/`output_per_mtok` — the natural thing to write,
   and exactly what the demo does — billed every cache read and write at zero.
   On a prompt-cache-heavy workload that is most of the spend.
4. **Bedrock cross-region prefixes missed the rate table.** The demo routed to
   `global.anthropic.claude-*` while pricing (at most) the unprefixed id, so
   lookup missed entirely. Bedrock Converse additionally dropped cache tokens at
   the client-struct level, while the InvokeModel passthrough preserved them —
   the same model costing different amounts depending on API mode.
5. **`pricing.on_missing: "block"` was dead config.** `Table.OnMissing()` had no
   caller outside the package. `allow` and `block` behaved identically: cost 0,
   `pricing_missing: true`, request served. An operator who set `block`
   believing unpriced traffic was refused was serving it free.

Two smaller ones in the same path: the OpenAI-compatible ingress billed cached
prompt tokens at the full input rate (`prompt_tokens` *includes*
`cached_tokens`) — over-billing, the opposite direction; and a stream that broke
mid-flight skipped settlement entirely, so tokens already delivered were free
with no flag to show it.

**Why it survived:** no test anywhere asserted the settled cost of a streaming
request. `internal/pricing`'s tests called `CostUSDMicros` directly with a
hand-built `Usage`, validating the arithmetic and nothing about the mapping.
Worse, the mock provider put input *and* output on `message_delta` — a shape no
real upstream produces — so the streaming tests that existed could not have
caught defect 1.

## Decision

### 1. Fold streaming usage across frames; never overwrite

New `schema.MergeUsage(acc, next)` folds each field to its latest non-nil value
(Anthropic *refines* counts across frames rather than adding, so summing would
double-bill). All three ingress handlers now fold both `ev.Chunk.Message.Usage`
(message_start) and `ev.Chunk.Usage` (message_delta).

### 2. Resolve cache-write TTL tiers in one place

New `schema.Usage.CacheWriteTiers()` returns `(write5m, write1h)`. The TTL split
is authoritative when present; the flat `cache_creation_input_tokens` is a
fallback attributed to the cheaper 5m tier. **Never summed** — a provider
sending both would otherwise double-count every cache write.

This lives on the wire type because the mapping was open-coded at six call sites
(`settle` + `observeTokens` across three handlers) and *every one* dropped the
1h tier. One tested function replaces six chances to get it wrong.

### 3. Derive cache rates from the input rate

Cache rates are fixed multiples of input on every provider that publishes them,
verified against two independent sources:

| | ratio | Anthropic first-party | Amazon Bedrock |
|---|---|---|---|
| cache read | **0.1x** | $3.00→$0.30, $5.00→$0.50 | $6.00→$0.60 |
| cache write 5m | **1.25x** | $3.00→$3.75, $5.00→$6.25 | $6.00→$7.50 |
| cache write 1h | **2.0x** | $3.00→$6.00, $5.00→$10.00 | $6.00→$12.00 |

`Bundled()`'s hardcoded figures match these ratios exactly, and a test asserts
the derivation reproduces them. So an operator declares two numbers; an
explicitly-set cache rate still wins, for special pricing agreements.

### 4. Strip the Bedrock cross-region prefix on lookup — and nothing else

`CostUSDMicros` retries with a single leading `global.` / `us.` / `eu.` /
`apac.` / `us-gov.` stripped. These are the same model reached through different
routing, with no published price differential. **Exact match always wins**, so a
per-prefix rate stays declarable.

**Model versions are deliberately NOT collapsed.** Opus 4.6/4.7/4.8/5 all cost
$5/$25 today, but that is a property of the current table, not a guarantee.
Silently billing a new model at an old model's rate is the same class of bug as
billing it at zero.

**Provider stays in the key**: Bedrock Opus 4.8 is $6.00/$30.00 against
first-party's $5.00/$25.00. The two are not interchangeable.

### 5. `on_missing` governs strictness — at boot *and* at runtime

Rather than overriding the operator's declaration, it is now enforced:

- **`block`** — `live.BuildState` refuses to boot when any configured route has
  no rate, naming the routes. At runtime `governance.PricingGuard` denies with
  402 / `pricing_missing`.
- **`allow`** (default) — boot logs the unpriced routes loudly and continues.
  This is a legitimate posture: a self-hosted vLLM deployment may genuinely have
  no meaningful per-token price. **Silence was the bug, not permissiveness.**

An unrecognized `on_missing` value is now a load error; it used to fall through
to `allow`, so `"blcok"` silently disabled the control.

The two enforcement points are complementary, not redundant. Boot validation
sees only config-declared routes; the runtime guard covers what it cannot —
models registered through UI-write (ADR-008) and targets appended by a
model-level fallback (ADR-029).

Pricing-override keys are also cross-checked against the real topology
unconditionally: a key naming a nonexistent provider or model is a typo, never a
policy, and at runtime it was indistinguishable from a missing rate.

### 6. Rate updates are a reviewed commit, not a runtime fetch

Keeping rates current was raised as a case for fetching them dynamically. **No
authoritative machine-readable source exists**, which we verified rather than
assumed:

- The **AWS Price List API carries no current Claude SKUs**. All 91 rows for
  `AmazonBedrock` in `ap-northeast-2` are Nova; `get-attribute-values
  --attribute-name model` returns only `Claude 2.0 / 2.1 / 3 Haiku / 3 Sonnet /
  Instant`.
- Anthropic publishes no pricing API — only documentation pages.

Even with a source, a gateway that fetches rates at runtime is wrong here: the
audit chain is tamper-evident by design, and a cost that can change because a
web page changed makes chargeback unprovable. It also violates the project's
"no external SaaS dependency" constraint.

So the goal — *a newly released model must not silently bill at zero* — is met
structurally instead: boot refuses (or warns loudly), the runtime guard denies,
`PricingVersion` becomes a real config-declared label so a disputed invoice can
be pinned to the rates that produced it, and the derivation above reduces adding
a model to reading one documented figure and writing two numbers.

## Consequences

### Positive
- Streaming requests bill their full usage. On the measured fixture the settled
  cost goes from 5 to 52 µUSD.
- Cache tokens bill correctly on all three ingresses and both Bedrock API modes.
- A config declaring only input/output now prices cache correctly, so the
  most common config shape is no longer silently wrong.
- Unpriced routes cannot be silent: boot fails or warns, runtime can deny, and
  the audit record carries a real `pricing_version`.
- Regression is now guarded by tests that assert settled cost end to end —
  verified to fail against the pre-fix code, not merely to pass against the new.

### Negative
- **Bills go up.** Anyone who was under-billed will see costs rise — for
  cache-heavy streaming workloads, by an order of magnitude. That is the
  correction, but it will look like a regression to whoever reads the dashboard
  first, and per-team budgets calibrated against the broken figures may start
  tripping.
- **`on_missing: "block"` is now load-bearing.** A deployment that set it
  without full rate coverage will refuse to boot after upgrading. Intended, but
  it is a breaking change for that configuration; `allow` is unaffected.
- **The 0.1/1.25/2.0 ratios are an observed invariant, not a contract.** They
  hold across two providers today and reproduce the bundled table exactly. If a
  provider ever publishes cache pricing off that ratio, the explicit-override
  path is the escape hatch and the ratios need revisiting.
- **Zero is not expressible as an explicit cache rate.** Because `ConfigRate`
  uses value floats, `0` means "derive". No provider publishes free cache
  tokens, so this trades an unreachable case for fixing the common one.
- The mock provider's streaming shape changed. Any test asserting the old
  (unrealistic) usage shape needed updating — five did.

## References
- `pkg/schema/usage.go` — `MergeUsage`, `CacheWriteTiers`
- `internal/pricing/{pricing,fromconfig}.go` — derivation, prefix lookup, `HasRate`
- `internal/live/live.go` — `validatePricingCoverage`, `UnpricedTargets`
- `internal/governance/governance.go` — `PricingGuard`
- `providers/bedrock/{client,converse}.go` — Converse cache tokens
- `internal/openai/convert.go` — `cached_tokens` split
