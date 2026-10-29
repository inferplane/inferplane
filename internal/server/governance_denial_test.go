package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/server/routingtest"
	"github.com/inferplane/inferplane/pkg/schema"
)

// denialRecord returns the single gateway denial record in an audit buffer.
func denialRecord(t *testing.T, buf *bytes.Buffer) audit.Record {
	t.Helper()
	var found []audit.Record
	sc := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	for sc.Scan() {
		var r audit.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.Event == "request_started" && r.Outcome != nil {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("denial records = %d: %s", len(found), buf)
	}
	return found[0]
}

// Every ingress writes the refusing limit onto its denial record (ADR-057):
// a PreCheck limit, an ADR-046 shared refusal and an ADR-045 authority
// refusal. Reservation refusals also carry the budget_authority_unavailable
// code on every ingress, as Responses already did.
func TestEveryIngressRecordsGovernanceDenialLimit(t *testing.T) {
	rule := &audit.PolicyRuleRef{Name: "team-rate", Generation: 3, Rule: "rpm"}
	reserved := &audit.GovernanceRef{Scope: audit.GovScopeKey, Limit: audit.GovLimitTPM, Source: audit.GovSourceKey}
	for _, ingress := range authorityIngresses() {
		t.Run(ingress.name()+"/precheck", func(t *testing.T) {
			f := routingtest.New(t, nil)
			in, out := int64(2), int64(1)
			f.Public.Usage = &schema.Usage{InputTokens: &in, OutputTokens: &out}
			team := governance.TeamPolicy{RatePerMin: 1, RateBurst: 1,
				Sources: governance.TeamSources{Rate: governance.LimitSource{Source: audit.GovSourcePolicy, Policy: rule}}}
			gov := governance.NewGovernor(map[string]governance.TeamPolicy{"team": team}, limiter.NewMemory(), budget.NewMemory(), nil)
			if rec := policyDo(policyMux(f, nil, gov, nil, nil), ingress.path, authorityBody(ingress), true); rec.Code != 200 {
				t.Fatalf("first request status %d: %s", rec.Code, rec.Body)
			}
			aud, buf := policyAudit(t)
			rec := policyDo(policyMux(f, aud, gov, nil, nil), ingress.path, authorityBody(ingress), true)
			aud.Close()
			r := denialRecord(t, buf)
			g := r.Governance
			if rec.Code != 429 || g == nil || g.Scope != audit.GovScopeTeam || g.Limit != audit.GovLimitRPM || g.Source != audit.GovSourcePolicy || g.Policy == nil || *g.Policy != *rule {
				t.Fatalf("status %d governance %+v", rec.Code, g)
			}
		})
		for _, refusal := range []string{"shared", "authority"} {
			t.Run(ingress.name()+"/"+refusal, func(t *testing.T) {
				f := routingtest.New(t, nil)
				gov := governance.NewGovernor(nil, limiter.NewMemory(), budget.NewMemory(), nil)
				want := 429
				if refusal == "shared" {
					f.Router.SetRoutingPolicySnapshot(func(string, string) ([]*policy.Policy, string, error) { return nil, "generation", nil })
					gov.SetSharedAuthority(&sharedProbe{deny: &governance.SharedDenial{Status: 429, Reason: "shared rate or token quota exhausted", RetryAfter: 60, Governance: reserved}})
				} else {
					want = 402
					gov.SetBudgetAuthority(&authorityProbe{failure: &governance.BudgetDenial{Err: governance.ErrAuthorityExhausted, Governance: reserved}})
				}
				aud, buf := policyAudit(t)
				rec := policyDo(policyMux(f, aud, gov, nil, nil), ingress.path, authorityBody(ingress), true)
				aud.Close()
				r := denialRecord(t, buf)
				if rec.Code != want || r.Outcome.Error == nil || *r.Outcome.Error != "budget_authority_unavailable" {
					t.Fatalf("status %d outcome %+v", rec.Code, r.Outcome)
				}
				if r.Governance == nil || *r.Governance != *reserved {
					t.Fatalf("governance = %+v", r.Governance)
				}
			})
		}
	}
}
