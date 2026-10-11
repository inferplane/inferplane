package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/pkg/schema"
)

func TestUsageEvidenceDistinguishesObservedTTLFromFallback(t *testing.T) {
	n := func(v int64) *int64 { return &v }
	for _, tc := range []struct {
		name           string
		u              schema.Usage
		ttl, uncertain bool
	}{
		{"zero", schema.Usage{InputTokens: n(0), OutputTokens: n(0)}, true, false},
		{"flat write", schema.Usage{InputTokens: n(2), OutputTokens: n(3), CacheCreationInputTokens: n(10)}, false, true},
		{"explicit split", schema.Usage{InputTokens: n(2), OutputTokens: n(3), CacheCreationInputTokens: n(10), CacheCreation: &schema.CacheCreation{Ephemeral5mInputTokens: n(4), Ephemeral1hInputTokens: n(6)}}, true, false},
		{"partial split", schema.Usage{InputTokens: n(2), OutputTokens: n(3), CacheCreation: &schema.CacheCreation{Ephemeral1hInputTokens: n(6)}}, false, true},
		{"partial with total", schema.Usage{InputTokens: n(2), OutputTokens: n(3), CacheCreationInputTokens: n(100), CacheCreation: &schema.CacheCreation{Ephemeral5mInputTokens: n(40)}}, false, true},
		{"inconsistent total", schema.Usage{InputTokens: n(2), OutputTokens: n(3), CacheCreationInputTokens: n(11), CacheCreation: &schema.CacheCreation{Ephemeral5mInputTokens: n(4), Ephemeral1hInputTokens: n(6)}}, false, true},
		{"new TTL", schema.Usage{InputTokens: n(2), OutputTokens: n(3), CacheCreation: &schema.CacheCreation{Ephemeral5mInputTokens: n(0), Ephemeral1hInputTokens: n(0), Extra: map[string]json.RawMessage{"ephemeral_30m_input_tokens": json.RawMessage("10")}}}, false, true},
		{"missing input", schema.Usage{OutputTokens: n(3)}, true, true},
		{"provider uncertainty", schema.Usage{InputTokens: n(2), OutputTokens: n(3), AccountingUncertain: true}, false, true},
		{"negative", schema.Usage{InputTokens: n(2), OutputTokens: n(3), CacheReadInputTokens: n(-1)}, true, true},
		{"overflow", schema.Usage{InputTokens: n(math.MaxInt64), OutputTokens: n(3), CacheReadInputTokens: n(1)}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := json.Marshal(tc.u)
			got := UsageFrom(&tc.u)
			if got.Evidence == nil || got.Evidence.CacheWriteTTLKnown != tc.ttl || got.Evidence.AccountingUncertain != tc.uncertain {
				t.Fatalf("evidence=%+v want ttl=%v uncertain=%v", got.Evidence, tc.ttl, tc.uncertain)
			}
			if got.Evidence.InputKnown != (tc.u.InputTokens != nil && *tc.u.InputTokens >= 0) {
				t.Fatal("missing input became a measured zero")
			}
			wantTotalKnown := tc.name == "zero" || tc.name == "flat write" || tc.name == "explicit split" || tc.name == "partial with total"
			if got.Evidence.InputTotalKnown != wantTotalKnown {
				t.Fatalf("input total known=%v want=%v", got.Evidence.InputTotalKnown, wantTotalKnown)
			}
			if tc.name == "partial with total" && got.CacheCreationInputTokens != 100 {
				t.Fatalf("known cache quantity lost: %d", got.CacheCreationInputTokens)
			}
			after, _ := json.Marshal(tc.u)
			if string(before) != string(after) {
				t.Fatal("evidence changed passthrough usage")
			}
		})
	}
	if UsageFrom(nil) != nil {
		t.Fatal("missing usage must stay missing")
	}
}

func TestUsageEvidenceMixedVersionChain(t *testing.T) {
	const old = `{"schema_version":1,"event":"request_completed","id":"old","ts":"2026-10-02T00:00:00Z","instance":"i","principal":{"key_id":"opaque","team":"t"},"request":{"ingress":"responses","model_requested":"m","stream":false},"usage":{"input_tokens":1,"output_tokens":2,"estimated":false},"trace_id":null,"prev_hash":"sha256:genesis"}`
	var rec Record
	if err := json.Unmarshal([]byte(old), &rec); err != nil {
		t.Fatal(err)
	}
	line, err := rec.Canonical()
	if err != nil || string(line) != old {
		t.Fatalf("old bytes changed: %s %v", line, err)
	}
	hash := func(b []byte) string { h := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(h[:]) }
	rec.ID, rec.PrevHash = "new", hash(line)
	rec.Usage.Evidence = &UsageEvidence{InputKnown: true, OutputKnown: true, AccountingUncertain: true}
	next, _ := rec.Canonical()
	rec.ID, rec.PrevHash, rec.Usage.Evidence = "old-again", hash(next), nil
	last, _ := rec.Canonical()
	fixture := old + "\n" + string(next) + "\n" + string(last) + "\n"
	result, err := Verify(strings.NewReader(fixture))
	if err != nil || !result.OK || result.Records != 3 {
		t.Fatalf("mixed chain: %+v %v", result, err)
	}
	result, err = Verify(strings.NewReader(strings.Replace(fixture, `"accounting_uncertain":true`, `"accounting_uncertain":false`, 1)))
	if err != nil || result.OK || result.BrokenAt != 3 {
		t.Fatal("evidence tampering was not detected")
	}
}

func TestUsageEvidenceDoesNotWrapCacheWriteTotal(t *testing.T) {
	zero, five, hour := int64(0), int64(math.MaxInt64), int64(1)
	u := &schema.Usage{InputTokens: &zero, OutputTokens: &zero,
		CacheCreation: &schema.CacheCreation{Ephemeral5mInputTokens: &five, Ephemeral1hInputTokens: &hour}}
	got := UsageFrom(u)
	if got.CacheCreationInputTokens != 0 {
		t.Fatalf("unrepresentable total must be omitted, not wrapped: %d", got.CacheCreationInputTokens)
	}
	if got.CacheCreation5mInputTokens != five || got.CacheCreation1hInputTokens != hour {
		t.Fatal("individual observed counts were changed")
	}
	if got.Evidence.InputTotalKnown || !got.Evidence.AccountingUncertain {
		t.Fatal("overflow was represented as known usage")
	}
	raw, err := json.Marshal(got)
	if err != nil || strings.Contains(string(raw), `"cache_creation_input_tokens"`) {
		t.Fatalf("invalid derived total serialized: %s %v", raw, err)
	}
}
