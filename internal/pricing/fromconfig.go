package pricing

import "math"

// ConfigRate holds per-MTok rates as human USD floats. It is defined here (not
// imported from internal/config) so pricing stays independent of the config
// package — the caller (main.go) maps config.RateConfig → ConfigRate.
type ConfigRate struct {
	InputPerMTok        float64
	OutputPerMTok       float64
	CacheReadPerMTok    float64
	CacheWrite5mPerMTok float64
	CacheWrite1hPerMTok float64
	// Free declares a genuinely zero-cost model, and is the ONLY way to get a
	// 0-cost row into the table (see Unpriced).
	Free bool
}

// Unpriced reports whether this override declares no price at all: both
// per-token rates zero with no explicit Free opt-in. Such a row is NOT put in
// the table, so HasRate stays false and the whole unpriced machinery engages —
// UnpricedTargets lists it, `mayu pricing check` exits 1, `on_missing: "block"`
// refuses to boot, the runtime guard 402s, and settlement records
// missing=true.
//
// Before this, `{"input_per_mtok": 0, "output_per_mtok": 0}` — the natural way
// to write a fill-in-the-blank placeholder, and exactly what
// examples/config.bedrock-glm.json shipped — was inserted as a real rate. It
// passed every one of those gates and settled at 0 uUSD with missing=false,
// which is the documented encoding for a genuinely FREE model. So an
// unfinished placeholder was indistinguishable in the audit record from a
// deliberate zero: the one thing ADR-030 set out to make impossible.
//
// Only BOTH rates being zero counts. A single-sided zero is unusual but not
// provably wrong (a provider could bill output only), so it is left alone.
//
// internal/config's load-time validation rejects the same shape with an
// actionable message; this is defense in depth for the paths that never see
// the file loader — BuildState is also reached by the ADR-008 UI-write overlay
// and by every hot reload.
func (cr ConfigRate) Unpriced() bool {
	return cr.InputPerMTok == 0 && cr.OutputPerMTok == 0 && !cr.Free
}

// Cache rates are fixed multiples of the input rate on every provider that
// publishes them — verified against Anthropic's first-party table (Sonnet
// $3.00 → $0.30 / $3.75 / $6.00; Opus $5.00 → $0.50 / $6.25 / $10.00) and
// against Amazon Bedrock's ($6.00 → $0.60 / $7.50 / $12.00). Bundled()'s
// hardcoded figures match these ratios exactly.
//
// So an operator declares input and output; the three cache rates are derived
// (ADR-030). Before this, a config that set only input/output — the natural
// thing to write — silently billed every cache read and write at ZERO, which
// is most of the spend on a prompt-cache-heavy workload like Claude Code.
// An explicitly-set cache rate always wins, for special pricing agreements.
const (
	cacheReadRatio    = 0.1
	cacheWrite5mRatio = 1.25
	cacheWrite1hRatio = 2.0
)

// FromConfig builds a Table starting from Bundled() rates and applying the
// per-(provider,model) overrides, converting USD-per-MTok floats to µUSD-per-
// MTok int64 via round-half-away-from-zero. Unset cache rates are derived from
// the input rate (see above). An Unpriced() override is skipped entirely rather
// than inserted as a 0-cost rate. onMissing "block" selects OnMissingBlock;
// anything else selects OnMissingAllow.
func FromConfig(onMissing string, overrides map[string]map[string]ConfigRate) *Table {
	return FromConfigVersioned(onMissing, "", overrides)
}

// FromConfigVersioned is FromConfig with an explicit rate-table label.
func FromConfigVersioned(onMissing, version string, overrides map[string]map[string]ConfigRate) *Table {
	rates := Bundled()
	for provider, models := range overrides {
		for model, cr := range models {
			if cr.Unpriced() {
				continue
			}
			rates[Key{Provider: provider, Model: model}] = Rate{
				InputPerMTok:        usdToMicros(cr.InputPerMTok),
				OutputPerMTok:       usdToMicros(cr.OutputPerMTok),
				CacheReadPerMTok:    derivedRate(cr.CacheReadPerMTok, cr.InputPerMTok, cacheReadRatio),
				CacheWrite5mPerMTok: derivedRate(cr.CacheWrite5mPerMTok, cr.InputPerMTok, cacheWrite5mRatio),
				CacheWrite1hPerMTok: derivedRate(cr.CacheWrite1hPerMTok, cr.InputPerMTok, cacheWrite1hRatio),
			}
		}
	}
	om := OnMissingAllow
	if onMissing == "block" {
		om = OnMissingBlock
	}
	return NewVersioned(om, rates, version)
}

func usdToMicros(usd float64) int64 {
	return int64(math.Round(usd * 1_000_000))
}

// derivedRate returns the explicit rate when the operator set one, else
// input × ratio. A zero explicit value is indistinguishable from "unset" in
// JSON without pointers, so zero means derive — a genuinely free token class
// is not a thing any provider publishes, whereas an accidentally-omitted cache
// rate is the common case this exists to fix.
func derivedRate(explicit, input, ratio float64) int64 {
	if explicit != 0 {
		return usdToMicros(explicit)
	}
	return usdToMicros(input * ratio)
}
