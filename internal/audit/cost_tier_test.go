package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestCostRefOldBytesRemainIdenticalWithContextTier(t *testing.T) {
	old := &CostRef{AmountUSDMicros: 644000, PricingVersion: "v1"}
	got, err := json.Marshal(old)
	const want = `{"amount_usd_micros":644000,"pricing_missing":false,"pricing_version":"v1"}`
	if err != nil || string(got) != want {
		t.Fatalf("legacy cost bytes changed: %s %v", got, err)
	}
	old.ContextTier = "long"
	got, err = json.Marshal(old)
	const tiered = `{"amount_usd_micros":644000,"pricing_missing":false,"pricing_version":"v1","context_tier":"long"}`
	if err != nil || string(got) != tiered {
		t.Fatalf("tier must be append-only: %s %v", got, err)
	}
}

func TestContextTierMixedVersionHashChain(t *testing.T) {
	const old = `{"schema_version":1,"event":"request_completed","id":"old","ts":"2026-09-09T00:00:00Z","instance":"i","principal":{"key_id":"opaque","team":"t"},"request":{"ingress":"responses","model_requested":"m","stream":false},"cost":{"amount_usd_micros":644000,"pricing_missing":false,"pricing_version":"v1"},"trace_id":null,"prev_hash":"sha256:genesis"}`
	var record Record
	if err := json.Unmarshal([]byte(old), &record); err != nil {
		t.Fatal(err)
	}
	line, err := record.Canonical()
	if err != nil || string(line) != old {
		t.Fatalf("legacy fixture changed: %s %v", line, err)
	}
	hash := func(b []byte) string { s := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(s[:]) }
	record.ID, record.PrevHash, record.Cost.ContextTier = "new", hash([]byte(old)), "long"
	next, _ := record.Canonical()
	record.ID, record.PrevHash, record.Cost.ContextTier = "old-again", hash(next), ""
	last, _ := record.Canonical()
	fixture := old + "\n" + string(next) + "\n" + string(last) + "\n"
	result, err := Verify(strings.NewReader(fixture))
	if err != nil || !result.OK || result.Records != 3 {
		t.Fatalf("mixed chain: %+v %v", result, err)
	}
	result, err = Verify(strings.NewReader(strings.Replace(fixture, `"context_tier":"long"`, `"context_tier":"short"`, 1)))
	if err != nil || result.OK || result.BrokenAt != 3 {
		t.Fatal("tier tampering was not detected")
	}
}
