// Package pricing computes per-request cost in integer micro-USD (µUSD) from a
// (provider, model)-keyed rate table. Money is NEVER float (design §5.3) — all
// rates are µUSD-per-million-tokens (int64) and the per-request cost is a
// round-half-even division per token class. cache write is TTL-tiered (5m vs 1h) and
// cache read is billed separately.
package pricing

import "strings"

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
	// LongContext prices the entire response when total disjoint input/cache
	// tokens exceed ShortContextLimit. Both fields must be declared together.
	ShortContextLimit     int64
	LongContext           *Rate
	CacheWriteUnsupported bool
}

type Usage struct {
	Input        int64
	Output       int64
	CacheRead    int64
	CacheWrite5m int64
	CacheWrite1h int64
	Invalid      bool // inconsistent observations must not mutate accounting
	Uncertain    bool // token quantities are incomplete; never refund an estimate
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
	owned := make(map[Key]Rate, len(rates))
	for key, rate := range rates {
		if rate.LongContext != nil {
			long := *rate.LongContext
			rate.LongContext = &long
		}
		owned[key] = rate
	}
	if version == "" {
		version = "unversioned"
	}
	return &Table{onMissing: onMissing, rates: owned, Version: version}
}

func (t *Table) OnMissing() OnMissing { return t.onMissing }

// bedrockRegionPrefixes are Bedrock cross-region inference-profile prefixes.
// `global.anthropic.claude-opus-5` and `us.anthropic.claude-opus-5` are the
// same model reached through different routing. Prefix fallback is a legacy
// operator convenience, not a claim of price parity; model cards may charge
// regional premiums, so those routes need exact profile-specific rows.
// The legacy fallback fixes ADR-030's original case, where a config
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

// BaseModelID returns model with one leading Bedrock cross-region prefix
// stripped, or model unchanged when it carries none. Exported so an offline
// tool that has to correlate a configured upstream id with an external catalog
// (`mayu pricing sync`) uses the SAME prefix list the rate lookup does, instead
// of a second copy that can drift from bedrockRegionPrefixes.
func BaseModelID(model string) string {
	if base := normalizeModel(model); base != "" {
		return base
	}
	return model
}

// HasRate reports whether a rate exists for this (provider, model), following
// the same two-stage lookup CostUSDMicros uses. This is the single predicate
// behind boot-time validation and `mayu pricing check`, so neither can
// drift from what actually gets billed.
func (t *Table) HasRate(provider, model string) bool {
	_, ok := t.lookup(provider, model)
	return ok
}

func (t *Table) lookup(provider, model string) (Rate, bool) {
	if t == nil {
		return Rate{}, false
	}
	r, ok := t.rates[Key{provider, model}]
	if !ok {
		if base := normalizeModel(model); base != "" {
			r, ok = t.rates[Key{provider, base}]
		}
	}
	return r, ok && r.valid()
}

func (r Rate) valid() bool {
	if r.CacheWriteUnsupported && (r.CacheWrite5mPerMTok != 0 || r.CacheWrite1hPerMTok != 0) {
		return false
	}
	for _, n := range []int64{r.InputPerMTok, r.OutputPerMTok, r.CacheReadPerMTok, r.CacheWrite5mPerMTok, r.CacheWrite1hPerMTok} {
		if n < 0 {
			return false
		}
	}
	if r.LongContext == nil {
		return r.ShortContextLimit == 0
	}
	long := r.LongContext
	return r.ShortContextLimit > 0 && long.LongContext == nil && long.ShortContextLimit == 0 && long.valid()
}

// isLong compares without summing int64 counts, so a huge cache-heavy request
// cannot wrap back into the cheaper tier. Output/reasoning never triggers it.
func (r Rate) isLong(u Usage) bool {
	if r.LongContext == nil {
		return false
	}
	remaining := r.ShortContextLimit
	for _, n := range []int64{u.Input, u.CacheRead, u.CacheWrite5m, u.CacheWrite1h} {
		if n > remaining {
			return true
		}
		remaining -= n
	}
	return false
}

// ContextTier is bounded audit evidence. Flat/missing tables have no tier.
func (t *Table) ContextTier(provider, model string, u Usage) string {
	if u.Invalid || u.Uncertain {
		return ""
	}
	for _, n := range []int64{u.Input, u.Output, u.CacheRead, u.CacheWrite5m, u.CacheWrite1h} {
		if n < 0 {
			return ""
		}
	}
	r, ok := t.lookup(provider, model)
	if !ok || r.LongContext == nil {
		return ""
	}
	if r.isLong(u) {
		return "long"
	}
	return "short"
}

// CostUSDMicros returns the request cost in µUSD and whether the (provider,
// model) rate was missing. Cost sums each token class rounded independently
// (round-half-even on the /1e6 division), computed once over the full token
// totals — never per-chunk. Lookup is exact-match first, then retried with a
// Bedrock cross-region prefix stripped (see bedrockRegionPrefixes).
func (t *Table) CostUSDMicros(provider, model string, u Usage) (cost int64, missing bool) {
	cost, err := t.CostUSDMicrosChecked(provider, model, u)
	return cost, err != nil
}
