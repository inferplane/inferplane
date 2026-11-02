package governance

import (
	"errors"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/pricing"
)

func policySource(name, rule string) LimitSource {
	return LimitSource{Source: audit.GovSourcePolicy, Policy: &audit.PolicyRuleRef{Name: name, Generation: 5, Rule: rule}}
}

func requireGovernance(t *testing.T, got *audit.GovernanceRef, scope, limit, source, rule string) {
	t.Helper()
	if got == nil || got.Scope != scope || got.Limit != limit || got.Source != source {
		t.Fatalf("governance = %+v, want %s/%s/%s", got, scope, limit, source)
	}
	gotRule := ""
	if got.Policy != nil {
		gotRule = got.Policy.Rule
	}
	if gotRule != rule {
		t.Fatalf("policy rule = %q, want %q", gotRule, rule)
	}
}

// Every PreCheck denial names the limit that refused it (ADR-057).
func TestPreCheckNamesDenyingLimit(t *testing.T) {
	sources := TeamSources{Rate: policySource("team-rate", "rpm"), TokenRate: policySource("team-rate", "tpm"),
		Quota: LimitSource{Source: audit.GovSourceTeamRecord}, Budget: policySource("money", "month"),
		BudgetDay: LimitSource{Source: audit.GovSourceConfig}}
	blocking := blockingStore{resets: map[string]time.Time{}}
	for _, tc := range []struct {
		name                       string
		team                       TeamPolicy
		key                        KeyPolicy
		store                      budget.BudgetStore
		estimate                   int64
		scope, limit, source, rule string
	}{
		{"team rpm", TeamPolicy{RatePerMin: 1, RateBurst: 1}, KeyPolicy{}, nil, 0, audit.GovScopeTeam, audit.GovLimitRPM, audit.GovSourcePolicy, "rpm"},
		{"team tpm", TeamPolicy{TokensPerMinute: 1000}, KeyPolicy{}, nil, 800, audit.GovScopeTeam, audit.GovLimitTPM, audit.GovSourcePolicy, "tpm"},
		{"team quota", TeamPolicy{TokensPerDay: 1000}, KeyPolicy{}, nil, 2000, audit.GovScopeTeam, audit.GovLimitTokensPerDay, audit.GovSourceTeamRecord, ""},
		{"team month", TeamPolicy{BudgetMicrosPerMonth: 1, BudgetExceeded: "block"}, KeyPolicy{}, blocking, 0, audit.GovScopeTeam, audit.GovLimitBudgetMonth, audit.GovSourcePolicy, "month"},
		{"team day", TeamPolicy{BudgetMicrosPerDay: 1, BudgetDayExceeded: "block"}, KeyPolicy{}, blocking, 0, audit.GovScopeTeam, audit.GovLimitBudgetDay, audit.GovSourceConfig, ""},
		{"team store capacity", TeamPolicy{BudgetMicrosPerMonth: 1, BudgetExceeded: "warn"}, KeyPolicy{}, capacityBlockingStore{blocking}, 0, audit.GovScopeTeam, audit.GovLimitBudgetMonth, audit.GovSourceBudgetStore, ""},
		{"key rpm", TeamPolicy{}, KeyPolicy{RatePerMin: 1}, nil, 0, audit.GovScopeKey, audit.GovLimitRPM, audit.GovSourceKey, ""},
		{"key tpm", TeamPolicy{}, KeyPolicy{TokensPerMinute: 1000}, nil, 800, audit.GovScopeKey, audit.GovLimitTPM, audit.GovSourceKey, ""},
		{"key day", TeamPolicy{}, KeyPolicy{BudgetMicrosPerDay: 1}, blocking, 0, audit.GovScopeKey, audit.GovLimitBudgetDay, audit.GovSourceKey, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.store
			if store == nil {
				store = budget.NewMemory()
			}
			team := tc.team
			team.Sources = sources
			g := NewGovernor(map[string]TeamPolicy{"t": team}, limiter.NewMemory(), store, nil)
			s := Subject{Team: "t", KeyID: "k"}
			dec := g.PreCheck(s, tc.key, tc.estimate)
			if dec.Allowed {
				dec.Admission.Close()
				dec = g.PreCheck(s, tc.key, tc.estimate)
			}
			if dec.Allowed {
				t.Fatalf("second request admitted: %+v", dec)
			}
			requireGovernance(t, dec.Governance, tc.scope, tc.limit, tc.source, tc.rule)
		})
	}
}

