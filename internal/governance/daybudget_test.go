package governance

import (
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/metrics"
	"github.com/inferplane/inferplane/internal/pricing"
)

// noonZone returns a fixed zone in which "now" is roughly midday, so a
// CalDay window's boundary is ~12 hours away in either direction.
//
// budget.Memory's clock is unexported, so this package cannot inject a fake
// one and every test here runs against the real wall clock. A CalDay bucket
// created at 23:59:59.9 local would roll between a Debit and the PreCheck that
// reads it, which is a once-in-a-few-thousand-runs flake nobody would ever
// reproduce. Anchoring the zone to the current instant removes the race
// entirely rather than making it rarer.
func noonZone() *time.Location {
	now := time.Now().UTC()
	sinceMidnight := time.Duration(now.Hour())*time.Hour +
		time.Duration(now.Minute())*time.Minute +
		time.Duration(now.Second())*time.Second
	off := 12*time.Hour - sinceMidnight
	// Normalize into (-12h, +12h] so the offset stays a plausible zone.
	for off > 12*time.Hour {
		off -= 24 * time.Hour
	}
	for off <= -12*time.Hour {
		off += 24 * time.Hour
	}
	return time.FixedZone("NOON", int(off/time.Second))
}

// TestPreCheckDailyBudgetBlocksWhileMonthlyAllows is the TDD entry point for
// the daily team budget: a team under its monthly cap but over its DAILY cap
// must be denied 402, and the two counters must not share a bucket (Phase 0's
// window-tag-first budget.Key is what makes that true).
func TestPreCheckDailyBudgetBlocksWhileMonthlyAllows(t *testing.T) {
	loc := noonZone()
	dw := budget.CalendarDayIn(loc)

	g := NewGovernor(map[string]TeamPolicy{
		"t": {
			BudgetMicrosPerMonth: 1_000_000_000, // $1000/month — nowhere near breached
			BudgetExceeded:       "block",
			BudgetMicrosPerDay:   1_000, // $0.001/day
			BudgetDayExceeded:    "block",
		},
	}, limiter.NewMemory(), budget.NewMemory(), nil)
	g.SetBudgetTimezone(loc)

	// Spend past the daily cap only. The monthly counter stays at zero because
	// it is a DIFFERENT store key.
	g.bud.Debit(budget.Key(budget.ScopeTeam, "t", dw), 2_000, dw)

	if got := g.bud.Spent(budget.Key(budget.ScopeTeam, "t", budget.CalendarMonth), budget.CalendarMonth); got != 0 {
		t.Fatalf("daily debit leaked into the monthly bucket: monthly spent = %d, want 0", got)
	}

	dec := g.PreCheck("t", "", KeyPolicy{}, 0)
	if dec.Allowed {
		t.Fatalf("daily budget exhausted must deny: %+v", dec)
	}
	if dec.Status != 402 {
		t.Fatalf("daily budget deny status = %d, want 402", dec.Status)
	}
	if dec.Code != audit.DenyTeamBudgetExceeded {
		t.Fatalf("daily budget deny code = %q, want %q", dec.Code, audit.DenyTeamBudgetExceeded)
	}
	if !strings.HasPrefix(dec.Reason, "daily budget exceeded") {
		t.Fatalf("daily budget deny reason = %q, want it to name the DAILY window", dec.Reason)
	}
}

// TestPreCheckDailyBudgetZeroMeansUnlimited is the INV-2 pin: with the daily
// limit left unset, a debited daily bucket must change nothing — 0 means "not
// limited on this dimension", exactly as BudgetMicrosPerMonth == 0 already does.
func TestPreCheckDailyBudgetZeroMeansUnlimited(t *testing.T) {
	loc := noonZone()
	dw := budget.CalendarDayIn(loc)

	g := NewGovernor(map[string]TeamPolicy{
		"t": {BudgetMicrosPerMonth: 1_000_000_000, BudgetExceeded: "block"}, // no daily cap
	}, limiter.NewMemory(), budget.NewMemory(), nil)
	g.SetBudgetTimezone(loc)
	g.bud.Debit(budget.Key(budget.ScopeTeam, "t", dw), 999_999_999, dw)

	if dec := g.PreCheck("t", "", KeyPolicy{}, 0); !dec.Allowed {
		t.Fatalf("an unset daily limit must not enforce anything: %+v", dec)
	}
}

