# Request-level context pricing

mayu settles integer microUSD per provider attempt from the installed topology's
price snapshot. It does not require or fabricate a Codex `reported_cost`.
These amounts are configured-price estimates, not invoices: taxes, private
discounts, hosted-tool fees and unconfigured service tiers are outside them.

## Configuration

The top-level rates are short-context rates. Add both `short_context_limit`
and `long_context` to opt into whole-response tiers:

```json
{
  "pricing": {
    "on_missing": "block",
    "version": "aws-gpt-standard-2026-10-02",
    "overrides": {
      "bedrock-responses-east": {
        "openai.gpt-6.1-sol": {
          "input_per_mtok": 2.2,
          "output_per_mtok": 11,
          "cache_read_per_mtok": 0.11,
          "cache_write_5m_per_mtok": 2.75,
          "cache_write_1h_per_mtok": 2.75,
          "short_context_limit": 272000,
          "long_context": {
            "input_per_mtok": 4.4,
            "output_per_mtok": 16.5,
            "cache_read_per_mtok": 0.22,
            "cache_write_5m_per_mtok": 5.5,
            "cache_write_1h_per_mtok": 5.5
          }
        }
      }
    }
  }
}
```

The example is **commercial regional Mantle Standard**, not Global CRIS.
Regional fees are already included; never add 10% again. Sol 6.1 cache reads
are 0.05x input, unlike Sol 6's 0.1x. A single published cache-write dimension
is placed in both legacy TTL fields; that does not attest TTL feature support.
For GPT-5.4/5.5, `cache_write_unsupported: true` in each rate suppresses derived
write prices. Nonzero write usage then has unavailable cost instead of a guessed
or silently free charge. Explicit cache prices override legacy ratio defaults.

The paired declaration refuses missing/nested/free/unpriced long rates,
negative/nonfinite prices and integer-microUSD overflow. Table construction also
rejects malformed UI/hot-reload declarations, including invalid exact keys that
would otherwise fall back to a bundled or region-stripped row. Flat declarations
retain their rate behavior. Standard context-priced requests allow an absent,
null or `default` `service_tier`; other explicit tiers refuse before inference.

## Selection and settlement

The canonical usage is disjoint. The selector sums uncached input, cache reads,
5m writes and 1h writes **once**, using an overflow-safe comparison. It excludes
output/reasoning from the context threshold; output already contains reasoning.
At 272,000 input tokens use short prices. At 272,001 use long prices for all
input/cache/output, not just excess input. Select separately for each attempt,
then aggregate costs; neither session totals nor streaming chunk sums select a
tier. Token classes keep existing per-class round-half-even microUSD rounding.

For Global Sol 6.1, uncached input 300,000 plus output 10,000 settles **$1.35**
($4/$15 per MTok); regional Mantle settles **$1.485** ($4.40/$16.50).
The 272K short-context limit is a pricing boundary, not a maximum context window.
Authority admission reserves each class at the higher short/long price and rounds
up; checked settlement shares the selector. Unknown/malformed usage retains
durable uncertainty. No policy/account/grant row or past audit record is rewritten.

## Evidence and operations

The [verified snapshot](../../examples/pricing/bedrock-gpt-standard-2026-10-02.json)
contains nine GPT model cards, including model-specific cache rates and exact
regional/global distinctions. The [native Sol 6.1 example](../../examples/config.bedrock-gpt-6-1-sol.json)
uses the same regional rates. The source links and date are part of the snapshot;
availability/model quality are not established by a price row.

Upgrade the gateway **before** installing the additive config fields: older
consumers may ignore them and charge only the base rate. Run `mayu pricing check`
on the full declaration, preserve the original deployment's profile/readiness
requirements, and verify new audit records' `cost.amount_usd_micros`,
`pricing_version`, `context_tier` and disjoint token classes. Runtime arithmetic,
audit, budgets, usage telemetry and OTel use the same settled amount.

New audit usage records append `usage.evidence` with `input_known`,
`output_known`, `input_total_known`, `cache_write_ttl_known`, and
`accounting_uncertain`. Missing evidence on older records is unknown, not
confirmation of complete usage. A flat cache-write total still follows the
legacy 5m estimate, but its TTL is marked unknown. Partial/inconsistent splits,
unrecognized TTL fields (including 30m), invalid counts and provider uncertainty
cannot attest a complete TTL breakdown. This evidence describes interpreted
token fields; it does not change settlement or authorize an authority refund.
Check `outcome.partial` separately for an interrupted response.

When a partial TTL breakdown accompanies a flat total, accounting preserves that
total: 100 cache-write tokens with only 40 reported in the 5m field still counts
100 toward quota and context pricing. Known 1h tokens keep their rate; the
unclassified remainder uses the legacy 5m estimate. A known quantity does not
prove its TTL or authorize a monetary reservation refund. Negative counts,
overflowing totals and contradictory nonzero complete splits are rejected before
counter updates. Missing or invalid usage retains the admitted TPM estimate.
Settlement is once per admission; pending TPM buckets survive cleanup sweeps.

With tracing enabled, each `inferplane.cost.settled` event contains one settled
attempt's amount, price version/context tier, routed `gen_ai.response.model`,
`gen_ai.system`, config alias `inferplane.provider.name`, partial marker, disjoint
token counts and the evidence flags under `inferplane.usage.*`. The model is the
configured upstream ID, not an attestation of what a remote gateway executed.
`gen_ai.usage.input_tokens` excludes cache. `inferplane.usage.input_tokens_total`
includes cache once and is emitted only when `input_tokens_total_known` is true;
unknown input/output counts are omitted on the event. A partial response also
sets the event's `accounting_uncertain` flag.

Deduplicate repeated exports by `inferplane.cost.event_id` (a generated opaque
ID). This does not correlate a client completion with its gateway attempts or
with an audit record. The scalar span amount remains the last settled attempt;
use the event's own model and token fields when repricing retries. Never add
span scalars, cost events, audit costs and client completion costs together.
Scalar token fields now explicitly clear zero counts between observed attempts.
Consumers must honor the evidence flags, including when a later field is unknown.

Traces remain diagnostic: sampling, parent sampling decisions, event limits and
export loss can omit attempts. No event is emitted without a cost settlement.
Dashboard collection and cross-source request correlation require a separate
integration; the Claude Code usage dashboard's current collector does not admit
the `inferplane` service. Existing audit is not retroactively corrected. A
separately labeled historical repricing report may be derived without altering
settled liabilities or hashed records.

## Sync limits

`mayu pricing sync` generates input/output rates for supported `bedrock` routes.
It reports `bedrock_responses` routes as unresolved (exit 1), including in a mixed
topology, instead of silently skipping them or borrowing runtime prices for
Mantle. Use reviewed model-card overrides for those routes.

Existing context tiers, explicit cache prices, unsupported-write declarations
and free rows are protected from replacement by this input/output-only generator.
Protection follows the same exact-key-then-base-key lookup as pricing. Such
routes do not trigger a catalog fetch. Multiple price dimensions and a single
bounded/incomplete range are rejected. Unresolved routes produce no placeholder;
resolved routes may still produce a partial fragment, so always check exit status.
The generator does not verify universal regional price parity, derive missing
service-tier prices, or supply a complete cross-provider cache catalog.
