package main

import (
	"testing"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/pricing"
)

func TestSonnet55Example(t *testing.T) {
	t.Setenv("INFERPLANE_ADMIN_TOKEN", "test-only")
	cfg, err := config.LoadRaw("../../examples/config.bedrock-sonnet-5-5.json")
	if err != nil {
		t.Fatal(err)
	}
	const id = "global.anthropic.claude-sonnet-5-5"
	model := cfg.Models[id]
	if model.ContextWindow != 1_000_000 || len(model.Targets) != 1 || model.Targets[0].Model != id || model.Targets[0].API != "invoke_model" {
		t.Fatalf("bad route %+v", model)
	}
	if len(model.Capabilities) != 1 || model.Capabilities[0] != "tools" {
		t.Fatalf("missing client tools metadata: %+v", model)
	}
	tbl := live.PricingTableFor(cfg)
	if tbl.OnMissing() != pricing.OnMissingBlock || len(live.UnpricedTargets(cfg, tbl)) != 0 {
		t.Fatal("pricing not fail-closed")
	}
	// Declared rates; cache read is 0.05x input, like Opus 5.5.
	for _, tc := range []struct {
		u    pricing.Usage
		want int64
	}{
		{pricing.Usage{Input: 1_000_000}, 2_000_000}, {pricing.Usage{Output: 1_000_000}, 10_000_000},
		{pricing.Usage{CacheRead: 1_000_000}, 100_000}, {pricing.Usage{CacheWrite5m: 1_000_000}, 2_500_000}, {pricing.Usage{CacheWrite1h: 1_000_000}, 4_000_000},
	} {
		got, missing := tbl.CostUSDMicros("bedrock-seoul", id, tc.u)
		if missing || got != tc.want {
			t.Fatalf("cost=%d missing=%v want=%d", got, missing, tc.want)
		}
	}
	if _, missing := tbl.CostUSDMicros("bedrock-seoul", "us.anthropic.claude-sonnet-5-5", pricing.Usage{Input: 1}); !missing {
		t.Fatal("Global price leaked to US CRIS")
	}
}
