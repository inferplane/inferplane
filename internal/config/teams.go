package config

import (
	"fmt"
	"math"
)

func validateTeams(teams map[string]TeamConfig) error {
	for name, team := range teams {
		at := fmt.Sprintf("config: teams[%q]", name)
		for field, limit := range map[string]int64{
			"rate_limit.requests_per_minute": team.RateLimit.RequestsPerMinute,
			"rate_limit.tokens_per_minute":   team.RateLimit.TokensPerMinute,
			"quota.tokens_per_day":           team.Quota.TokensPerDay,
		} {
			if limit < 0 {
				return fmt.Errorf("%s.%s must be >= 0", at, field)
			}
		}
		if team.Quota.TokensPerMonth != 0 {
			return fmt.Errorf("%s.quota.tokens_per_month is unsupported; use GovernancePolicy.tokenQuota with CalendarMonth in the shared Postgres profile", at)
		}
		for field, limit := range map[string]float64{
			"budget.usd_per_day":   team.Budget.USDPerDay,
			"budget.usd_per_month": team.Budget.USDPerMonth,
		} {
			if limit < 0 || math.IsNaN(limit) || math.IsInf(limit, 0) || limit >= float64(math.MaxInt64)/1_000_000 {
				return fmt.Errorf("%s.%s must be nonnegative and representable in integer microUSD", at, field)
			}
		}
		for field, action := range map[string]string{
			"budget.on_exceeded": team.Budget.OnExceeded,
			"quota.on_exceeded":  team.Quota.OnExceeded,
		} {
			if action != "" && action != "block" && action != "warn" {
				return fmt.Errorf("%s.%s must be block or warn (empty defaults to block)", at, field)
			}
		}
	}
	return nil
}