// blockingStore is a budget.BudgetStore that always blocks and reports a
// caller-chosen ResetsAt per window tag. It exists because the soonest-reset
// tie-break cannot be exercised against the real store: making the next daily
// midnight fall AFTER the month boundary needs "now" to be the last day of a
// month in a specific timezone, which no test can arrange from the wall clock.
type blockingStore struct{ resets map[string]time.Time }

func (b blockingStore) Check(string, int64, int64, budget.Window) budget.Decision {
	return budget.Block
}
func (b blockingStore) Debit(string, int64, budget.Window) {}
func (b blockingStore) Spent(string, budget.Window) int64  { return 0 }
func (b blockingStore) ResetsAt(_ string, w budget.Window) time.Time {
	return b.resets[w.Tag()]
}

// TestPreCheckReturnsSoonestResettingBudgetWindow pins "block wins on tie,
// soonest-binding window wins the 402": when BOTH windows would deny, the
// response must name whichever one the caller can actually retry after. Both
// directions are covered, because a non-UTC operator timezone genuinely can
// put the next daily midnight after the UTC month boundary.
func TestPreCheckReturnsSoonestResettingBudgetWindow(t *testing.T) {
	base := time.Date(2026, 8, 31, 20, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		dayResets  time.Time
		monthReset time.Time
		wantPrefix string
	}{
		{
			name:       "the day window resets first",
			dayResets:  base.Add(4 * time.Hour),
			monthReset: base.Add(30 * 24 * time.Hour),
			wantPrefix: "daily budget exceeded",
		},
		{
			name:       "the month window resets first",
			dayResets:  base.Add(19 * time.Hour), // next KST midnight, past the UTC 1st
			monthReset: base.Add(4 * time.Hour),  // 2026-09-01T00:00Z
			wantPrefix: "budget exceeded",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := blockingStore{resets: map[string]time.Time{
				"day":   tc.dayResets,
				"month": tc.monthReset,
			}}
			g := NewGovernor(map[string]TeamPolicy{
				"t": {
					BudgetMicrosPerMonth: 1_000,
					BudgetExceeded:       "block",
					BudgetMicrosPerDay:   1_000,
					BudgetDayExceeded:    "block",
				},
			}, limiter.NewMemory(), store, nil)

			dec := g.PreCheck("t", "", KeyPolicy{}, 0)
			if dec.Allowed || dec.Status != 402 {
				t.Fatalf("both windows block, want a 402 deny: %+v", dec)
			}
			if !strings.HasPrefix(dec.Reason, tc.wantPrefix) {
				t.Fatalf("reason = %q, want it to start with %q (the soonest-resetting window)", dec.Reason, tc.wantPrefix)
			}
		})
	}
}

// TestSettleDebitsBothBudgetWindows pins §C6's double debit: one Settle with a
// known cost must move the DAY counter and the MONTH counter by the same
// amount — two independent store keys, one debit each.
func TestSettleDebitsBothBudgetWindows(t *testing.T) {
	loc := noonZone()
	dw := budget.CalendarDayIn(loc)

	g := NewGovernor(map[string]TeamPolicy{
		"t": {
			BudgetMicrosPerMonth: 1_000_000_000,
			BudgetExceeded:       "block",
			BudgetMicrosPerDay:   1_000_000,
			BudgetDayExceeded:    "block",
		},
	}, limiter.NewMemory(), budget.NewMemory(), nil)
	g.SetBudgetTimezone(loc)

	cost, missing := g.Settle("t", "", KeyPolicy{}, "p", "m", pricing.Usage{Input: 1000, Output: 500}, testTable(), 0)
	if missing || cost != 1500 {
		t.Fatalf("settle cost=%d missing=%v, want 1500 µUSD priced", cost, missing)
	}

	if got := g.bud.Spent(budget.Key(budget.ScopeTeam, "t", dw), dw); got != cost {
		t.Fatalf("day counter spent = %d, want %d", got, cost)
	}
	if got := g.bud.Spent(budget.Key(budget.ScopeTeam, "t", budget.CalendarMonth), budget.CalendarMonth); got != cost {
		t.Fatalf("month counter spent = %d, want %d", got, cost)
	}
}

