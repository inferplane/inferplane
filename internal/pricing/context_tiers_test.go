package pricing

import (
	"math"
	"testing"
)

func contextTable() *Table {
	return New(OnMissingBlock, map[Key]Rate{{"p", "global.openai.gpt-6.1-sol"}: {
		InputPerMTok: 2_000_000, OutputPerMTok: 10_000_000,
		CacheReadPerMTok: 100_000, CacheWrite5mPerMTok: 2_500_000, CacheWrite1hPerMTok: 2_500_000,
		ShortContextLimit: 272_000,
		LongContext: &Rate{InputPerMTok: 4_000_000, OutputPerMTok: 15_000_000,
			CacheReadPerMTok: 200_000, CacheWrite5mPerMTok: 5_000_000, CacheWrite1hPerMTok: 5_000_000},
	}})
}

func TestContextTierUsesFullInputAndPricesWholeResponse(t *testing.T) {
	table := contextTable()
	for _, tc := range []struct {
		name string
		u    Usage
		cost int64
		tier string
	}{
		{"boundary", Usage{Input: 272000, Output: 10000}, 644000, "short"},
		{"one_above", Usage{Input: 272001, Output: 10000}, 1238004, "long"},
		{"all_request_not_excess", Usage{Input: 300000, Output: 10000}, 1350000, "long"},
		{"read_boundary", Usage{Input: 2000, CacheRead: 270000, Output: 10000}, 131000, "short"},
		{"read_above", Usage{Input: 2001, CacheRead: 270000, Output: 10000}, 212004, "long"},
		{"write_both_tiers", Usage{Input: 1, CacheWrite5m: 136000, CacheWrite1h: 136000, Output: 10000}, 1510004, "long"},
		{"output_not_context_trigger", Usage{Input: 1, Output: 272001}, 2720012, "short"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, missing := table.CostUSDMicros("p", "global.openai.gpt-6.1-sol", tc.u)
			checked, err := table.CostUSDMicrosChecked("p", "global.openai.gpt-6.1-sol", tc.u)
			if missing || err != nil || got != tc.cost || checked != tc.cost {
				t.Fatalf("legacy=%d checked=%d want=%d missing=%v err=%v", got, checked, tc.cost, missing, err)
			}
			if tier := table.ContextTier("p", "global.openai.gpt-6.1-sol", tc.u); tier != tc.tier {
				t.Fatalf("tier=%q want %q", tier, tc.tier)
			}
		})
	}
}

func TestContextSelectionIsPerResponseAndOverflowSafe(t *testing.T) {
	table := contextTable()
	for i := 0; i < 2; i++ {
		if got := table.ContextTier("p", "global.openai.gpt-6.1-sol", Usage{Input: 200000}); got != "short" {
			t.Fatal("separate responses must not accumulate into long context")
		}
	}
	if got := table.ContextTier("p", "global.openai.gpt-6.1-sol", Usage{Input: math.MaxInt64, CacheRead: math.MaxInt64}); got != "long" {
		t.Fatal("overflowed full input selected short context")
	}
	if _, err := table.CostUSDMicrosChecked("p", "global.openai.gpt-6.1-sol", Usage{Input: -1, CacheRead: 300000}); err == nil {
		t.Fatal("negative input became valid through tier selection")
	}
}

func TestAuthorityBoundCoversBothTiersEvenIfOneRateDecreases(t *testing.T) {
	table := New(OnMissingBlock, map[Key]Rate{{"p", "m"}: {
		InputPerMTok: 10_000_000, OutputPerMTok: 1_000_000, ShortContextLimit: 100,
		LongContext: &Rate{InputPerMTok: 1_000_000, OutputPerMTok: 20_000_000},
	}})
	bound, err := table.AuthorityBound("p", "m", 100, 100)
	if err != nil || bound != 3000 {
		t.Fatalf("bound=%d err=%v; must bound each class at its highest applicable tier", bound, err)
	}
	for _, u := range []Usage{{Input: 100, Output: 100}, {Input: 100, CacheRead: 100, Output: 100}} {
		cost, missing := table.CostUSDMicros("p", "m", u)
		if missing || cost > bound {
			t.Fatalf("cost=%d exceeds bound=%d", cost, bound)
		}
	}
}

