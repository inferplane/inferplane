package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/pkg/schema"
)

func TestNativeConverseEvaluationRejectsInvalidAccountingEvidence(t *testing.T) {
	f := localConverseFixture(t)
	in, out := int64(100001), int64(1) // Input exceeds the fixture's declared 100K window.
	f.Public.Usage = &schema.Usage{InputTokens: &in, OutputTokens: &out}
	g := governance.NewGovernor(map[string]governance.TeamPolicy{
		"team": {BudgetMicrosPerMonth: 100, BudgetExceeded: "block"},
	}, limiter.NewMemory(), budget.NewMemory(), nil)
	h := WithEvaluations(http.NotFoundHandler(), localConverseMoneyMux(f, g, 100),
		[]string{"admin-fixture"}, nil, adminauth.MappingConfig{}, nil)
	req := httptest.NewRequest("POST", "/admin/evaluations", strings.NewReader(
		`{"protocol":"bedrock-converse","model":"premium","body":`+localConverseBody+`}`))
	req.Header.Set("Authorization", "Bearer admin-fixture")
	req.Header.Set("X-Inferplane-Virtual-Key", "dev-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var result evaluationResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || result.HTTPStatus != 200 || result.Attempts != 1 {
		t.Fatalf("response success and observed attempt changed: %d %+v", rec.Code, result)
	}
	if result.AccountingKnown || result.Settled {
		t.Fatalf("invalid usage reported as proved complete monetary settlement: %+v", result)
	}
	status := g.UsageOf(governance.Subject{Team: "team", KeyID: "opaque-key"},
		governance.KeyPolicy{BudgetMicrosPerDay: 100})
	for _, money := range []*governance.BudgetUsage{status.TeamBudget, status.KeyBudgetDay} {
		if money == nil || !money.AccountingUncertain || money.ReservedUSDMicros == 0 ||
			money.SpentUSDMicros != 2 {
			t.Fatalf("invalid input lost valid output cost or uncertainty protection: %+v", money)
		}
	}
}
