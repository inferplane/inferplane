package pricing

import (
	"math"

	"github.com/inferplane/inferplane/pkg/schema"
)

// FromUsage projects observations without changing passthrough bytes. TTL
// uncertainty retains the legacy 5m estimate; missing token quantities cannot
// justify returning previously reserved TPM capacity.
func FromUsage(u *schema.Usage) Usage {
	if u == nil {
		return Usage{Uncertain: true}
	}
	number := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	c := u.CacheWriteAccounting()
	return Usage{
		Input: number(u.InputTokens), Output: number(u.OutputTokens), CacheRead: number(u.CacheReadInputTokens),
		CacheWrite5m: c.FiveMinutes, CacheWrite1h: c.OneHour, Invalid: c.Invalid,
		Uncertain: u.AccountingUncertain || u.InputTokens == nil || u.OutputTokens == nil || !c.QuantityKnown,
	}
}

// TotalTokens validates before any quota debit or TPM correction. Checking
// individual classes is necessary even if a negative class cancels a positive
// one; checking each addition prevents wraparound refunds.
func (u Usage) TotalTokens() (int64, error) {
	if u.Invalid {
		return 0, ErrInvalidCost
	}
	var total int64
	for _, n := range []int64{u.Input, u.Output, u.CacheRead, u.CacheWrite5m, u.CacheWrite1h} {
		if n < 0 || n > math.MaxInt64-total {
			return 0, ErrInvalidCost
		}
		total += n
	}
	return total, nil
}