func TestInvalidTierDeclarationCannotBecomePriced(t *testing.T) {
	for _, rate := range []Rate{
		{InputPerMTok: 1, ShortContextLimit: 100},
		{InputPerMTok: 1, LongContext: &Rate{InputPerMTok: 2}},
		{InputPerMTok: 1, ShortContextLimit: -1, LongContext: &Rate{InputPerMTok: 2}},
		{InputPerMTok: 1, ShortContextLimit: 100, LongContext: &Rate{InputPerMTok: -1}},
		{InputPerMTok: 1, ShortContextLimit: 100, LongContext: &Rate{InputPerMTok: 2, ShortContextLimit: 100}},
	} {
		table := New(OnMissingBlock, map[Key]Rate{{"p", "m"}: rate})
		if table.HasRate("p", "m") {
			t.Fatal("invalid tier passed pricing guard")
		}
		if _, missing := table.CostUSDMicros("p", "m", Usage{Input: 1}); !missing {
			t.Fatal("invalid tier was priced")
		}
		if _, err := table.AuthorityBound("p", "m", 100, 100); err == nil {
			t.Fatal("invalid tier granted authority")
		}
	}
}

func TestTableOwnsLongContextSnapshot(t *testing.T) {
	long := &Rate{InputPerMTok: 4_000_000}
	table := New(OnMissingBlock, map[Key]Rate{{"p", "m"}: {InputPerMTok: 2_000_000, ShortContextLimit: 1, LongContext: long}})
	long.InputPerMTok = 1
	if got, _ := table.CostUSDMicros("p", "m", Usage{Input: 100}); got != 400 {
		t.Fatal("caller mutated the captured pricing generation")
	}
}

func TestUnsupportedCacheWriteUsageIsUnavailableNotFree(t *testing.T) {
	table := FromConfig("block", map[string]map[string]ConfigRate{"p": {"m": {InputPerMTok: 2.75, OutputPerMTok: 16.5, CacheWriteUnsupported: true, ShortContextLimit: 272000,
		LongContext: &ConfigRate{InputPerMTok: 5.5, OutputPerMTok: 24.75, CacheWriteUnsupported: true}}}})
	if !table.HasRate("p", "m") {
		t.Fatal("published input/output/read rates missing")
	}
	if _, err := table.CostUSDMicrosChecked("p", "m", Usage{Input: 300000, CacheWrite5m: 1}); err == nil {
		t.Fatal("unpublished cache-write price was fabricated")
	}
	if _, missing := table.CostUSDMicros("p", "m", Usage{Input: 300000, CacheWrite1h: 1}); !missing {
		t.Fatal("unsupported write recorded as free")
	}
}

func TestInvalidExactOverrideCannotBorrowBundledOrBaseRate(t *testing.T) {
	table := FromConfig("block", map[string]map[string]ConfigRate{"p": {
		"openai.gpt-6-sol":        {InputPerMTok: 2, OutputPerMTok: 10},
		"global.openai.gpt-6-sol": {InputPerMTok: 2, ShortContextLimit: 272000},
	}})
	if table.HasRate("p", "global.openai.gpt-6-sol") {
		t.Fatal("invalid exact declaration fell through to cheaper base row")
	}
	table = FromConfig("block", map[string]map[string]ConfigRate{"anthropic-direct": {"claude-sonnet-4-6": {InputPerMTok: 3, ShortContextLimit: 272000}}})
	if table.HasRate("anthropic-direct", "claude-sonnet-4-6") {
		t.Fatal("invalid override fell through to bundled row")
	}
}
