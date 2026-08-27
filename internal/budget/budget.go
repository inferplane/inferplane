// Package budget tracks per-team spend in integer micro-USD (§5.3). Same
// two-phase optimistic-check + post-debit shape as quota, but the unit is
// money (µUSD), fed by the pricing table. In-memory now; Redis v0.2.
package budget

import (
	"sync"
	"time"
)

type Decision int

const (
	Allow Decision = iota
	Block
)

type BudgetStore interface {
	Check(key string, estimateMicros, limitMicros int64, w Window) Decision
	Debit(key string, actualMicros int64, w Window)
	// Spent reports µUSD debited in the current window (0 if none or elapsed).
	// Used for the budget-utilization gauge and alert threshold evaluation
	// (D5b, ADR-017) — mirrors limiter.LimiterStore.QuotaUsed.
	Spent(key string, w Window) int64
	// ResetsAt reports when the current window ends (creating one with no
	// spend yet if none exists) — the client-facing "budget resets on ..."
	// timestamp for a CalendarMonth window (or any other).
	ResetsAt(key string, w Window) time.Time
}

// WindowKind selects how a Window's boundary is computed. Rolling is the
// original fixed-duration behavior (t+Dur); the calendar kinds anchor to a
// midnight instead, because a money cap that resets on a rolling N-day
// boundary reads as arbitrary to anyone reconciling spend against a billing
// period — "resets on the 1st" is what a monthly cap actually means.
type WindowKind uint8

const (
	Rolling WindowKind = iota // uses Dur
	CalDay
	CalMonth
)

// Window is a budget counter's reset rule. It replaced a bare time.Duration
// parameter carrying a negative sentinel, which had nowhere to put a
// timezone: Loc is the location the calendar kinds anchor their midnight to
// (nil = UTC, which is exactly what the sentinel always meant). Dur is read
// by Rolling only.
//
// Window holds a pointer, so it is not usefully comparable with == — pass it
// by value, never compare two of them.
type Window struct {
	Kind WindowKind
	Dur  time.Duration  // Rolling only
	Loc  *time.Location // nil = UTC
}

// CalendarMonth is the window every money cap has used since §5.3: the
// counter ends at the first instant of next month in Loc (nil here, so UTC).
// Kept under its original name so the call sites that predate WindowKind read
// unchanged.
//
// It is a var only because a struct cannot be a Go constant — never reassign
// it.
var CalendarMonth = Window{Kind: CalMonth}

// CalendarDayIn is CalendarMonth's daily counterpart: the counter ends at the
// next midnight in loc (nil = UTC, the same default every Window carries). It
// is a function, not a var, because the operator timezone is config-supplied
// (budget_timezone) rather than a compile-time constant — one Window value per
// caller, so nobody can mutate a shared one.
func CalendarDayIn(loc *time.Location) Window {
	return Window{Kind: CalDay, Loc: loc}
}

// Tag is the window's short, stable identifier for store-key namespacing:
// "day", "month", or "r"+Dur for a rolling window. See Key (keys.go) for why
// a window has to identify itself inside its own store key at all.
func (w Window) Tag() string {
	switch w.Kind {
	case CalDay:
		return "day"
	case CalMonth:
		return "month"
	default:
		return "r" + w.Dur.String()
	}
}

// loc resolves the window's timezone, defaulting to UTC — the behavior the
// pre-Window CalendarMonth sentinel hard-coded.
func (w Window) loc() *time.Location {
	if w.Loc == nil {
		return time.UTC
	}
	return w.Loc
}

type win struct {
	spent     int64
	windowEnd time.Time
}

type Memory struct {
	mu  sync.Mutex
	m   map[string]*win
	now func() time.Time
}

func NewMemory() *Memory { return &Memory{m: map[string]*win{}, now: time.Now} }

func (b *Memory) Check(key string, estimateMicros, limitMicros int64, w Window) Decision {
	if limitMicros <= 0 {
		return Allow
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	bkt := b.cur(key, w)
	if bkt.spent+estimateMicros > limitMicros {
		return Block
	}
	return Allow
}

func (b *Memory) Debit(key string, actualMicros int64, w Window) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cur(key, w).spent += actualMicros
}

func (b *Memory) Spent(key string, w Window) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cur(key, w).spent
}

func (b *Memory) ResetsAt(key string, w Window) time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cur(key, w).windowEnd
}

func (b *Memory) cur(key string, w Window) *win {
	t := b.now()
	bkt := b.m[key]
	if bkt == nil || !t.Before(bkt.windowEnd) {
		bkt = &win{windowEnd: windowEnd(t, w)}
		b.m[key] = bkt
	}
	return bkt
}

// windowEnd resolves a window's boundary from t: CalDay anchors to the next
// midnight in the window's timezone, CalMonth to the first instant of next
// month there, and Rolling is the plain t+Dur it always was.
func windowEnd(t time.Time, w Window) time.Time {
	switch w.Kind {
	case CalDay:
		l := w.loc()
		u := t.In(l)
		return time.Date(u.Year(), u.Month(), u.Day()+1, 0, 0, 0, 0, l)
	case CalMonth:
		l := w.loc()
		u := t.In(l)
		return time.Date(u.Year(), u.Month()+1, 1, 0, 0, 0, 0, l)
	default:
		return t.Add(w.Dur)
	}
}

var _ BudgetStore = (*Memory)(nil)
