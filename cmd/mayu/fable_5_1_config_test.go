package main

import (
	"testing"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/pricing"
)

func TestFable51Example(t *testing.T) {
	t.Setenv("INFERPLANE_ADMIN_TOKEN", "test-only")
	cfg, err := config.LoadRaw("../../examples/config.bedrock-fable-5-1.json")
	if err != nil {
		t.Fatal(err)
	}
	const id = "global.anthropic.claude-fable-5-1"
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
	for _, tc := range []struct {
		u    pricing.Usage
		want int64
	}{
		{pricing.Usage{Input: 1_000_000}, 10_000_000}, {pricing.Usage{Output: 1_000_000}, 50_000_000},
		{pricing.Usage{CacheRead: 1_000_000}, 250_000}, {pricing.Usage{CacheWrite5m: 1_000_000}, 12_500_000}, {pricing.Usage{CacheWrite1h: 1_000_000}, 20_000_000},
	} {
		got, missing := tbl.CostUSDMicros("bedrock-seoul", id, tc.u)
		if missing || got != tc.want {
			t.Fatalf("cost=%d missing=%v want=%d", got, missing, tc.want)
		}
	}
	if _, missing := tbl.CostUSDMicros("bedrock-seoul", "us.anthropic.claude-fable-5-1", pricing.Usage{Input: 1}); !missing {
		t.Fatal("Global price leaked to US CRIS")
	}
}
