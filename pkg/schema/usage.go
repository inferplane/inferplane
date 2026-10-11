package schema

import "math"

// MergeUsage folds a newly-observed Usage into a running one and returns the
// result. Each field takes the latest NON-NIL value seen — Anthropic refines
// and repeats counts across streaming frames rather than adding to them, so
// this is a fold, not a sum (summing would double-bill any count that appears
// in more than one frame).
//
// Why this exists (ADR-030): the settlement path used to keep only the LAST
// frame's top-level `usage`, which on the Anthropic wire is `message_delta` —
// and a `message_delta` commonly carries `output_tokens` alone. The input and
// prompt-cache counts live on `message_start`, nested under `message.usage`
// (schema.ChatChunk.Message.Usage). Overwriting instead of folding therefore
// billed streaming requests for output only, silently dropping every input and
// cache token. Both frames must be folded in, in arrival order.
//
// A nil acc returns a copy of next (never next itself — callers keep folding
// into the result and must not mutate the provider's frame). A nil next leaves
// acc unchanged. Extra is carried from acc, else next: it is passthrough
// preservation for round-tripping, never a billed field.
func MergeUsage(acc, next *Usage) *Usage {
	if next == nil {
		return acc
	}
	if acc == nil {
		cp := *next
		cp.CacheCreation = mergeCacheCreation(nil, next.CacheCreation)
		return &cp
	}
	out := *acc
	out.AccountingUncertain = acc.AccountingUncertain || next.AccountingUncertain
	if next.InputTokens != nil {
		out.InputTokens = next.InputTokens
	}
	if next.OutputTokens != nil {
		out.OutputTokens = next.OutputTokens
	}
	if next.CacheReadInputTokens != nil {
		out.CacheReadInputTokens = next.CacheReadInputTokens
	}
	if next.CacheCreationInputTokens != nil {
		out.CacheCreationInputTokens = next.CacheCreationInputTokens
	}
	out.CacheCreation = mergeCacheCreation(acc.CacheCreation, next.CacheCreation)
	if out.Extra == nil {
		out.Extra = next.Extra
	}
	return &out
}

// CacheWriteTiers resolves cache-CREATION tokens into the 5-minute and 1-hour
// tiers that pricing bills separately (5m = 1.25x the input rate, 1h = 2x).
//
// Two wire shapes carry the same information and MUST NOT be added together:
//   - `cache_creation: {ephemeral_5m_input_tokens, ephemeral_1h_input_tokens}`
//     — the TTL-split form. Complete, consistent splits set the tier counts.
//   - `cache_creation_input_tokens` — a flat total that predates the split.
//     A flat total also preserves tokens omitted by a partial split. Known
//     1h tokens retain that tier; the remainder uses the legacy 5m estimate.
//     This estimate does not establish the actual TTL or authorize refunds.
//
// Summing both would double-count every cache write, since a provider that
// sends the split generally also sends the flat total.
//
// This lives here, on the wire type, because the mapping was previously
// open-coded at six call sites (settle + observeTokens across three ingress
// handlers) and every one of them dropped the 1h tier — see ADR-030.
func (u *Usage) CacheWriteTiers() (write5m, write1h int64) {
	c := u.CacheWriteAccounting()
	return c.FiveMinutes, c.OneHour
}

// CacheWriteCounts separates a known quantity from known TTL prices. Unknown
// TTLs use the legacy 5m estimate but never become evidence for a money refund.
// Invalid observations remain available for validation; callers must check
// Invalid before using these numbers for accounting or settled audit usage.
type CacheWriteCounts struct {
	FiveMinutes, OneHour, Total      int64
	QuantityKnown, TTLKnown, Invalid bool
}

// CacheWriteAccounting never mutates the wire. A partial split cannot hide a
// larger reported total: retain its known 1h portion and assign only the
// remaining unknown quantity to the legacy 5m estimate. Never add the flat
// total to the split. A complete contradictory split is invalid, not a refund.
func (u *Usage) CacheWriteAccounting() CacheWriteCounts {
	c := CacheWriteCounts{QuantityKnown: true, TTLKnown: true}
	if u == nil {
		return c
	}
	flat := deref64(u.CacheCreationInputTokens)
	cc := u.CacheCreation
	if cc == nil {
		c.FiveMinutes, c.Total = flat, flat
		c.Invalid = flat < 0
		c.QuantityKnown = !c.Invalid
		c.TTLKnown = flat == 0
		return c
	}
	c.FiveMinutes, c.OneHour = deref64(cc.Ephemeral5mInputTokens), deref64(cc.Ephemeral1hInputTokens)
	c.Invalid = flat < 0 || c.FiveMinutes < 0 || c.OneHour < 0 || c.FiveMinutes > math.MaxInt64-c.OneHour
	c.QuantityKnown, c.TTLKnown = false, false
	if c.Invalid {
		return c
	}
	split := c.FiveMinutes + c.OneHour
	complete := cc.Ephemeral5mInputTokens != nil && cc.Ephemeral1hInputTokens != nil
	c.Total = split
	if u.CacheCreationInputTokens != nil {
		if flat < split || (complete && split != 0 && flat != split) {
			c.Invalid = true
			return c
		}
		c.Total = flat
		c.FiveMinutes += flat - split
		c.QuantityKnown = len(cc.Extra) == 0
	} else {
		c.QuantityKnown = complete && len(cc.Extra) == 0
	}
	c.TTLKnown = complete && len(cc.Extra) == 0 && c.Total == split
	return c
}

func deref64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// mergeCacheCreation folds the TTL-split cache-write counts with the same
// latest-non-nil rule. The two tiers are priced differently (1h is 2x input,
// 5m is 1.25x), so they must stay separate all the way to pricing.Usage —
// collapsing them is the mis-billing this split exists to prevent.
func mergeCacheCreation(acc, next *CacheCreation) *CacheCreation {
	if next == nil {
		return acc
	}
	if acc == nil {
		cp := *next
		return &cp
	}
	out := *acc
	if next.Ephemeral5mInputTokens != nil {
		out.Ephemeral5mInputTokens = next.Ephemeral5mInputTokens
	}
	if next.Ephemeral1hInputTokens != nil {
		out.Ephemeral1hInputTokens = next.Ephemeral1hInputTokens
	}
	if out.Extra == nil {
		out.Extra = next.Extra
	}
	return &out
}