// TestSettleDailyDebitDoesNotTouchMetricsOrAlerts is §C6's no-gauge/no-hook
// rule made checkable: a team with ONLY a daily limit must settle without the
// budget-alert hook firing, because SetBudgetUtilization carries no window
// label and alert.Notifier dedupes by team alone — a daily write here would
// flap the gauge and suppress month crossings. This is what stops a future
// edit from silently double-writing the unlabelled budget gauge.
func TestSettleDailyDebitDoesNotTouchMetricsOrAlerts(t *testing.T) {
	loc := noonZone()

	m := metrics.New()
	g := NewGovernor(map[string]TeamPolicy{
		"t": {
			BudgetMicrosPerMonth: 0, // no monthly limit — only the daily one
			BudgetMicrosPerDay:   1_000_000,
			BudgetDayExceeded:    "block",
		},
	}, limiter.NewMemory(), budget.NewMemory(), m)
	g.SetBudgetTimezone(loc)

	calls := 0
	g.SetBudgetNotify(func(team string, spentMicros, limitMicros int64) { calls++ })

	g.Settle("t", "", KeyPolicy{}, "p", "m", pricing.Usage{Input: 1000, Output: 500}, testTable(), 0)
	if calls != 0 {
		t.Fatalf("budget-alert hook fired %d times for a daily-only debit, want 0", calls)
	}
}

// TestUsageOfReportsBothBudgetWindows pins §C7: with all four limits set,
// UsageOf must populate the month pair AND the day pair, each with its own
// window string and its OWN limit — four distinct numbers so a transposition
// fails.
func TestUsageOfReportsBothBudgetWindows(t *testing.T) {
	loc := noonZone()

	g := NewGovernor(map[string]TeamPolicy{
		"t": {
			BudgetMicrosPerMonth: 1_001,
			BudgetExceeded:       "block",
			BudgetMicrosPerDay:   2_002,
			BudgetDayExceeded:    "block",
		},
	}, limiter.NewMemory(), budget.NewMemory(), nil)
	g.SetBudgetTimezone(loc)

	u := g.UsageOf("t", "k", KeyPolicy{BudgetMicrosPerMonth: 3_003, BudgetMicrosPerDay: 4_004})
	if u.TeamBudget == nil || u.TeamBudgetDay == nil || u.KeyBudget == nil || u.KeyBudgetDay == nil {
		t.Fatalf("all four budget fields must be set: %+v", u)
	}
	if u.TeamBudget.Window != "calendar-month" {
		t.Fatalf("team_budget window = %q, want %q", u.TeamBudget.Window, "calendar-month")
	}
	if u.KeyBudget.Window != "calendar-month" {
		t.Fatalf("key_budget window = %q, want %q", u.KeyBudget.Window, "calendar-month")
	}
	if u.TeamBudgetDay.Window != "calendar-day" {
		t.Fatalf("team_budget_day window = %q, want %q", u.TeamBudgetDay.Window, "calendar-day")
	}
	if u.KeyBudgetDay.Window != "calendar-day" {
		t.Fatalf("key_budget_day window = %q, want %q", u.KeyBudgetDay.Window, "calendar-day")
	}
	if u.TeamBudget.LimitUSDMicros != 1_001 {
		t.Fatalf("team_budget limit = %d, want 1001", u.TeamBudget.LimitUSDMicros)
	}
	if u.TeamBudgetDay.LimitUSDMicros != 2_002 {
		t.Fatalf("team_budget_day limit = %d, want 2002", u.TeamBudgetDay.LimitUSDMicros)
	}
	if u.KeyBudget.LimitUSDMicros != 3_003 {
		t.Fatalf("key_budget limit = %d, want 3003", u.KeyBudget.LimitUSDMicros)
	}
	if u.KeyBudgetDay.LimitUSDMicros != 4_004 {
		t.Fatalf("key_budget_day limit = %d, want 4004", u.KeyBudgetDay.LimitUSDMicros)
	}
}

