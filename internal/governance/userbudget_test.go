package governance

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/pricing"
)

// TestUserBudgetBlocksTheUserAndNotTheTeam is the headline Phase 3 test: one
// user over their PERSONAL cap is denied 402 while a teammate on the SAME
// team — governed by the same per-user policy — stays allowed. The lookup
// deliberately returns the policy for BOTH sub-1 and sub-2: an implementation
// that keyed the user counter on the team alone would then charge sub-1's
// spend to sub-2 and fail the teammate assertion, which is the mutation this
// test exists to catch. A subject with no user at all must also stay allowed
// (the s.User == "" guard skips the lookup entirely).
func TestUserBudgetBlocksTheUserAndNotTheTeam(t *testing.T) {
	loc := noonZone()

	// No team budget at all — only the per-user policy governs.
	g := NewGovernor(map[string]TeamPolicy{}, limiter.NewMemory(), budget.NewMemory(), nil)
	g.SetBudgetTimezone(loc)
	g.SetUserLookup(func(team, user string) (UserPolicy, bool) {
		if team == "t" && (user == "sub-1" || user == "sub-2") {
			return UserPolicy{BudgetMicrosPerMonth: 1_000, BudgetExceeded: "block"}, true
		}
		return UserPolicy{}, false
	})

	// Spend 2_000 µUSD as sub-1 — past the 1_000 µUSD personal cap.
	cost, missing := g.Settle(Subject{Team: "t", User: "sub-1"}, KeyPolicy{}, "p", "m", pricing.Usage{Input: 1000, Output: 1000}, testTable(), 0)
	if missing || cost != 2_000 {
		t.Fatalf("settle cost=%d missing=%v, want 2000 µUSD priced", cost, missing)
	}

	dec := g.PreCheck(Subject{Team: "t", User: "sub-1"}, KeyPolicy{}, 0)
	if dec.Allowed {
		t.Fatalf("user budget exhausted must deny: %+v", dec)
	}
	if dec.Status != 402 {
		t.Fatalf("user budget deny status = %d, want 402", dec.Status)
	}
	if dec.Code != audit.DenyUserBudgetExceeded {
		t.Fatalf("user budget deny code = %q, want %q", dec.Code, audit.DenyUserBudgetExceeded)
	}
	if !strings.Contains(dec.Reason, "user budget exceeded") {
		t.Fatalf("user budget deny reason = %q, want it to contain %q", dec.Reason, "user budget exceeded")
	}

	// The whole feature: a teammate under the SAME policy must be unaffected.
	if dec := g.PreCheck(Subject{Team: "t", User: "sub-2"}, KeyPolicy{}, 0); !dec.Allowed {
		t.Fatalf("sub-1's spend must not deny teammate sub-2: %+v", dec)
	}

	// And a subject with no user at all is the pre-Phase-3 path: allowed.
	if dec := g.PreCheck(Subject{Team: "t"}, KeyPolicy{}, 0); !dec.Allowed {
		t.Fatalf("a subject with no user must not be user-governed: %+v", dec)
	}
}

// TestUserBudgetDayAndMonthAreSeparateCounters pins the two-window shape: a
// user over their DAY cap but under their MONTH cap is denied by the day
// window, while the month counter carries the same debit against its own,
// much larger, limit. The two limits differ by 20x so a swapped pair of
// arguments denies the wrong window and fails loudly.
func TestUserBudgetDayAndMonthAreSeparateCounters(t *testing.T) {
	loc := noonZone()
	dw := budget.CalendarDayIn(loc)
	mw := budget.CalendarMonthIn(loc)

	g := NewGovernor(map[string]TeamPolicy{}, limiter.NewMemory(), budget.NewMemory(), nil)
	g.SetBudgetTimezone(loc)
	g.SetUserLookup(func(team, user string) (UserPolicy, bool) {
		if team == "t" && user == "sub-1" {
			return UserPolicy{BudgetMicrosPerDay: 50_000, BudgetMicrosPerMonth: 1_000_000, BudgetExceeded: "block"}, true
		}
		return UserPolicy{}, false
	})

	// 60_000 µUSD: over the 50_000 day cap, well under the 1_000_000 month cap.
	cost, missing := g.Settle(Subject{Team: "t", User: "sub-1"}, KeyPolicy{}, "p", "m", pricing.Usage{Input: 30_000, Output: 30_000}, testTable(), 0)
	if missing || cost != 60_000 {
		t.Fatalf("settle cost=%d missing=%v, want 60000 µUSD priced", cost, missing)
	}

	dec := g.PreCheck(Subject{Team: "t", User: "sub-1"}, KeyPolicy{}, 0)
	if dec.Allowed || dec.Status != 402 {
		t.Fatalf("day cap exhausted must deny 402: %+v", dec)
	}
	if !strings.Contains(dec.Reason, "user daily budget exceeded") {
		t.Fatalf("deny reason = %q, want the DAY window (%q) to bind", dec.Reason, "user daily budget exceeded")
	}

	// Both counters carry the debit — separate store keys, one debit each.
	if got := g.bud.Spent(budget.Key(budget.ScopeUser, "t/sub-1", dw), dw); got != 60_000 {
		t.Fatalf("user day counter spent = %d, want 60000", got)
	}
	monthSpent := g.bud.Spent(budget.Key(budget.ScopeUser, "t/sub-1", mw), mw)
	if monthSpent != 60_000 {
		t.Fatalf("user month counter spent = %d, want 60000", monthSpent)
	}
	if monthSpent >= 1_000_000 {
		t.Fatalf("month counter spent = %d must be under its own 1000000 limit — only the day window binds", monthSpent)
	}
}

