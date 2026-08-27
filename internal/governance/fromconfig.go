package governance

// ConfigTeam is the flat, config-shaped per-team input for PoliciesFromConfig.
// It is defined here (not imported from internal/config) so governance stays
// independent of the config package — the caller (main.go) maps
// config.TeamConfig → ConfigTeam. BudgetUSDPerMonth is a human USD float;
// PoliciesFromConfig converts it to µUSD.
type ConfigTeam struct {
	RatePerMin        int64
	TokensPerMinute   int64
	TokensPerDay      int64
	QuotaExceeded     string // block|warn
	BudgetUSDPerMonth float64
	// BudgetUSDPerDay is the calendar-day counterpart; there is deliberately
	// no BudgetDayExceeded here because config.BudgetConfig carries a single
	// on_exceeded knob that governs both windows (BudgetExceeded feeds both).
	BudgetUSDPerDay float64
	BudgetExceeded  string // block|warn
}

// Limits is PolicyFromLimits's already-resolved input, one field per governance
// dimension. It replaced six positional arguments — two adjacent int64 pairs
// among them, which a caller could silently transpose — so that adding a
// dimension is an additive struct field rather than an edit at every call site.
// Field names match the TeamPolicy fields they feed.
type Limits struct {
	RatePerMin           int64
	TokensPerMinute      int64
	TokensPerDay         int64
	QuotaExceeded        string // block|warn
	BudgetMicrosPerMonth int64
	BudgetExceeded       string // block|warn
	BudgetMicrosPerDay   int64
	BudgetDayExceeded    string // block|warn
}

// PolicyFromLimits builds a TeamPolicy from already-resolved limits (budget in
// µUSD, no unit conversion here). Factored out so the burst rule below can
// never diverge between the config path (PoliciesFromConfig) and the D3
// keystore-team-record path (cmd/mayu assembly's Governor.SetTeamLookup
// callback) — both must produce byte-identical TeamPolicy shapes for the same
// numbers, or ADR-016's "DB record wins" precedence would also silently
// change enforcement behavior, not just the source of the values.
func PolicyFromLimits(l Limits) TeamPolicy {
	burst := l.RatePerMin
	if burst <= 0 {
		burst = 1
	}
	return TeamPolicy{
		RatePerMin:           l.RatePerMin,
		RateBurst:            burst,
		TokensPerMinute:      l.TokensPerMinute,
		TokensPerDay:         l.TokensPerDay,
		QuotaExceeded:        l.QuotaExceeded,
		BudgetMicrosPerMonth: l.BudgetMicrosPerMonth,
		BudgetExceeded:       l.BudgetExceeded,
		BudgetMicrosPerDay:   l.BudgetMicrosPerDay,
		BudgetDayExceeded:    l.BudgetDayExceeded,
	}
}

// PoliciesFromConfig converts config-shaped teams into resolved TeamPolicy.
// USD→µUSD is ×1_000_000; see PolicyFromLimits for the burst rule.
func PoliciesFromConfig(in map[string]ConfigTeam) map[string]TeamPolicy {
	out := make(map[string]TeamPolicy, len(in))
	for name, c := range in {
		out[name] = PolicyFromLimits(Limits{
			RatePerMin:           c.RatePerMin,
			TokensPerMinute:      c.TokensPerMinute,
			TokensPerDay:         c.TokensPerDay,
			QuotaExceeded:        c.QuotaExceeded,
			BudgetMicrosPerMonth: int64(c.BudgetUSDPerMonth * 1_000_000),
			BudgetExceeded:       c.BudgetExceeded,
			BudgetMicrosPerDay:   int64(c.BudgetUSDPerDay * 1_000_000),
			BudgetDayExceeded:    c.BudgetExceeded,
		})
	}
	return out
}
