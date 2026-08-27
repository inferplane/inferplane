package governance

import (
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/limiter"
)

// seoulLocation loads Asia/Seoul, skipping the test when the host carries no
// tzdata for it — asserting Seoul boundaries in a fallback zone would be
// meaningless.
func seoulLocation(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Skipf("Asia/Seoul tzdata unavailable: %v", err)
	}
	return loc
}

// TestUsageOfTeamMonthResetHonoursBudgetTimezone is the load-bearing pin for
// the month window honouring budget_timezone: with the operator zone set to
// Asia/Seoul, the TEAM month counter must reset at the first instant of next
// month IN SEOUL — and must NOT sit on the UTC month boundary. Both
// directions are asserted deliberately: a date-only or single-sided check
// would pass in UTC by accident, which is exactly the regression this test
// exists to catch.
func TestUsageOfTeamMonthResetHonoursBudgetTimezone(t *testing.T) {
	seoul := seoulLocation(t)
	g := NewGovernor(map[string]TeamPolicy{
		"t": {BudgetMicrosPerMonth: 1_000_000, BudgetExceeded: "block"},
	}, limiter.NewMemory(), budget.NewMemory(), nil)
	g.SetBudgetTimezone(seoul)

	u := g.UsageOf("t", "", KeyPolicy{})
	if u.TeamBudget == nil {
		t.Fatalf("team_budget must be set: %+v", u)
	}
	nowSeoul := time.Now().In(seoul)
	want := time.Date(nowSeoul.Year(), nowSeoul.Month()+1, 1, 0, 0, 0, 0, seoul)
	if !u.TeamBudget.ResetsAt.Equal(want) {
		t.Fatalf("team month ResetsAt = %v, want the first instant of next month in Seoul, %v", u.TeamBudget.ResetsAt, want)
	}
	nowUTC := time.Now().UTC()
	utcBoundary := time.Date(nowUTC.Year(), nowUTC.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	if u.TeamBudget.ResetsAt.Equal(utcBoundary) {
		t.Fatalf("team month ResetsAt = %v still sits on the UTC month boundary — budget_timezone is not honoured", u.TeamBudget.ResetsAt)
	}
}

// TestUsageOfKeyMonthResetHonoursBudgetTimezone is the per-KEY mirror of the
// team test above: the key month counter anchors to the same operator zone.
func TestUsageOfKeyMonthResetHonoursBudgetTimezone(t *testing.T) {
	seoul := seoulLocation(t)
	g := NewGovernor(map[string]TeamPolicy{}, limiter.NewMemory(), budget.NewMemory(), nil)
	g.SetBudgetTimezone(seoul)

	u := g.UsageOf("t", "k", KeyPolicy{BudgetMicrosPerMonth: 1_000_000})
	if u.KeyBudget == nil {
		t.Fatalf("key_budget must be set: %+v", u)
	}
	nowSeoul := time.Now().In(seoul)
	want := time.Date(nowSeoul.Year(), nowSeoul.Month()+1, 1, 0, 0, 0, 0, seoul)
	if !u.KeyBudget.ResetsAt.Equal(want) {
		t.Fatalf("key month ResetsAt = %v, want the first instant of next month in Seoul, %v", u.KeyBudget.ResetsAt, want)
	}
	nowUTC := time.Now().UTC()
	utcBoundary := time.Date(nowUTC.Year(), nowUTC.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	if u.KeyBudget.ResetsAt.Equal(utcBoundary) {
		t.Fatalf("key month ResetsAt = %v still sits on the UTC month boundary — budget_timezone is not honoured", u.KeyBudget.ResetsAt)
	}
}

// TestCalendarMonthInKeepsStoreKey is the guard against a future change
// silently orphaning every existing month counter: Tag() is "month" for ANY
// CalMonth window regardless of Loc, so a store key built from
// CalendarMonthIn(seoul) and one built from the UTC CalendarMonth var are the
// SAME string — switching the operator timezone moves a counter's boundary,
// never its identity.
func TestCalendarMonthInKeepsStoreKey(t *testing.T) {
	seoul := seoulLocation(t)
	got := budget.Key(budget.ScopeTeam, "t", budget.CalendarMonthIn(seoul))
	want := budget.Key(budget.ScopeTeam, "t", budget.CalendarMonth)
	if got != want {
		t.Fatalf("month store key moved with the timezone: %q != %q — every existing month counter would be orphaned", got, want)
	}
}