// TestUsageOfOmitsDayFieldsWhenUnset is the /v1/usage INV-2 pin: with the
// daily limits unset, both day pointers must stay nil so their omitempty tags
// keep the serialized body byte-identical for existing clients.
func TestUsageOfOmitsDayFieldsWhenUnset(t *testing.T) {
	g := NewGovernor(map[string]TeamPolicy{
		"t": {BudgetMicrosPerMonth: 1_000_000, BudgetExceeded: "block"},
	}, limiter.NewMemory(), budget.NewMemory(), nil)

	u := g.UsageOf("t", "k", KeyPolicy{BudgetMicrosPerMonth: 500_000})
	if u.TeamBudgetDay != nil {
		t.Fatalf("team_budget_day must be nil with no daily limit: %+v", u.TeamBudgetDay)
	}
	if u.KeyBudgetDay != nil {
		t.Fatalf("key_budget_day must be nil with no daily limit: %+v", u.KeyBudgetDay)
	}
}

// TestPreCheckKeyDailyBudgetBlocks is the per-key analogue of the daily team
// test: a key over its daily cap must deny 402 even with no team policy at
// all, and — a per-key limit having no on_exceeded knob — it always blocks.
func TestPreCheckKeyDailyBudgetBlocks(t *testing.T) {
	loc := noonZone()
	dw := budget.CalendarDayIn(loc)

	g := NewGovernor(map[string]TeamPolicy{}, limiter.NewMemory(), budget.NewMemory(), nil)
	g.SetBudgetTimezone(loc)
	g.bud.Debit(budget.Key(budget.ScopeKey, "k", dw), 2_000, dw)

	dec := g.PreCheck("t", "k", KeyPolicy{BudgetMicrosPerDay: 1_000}, 0)
	if dec.Allowed {
		t.Fatalf("key daily budget exhausted must deny: %+v", dec)
	}
	if dec.Status != 402 {
		t.Fatalf("key daily budget deny status = %d, want 402", dec.Status)
	}
	if dec.Code != audit.DenyKeyBudgetExceeded {
		t.Fatalf("key daily budget deny code = %q, want %q", dec.Code, audit.DenyKeyBudgetExceeded)
	}
	if !strings.HasPrefix(dec.Reason, "key daily budget exceeded") {
		t.Fatalf("key daily budget deny reason = %q, want it to name the key DAILY window", dec.Reason)
	}
}

// TestPreCheckMonthlyBudgetUnchangedWhenDailyUnset is the INV-2 regression
// pin for the deny path: a team with only the monthly limit set must behave
// exactly as before the daily window existed — 402, the same reason prefix,
// the same audit code.
func TestPreCheckMonthlyBudgetUnchangedWhenDailyUnset(t *testing.T) {
	g := NewGovernor(map[string]TeamPolicy{
		"t": {BudgetMicrosPerMonth: 1_000, BudgetExceeded: "block"},
	}, limiter.NewMemory(), budget.NewMemory(), nil)
	g.bud.Debit(budget.Key(budget.ScopeTeam, "t", budget.CalendarMonth), 2_000, budgetWindow)

	dec := g.PreCheck("t", "", KeyPolicy{}, 0)
	if dec.Allowed {
		t.Fatalf("monthly budget exhausted must deny: %+v", dec)
	}
	if dec.Status != 402 {
		t.Fatalf("monthly budget deny status = %d, want 402", dec.Status)
	}
	if dec.Code != audit.DenyTeamBudgetExceeded {
		t.Fatalf("monthly budget deny code = %q, want %q", dec.Code, audit.DenyTeamBudgetExceeded)
	}
	if !strings.HasPrefix(dec.Reason, "budget exceeded") {
		t.Fatalf("monthly budget deny reason = %q, want the pre-existing prefix %q", dec.Reason, "budget exceeded")
	}
}
