package usageapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/principal"
)

func TestUsageHandlerReportsBudget(t *testing.T) {
	teams := map[string]governance.TeamPolicy{"t": {BudgetMicrosPerMonth: 1_000_000}}
	g := governance.NewGovernor(teams, limiter.NewMemory(), budget.NewMemory(), nil)
	h := NewHandler(g)

	req := httptest.NewRequest("GET", "/v1/usage", nil)
	ctx := principal.With(req.Context(), keystore.Principal{
		KeyID: "ik_secret", Team: "t",
		KeyOptions: keystore.KeyOptions{BudgetUSDMicros: 500_000},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "ik_secret") {
		t.Fatalf("usage response must not leak key id: %s", body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if got["team"] != "t" {
		t.Fatalf("team missing: %v", got)
	}
	// integer microUSD survives JSON round-trip (json numbers are float64 but
	// these are small exact integers).
	if !strings.Contains(body, "1000000") || !strings.Contains(body, "500000") {
		t.Fatalf("expected team + key budget micros in body: %s", body)
	}
}

func TestUsageHandlerNoPrincipal401(t *testing.T) {
	g := governance.NewGovernor(nil, limiter.NewMemory(), budget.NewMemory(), nil)
	h := NewHandler(g)
	req := httptest.NewRequest("GET", "/v1/usage", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req) // no principal
	if rec.Code != 401 {
		t.Fatalf("missing principal must be 401, got %d", rec.Code)
	}
}

// F4: a nil governor must not panic — return a well-formed ungoverned payload.
func TestUsageHandlerNilGovernor(t *testing.T) {
	h := NewHandler(nil)
	req := httptest.NewRequest("GET", "/v1/usage", nil)
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "t"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != 200 {
		t.Fatalf("nil governor must be 200 ungoverned, got %d", rec.Code)
	}
}

// TestUsageHandlerReportsBothBudgetWindows pins THIS package's own
// Principal → KeyPolicy mapping, which is a fourth copy of the same three-line
// mapping the three ingress packages each carry (governance stays a leaf and
// does not import keystore). A per-day cap that never reaches KeyPolicy is
// completely silent — the field is stored, surfaced by the admin API, and then
// simply not enforced — so the mapping needs its own assertion rather than
// relying on the handler's other tests, which all leave it zero.
func TestUsageHandlerReportsBothBudgetWindows(t *testing.T) {
	teams := map[string]governance.TeamPolicy{
		"t": {BudgetMicrosPerMonth: 1_000_000, BudgetMicrosPerDay: 400_000},
	}
	g := governance.NewGovernor(teams, limiter.NewMemory(), budget.NewMemory(), nil)
	h := NewHandler(g)

	req := httptest.NewRequest("GET", "/v1/usage", nil)
	ctx := principal.With(req.Context(), keystore.Principal{
		KeyID: "ik_secret", Team: "t",
		// Four distinct limits so a transposed mapping fails rather than passing.
		KeyOptions: keystore.KeyOptions{BudgetUSDMicros: 500_000, BudgetUSDMicrosPerDay: 250_000},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d body %s", rec.Code, rec.Body.String())
	}
	var got governance.UsageStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	for _, tc := range []struct {
		name  string
		u     *governance.BudgetUsage
		limit int64
		win   string
	}{
		{"team_budget", got.TeamBudget, 1_000_000, "calendar-month"},
		{"team_budget_day", got.TeamBudgetDay, 400_000, "calendar-day"},
		{"key_budget", got.KeyBudget, 500_000, "calendar-month"},
		{"key_budget_day", got.KeyBudgetDay, 250_000, "calendar-day"},
	} {
		if tc.u == nil {
			t.Fatalf("%s missing from /v1/usage: %s", tc.name, rec.Body.String())
		}
		if tc.u.LimitUSDMicros != tc.limit {
			t.Fatalf("%s limit = %d, want %d", tc.name, tc.u.LimitUSDMicros, tc.limit)
		}
		if tc.u.Window != tc.win {
			t.Fatalf("%s window = %q, want %q", tc.name, tc.u.Window, tc.win)
		}
	}
	if strings.Contains(rec.Body.String(), "ik_secret") {
		t.Fatalf("usage response must not leak key id: %s", rec.Body.String())
	}
}