// TestUserBudgetWarnDoesNotDeny pins the warn posture: over the cap with
// BudgetExceeded "warn", PreCheck still admits — and the debit still landed,
// because warn admits and still settles.
func TestUserBudgetWarnDoesNotDeny(t *testing.T) {
	loc := noonZone()
	mw := budget.CalendarMonthIn(loc)

	g := NewGovernor(map[string]TeamPolicy{}, limiter.NewMemory(), budget.NewMemory(), nil)
	g.SetBudgetTimezone(loc)
	g.SetUserLookup(func(team, user string) (UserPolicy, bool) {
		if team == "t" && user == "sub-1" {
			return UserPolicy{BudgetMicrosPerMonth: 1_000, BudgetExceeded: "warn"}, true
		}
		return UserPolicy{}, false
	})

	g.Settle(Subject{Team: "t", User: "sub-1"}, KeyPolicy{}, "p", "m", pricing.Usage{Input: 1000, Output: 1000}, testTable(), 0)

	if dec := g.PreCheck(Subject{Team: "t", User: "sub-1"}, KeyPolicy{}, 0); !dec.Allowed {
		t.Fatalf("warn must admit past the cap: %+v", dec)
	}
	if got := g.bud.Spent(budget.Key(budget.ScopeUser, "t/sub-1", mw), mw); got != 2_000 {
		t.Fatalf("warn must still settle: user month counter spent = %d, want 2000", got)
	}
}

// TestUserBudgetNilLookupIsByteIdenticalToPhase2 pins the default: with no
// SetUserLookup call, a Subject carrying a User behaves exactly like one
// without — allowed, no user counter written, no user field in /v1/usage.
func TestUserBudgetNilLookupIsByteIdenticalToPhase2(t *testing.T) {
	loc := noonZone()
	mw := budget.CalendarMonthIn(loc)

	g := NewGovernor(map[string]TeamPolicy{}, limiter.NewMemory(), budget.NewMemory(), nil)
	g.SetBudgetTimezone(loc)

	if dec := g.PreCheck(Subject{Team: "t", User: "sub-1"}, KeyPolicy{}, 0); !dec.Allowed {
		t.Fatalf("nil lookup must not deny: %+v", dec)
	}
	if dec := g.PreCheck(Subject{Team: "t"}, KeyPolicy{}, 0); !dec.Allowed {
		t.Fatalf("no-user subject must be allowed too: %+v", dec)
	}

	g.Settle(Subject{Team: "t", User: "sub-1"}, KeyPolicy{}, "p", "m", pricing.Usage{Input: 1000, Output: 1000}, testTable(), 0)

	u := g.UsageOf(Subject{Team: "t", User: "sub-1"}, KeyPolicy{})
	if u.UserBudget != nil {
		t.Fatalf("user_budget must be nil with no lookup installed: %+v", u.UserBudget)
	}
	if u.UserBudgetDay != nil {
		t.Fatalf("user_budget_day must be nil with no lookup installed: %+v", u.UserBudgetDay)
	}
	if got := g.bud.Spent(budget.Key(budget.ScopeUser, "t/sub-1", mw), mw); got != 0 {
		t.Fatalf("no user counter may exist with a nil lookup: spent = %d, want 0", got)
	}
}

// TestUserBudgetLookupMissIsUngoverned pins the lookup's second return value:
// installed but returning false for this (team, user) means no user counter
// is checked or debited and no user field is reported.
func TestUserBudgetLookupMissIsUngoverned(t *testing.T) {
	loc := noonZone()
	mw := budget.CalendarMonthIn(loc)

	g := NewGovernor(map[string]TeamPolicy{}, limiter.NewMemory(), budget.NewMemory(), nil)
	g.SetBudgetTimezone(loc)
	g.SetUserLookup(func(team, user string) (UserPolicy, bool) {
		if team == "t" && user == "someone-else" {
			return UserPolicy{BudgetMicrosPerMonth: 1_000, BudgetExceeded: "block"}, true
		}
		return UserPolicy{}, false
	})

	g.Settle(Subject{Team: "t", User: "sub-1"}, KeyPolicy{}, "p", "m", pricing.Usage{Input: 1000, Output: 1000}, testTable(), 0)

	if dec := g.PreCheck(Subject{Team: "t", User: "sub-1"}, KeyPolicy{}, 0); !dec.Allowed {
		t.Fatalf("a lookup miss must leave the user ungoverned: %+v", dec)
	}
	if got := g.bud.Spent(budget.Key(budget.ScopeUser, "t/sub-1", mw), mw); got != 0 {
		t.Fatalf("a lookup miss must not debit a user counter: spent = %d, want 0", got)
	}
	if u := g.UsageOf(Subject{Team: "t", User: "sub-1"}, KeyPolicy{}); u.UserBudget != nil {
		t.Fatalf("user_budget must be nil on a lookup miss: %+v", u.UserBudget)
	}
}

