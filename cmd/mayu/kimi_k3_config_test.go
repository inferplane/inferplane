package main

import (
	"testing"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/pricing"
)

// Exercise the shipped example with the real loader and settlement table,
// with a test-only admin token, without AWS credentials or starting a gateway.
func TestKimiK3Example(t *testing.T) {
	t.Setenv("INFERPLANE_ADMIN_TOKEN", "test-only-admin-token")
	cfg, err := config.LoadRaw("../../examples/config.bedrock-kimi-k3.json")
	if err != nil {
		t.Fatal(err)
	}
	const provider = "bedrock-seoul"
	const upstream = "global.moonshotai.kimi-k3"
	m, ok := cfg.Models["kimi-k3"]
	if !ok || len(m.Targets) != 1 {
		t.Fatalf("missing K3 route: %+v", m)
	}
	if m.ContextWindow != 1_000_000 || len(m.Capabilities) != 1 || m.Capabilities[0] != "tools" {
		t.Fatalf("invalid client metadata: %+v", m)
	}
	target := m.Targets[0]
	if target.Provider != provider || target.Model != upstream || target.API != "converse" {
		t.Fatalf("unexpected target: %+v", target)
	}
	p := cfg.Providers[provider]
	if p.Type != "bedrock" || p.Region != "ap-northeast-2" || p.Auth.Mode != "irsa" || p.Auth.Profile != "" {
		t.Fatalf("unexpected AWS config: %+v", p)
	}
	allowed := cfg.Teams["demo"].AllowedModels
	if len(allowed) != 1 || allowed[0] != "kimi-k3" {
		t.Fatalf("unexpected RBAC: %v", allowed)
	}
	tbl := live.PricingTableFor(cfg)
	if tbl.OnMissing() != pricing.OnMissingBlock {
		t.Fatal("unpriced traffic must block")
	}
	if missing := live.UnpricedTargets(cfg, tbl); len(missing) != 0 {
		t.Fatalf("unpriced: %v", missing)
	}
	for _, tc := range []struct {
		name  string
		usage pricing.Usage
		want  int64
	}{
		{"input", pricing.Usage{Input: 1_000_000}, 3_000_000},
		{"output", pricing.Usage{Output: 1_000_000}, 15_000_000},
		{"cache read", pricing.Usage{CacheRead: 1_000_000}, 300_000},
		{"untiered cache write", pricing.Usage{CacheWrite5m: 1_000_000}, 3_750_000},
		{"other write bucket", pricing.Usage{CacheWrite1h: 1_000_000}, 3_750_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, missing := tbl.CostUSDMicros(provider, upstream, tc.usage)
			if missing || got != tc.want {
				t.Fatalf("cost=%d missing=%v want=%d", got, missing, tc.want)
			}
		})
	}
	// US CRIS has a different rate: never silently reuse Global prices for it.
	if _, missing := tbl.CostUSDMicros(provider, "us.moonshotai.kimi-k3", pricing.Usage{Input: 1}); !missing {
		t.Fatal("US CRIS incorrectly inherits Global price")
	}
}
