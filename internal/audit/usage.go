package audit

import (
	"math"

	"github.com/inferplane/inferplane/pkg/schema"
)

// UsageEvidence describes the interpreted token fields, not invoice coverage.
// It does not authorize a refund or replace authority's settlement validation.
type UsageEvidence struct {
	InputKnown          bool `json:"input_known"`
	OutputKnown         bool `json:"output_known"`
	InputTotalKnown     bool `json:"input_total_known"`
	CacheWriteTTLKnown  bool `json:"cache_write_ttl_known"`
	AccountingUncertain bool `json:"accounting_uncertain"`
}

// UsageFrom preserves the legacy settlement mapping while recording what was
// actually known before nil counts and unknown cache TTLs became numeric zeros
// and 5m estimates. It never mutates the wire usage.
func UsageFrom(u *schema.Usage) *UsageRef {
	if u == nil {
		return nil
	}
	number := func(n *int64) int64 {
		if n == nil {
			return 0
		}
		return *n
	}
	w5, w1 := u.CacheWriteTiers()
	e := &UsageEvidence{
		InputKnown:  u.InputTokens != nil && *u.InputTokens >= 0,
		OutputKnown: u.OutputTokens != nil && *u.OutputTokens >= 0,
	}
	c := u.CacheWriteAccounting()
	e.CacheWriteTTLKnown = !u.AccountingUncertain && c.TTLKnown && !c.Invalid
	e.AccountingUncertain = u.AccountingUncertain || !e.InputKnown || !e.OutputKnown || !e.CacheWriteTTLKnown
	e.InputTotalKnown = e.InputKnown && !u.AccountingUncertain && c.QuantityKnown && !c.Invalid
	var total int64
	for _, n := range []int64{number(u.InputTokens), number(u.CacheReadInputTokens), w5, w1} {
		if n < 0 || n > math.MaxInt64-total {
			e.InputTotalKnown = false
			e.AccountingUncertain = true
			break
		}
		total += n
	}
	if !e.InputTotalKnown || number(u.OutputTokens) > math.MaxInt64-total {
		e.AccountingUncertain = true
	}
	// Retain individual observations, but omit a derived total that cannot
	// be represented. The evidence above already marks it unknown/uncertain;
	// wrapping into a negative value would corrupt downstream aggregation.
	var writeTotal int64
	if w5 >= 0 && w1 >= 0 && w5 <= math.MaxInt64-w1 {
		writeTotal = w5 + w1
	}
	return &UsageRef{
		InputTokens: number(u.InputTokens), OutputTokens: number(u.OutputTokens),
		CacheReadInputTokens: number(u.CacheReadInputTokens), CacheCreationInputTokens: writeTotal,
		CacheCreation5mInputTokens: w5, CacheCreation1hInputTokens: w1, Evidence: e,
	}
}