// TestUsageOfReportsUserWindows pins §C5's reporting: both user windows with
// their own limit, window string, spend and remaining — every number distinct
// so a transposition fails. The user fields must be ADDITIVE: the team and
// key slots stay nil, never reused.
func TestUsageOfReportsUserWindows(t *testing.T) {
	loc := noonZone()

	g := NewGovernor(map[string]TeamPolicy{}, limiter.NewMemory(), budget.NewMemory(), nil)
	g.SetBudgetTimezone(loc)
	g.SetUserLookup(func(team, user string) (UserPolicy, bool) {
		if team == "t" && user == "sub-1" {
			return UserPolicy{BudgetMicrosPerMonth: 3_003_003, BudgetMicrosPerDay: 4_004, BudgetExceeded: "block"}, true
		}
		return UserPolicy{}, false
	})

	cost, missing := g.Settle(Subject{Team: "t", User: "sub-1"}, KeyPolicy{}, "p", "m", pricing.Usage{Input: 1000, Output: 1}, testTable(), 0)
	if missing || cost != 1_001 {
		t.Fatalf("settle cost=%d missing=%v, want 1001 µUSD priced", cost, missing)
	}

	u := g.UsageOf(Subject{Team: "t", User: "sub-1"}, KeyPolicy{})
	if u.UserBudget == nil || u.UserBudgetDay == nil {
		t.Fatalf("both user budget fields must be set: %+v", u)
	}
	if u.UserBudget.LimitUSDMicros != 3_003_003 {
		t.Fatalf("user_budget limit = %d, want 3003003", u.UserBudget.LimitUSDMicros)
	}
	if u.UserBudget.Window != "calendar-month" {
		t.Fatalf("user_budget window = %q, want %q", u.UserBudget.Window, "calendar-month")
	}
	if u.UserBudget.SpentUSDMicros != 1_001 {
		t.Fatalf("user_budget spent = %d, want 1001", u.UserBudget.SpentUSDMicros)
	}
	if u.UserBudget.RemainingUSDMicros != 3_002_002 {
		t.Fatalf("user_budget remaining = %d, want 3002002", u.UserBudget.RemainingUSDMicros)
	}
	if u.UserBudgetDay.LimitUSDMicros != 4_004 {
		t.Fatalf("user_budget_day limit = %d, want 4004", u.UserBudgetDay.LimitUSDMicros)
	}
	if u.UserBudgetDay.Window != "calendar-day" {
		t.Fatalf("user_budget_day window = %q, want %q", u.UserBudgetDay.Window, "calendar-day")
	}
	if u.UserBudgetDay.SpentUSDMicros != 1_001 {
		t.Fatalf("user_budget_day spent = %d, want 1001", u.UserBudgetDay.SpentUSDMicros)
	}
	if u.UserBudgetDay.RemainingUSDMicros != 3_003 {
		t.Fatalf("user_budget_day remaining = %d, want 3003", u.UserBudgetDay.RemainingUSDMicros)
	}
	if u.TeamBudget != nil {
		t.Fatalf("team_budget must stay nil — user fields are additive, never a reuse: %+v", u.TeamBudget)
	}
	if u.KeyBudget != nil {
		t.Fatalf("key_budget must stay nil — user fields are additive, never a reuse: %+v", u.KeyBudget)
	}
}

// TestUserBudgetJSONKeysAreAppended pins the wire contract: the two user
// fields serialize as "user_budget"/"user_budget_day" and omitempty holds, so
// an existing /v1/usage client reads exactly what it read before. The day key
// is asserted as the full `"user_budget_day":` including the colon, because
// "user_budget" is a PREFIX of "user_budget_day" and a bare substring check
// could not tell the two apart.
func TestUserBudgetJSONKeysAreAppended(t *testing.T) {
	empty, err := json.Marshal(UsageStatus{Team: "t"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(empty), `"user_budget"`) {
		t.Fatalf("omitempty must drop user_budget when unset: %s", empty)
	}
	if strings.Contains(string(empty), `"user_budget_day"`) {
		t.Fatalf("omitempty must drop user_budget_day when unset: %s", empty)
	}

	full, err := json.Marshal(UsageStatus{
		Team:          "t",
		UserBudget:    &BudgetUsage{LimitUSDMicros: 1},
		UserBudgetDay: &BudgetUsage{LimitUSDMicros: 2},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(full), `"user_budget":`) {
		t.Fatalf("serialized body must carry the user_budget key: %s", full)
	}
	if !strings.Contains(string(full), `"user_budget_day":`) {
		t.Fatalf("serialized body must carry the user_budget_day key: %s", full)
	}
}
