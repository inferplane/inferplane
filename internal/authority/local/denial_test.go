package local

import (
	"context"
	"errors"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/policy"
)

// An exhausted hard budget names its policy rule (ADR-057) and keeps the
// sentinel the 402 mapping reads.
func TestExhaustedAuthorityNamesItsBudget(t *testing.T) {
	s, _ := openTest(t)
	r := response(definition("hard", true))
	applyTest(t, s, r)
	if _, err := s.Reserve(context.Background(), testSubject, 0); !errors.Is(err, governance.ErrAuthorityUnavailable) {
		t.Fatalf("first reserve: %v", err)
	}
	want := requestTest(t, s).Requests[0]
	r.Denied = []policy.AuthorityDenial{{RequestID: want.RequestID, Key: want.Key, Reason: "exhausted"}}
	applyTest(t, s, r)
	_, err := s.Reserve(context.Background(), testSubject, 0)
	if !errors.Is(err, governance.ErrAuthorityExhausted) {
		t.Fatalf("err = %v", err)
	}
	g := governance.DenialGovernance(err)
	if g == nil || g.Scope != audit.GovScopeTeam || g.Limit != audit.GovLimitBudgetDay || g.Source != audit.GovSourcePolicy ||
		g.Policy == nil || *g.Policy != (audit.PolicyRuleRef{Name: "policy", Rule: "hard"}) {
		t.Fatalf("governance = %+v", g)
	}
}
