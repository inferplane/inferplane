package pricing

import "testing"

func TestTableFromConfig(t *testing.T) {
	overrides := map[string]map[string]ConfigRate{
		"anthropic-direct": {"claude-sonnet-4-6": {InputPerMTok: 3.0, OutputPerMTok: 15.0, CacheReadPerMTok: 0.3, CacheWrite5mPerMTok: 3.75, CacheWrite1hPerMTok: 6.0}},
	}
	tbl := FromConfig("allow", overrides)
	if tbl.OnMissing() != OnMissingAllow {
		t.Fatal("on_missing allow")
	}
	cost, missing := tbl.CostUSDMicros("anthropic-direct", "claude-sonnet-4-6", Usage{Input: 1_000_000})
	if missing || cost != 3_000_000 { // 1M tokens * 3.0 USD/MTok = 3 USD = 3_000_000 µUSD
		t.Fatalf("cost=%d missing=%v", cost, missing)
	}
}

func TestFromConfigStartsFromBundled(t *testing.T) {
	// with no overrides, bundled rates apply
	tbl := FromConfig("allow", nil)
	cost, missing := tbl.CostUSDMicros("anthropic-direct", "claude-sonnet-4-6", Usage{Input: 1_000_000})
	if missing || cost == 0 {
		t.Fatalf("bundled rate should apply: cost=%d missing=%v", cost, missing)
	}
}

// ADR-030: cache rates are fixed multiples of the input rate on every provider
// that publishes them, so an operator who declares only input/output must not
// silently get zero-priced cache tokens (the deployed-demo bug).
func TestFromConfig_derivesCacheRatesFromInput(t *testing.T) {
	tbl := FromConfig("allow", map[string]map[string]ConfigRate{
		"bedrock-global": {
			"anthropic.claude-sonnet-4-6": {InputPerMTok: 3.0, OutputPerMTok: 15.0},
		},
	})
	// 40 cache-read tokens at the derived 0.1x rate ($0.30/MTok) = 12 µUSD.
	got, missing := tbl.CostUSDMicros("bedrock-global", "anthropic.claude-sonnet-4-6",
		Usage{CacheRead: 40_000})
	if missing {
		t.Fatal("rate must be present")
	}
	if got != 12_000 {
		t.Errorf("cache_read cost = %d µUSD, want 12000 (0.1 x $3.00/MTok); 0 means the derivation is missing", got)
	}

	// 5m write derives to 1.25x = $3.75/MTok, 1h write to 2x = $6.00/MTok.
	got5m, _ := tbl.CostUSDMicros("bedrock-global", "anthropic.claude-sonnet-4-6", Usage{CacheWrite5m: 1_000_000})
	if got5m != 3_750_000 {
		t.Errorf("cache_write_5m = %d, want 3750000", got5m)
	}
	got1h, _ := tbl.CostUSDMicros("bedrock-global", "anthropic.claude-sonnet-4-6", Usage{CacheWrite1h: 1_000_000})
	if got1h != 6_000_000 {
		t.Errorf("cache_write_1h = %d, want 6000000", got1h)
	}
}

// An explicitly declared cache rate must win over the derivation, so a special
// pricing agreement stays expressible.
func TestFromConfig_explicitCacheRateWinsOverDerivation(t *testing.T) {
	tbl := FromConfig("allow", map[string]map[string]ConfigRate{
		"p": {"m": {InputPerMTok: 3.0, OutputPerMTok: 15.0, CacheReadPerMTok: 0.01}},
	})
	got, _ := tbl.CostUSDMicros("p", "m", Usage{CacheRead: 1_000_000})
	if got != 10_000 {
		t.Errorf("explicit cache_read = %d, want 10000 ($0.01/MTok), not the derived 300000", got)
	}
}

// The derived figures must reproduce Bundled()'s hardcoded table exactly —
// that equivalence is the evidence the ratios are right.
func TestFromConfig_derivationMatchesBundledFigures(t *testing.T) {
	derived := FromConfig("allow", map[string]map[string]ConfigRate{
		"anthropic-direct": {
			"claude-sonnet-4-6": {InputPerMTok: 3.0, OutputPerMTok: 15.0},
			"claude-opus-4-8":   {InputPerMTok: 5.0, OutputPerMTok: 25.0},
		},
	})
	bundled := New(OnMissingAllow, Bundled())
	for _, model := range []string{"claude-sonnet-4-6", "claude-opus-4-8"} {
		u := Usage{Input: 1_000_000, Output: 1_000_000, CacheRead: 1_000_000, CacheWrite5m: 1_000_000, CacheWrite1h: 1_000_000}
		want, _ := bundled.CostUSDMicros("anthropic-direct", model, u)
		got, _ := derived.CostUSDMicros("anthropic-direct", model, u)
		if got != want {
			t.Errorf("%s: derived %d != bundled %d — the cache ratios do not reproduce the published table", model, got, want)
		}
	}
}

// ADR-030 zero-rate hole: a 0/0 override means UNPRICED, not free. It used to
// be inserted into the table anyway, so HasRate said true, `mayu pricing check`
// passed, `on_missing: "block"` booted, the runtime PricingGuard let it through,
// and settlement recorded missing=false at 0 uUSD — indistinguishable in the
// audit record from a genuinely free rate.
func TestFromConfig_zeroRateOverrideIsUnpriced(t *testing.T) {
	tbl := FromConfig("allow", map[string]map[string]ConfigRate{
		"bedrock-apne1": {"zai.glm-5": {InputPerMTok: 0, OutputPerMTok: 0}},
	})
	if tbl.HasRate("bedrock-apne1", "zai.glm-5") {
		t.Error("HasRate = true for a 0/0 override — 0 means unpriced, not free, so the row must not be in the table at all")
	}
	cost, missing := tbl.CostUSDMicros("bedrock-apne1", "zai.glm-5", Usage{Input: 1_000_000})
	if cost != 0 || !missing {
		t.Errorf("cost=%d missing=%v, want cost=0 missing=true", cost, missing)
	}
}

// The documented convention "0 + missing=false means genuinely free" stays
// expressible — with an explicit opt-in, so it can never be reached by omission.
func TestFromConfig_freeRateIsPricedAtZero(t *testing.T) {
	tbl := FromConfig("allow", map[string]map[string]ConfigRate{
		"selfhosted": {"llama3.3": {InputPerMTok: 0, OutputPerMTok: 0, Free: true}},
	})
	if !tbl.HasRate("selfhosted", "llama3.3") {
		t.Error("HasRate = false for free:true — an explicitly free rate IS a rate")
	}
	cost, missing := tbl.CostUSDMicros("selfhosted", "llama3.3", Usage{Input: 1_000_000, Output: 1_000_000, CacheRead: 1_000_000})
	if cost != 0 || missing {
		t.Errorf("cost=%d missing=%v, want cost=0 missing=false", cost, missing)
	}
}

// A single-sided zero is unusual but not provably wrong (a provider could bill
// only output), so it must still be inserted.
func TestFromConfig_singleSidedZeroIsStillPriced(t *testing.T) {
	tbl := FromConfig("allow", map[string]map[string]ConfigRate{
		"p": {"m": {InputPerMTok: 0, OutputPerMTok: 15.0}},
	})
	if !tbl.HasRate("p", "m") {
		t.Error("HasRate = false for input 0 / output 15 — only BOTH being zero means unpriced")
	}
	cost, missing := tbl.CostUSDMicros("p", "m", Usage{Output: 1_000_000})
	if missing || cost != 15_000_000 {
		t.Errorf("cost=%d missing=%v, want 15000000/false", cost, missing)
	}
}
