package audit

// DenyReason constrains the closed set of machine-readable deny-reason codes
// written to OutcomeRef.Error. OutcomeRef.Error keeps its existing *string JSON
// wire shape as a documented external schema contract; this type only limits
// which string values are written there, avoiding a wire-format break.
type DenyReason string

const (
	DenyModelNotAllowed      DenyReason = "model_not_allowed"
	DenyTeamRateLimited      DenyReason = "team_rate_limited"
	DenyTeamTokenRateLimited DenyReason = "team_token_rate_limited"
	DenyTeamQuotaExceeded    DenyReason = "team_quota_exceeded"
	DenyKeyRateLimited       DenyReason = "key_rate_limited"
	DenyKeyTokenRateLimited  DenyReason = "key_token_rate_limited"
	DenyTeamBudgetExceeded   DenyReason = "team_budget_exceeded"
	DenyKeyBudgetExceeded    DenyReason = "key_budget_exceeded"
	// DenyUserBudgetExceeded: a per-USER budget rule denied the request
	// (ADR-042 Phase 3). Its own code rather than a reuse of
	// team_budget_exceeded because the two say different things to whoever
	// reads the audit log: the team still has headroom, this individual does
	// not, and only one of those is fixed by raising the team's cap.
	DenyUserBudgetExceeded DenyReason = "user_budget_exceeded"
	DenyRegionBlocked      DenyReason = "region_blocked"
	// DenyPricingMissing: pricing.on_missing is "block" and the resolved
	// (provider, upstream) has no rate, so serving the request would bill 0
	// (ADR-030). Distinct from a budget denial — nothing was exceeded; the
	// gateway simply cannot price the call.
	DenyPricingMissing DenyReason = "pricing_missing"
)

// Ptr returns d as a plain string pointer.
func (d DenyReason) Ptr() *string {
	s := string(d)
	return &s
}
