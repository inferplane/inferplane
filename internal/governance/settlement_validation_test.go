package governance

import (
	"math"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/pricing"
)

func TestInvalidSettlementCannotRefundQuotaOrTPM(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage pricing.Usage
	}{
		{"negative-output", pricing.Usage{Input: 10, Output: -1000}},
		{"negative-input", pricing.Usage{Input: -1}},
		{"negative-cache-read", pricing.Usage{CacheRead: -1}},
		{"negative-cache-write-5m", pricing.Usage{CacheWrite5m: -1}},
		{"negative-cache-write-1h", pricing.Usage{CacheWrite1h: -1}},
		{"overflow", pricing.Usage{Input: math.MaxInt64, Output: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := limiter.NewMemory()
			l.DebitQuota("quota:t", 900, 24*time.Hour)
			g := NewGovernor(map[string]TeamPolicy{"t": {TokensPerDay: 1000, TokensPerMinute: 1000}}, l, budget.NewMemory(), nil)
			d := g.PreCheck(Subject{Team: "t"}, KeyPolicy{}, 100)
			if !d.Allowed {
				t.Fatal(d)
			}
			defer d.Admission.Close()
			_, missing := g.Settle(Subject{Team: "t"}, KeyPolicy{}, "p", "m", tc.usage, testTable(), d.Admission)
			if !missing || l.QuotaUsed("quota:t", 24*time.Hour) != 900 || l.RateUsed("tpm:t", 1000, 1000) != 100 {
				t.Fatalf("invalid usage %+v changed counters: quota=%d rate=%d missing=%v", tc.usage, l.QuotaUsed("quota:t", 24*time.Hour), l.RateUsed("tpm:t", 1000, 1000), missing)
			}
		})
	}
}

func TestNegativeEstimateHasDistinctDenialWithoutConsumingLimits(t *testing.T) {
	l := limiter.NewMemory()
	g := NewGovernor(map[string]TeamPolicy{"t": {RatePerMin: 1, TokensPerMinute: 100}}, l, budget.NewMemory(), nil)
	s := Subject{Team: "t", KeyID: "k"}
	kp := KeyPolicy{RatePerMin: 1, TokensPerMinute: 100}
	d := g.PreCheck(s, kp, -1)
	if d.Allowed || d.Admission != nil || string(d.Code) != "invalid_token_estimate" {
		t.Fatalf("negative estimate misclassified: %+v", d)
	}
	d = g.PreCheck(s, kp, 100)
	if !d.Allowed {
		t.Fatalf("negative estimate consumed admission capacity: %+v", d)
	}
	defer d.Admission.Close()
}
