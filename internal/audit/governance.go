package audit

import "context"

// Governance denial evidence (ADR-057): which limit refused a request before
// dispatch. Every value is from a closed set; a GovernanceRef never carries a
// limit value, a counter, a key, a user identifier or a secret.
const (
	GovScopeTeam    = "team"
	GovScopeKey     = "key"
	GovScopeUser    = "user"
	GovScopeGateway = "gateway"

	GovLimitRPM            = "rpm"
	GovLimitTPM            = "tpm"
	GovLimitTokensPerDay   = "tokens_per_day"
	GovLimitTokensPerMonth = "tokens_per_month"
	GovLimitBudgetMonth    = "budget_month"
	GovLimitBudgetDay      = "budget_day"
	// GovLimitBudget is a budget whose window the refusing gate does not
	// report (the ADR-034 lease gate).
	GovLimitBudget  = "budget"
	GovLimitPricing = "pricing"

	GovSourceConfig      = "config"       // config file: teams.<team>, pricing.on_missing
	GovSourceTeamRecord  = "team_record"  // keystore team record (/admin/teams)
	GovSourcePolicy      = "policy"       // a GovernancePolicy rule, named in Policy
	GovSourceLease       = "lease"        // a control-plane budget lease (ADR-034)
	GovSourceKey         = "key"          // the virtual key's own limit
	GovSourceBudgetStore = "budget_store" // the budget store's capacity fail-safe
)

// PolicyRuleRef names one GovernancePolicy rule. Generation is omitted when
// the enforcing authority does not carry it (ADR-045 grants name only the
// policy and the rule).
type PolicyRuleRef struct {
	Name       string `json:"name"`
	Generation int64  `json:"generation,omitempty"`
	Rule       string `json:"rule"`
}

// GovernanceRef names the limit behind a governance denial: its scope, the
// kind of limit, and where that limit was set. Source is omitted when the
// governor was built without sources (a library caller, a test).
type GovernanceRef struct {
	Scope  string         `json:"scope"`
	Limit  string         `json:"limit"`
	Source string         `json:"source,omitempty"`
	Policy *PolicyRuleRef `json:"policy,omitempty"`
}

// SetGovernance stores the limit that is refusing this request. Finish writes
// it onto the gateway denial record (a request_started record with an
// outcome) and nowhere else; a nil ref clears it.
func SetGovernance(ctx context.Context, ref *GovernanceRef) {
	e := evidenceFrom(ctx)
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.gov = cloneGovernance(ref)
}

func cloneGovernance(ref *GovernanceRef) *GovernanceRef {
	if ref == nil {
		return nil
	}
	c := *ref
	if ref.Policy != nil {
		p := *ref.Policy
		c.Policy = &p
	}
	return &c
}
