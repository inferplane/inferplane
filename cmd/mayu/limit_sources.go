package main

import (
	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/policy"
)

// policySource names a GovernancePolicy rule as the source of one limit, so
// the denial it produces names that rule (ADR-057). A zero RuleRef (no rule
// sets the limit) names nothing.
func policySource(r policy.RuleRef) governance.LimitSource {
	if r.Rule == "" {
		return governance.LimitSource{}
	}
	return governance.LimitSource{Source: audit.GovSourcePolicy,
		Policy: &audit.PolicyRuleRef{Name: r.Policy, Generation: r.Generation, Rule: r.Rule}}
}

// teamRecordSources marks every limit of a keystore team record (the
// /admin/teams console edit) as its own source.
func teamRecordSources() governance.TeamSources {
	r := governance.LimitSource{Source: audit.GovSourceTeamRecord}
	return governance.TeamSources{Rate: r, TokenRate: r, Quota: r, Budget: r, BudgetDay: r}
}
