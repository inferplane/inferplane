package main

import (
	"testing"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/pricing"
)

func TestOpus55Example(t *testing.T) {
	t.Setenv("INFERPLANE_ADMIN_TOKEN", "test-only")
	cfg, err := config.LoadRaw("../../examples/config.bedrock-opus-5-5.json")
	if err != nil {
		t.Fatal(err)
	}
	const id = "global.anthropic.claude-opus-5-5"
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
	// Cache read is 0.05x input; a derived 0.1x rate would bill 400_000.
	for _, tc := range []struct {
		u    pricing.Usage
		want int64
	}{
		{pricing.Usage{Input: 1_000_000}, 4_000_000}, {pricing.Usage{Output: 1_000_000}, 20_000_000},
		{pricing.Usage{CacheRead: 1_000_000}, 200_000}, {pricing.Usage{CacheWrite5m: 1_000_000}, 5_000_000}, {pricing.Usage{CacheWrite1h: 1_000_000}, 8_000_000},
	} {
		got, missing := tbl.CostUSDMicros("bedrock-seoul", id, tc.u)
		if missing || got != tc.want {
			t.Fatalf("cost=%d missing=%v want=%d", got, missing, tc.want)
		}
	}
	if _, missing := tbl.CostUSDMicros("bedrock-seoul", "us.anthropic.claude-opus-5-5", pricing.Usage{Input: 1}); !missing {
		t.Fatal("Global price leaked to US CRIS")
	}
}
