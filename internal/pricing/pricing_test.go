package pricing

import "testing"

func TestCostUSDMicrosRoundHalfEven(t *testing.T) {
	tbl := New(OnMissingAllow, map[Key]Rate{
		{"anthropic-direct", "claude-sonnet-4-6"}: {InputPerMTok: 3_000_000, OutputPerMTok: 15_000_000, CacheReadPerMTok: 300_000, CacheWrite5mPerMTok: 3_750_000, CacheWrite1hPerMTok: 6_000_000},
	})
	// 1000 input, 500 output, 45000 cache_read, 1024 cache_write(5m)
	u := Usage{Input: 1000, Output: 500, CacheRead: 45000, CacheWrite5m: 1024}
	cost, missing := tbl.CostUSDMicros("anthropic-direct", "claude-sonnet-4-6", u)
	if missing {
		t.Fatal("rate present, should not be missing")
	}
	// input 1000*3_000_000/1e6=3000; output 500*15_000_000/1e6=7500;
	// cache_read 45000*300_000/1e6=13500; cache_write5m 1024*3_750_000/1e6=3840
	want := int64(3000 + 7500 + 13500 + 3840)
	if cost != want {
		t.Fatalf("cost = %d µUSD, want %d", cost, want)
	}
}

func TestOnMissingAllowReturnsZeroAndMissing(t *testing.T) {
	tbl := New(OnMissingAllow, nil)
	cost, missing := tbl.CostUSDMicros("p", "unknown-model", Usage{Input: 100})
	if cost != 0 || !missing {
		t.Fatalf("missing model: cost=%d missing=%v (want 0,true)", cost, missing)
	}
}

func TestOnMissingBlock(t *testing.T) {
	tbl := New(OnMissingBlock, nil)
	if tbl.OnMissing() != OnMissingBlock {
		t.Fatal("on_missing policy not stored")
	}
}

func TestCacheWriteTTLTiers(t *testing.T) {
	tbl := New(OnMissingAllow, map[Key]Rate{
		{"p", "m"}: {CacheWrite5mPerMTok: 1_250_000, CacheWrite1hPerMTok: 2_000_000},
	})
	c5, _ := tbl.CostUSDMicros("p", "m", Usage{CacheWrite5m: 1_000_000})
	c1h, _ := tbl.CostUSDMicros("p", "m", Usage{CacheWrite1h: 1_000_000})
	if c5 != 1_250_000 || c1h != 2_000_000 {
		t.Fatalf("ttl tiers: 5m=%d 1h=%d", c5, c1h)
	}
}

// ADR-030: Bedrock cross-region inference profiles are the same model reached
// through different routing and carry no published price differential, so one
// rate row must cover every prefix. The deployed demo routed to
// `global.anthropic.*` while pricing only the unprefixed id — every request
// billed zero.
func TestCostUSDMicros_stripsBedrockRegionPrefix(t *testing.T) {
	tbl := New(OnMissingAllow, map[Key]Rate{
		{Provider: "bedrock", Model: "anthropic.claude-opus-5"}: {InputPerMTok: 5_000_000},
	})
	for _, model := range []string{
		"anthropic.claude-opus-5",
		"global.anthropic.claude-opus-5",
		"us.anthropic.claude-opus-5",
		"eu.anthropic.claude-opus-5",
		"apac.anthropic.claude-opus-5",
	} {
		got, missing := tbl.CostUSDMicros("bedrock", model, Usage{Input: 1_000_000})
		if missing {
			t.Errorf("%s: rate reported missing — prefix not stripped", model)
			continue
		}
		if got != 5_000_000 {
			t.Errorf("%s: cost = %d, want 5000000", model, got)
		}
	}
}

// An exact match must always win, so a per-prefix special rate is declarable.
func TestCostUSDMicros_exactMatchBeatsStrippedPrefix(t *testing.T) {
	tbl := New(OnMissingAllow, map[Key]Rate{
		{Provider: "bedrock", Model: "anthropic.claude-opus-5"}:        {InputPerMTok: 5_000_000},
		{Provider: "bedrock", Model: "global.anthropic.claude-opus-5"}: {InputPerMTok: 9_000_000},
	})
	got, _ := tbl.CostUSDMicros("bedrock", "global.anthropic.claude-opus-5", Usage{Input: 1_000_000})
	if got != 9_000_000 {
		t.Errorf("cost = %d, want the prefix-specific 9000000", got)
	}
}

// Model VERSIONS are never collapsed: a rate for one version must not silently
// price a different one, even though today's published prices coincide.
func TestCostUSDMicros_doesNotCollapseModelVersions(t *testing.T) {
	tbl := New(OnMissingAllow, map[Key]Rate{
		{Provider: "bedrock", Model: "anthropic.claude-opus-4-8"}: {InputPerMTok: 5_000_000},
	})
	if _, missing := tbl.CostUSDMicros("bedrock", "anthropic.claude-opus-5", Usage{Input: 100}); !missing {
		t.Fatal("a different model version must report missing, not borrow another version's rate")
	}
}

func TestHasRate_matchesCostLookup(t *testing.T) {
	tbl := New(OnMissingAllow, map[Key]Rate{
		{Provider: "bedrock", Model: "anthropic.claude-opus-5"}: {InputPerMTok: 5_000_000},
	})
	cases := map[string]bool{
		"anthropic.claude-opus-5":        true,
		"global.anthropic.claude-opus-5": true,
		"anthropic.claude-opus-4-8":      false,
		"global.anthropic.claude-fake":   false,
	}
	for model, want := range cases {
		got := tbl.HasRate("bedrock", model)
		if got != want {
			t.Errorf("HasRate(%q) = %v, want %v", model, got, want)
		}
		// HasRate must agree with what actually gets billed — boot validation
		// and `pricing check` both rely on that equivalence.
		_, missing := tbl.CostUSDMicros("bedrock", model, Usage{Input: 1})
		if got == missing {
			t.Errorf("HasRate(%q)=%v disagrees with CostUSDMicros missing=%v", model, got, missing)
		}
	}
}
