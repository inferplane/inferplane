package pricing

import (
	"encoding/json"
	"testing"

	"github.com/inferplane/inferplane/pkg/schema"
)

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
