package live

import (
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/pricing"
)

// zeroRateFixture is the shared shape of these tests: one provider `up`, one
// model `m` routed to it, and a pricing override for that exact route.
func zeroRateFixture(onMissing string, rc config.RateConfig) *config.Config {
	return &config.Config{
		Providers: map[string]config.ProviderConfig{
			"up": {Type: "anthropic", BaseURL: "https://api.anthropic.com", APIKey: "sk-x"},
		},
		Models: map[string]config.ModelConfig{
			"m": {Targets: []config.Target{{Provider: "up", Model: "m"}}},
		},
		Pricing: config.PricingConfig{
			OnMissing: onMissing,
			Overrides: map[string]map[string]config.RateConfig{"up": {"m": rc}},
		},
	}
}

// ADR-030 zero-rate hole: a `{"input_per_mtok": 0, "output_per_mtok": 0}`
// override is an unfinished placeholder, not a price — it must be treated as
// UNPRICED, exactly as if no override existed. The load-time validation in
// internal/config rejects the same shape, but it cannot protect this layer:
// BuildState is also reached by paths that never go through config.Load — the
// ADR-008 UI-write provider overlay and every hot reload — so the table
// assembly must refuse the row itself.
func TestBuildState_zeroRateOverrideIsUnpriced(t *testing.T) {
	if _, _, err := BuildState(zeroRateFixture("block", config.RateConfig{})); err == nil {
		t.Error(`on_missing "block" with a 0/0 override must refuse to boot`)
	} else if !strings.Contains(err.Error(), "up/m") {
		t.Errorf("error must name the unpriced route, got: %v", err)
	}

	cfg := zeroRateFixture("allow", config.RateConfig{})
	if _, _, err := BuildState(cfg); err != nil {
		t.Fatalf(`on_missing "allow" with a 0/0 override must boot (warn only), got: %v`, err)
	}
	got := UnpricedTargets(cfg, pricingFromConfig(cfg))
	if len(got) != 1 || got[0] != "up/m" {
		t.Errorf("UnpricedTargets = %v, want [up/m] — a 0/0 override must not count as a rate", got)
	}
}

// The `free: true` escape hatch: a genuinely zero-cost model must boot under
// `block` and must NOT be listed as unpriced. This test exists because the
// mutation "delete `Free: rc.Free,` from pricingFromConfig" survived every
// existing test in the repo — without it, a `free: true` override silently
// becomes an unpriced row, the exact inversion of the bug the field exists to
// fix.
func TestBuildState_freeOverrideIsPricedUnderBlock(t *testing.T) {
	cfg := zeroRateFixture("block", config.RateConfig{Free: true})
	if _, _, err := BuildState(cfg); err != nil {
		t.Fatalf("a free model must boot under block: %v", err)
	}
	if got := UnpricedTargets(cfg, pricingFromConfig(cfg)); len(got) != 0 {
		t.Errorf("UnpricedTargets = %v, want empty — free means priced at 0, not unpriced", got)
	}
}

// End to end through the live assembly: a free model costs 0 with
// missing=false — the documented encoding for "genuinely free", distinct from
// the (0, true) an unpriced route settles at. The CacheRead usage also proves
// the DERIVED cache rates stay 0 for a free model (0.1 × 0 is 0), so a free
// model never acquires a nonzero cache rate by derivation.
func TestPricingFromConfig_freeOverrideCostsZeroAndIsNotMissing(t *testing.T) {
	cfg := zeroRateFixture("allow", config.RateConfig{Free: true})
	tbl := pricingFromConfig(cfg)
	cost, missing := tbl.CostUSDMicros("up", "m", pricing.Usage{Input: 1_000_000, Output: 1_000_000, CacheRead: 1_000_000})
	if cost != 0 || missing {
		t.Errorf("free model: cost=%d missing=%v, want (0, false)", cost, missing)
	}
}

// Only BOTH rates being zero means unpriced — an output-only rate is a real
// price. This guards against tightening Unpriced's `&&` to `||`, which would
// make a single-sided rate unpriced. (The 15 USD/MTok figure is FAKE, a round
// number for the arithmetic, not a published rate.)
func TestPricingFromConfig_singleSidedZeroIsStillPriced(t *testing.T) {
	cfg := zeroRateFixture("allow", config.RateConfig{InputPerMTok: 0, OutputPerMTok: 15})
	tbl := pricingFromConfig(cfg)
	if got := UnpricedTargets(cfg, tbl); len(got) != 0 {
		t.Errorf("UnpricedTargets = %v, want empty — a single-sided zero is still a price", got)
	}
	cost, missing := tbl.CostUSDMicros("up", "m", pricing.Usage{Output: 1_000_000})
	if cost != 15_000_000 || missing {
		t.Errorf("output-only rate: cost=%d missing=%v, want (15000000, false)", cost, missing)
	}
}
