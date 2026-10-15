package pricing

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/inferplane/inferplane/pkg/schema"
)

func TestPartialCacheQuantityRemainsPriced(t *testing.T) {
	var u schema.Usage
	if err := json.Unmarshal([]byte(`{"input_tokens":2,"output_tokens":3,"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":40}}`), &u); err != nil {
		t.Fatal(err)
	}
	p := FromUsage(&u)
	if total, err := p.TotalTokens(); err != nil || total != 105 {
		t.Fatalf("total=%d err=%v", total, err)
	}
	table := New(OnMissingAllow, map[Key]Rate{{"p", "m"}: {
		InputPerMTok: 1_000_000, OutputPerMTok: 1_000_000, CacheWrite5mPerMTok: 1_250_000,
	}})
	if cost, err := table.CostUSDMicrosChecked("p", "m", p); err != nil || cost != 130 {
		t.Fatalf("estimated cost=%d err=%v", cost, err)
	}
	if u.CacheWriteAccounting().TTLKnown {
		t.Fatal("estimated 5m remainder became proven TTL")
	}
}

func TestInvalidObservationsCannotBecomeKnownCost(t *testing.T) {
	table := New(OnMissingAllow, map[Key]Rate{{"p", "m"}: {InputPerMTok: 1_000_000}})
	for _, u := range []Usage{{Input: -1}, {Input: math.MaxInt64, Output: 1}, {Invalid: true}} {
		if cost, missing := table.CostUSDMicros("p", "m", u); cost != 0 || !missing {
			t.Fatalf("invalid usage %+v produced cost=%d missing=%v", u, cost, missing)
		}
	}
}

func TestContradictoryCacheSplitCannotBecomePriced(t *testing.T) {
	for _, raw := range []string{
		`{"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":101}}`,
		`{"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":40,"ephemeral_1h_input_tokens":10}}`,
		`{"cache_creation_input_tokens":-1}`,
	} {
		var u schema.Usage
		if err := json.Unmarshal([]byte(raw), &u); err != nil {
			t.Fatal(err)
		}
		if _, err := FromUsage(&u).TotalTokens(); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestPartialCacheQuantitySelectsLongPrice(t *testing.T) {
	var u schema.Usage
	if err := json.Unmarshal([]byte(`{"input_tokens":271950,"output_tokens":0,"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":40}}`), &u); err != nil {
		t.Fatal(err)
	}
	p := FromUsage(&u)
	if total, err := p.TotalTokens(); err != nil || total != 272050 {
		t.Fatalf("total=%d err=%v", total, err)
	}
	table := contextTable()
	if tier := table.ContextTier("p", "global.openai.gpt-6.1-sol", p); tier != "long" {
		t.Fatalf("tier=%s", tier)
	}
	if cost, err := table.CostUSDMicrosChecked("p", "global.openai.gpt-6.1-sol", p); err != nil || cost != 1088300 {
		t.Fatalf("estimated cost=%d err=%v", cost, err)
	}
	if u.CacheWriteAccounting().TTLKnown {
		t.Fatal("estimated 5m remainder became proven TTL")
	}
}
