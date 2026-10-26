package governance

import (
	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/limiter"
	"sync"
	"testing"
	"time"
)

type calendarTestBudget struct {
	mu      sync.Mutex
	now     time.Time
	entries map[string]calendarTestEntry
}
type calendarTestEntry struct {
	end   time.Time
	spent int64
}

func (b *calendarTestBudget) current(k string, w budget.Window) calendarTestEntry {
	e := b.entries[k]
	if !b.now.Before(e.end) {
		n := b.now.UTC()
		end := time.Date(n.Year(), n.Month(), n.Day()+1, 0, 0, 0, 0, time.UTC)
		if w.Kind == budget.CalMonth {
			end = time.Date(n.Year(), n.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		}
		e = calendarTestEntry{end: end}
		b.entries[k] = e
	}
	return e
}
func (b *calendarTestBudget) Check(k string, n, l int64, w budget.Window) budget.Decision {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.current(k, w)
	if e.spent > l || n > l-e.spent {
		return budget.Block
	}
	return budget.Allow
}
func (b *calendarTestBudget) Debit(k string, n int64, w budget.Window) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.current(k, w)
	e.spent += n
	b.entries[k] = e
}
func (b *calendarTestBudget) Spent(k string, w budget.Window) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.current(k, w).spent
}
func (b *calendarTestBudget) ResetsAt(k string, w budget.Window) time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.current(k, w).end
}
func (b *calendarTestBudget) advance(now time.Time) { b.mu.Lock(); defer b.mu.Unlock(); b.now = now }

func TestLocalMoneyLateOldPermitDoesNotReleaseNewWindowLiability(t *testing.T) {
	b := &calendarTestBudget{now: time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC), entries: map[string]calendarTestEntry{}}
	g := NewGovernor(map[string]TeamPolicy{"t": {BudgetMicrosPerMonth: 100, BudgetMicrosPerDay: 100}}, limiter.NewMemory(), b, nil)
	s := Subject{Team: "t"}
	old, err := g.ReserveLocalBudget(s, KeyPolicy{}, 100, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.ReserveLocalBudget(s, KeyPolicy{}, 100, true); err == nil {
		t.Fatal("old window unresolved liability bypassed")
	}
	b.advance(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	fresh, err := g.ReserveLocalBudget(s, KeyPolicy{}, 100, true)
	if err != nil {
		t.Fatalf("calendar rollover did not reset: %v", err)
	}
	ten := int64(10)
	g.FinishLocalBudget(old, &ten, true, false, "p", "m")
	for _, u := range []*BudgetUsage{g.UsageOf(s, KeyPolicy{}).TeamBudget, g.UsageOf(s, KeyPolicy{}).TeamBudgetDay} {
		if u.ReservedUSDMicros != 100 || u.SpentUSDMicros != 10 {
			t.Fatalf("late old permit affected new hold: %+v", u)
		}
	}
	g.FinishLocalBudget(fresh, &ten, true, false, "p", "m")
	if u := g.UsageOf(s, KeyPolicy{}).TeamBudget; u.ReservedUSDMicros != 0 || u.SpentUSDMicros != 20 {
		t.Fatalf("new permit did not settle: %+v", u)
	}
}
