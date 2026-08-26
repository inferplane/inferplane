package governance

import "testing"

func TestPoliciesFromConfig(t *testing.T) {
	in := map[string]ConfigTeam{
		"platform-eng": {
			RatePerMin: 300, TokensPerMinute: 2_000_000, TokensPerDay: 50_000_000, QuotaExceeded: "block",
			BudgetUSDPerMonth: 5000, BudgetExceeded: "warn",
		},
	}
	pol := PoliciesFromConfig(in)
	p := pol["platform-eng"]
	if p.RatePerMin != 300 || p.TokensPerDay != 50_000_000 || p.QuotaExceeded != "block" {
		t.Fatalf("policy: %+v", p)
	}
	// 5000 USD → 5_000_000_000 µUSD
	if p.BudgetMicrosPerMonth != 5_000_000_000 {
		t.Fatalf("budget µUSD: %d", p.BudgetMicrosPerMonth)
	}
	if p.BudgetExceeded != "warn" {
		t.Fatalf("budget exceeded policy: %q", p.BudgetExceeded)
	}
	// burst defaults to RatePerMin when unset (so a full minute's worth can burst)
	if p.RateBurst <= 0 {
		t.Fatalf("burst should default >0: %d", p.RateBurst)
	}
}

// TestPolicyFromLimitsMapsEveryField pins the struct-argument form of
// PolicyFromLimits: six positional arguments (including two adjacent int64
// pairs) became one Limits value, and the risk that conversion carries is a
// silently transposed field. Every number below is distinct so a swap fails.
func TestPolicyFromLimitsMapsEveryField(t *testing.T) {
	got := PolicyFromLimits(Limits{
		RatePerMin:           11,
		TokensPerMinute:      22,
		TokensPerDay:         33,
		QuotaExceeded:        "warn",
		BudgetMicrosPerMonth: 44,
		BudgetExceeded:       "block",
	})
	want := TeamPolicy{
		RatePerMin:           11,
		RateBurst:            11, // burst defaults to RatePerMin
		TokensPerMinute:      22,
		TokensPerDay:         33,
		QuotaExceeded:        "warn",
		BudgetMicrosPerMonth: 44,
		BudgetExceeded:       "block",
	}
	if got != want {
		t.Fatalf("PolicyFromLimits = %+v, want %+v", got, want)
	}
}

// TestPolicyFromLimitsBurstFloor keeps the burst rule pinned through the
// signature change: a zero or negative RatePerMin must still floor to 1, never
// 0, because 0 means "unlimited" to the limiter.
func TestPolicyFromLimitsBurstFloor(t *testing.T) {
	for _, rpm := range []int64{0, -5} {
		if got := PolicyFromLimits(Limits{RatePerMin: rpm}).RateBurst; got != 1 {
			t.Fatalf("RatePerMin=%d gave RateBurst=%d, want 1", rpm, got)
		}
	}
}
