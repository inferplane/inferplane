// Package pricing computes per-request cost in integer micro-USD (µUSD) from a
// (provider, model)-keyed rate table. Money is NEVER float (design §5.3) — all
// rates are µUSD-per-million-tokens (int64) and the per-request cost is a
// single round-half-even division. cache write is TTL-tiered (5m vs 1h) and
// cache read is billed separately.
package pricing

import (
	"math/big"
	"strings"
)

type Key struct {
	Provider string
	Model    string
}

// Rate holds µUSD per 1,000,000 tokens for each token class.
type Rate struct {
	InputPerMTok        int64
	OutputPerMTok       int64
	CacheReadPerMTok    int64
	CacheWrite5mPerMTok int64
	CacheWrite1hPerMTok int64
}

type Usage struct {
	Input        int64
	Output       int64
	CacheRead    int64
	CacheWrite5m int64
	CacheWrite1h int64
}

type OnMissing int

const (
	OnMissingAllow OnMissing = iota // cost 0 + missing=true (default; self-hosted chargeback unknown)
	OnMissingBlock
)

type Table struct {
	onMissing OnMissing
	rates     map[Key]Rate
	Version   string
}

func New(onMissing OnMissing, rates map[Key]Rate) *Table {
	return NewVersioned(onMissing, rates, "bundled")
}

// NewVersioned is New with an explicit rate-table label, surfaced in every
// audit record's cost.pricing_version. New() keeps the historical "bundled"
// default so tests and the zero-config path are unchanged.
func NewVersioned(onMissing OnMissing, rates map[Key]Rate, version string) *Table {
	if rates == nil {
		rates = map[Key]Rate{}
	}
	if version == "" {
		version = "unversioned"
	}
	return &Table{onMissing: onMissing, rates: rates, Version: version}
}

func (t *Table) OnMissing() OnMissing { return t.onMissing }

// bedrockRegionPrefixes are Bedrock cross-region inference-profile prefixes.
// `global.anthropic.claude-opus-5` and `us.anthropic.claude-opus-5` are the
// same model reached through different routing, and AWS publishes no
// cross-region price differential — so one rate row must cover all of them
// rather than each prefix needing its own (ADR-030). Without this, a config
// that priced `anthropic.claude-opus-5` billed every `global.`-prefixed
// request at zero.
//
// This is deliberately the ONLY normalization: model VERSIONS are never
// collapsed. Opus 4.6/4.7/4.8/5 all cost $5/$25 today, but that is a property
// of the current table and not a guarantee — silently billing a new model at
// an old model's rate is the same class of bug as billing it at zero.
var bedrockRegionPrefixes = []string{"global.", "us.", "eu.", "apac.", "us-gov."}

// normalizeModel strips a single leading Bedrock cross-region prefix, or
// returns "" when there is none to strip.
func normalizeModel(model string) string {
	for _, p := range bedrockRegionPrefixes {
		if strings.HasPrefix(model, p) {
			return model[len(p):]
		}
	}
	return ""
}

// HasRate reports whether a rate exists for this (provider, model), following
// the same two-stage lookup CostUSDMicros uses. This is the single predicate
// behind boot-time validation and `mayu pricing check`, so neither can
// drift from what actually gets billed.
func (t *Table) HasRate(provider, model string) bool {
	if _, ok := t.rates[Key{provider, model}]; ok {
		return true
	}
	if base := normalizeModel(model); base != "" {
		_, ok := t.rates[Key{provider, base}]
		return ok
	}
	return false
}

// CostUSDMicros returns the request cost in µUSD and whether the (provider,
// model) rate was missing. Cost sums each token class rounded independently
// (round-half-even on the /1e6 division), computed once over the full token
// totals — never per-chunk. Lookup is exact-match first, then retried with a
// Bedrock cross-region prefix stripped (see bedrockRegionPrefixes).
func (t *Table) CostUSDMicros(provider, model string, u Usage) (cost int64, missing bool) {
	r, ok := t.rates[Key{provider, model}]
	if !ok {
		// Exact match wins, so a per-prefix special rate stays declarable;
		// only fall back to the region-stripped id.
		if base := normalizeModel(model); base != "" {
			r, ok = t.rates[Key{provider, base}]
		}
	}
	if !ok {
		return 0, true
	}
	total := int64(0)
	total += mulDivRoundHalfEven(u.Input, r.InputPerMTok)
	total += mulDivRoundHalfEven(u.Output, r.OutputPerMTok)
	total += mulDivRoundHalfEven(u.CacheRead, r.CacheReadPerMTok)
	total += mulDivRoundHalfEven(u.CacheWrite5m, r.CacheWrite5mPerMTok)
	total += mulDivRoundHalfEven(u.CacheWrite1h, r.CacheWrite1hPerMTok)
	return total, false
}

// mulDivRoundHalfEven computes tokens * perMTok / 1_000_000 with banker's
// rounding, using math/big to avoid int64 overflow on large token counts.
func mulDivRoundHalfEven(tokens, perMTok int64) int64 {
	if tokens == 0 || perMTok == 0 {
		return 0
	}
	num := new(big.Int).Mul(big.NewInt(tokens), big.NewInt(perMTok))
	denom := big.NewInt(1_000_000)
	q := new(big.Int)
	rem := new(big.Int)
	q.QuoRem(num, denom, rem)
	// round half to even
	twice := new(big.Int).Mul(rem, big.NewInt(2))
	cmp := twice.CmpAbs(denom)
	if cmp > 0 || (cmp == 0 && q.Bit(0) == 1) {
		q.Add(q, big.NewInt(1))
	}
	return q.Int64()
}
