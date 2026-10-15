package live

import (
	"testing"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/pricing"
)

func TestPricingContextConfigMirrorsAndRejectsMalformedOverlays(t *testing.T) {
	rc := config.RateConfig{InputPerMTok: 2, OutputPerMTok: 10, ShortContextLimit: 272000,
		LongContext: &config.RateConfig{InputPerMTok: 4, OutputPerMTok: 15, CacheReadPerMTok: .2}}
	cfg := &config.Config{Pricing: config.PricingConfig{Overrides: map[string]map[string]config.RateConfig{"p": {"m": rc}}}}
	table := PricingTableFor(cfg)
	if got, missing := table.CostUSDMicros("p", "m", pricing.Usage{Input: 300000, Output: 10000}); missing || got != 1350000 {
		t.Fatalf("mirror lost tier: %d %v", got, missing)
	}
	rc.LongContext.LongContext = &config.RateConfig{InputPerMTok: 8}
	cfg.Pricing.Overrides["p"]["m"] = rc
	if PricingTableFor(cfg).HasRate("p", "m") {
		t.Fatal("invalid UI/hot-reload nesting passed rate coverage")
	}
}
