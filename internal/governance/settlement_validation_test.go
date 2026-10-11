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
	for _, u := range []pricing.Usage{
		{Input: 10, Output: -1000}, {Input: -1}, {CacheRead: -1}, {CacheWrite5m: -1}, {CacheWrite1h: -1},
		{Input: math.MaxInt64, Output: 1},
	} {
		l := limiter.NewMemory()
		l.DebitQuota("quota:t", 900, 24*time.Hour)
		g := NewGovernor(map[string]TeamPolicy{"t": {TokensPerDay: 1000, TokensPerMinute: 1000}}, l, budget.NewMemory(), nil)
		d := g.PreCheck(Subject{Team: "t"}, KeyPolicy{}, 100)
		defer d.Admission.Close()
		if !d.Allowed {
			t.Fatal(d)
		}
		_, missing := g.Settle(Subject{Team: "t"}, KeyPolicy{}, "p", "m", u, testTable(), d.Admission)
		if !missing || l.QuotaUsed("quota:t", 24*time.Hour) != 900 || l.RateUsed("tpm:t", 1000, 1000) != 100 {
			t.Fatalf("invalid usage %+v changed counters: quota=%d rate=%d missing=%v", u, l.QuotaUsed("quota:t", 24*time.Hour), l.RateUsed("tpm:t", 1000, 1000), missing)
		}
	}
}