func TestPreCheckNamesUserRuleLeaseAndPricing(t *testing.T) {
	g := NewGovernor(map[string]TeamPolicy{"t": {}}, limiter.NewMemory(), blockingStore{resets: map[string]time.Time{}}, nil)
	g.SetUserLookup(func(team, user string) (UserPolicy, bool) {
		return UserPolicy{BudgetMicrosPerDay: 1, BudgetExceeded: "block", Sources: UserSources{BudgetDay: policySource("person", "day")}}, true
	})
	dec := g.PreCheck(Subject{Team: "t", KeyID: "k", User: "alice"}, KeyPolicy{}, 0)
	requireGovernance(t, dec.Governance, audit.GovScopeUser, audit.GovLimitBudgetDay, audit.GovSourcePolicy, "day")

	g.SetLeaseGate(func(string) (bool, string) { return true, "budget lease expired" })
	dec = g.PreCheck(Subject{Team: "t", KeyID: "k"}, KeyPolicy{}, 0)
	requireGovernance(t, dec.Governance, audit.GovScopeTeam, audit.GovLimitBudget, audit.GovSourceLease, "")

	dec = PricingGuard(pricing.New(pricing.OnMissingBlock, nil), []PricedTarget{{Provider: "p", Upstream: "m"}})
	requireGovernance(t, dec.Governance, audit.GovScopeGateway, audit.GovLimitPricing, audit.GovSourceConfig, "")

	if dec := g.PreCheck(Subject{Team: "t"}, KeyPolicy{}, -1); dec.Allowed || dec.Governance != nil {
		t.Fatalf("an invalid estimate is not a configured limit: %+v", dec)
	}
}

// Config teams name the config file as the source of every limit.
func TestPoliciesFromConfigNameConfig(t *testing.T) {
	p := PoliciesFromConfig(map[string]ConfigTeam{"t": {RatePerMin: 1}})["t"]
	if p.Sources != ConfigSources() || p.Sources.Rate.Source != audit.GovSourceConfig {
		t.Fatalf("sources = %+v", p.Sources)
	}
}

// A local money refusal names its scope's limit and still matches its
// sentinel, so the 402 mapping is unchanged.
func TestReserveLocalBudgetNamesExhaustedScope(t *testing.T) {
	team := TeamPolicy{BudgetMicrosPerMonth: 100, Sources: TeamSources{Budget: policySource("money", "month")}}
	g := NewGovernor(map[string]TeamPolicy{"t": team}, limiter.NewMemory(), budget.NewMemory(), nil)
	s := Subject{Team: "t", KeyID: "k"}
	g.bud.Debit(budget.Key(budget.ScopeTeam, "t", g.monthWindow()), 100, g.monthWindow())
	_, err := g.ReserveLocalBudget(s, KeyPolicy{}, 10, true)
	if !errors.Is(err, ErrLocalBudgetExhausted) {
		t.Fatalf("err = %v", err)
	}
	requireGovernance(t, DenialGovernance(err), audit.GovScopeTeam, audit.GovLimitBudgetMonth, audit.GovSourcePolicy, "month")

	g = NewGovernor(nil, limiter.NewMemory(), budget.NewMemory(), nil)
	g.bud.Debit(budget.Key(budget.ScopeKey, "k", g.dayWindow()), 1, g.dayWindow())
	_, err = g.ReserveLocalBudget(s, KeyPolicy{BudgetMicrosPerDay: 1}, 10, true)
	requireGovernance(t, DenialGovernance(err), audit.GovScopeKey, audit.GovLimitBudgetDay, audit.GovSourceKey, "")
}

func TestDenialGovernanceReadsSharedAndPlainErrors(t *testing.T) {
	ref := &audit.GovernanceRef{Scope: audit.GovScopeKey, Limit: audit.GovLimitRPM, Source: audit.GovSourceKey}
	if got := DenialGovernance(&SharedDenial{Status: 429, Reason: "shared rate or token quota exhausted", Governance: ref}); got != ref {
		t.Fatalf("shared = %+v", got)
	}
	if got := DenialGovernance(ErrAuthorityUnavailable); got != nil {
		t.Fatalf("an unavailable authority names no limit: %+v", got)
	}
	wrapped := &BudgetDenial{Err: ErrAuthorityExhausted, Governance: ref}
	if !errors.Is(wrapped, ErrAuthorityExhausted) || DenialGovernance(wrapped) != ref {
		t.Fatal("budget denial lost its sentinel or its limit")
	}
}
