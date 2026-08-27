package adminapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestBudgetPerDayRoundTripsThroughTeamAndKeyAPIs is the TDD entry point for
// the admin-plane per-day budget field: `budget_usd_micros_per_day` must
// survive a team upsert → list and a key create → response, alongside the
// existing `budget_usd_micros` and never instead of it. Distinct values on the
// two dimensions, so a transposition fails rather than passing.
//
// Both halves live in one test because they share exactly one requirement —
// the wire name — and a reviewer should see the two surfaces agree on it.
func TestBudgetPerDayRoundTripsThroughTeamAndKeyAPIs(t *testing.T) {
	store := newTestStore(t)

	// --- teams: PUT then GET ---
	th := NewTeamsHandler(store, nil, nil)
	body := `{"rpm":60,"budget_usd_micros":5000000,"budget_usd_micros_per_day":250000,"budget_on_exceeded":"block"}`
	rec := doAsTeams(t, th, &adminID, "PUT", "/admin/teams/platform-eng", body)
	if rec.Code != 200 {
		t.Fatalf("team upsert: %d %s", rec.Code, rec.Body.String())
	}
	var upserted map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &upserted); err != nil {
		t.Fatal(err)
	}
	if got := upserted["budget_usd_micros"]; got != float64(5_000_000) {
		t.Fatalf("team upsert budget_usd_micros = %v, want 5000000", got)
	}
	if got := upserted["budget_usd_micros_per_day"]; got != float64(250_000) {
		t.Fatalf("team upsert budget_usd_micros_per_day = %v, want 250000 — the daily cap must persist through the write body into the view", got)
	}

	rec = doAsTeams(t, th, &adminID, "GET", "/admin/teams", "")
	if rec.Code != 200 {
		t.Fatalf("team list: %d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Data) != 1 {
		t.Fatalf("team list returned %d rows, want 1: %+v", len(list.Data), list.Data)
	}
	if got := list.Data[0]["budget_usd_micros_per_day"]; got != float64(250_000) {
		t.Fatalf("team list budget_usd_micros_per_day = %v, want 250000 (read back from the keystore row)", got)
	}

	// --- keys: POST ---
	kh := NewKeysHandler(store, nil)
	keyBody := `{"team":"platform-eng","allowed_models":["*"],"budget_usd_micros":9000000,"budget_usd_micros_per_day":450000}`
	rec = doAs(t, kh, &adminID, "POST", "/admin/keys", keyBody)
	if rec.Code != 200 {
		t.Fatalf("key create: %d %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if got := created["budget_usd_micros"]; got != float64(9_000_000) {
		t.Fatalf("key create budget_usd_micros = %v, want 9000000", got)
	}
	if got := created["budget_usd_micros_per_day"]; got != float64(450_000) {
		t.Fatalf("key create budget_usd_micros_per_day = %v, want 450000 — the daily cap must persist through keyOptionsBody into keystore.KeyOptions and back out through keyView", got)
	}
}

// TestTeamsHandler_budgetPerDayValidation mirrors TestTeamsHandler_validation's
// table shape: a negative daily budget is a 400, same as the monthly field.
func TestTeamsHandler_budgetPerDayValidation(t *testing.T) {
	h := NewTeamsHandler(newTestStore(t), nil, nil)
	cases := []struct {
		name, path, body string
	}{
		{"negative budget_per_day", "/admin/teams/t", `{"budget_usd_micros_per_day":-1}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := doAsTeams(t, h, &adminID, "PUT", c.path, c.body)
			if rec.Code != 400 {
				t.Fatalf("%s: got %d %s, want 400", c.name, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestTeamsHandler_budgetPerDayZeroSurfacedAsZero is the INV-2 pin for the
// team view: teamView emits budget_usd_micros_per_day unconditionally for a
// record (matching budget_usd_micros' own convention), so an upsert that never
// mentions either budget field still surfaces the day field, as 0.
func TestTeamsHandler_budgetPerDayZeroSurfacedAsZero(t *testing.T) {
	h := NewTeamsHandler(newTestStore(t), nil, nil)
	rec := doAsTeams(t, h, &adminID, "PUT", "/admin/teams/t", `{}`)
	if rec.Code != 200 {
		t.Fatalf("empty body upsert: %d %s", rec.Code, rec.Body.String())
	}
	rec = doAsTeams(t, h, &adminID, "GET", "/admin/teams", "")
	if rec.Code != 200 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Data) != 1 {
		t.Fatalf("list returned %d rows, want 1: %+v", len(list.Data), list.Data)
	}
	got, ok := list.Data[0]["budget_usd_micros_per_day"]
	if !ok {
		t.Fatalf("budget_usd_micros_per_day missing from record view: %+v", list.Data[0])
	}
	if got.(float64) != 0 {
		t.Fatalf("budget_usd_micros_per_day = %v, want 0 (zero means unlimited)", got)
	}
}

// TestCreateKey_budgetPerDayRoundTrip uses DISTINCT values on the two budget
// dimensions and asserts both, so a transposition in toKeyOptions fails.
func TestCreateKey_budgetPerDayRoundTrip(t *testing.T) {
	h := NewKeysHandler(newTestStore(t), nil)
	body := `{"team":"platform-eng","allowed_models":["*"],"budget_usd_micros":5000000,"budget_usd_micros_per_day":250000}`
	rec := doAs(t, h, &adminID, "POST", "/admin/keys", body)
	if rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["budget_usd_micros"] != float64(5000000) {
		t.Fatalf("budget_usd_micros = %v, want 5000000", out["budget_usd_micros"])
	}
	if out["budget_usd_micros_per_day"] != float64(250000) {
		t.Fatalf("budget_usd_micros_per_day = %v, want 250000", out["budget_usd_micros_per_day"])
	}
}

func TestCreateKey_budgetPerDayNegativeRejected(t *testing.T) {
	h := NewKeysHandler(newTestStore(t), nil)
	rec := doAs(t, h, &adminID, "POST", "/admin/keys", `{"team":"t","budget_usd_micros_per_day":-1}`)
	if rec.Code != 400 {
		t.Fatalf("negative budget_usd_micros_per_day: got %d %s, want 400", rec.Code, rec.Body.String())
	}
}

// TestListKeys_omitsZeroBudgetPerDay guards keyView's != 0 emit: the string
// "budget_usd_micros_per_day" contains "budget_usd_micros" as a substring, so
// an unconditional emit would break TestListKeys_omitsZeroGovernanceFields
// with a message pointing at the wrong field. This test keeps the guard from
// silently regressing.
func TestListKeys_omitsZeroBudgetPerDay(t *testing.T) {
	store := newTestStore(t)
	store.Create(context.Background(), "t", []string{"*"})
	h := NewKeysHandler(store, nil)
	rec := doAs(t, h, &adminID, "GET", "/admin/keys", "")
	if rec.Code != 200 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "budget_usd_micros_per_day") {
		t.Fatalf("zero-value budget_usd_micros_per_day should be omitted: %s", rec.Body.String())
	}
}
