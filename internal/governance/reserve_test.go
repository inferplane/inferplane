package governance

// Reserve/settle at the Governor level (strategy Phase 1): a request's cost
// upper bound is HELD on every block-posture budget window between
// PreCheckCost and SettleCost, so concurrent near-cap requests cannot bypass
// the cap; a deny after some windows already reserved rolls their holds back.

import (
	"testing"

	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/pricing"
)

func TestPreCheckCostConcurrentNearCapCannotBypass(t *testing.T) {
	g := NewGovernor(map[string]TeamPolicy{
		"t": {BudgetMicrosPerMonth: 100, BudgetExceeded: "block"},
	}, limiter.NewMemory(), budget.NewMemory(), nil)
	s := Subject{Team: "t", KeyID: "k"}

	// Two in-flight requests, each bounded at 60 µUSD against a 100 µUSD
	// cap: the first holds, the second must deny BEFORE egress.
	if d := g.PreCheckCost(s, KeyPolicy{}, 10, 60); !d.Allowed {
		t.Fatalf("first near-cap request: %+v", d)
	}
	if d := g.PreCheckCost(s, KeyPolicy{}, 10, 60); d.Allowed || d.Status != 402 {
		t.Fatalf("concurrent second request must deny on the held balance: %+v", d)
	}
	// The first settles at an actual 30: the hold is released, and the next
	// 60-bound request fits (30 + 60 ≤ 100).
	g.SettleCost(s, KeyPolicy{}, "p", "m", pricing.Usage{Input: 30}, testTable(), 10, 60)
	if d := g.PreCheckCost(s, KeyPolicy{}, 10, 60); !d.Allowed {
		t.Fatalf("post-settle request must pass on the freed balance: %+v", d)
	}
	// An upper bound exceeding the remaining balance denies before egress.
	if d := g.PreCheckCost(s, KeyPolicy{}, 10, 20); d.Allowed {
		t.Fatalf("bound exceeding balance (30 spent + 60 held + 20 > 100) must deny: %+v", d)
	}
}

func TestPreCheckCostRollsBackHoldsOnLaterDeny(t *testing.T) {
	g := NewGovernor(map[string]TeamPolicy{
		"t": {BudgetMicrosPerMonth: 100, BudgetExceeded: "block"},
	}, limiter.NewMemory(), budget.NewMemory(), nil)
	s := Subject{Team: "t", KeyID: "k"}

	// The team window reserves 60, then the key budget (limit 1) denies:
	// the team hold must be rolled back, not leaked.
	if d := g.PreCheckCost(s, KeyPolicy{BudgetMicrosPerMonth: 1}, 10, 60); d.Allowed {
		t.Fatalf("key budget must deny: %+v", d)
	}
	// Proof of rollback: a fresh 60-bound request fits only if the denied
	// request's team hold was released (60 + 60 > 100 otherwise).
	if d := g.PreCheckCost(s, KeyPolicy{}, 10, 60); !d.Allowed {
		t.Fatalf("denied request leaked its team hold: %+v", d)
	}
}

// A warn window keeps its meaning: it never denies, and it never holds.
func TestPreCheckCostWarnWindowNeverHolds(t *testing.T) {
	g := NewGovernor(map[string]TeamPolicy{
		"t": {BudgetMicrosPerMonth: 100, BudgetExceeded: "warn"},
	}, limiter.NewMemory(), budget.NewMemory(), nil)
	s := Subject{Team: "t", KeyID: "k"}
	for i := 0; i < 3; i++ {
		if d := g.PreCheckCost(s, KeyPolicy{}, 10, 60); !d.Allowed {
			t.Fatalf("warn window request %d denied: %+v", i, d)
		}
	}
}
