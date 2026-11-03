package pgstore

import (
	"context"
	"errors"
	"testing"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/keystore"
)

// A shared refusal names the definition that refused it (ADR-057): the team
// record, the key option, or the policy rule with its generation.
func TestSharedDenialNamesItsLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		team keystore.TeamRecord
		opts keystore.KeyOptions
		doc  bool
		want audit.GovernanceRef
		rule *audit.PolicyRuleRef
	}{
		{name: "team record rpm", team: keystore.TeamRecord{Name: "alpha", RPM: 1},
			want: audit.GovernanceRef{Scope: audit.GovScopeTeam, Limit: audit.GovLimitRPM, Source: audit.GovSourceTeamRecord}},
		{name: "key day budget", team: keystore.TeamRecord{Name: "alpha"}, opts: keystore.KeyOptions{BudgetUSDMicrosPerDay: 1000},
			want: audit.GovernanceRef{Scope: audit.GovScopeKey, Limit: audit.GovLimitBudgetDay, Source: audit.GovSourceKey}},
		{name: "policy month quota", team: keystore.TeamRecord{Name: "alpha"}, doc: true,
			want: audit.GovernanceRef{Scope: audit.GovScopeTeam, Limit: audit.GovLimitTokensPerMonth, Source: audit.GovSourcePolicy},
			rule: &audit.PolicyRuleRef{Name: "tokens", Generation: 4, Rule: "cap"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSharedFixture(t)
			if tc.doc {
				doc := sharedDoc("tokens", v1alpha1.Subject{Team: "alpha"}, sharedQuota("cap", 10, v1alpha1.PeriodCalendarMonth))
				doc.Metadata.Generation = 4
				putDocument(t, f.db, doc)
			}
			f.team(t, tc.team)
			_, req := f.key(t, "alpha", tc.opts)
			sharedReserve(t, f.a, req)
			_, err := f.b.ReserveShared(context.Background(), req)
			var denial *governance.SharedDenial
			if !errors.As(err, &denial) {
				t.Fatalf("second admission: %v", err)
			}
			g := denial.Governance
			if g == nil || g.Scope != tc.want.Scope || g.Limit != tc.want.Limit || g.Source != tc.want.Source {
				t.Fatalf("governance = %+v, want %+v", g, tc.want)
			}
			if (g.Policy == nil) != (tc.rule == nil) || (g.Policy != nil && *g.Policy != *tc.rule) {
				t.Fatalf("policy = %+v, want %+v", g.Policy, tc.rule)
			}
		})
	}
}
