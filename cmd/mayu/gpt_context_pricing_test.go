package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/pricing"
)

func TestVerifiedGPTPriceSnapshotPricesBothProfilesAndContextTiers(t *testing.T) {
	data, err := os.ReadFile("../../examples/pricing/bedrock-gpt-standard-2026-10-02.json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Models map[string]struct {
			Regional config.RateConfig  `json:"regional"`
			Global   *config.RateConfig `json:"global"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Models) != 9 {
		t.Fatal("verified GPT model missing")
	}
	for model, entry := range snapshot.Models {
		for scope, rate := range map[string]*config.RateConfig{"regional": &entry.Regional, "global": entry.Global} {
			if rate == nil {
				continue
			}
			cfg := &config.Config{Pricing: config.PricingConfig{Overrides: map[string]map[string]config.RateConfig{"p": {model: *rate}}}}
			table := live.PricingTableFor(cfg)
			if !table.HasRate("p", model) || rate.ShortContextLimit != 272000 || rate.LongContext == nil {
				t.Fatalf("unpriced profile %s/%s", model, scope)
			}
			for _, total := range []int64{272000, 272001} {
				usage := pricing.Usage{Input: total, Output: 10000}
				got, missing := table.CostUSDMicros("p", model, usage)
				checked, err := table.CostUSDMicrosChecked("p", model, usage)
				if missing || err != nil || got != checked || got <= 0 {
					t.Fatalf("%s/%s total=%d cost=%d checked=%d err=%v", model, scope, total, got, checked, err)
				}
				want := "short"
				if total > 272000 {
					want = "long"
				}
				if table.ContextTier("p", model, usage) != want {
					t.Fatal("profile context boundary drift")
				}
			}
		}
	}
}

func TestGPT61NativeExampleUsesRegionalShortAndLongRates(t *testing.T) {
	t.Setenv("INFERPLANE_ADMIN_TOKEN", "test-only")
	cfg, err := config.LoadRaw("../../examples/config.bedrock-gpt-6-1-sol.json")
	if err != nil {
		t.Fatal(err)
	}
	table := live.PricingTableFor(cfg)
	for _, tc := range []struct{ input, cost int64 }{{272000, 708400}, {272001, 1361804}, {300000, 1485000}} {
		got, missing := table.CostUSDMicros("bedrock-responses-east", "openai.gpt-6.1-sol", pricing.Usage{Input: tc.input, Output: 10000})
		if missing || got != tc.cost {
			t.Fatalf("input=%d cost=%d want=%d missing=%v", tc.input, got, tc.cost, missing)
		}
	}
}
