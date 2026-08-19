package audit

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestUsageRefPreTierRecordsStayByteIdentical is the hash-chain guard: a record
// written before the cache-write TTL fields existed must marshal to the exact
// same bytes afterwards, or every mixed-version chain fails verification. The
// new fields are appended at the END with omitempty (the AuthMethod/BodyRef
// precedent), so an unset tier adds no key.
func TestUsageRefPreTierRecordsStayByteIdentical(t *testing.T) {
	rec := Record{
		SchemaVersion: 1,
		Event:         "request_completed",
		ID:            "01J0",
		TS:            "2026-08-19T00:00:00Z",
		Usage: &UsageRef{
			InputTokens:              10,
			OutputTokens:             5,
			CacheReadInputTokens:     40,
			CacheCreationInputTokens: 24,
		},
	}
	b, err := rec.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	const want = `"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":40,"cache_creation_input_tokens":24,"estimated":false}`
	if !strings.Contains(string(b), want) {
		t.Fatalf("pre-tier usage bytes changed:\n got %s\nwant substring %s", b, want)
	}
}

// TestUsageRefCarriesCacheWriteTiers pins that the two TTL tiers survive the
// audit record, so the billed 1.25x/2x split can be reconstructed from the
// ledger alone. Before this, only the flat total was written — and when the
// upstream sent ONLY the split (Anthropic's cache_creation object), the flat
// field was 0 and the audit record showed no cache write at all.
func TestUsageRefCarriesCacheWriteTiers(t *testing.T) {
	u := &UsageRef{
		InputTokens:                10,
		OutputTokens:               5,
		CacheCreationInputTokens:   24,
		CacheCreation5mInputTokens: 20,
		CacheCreation1hInputTokens: 4,
	}
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"cache_creation_input_tokens":24`,
		`"cache_creation_5m_input_tokens":20`,
		`"cache_creation_1h_input_tokens":4`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}

	var back UsageRef
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back != *u {
		t.Fatalf("round-trip lost data: got %+v want %+v", back, *u)
	}
}
