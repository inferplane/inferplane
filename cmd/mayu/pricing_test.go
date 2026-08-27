package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writePricingTestConfig writes a minimal valid config whose single model routes
// to `upstream`, with pricing overrides taken verbatim from `overrides`.
func writePricingTestConfig(t *testing.T, upstream string, overrides map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	cfg := map[string]any{
		"server": map[string]any{"listen": "127.0.0.1:0", "admin_listen": "127.0.0.1:0"},
		"providers": map[string]any{
			"up": map[string]any{
				"type": "anthropic", "base_url": "https://api.anthropic.com",
				"api_key_ref": map[string]any{"env": "PRICING_TEST_KEY"},
			},
		},
		"models": map[string]any{
			"m": map[string]any{"targets": []any{map[string]any{"provider": "up", "model": upstream}}},
		},
	}
	if overrides != nil {
		cfg["pricing"] = map[string]any{"overrides": overrides}
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// `pricing check` is the CI guard for ADR-030: a newly added model with no rate
// must fail the build at the commit that introduced it, instead of quietly
// billing 0 uUSD in production.
func TestPricingCheck(t *testing.T) {
	t.Setenv("INFERPLANE_ADMIN_TOKEN", "lint")

	t.Run("unpriced route exits 1", func(t *testing.T) {
		p := writePricingTestConfig(t, "m", nil)
		if got := pricingCheck([]string{"--config", p}); got != 1 {
			t.Fatalf("exit = %d, want 1", got)
		}
	})

	t.Run("priced route exits 0", func(t *testing.T) {
		p := writePricingTestConfig(t, "m", map[string]any{
			"up": map[string]any{"m": map[string]any{"input_per_mtok": 3.0, "output_per_mtok": 15.0}},
		})
		if got := pricingCheck([]string{"--config", p}); got != 0 {
			t.Fatalf("exit = %d, want 0", got)
		}
	})

	// The check must agree with the gateway's own lookup, including the Bedrock
	// region-prefix fallback — otherwise CI would fail a config that boots fine.
	t.Run("region-prefixed route satisfied by the base rate exits 0", func(t *testing.T) {
		p := writePricingTestConfig(t, "global.anthropic.claude-opus-5", map[string]any{
			"up": map[string]any{
				"anthropic.claude-opus-5": map[string]any{"input_per_mtok": 5.0, "output_per_mtok": 25.0},
			},
		})
		if got := pricingCheck([]string{"--config", p}); got != 0 {
			t.Fatalf("exit = %d, want 0 — the base rate covers the global.-prefixed route", got)
		}
	})

	t.Run("missing config exits 2", func(t *testing.T) {
		if got := pricingCheck([]string{"--config", filepath.Join(t.TempDir(), "nope.json")}); got != 2 {
			t.Fatalf("exit = %d, want 2", got)
		}
	})

	// The CLI-level proof of the zero-rate hole fix: a 0/0 override must never
	// pass the guard. It exits 2, not 1: LoadRaw's validatePricing — the other
	// half of the same fix — rejects the override as a LOAD ERROR before
	// UnpricedTargets can run, so the "unpriced route" report is never reached.
	// Either way the CLI is loud and nonzero; what this pins is that a 0/0
	// placeholder can never read as "priced" (exit 0).
	t.Run("zero-rate override is refused at load", func(t *testing.T) {
		p := writePricingTestConfig(t, "m", map[string]any{
			"up": map[string]any{"m": map[string]any{"input_per_mtok": 0.0, "output_per_mtok": 0.0}},
		})
		if got := pricingCheck([]string{"--config", p}); got != 2 {
			t.Fatalf("exit = %d, want 2 (refused at load) — 0 must never read as a real rate", got)
		}
	})

	t.Run("free override exits 0", func(t *testing.T) {
		p := writePricingTestConfig(t, "m", map[string]any{
			"up": map[string]any{"m": map[string]any{"input_per_mtok": 0.0, "output_per_mtok": 0.0, "free": true}},
		})
		if got := pricingCheck([]string{"--config", p}); got != 0 {
			t.Fatalf("exit = %d, want 0 — a genuinely free model is priced", got)
		}
	})
}

func TestPricingCmd_usage(t *testing.T) {
	if got := pricingCmd(nil); got != 2 {
		t.Errorf("no subcommand: exit = %d, want 2", got)
	}
	if got := pricingCmd([]string{"bogus"}); got != 2 {
		t.Errorf("unknown subcommand: exit = %d, want 2", got)
	}
	// Asserts the DISPATCH to `pricing sync` exists, not the sync behaviour: an
	// unreadable --config fails in config.LoadRaw and returns 2 without ever
	// constructing an AWS client. Point it at a path that cannot exist rather
	// than relying on the default ./config.json being absent from this
	// package's directory — if one ever appeared there, the test would reach
	// the network.
	missing := filepath.Join(t.TempDir(), "nope.json")
	if got := pricingCmd([]string{"sync", "--config", missing}); got != 2 {
		t.Errorf("sync with an unreadable config: exit = %d, want 2", got)
	}
}
