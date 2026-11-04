package governance

import (
	"errors"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/budget"
)

// LimitSource says where one enforced limit was set (ADR-057), so the denial
// it produces can name it in the audit record. Source is an audit.GovSource*
// value; Policy names the GovernancePolicy rule when Source is
// audit.GovSourcePolicy. The zero value names no source.
type LimitSource struct {
	Source string
	Policy *audit.PolicyRuleRef
}

// TeamSources names the source of each TeamPolicy limit.
type TeamSources struct {
	Rate, TokenRate, Quota, Budget, BudgetDay LimitSource
}

// UserSources names the source of each UserPolicy window.
type UserSources struct {
	Budget, BudgetDay LimitSource
}

// ConfigSources marks every team limit as set by the config file.
func ConfigSources() TeamSources {
	c := LimitSource{Source: audit.GovSourceConfig}
	return TeamSources{Rate: c, TokenRate: c, Quota: c, Budget: c, BudgetDay: c}
}

// keySource is every per-key limit's source: the virtual key's own options.
var keySource = LimitSource{Source: audit.GovSourceKey}

// limitRef names one limit by value; callers that keep it per request (the
// local money scopes) avoid an allocation until a denial needs it.
func limitRef(scope, limit string, src LimitSource) audit.GovernanceRef {
	return audit.GovernanceRef{Scope: scope, Limit: limit, Source: src.Source, Policy: src.Policy}
}

func denial(scope, limit string, src LimitSource) *audit.GovernanceRef {
	ref := limitRef(scope, limit, src)
	if src.Policy != nil {
		p := *src.Policy
		ref.Policy = &p
	}
	return &ref
}

// budgetDenial names a budget refusal. A store at capacity refuses whatever
// the limit says (mustDenyBudget), so the store is named instead.
func budgetDenial(scope, limit string, src LimitSource, dec budget.Decision) *audit.GovernanceRef {
	if dec == budget.BlockCapacity {
		src = LimitSource{Source: audit.GovSourceBudgetStore}
	}
	return denial(scope, limit, src)
}

// PricingDenial names the pricing.on_missing block setting (ADR-030), the
// source of every pricing_missing refusal.
func PricingDenial() *audit.GovernanceRef {
	return denial(audit.GovScopeGateway, audit.GovLimitPricing, LimitSource{Source: audit.GovSourceConfig})
}

// BudgetDenial is a pre-dispatch money refusal that names its binding limit
// (ADR-057). errors.Is still matches its sentinel (ErrLocalBudgetExhausted or
// ErrAuthorityExhausted), so status mapping is unchanged.
type BudgetDenial struct {
	Err        error
	Governance *audit.GovernanceRef
}

func (e *BudgetDenial) Error() string { return e.Err.Error() }
func (e *BudgetDenial) Unwrap() error { return e.Err }

// DenialGovernance returns the limit an admission error names, or nil when it
// names none (an unavailable store or authority, an invalid request).
func DenialGovernance(err error) *audit.GovernanceRef {
	var b *BudgetDenial
	if errors.As(err, &b) {
		return b.Governance
	}
	var s *SharedDenial
	if errors.As(err, &s) {
		return s.Governance
	}
	return nil
}
