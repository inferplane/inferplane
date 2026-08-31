// Package budget tracks per-team spend in integer micro-USD (§5.3). Same
// two-phase optimistic-check + post-debit shape as quota, but the unit is
// money (µUSD), fed by the pricing table. In-memory now; Redis v0.2.
package budget

import (
	"strconv"
	"sync"
	"time"
)

type Decision int

const (
	Allow Decision = iota
	Block
	// BlockCapacity is the store's at-capacity fail-safe (Check could not
	// admit a genuinely new counter) — distinct from Block (a real
	// budget/quota breach) because a caller's on_exceeded=warn policy answers
	// "should a real breach still admit the request?", not "should we serve
	// a request the store can never account for?" A caller (governance) MUST
	// treat BlockCapacity as unconditional: never downgrade it via warn. See
	// Check's capacity branch for the full fail-closed rationale.
	BlockCapacity
)

// String makes a test failure or log line self-describing (e.g. "want
// BlockCapacity, got Block") instead of printing the bare underlying int.
func (d Decision) String() string {
	switch d {
	case Allow:
		return "Allow"
	case Block:
		return "Block"
	case BlockCapacity:
		return "BlockCapacity"
	default:
		return "Decision(" + strconv.Itoa(int(d)) + ")"
	}
}

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

// CalendarDayIn is CalendarMonthIn's daily counterpart: the counter ends at the
// next midnight in loc (nil = UTC, the same default every Window carries). It
// is a function, not a var, because the operator timezone is config-supplied
// (budget_timezone) rather than a compile-time constant — one Window value per
// caller, so nobody can mutate a shared one.
func CalendarDayIn(loc *time.Location) Window {
	return Window{Kind: CalDay, Loc: loc}
}

// CalendarMonthIn is the calendar-month window in an explicit timezone: the
// counter ends at the first instant of next month in loc (nil = UTC, the same
// default every Window carries). It exists so the MONTH window can honour the
// operator's budget_timezone the way CalendarDayIn already does — a daily cap
// anchored to KST midnight and a monthly cap anchored to UTC midnight would
// put two different boundaries into one billing reconciliation. A function,
// not a var, for the same reason CalendarDayIn is: a shared package-level
// Window value would be a mutable-in-place footgun (its fields are exported)
// with nothing enforcing "never reassign it" beyond a comment.
//
// Tag() is "month" for every CalMonth window regardless of Loc, so a store
// key built from any two CalendarMonthIn calls is the SAME key: switching the
// operator timezone moves a counter's boundary, never its identity.
func CalendarMonthIn(loc *time.Location) Window {
	return Window{Kind: CalMonth, Loc: loc}
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

// sweepEvery amortizes the expired-entry scan: every sweepEvery-th cur call
// walks the map once and deletes what has already expired. Chosen over both a
// probabilistic (rand) sweep and a secondary expiry-ordered heap. Against
// rand: this is deterministic, so a test can pin it without seeding a RNG.
// Against a heap: a heap is O(log n) on EVERY touch and is a second structure
// that has to stay consistent with m, which is a whole new class of bug for a
// money store — a full scan every 256 calls is amortized O(n/256) per call
// and cannot disagree with m, because m is the only structure there is.
const sweepEvery = 256

// defaultMaxEntries caps live counters per store. ~100k live counters is
// roughly 15 MB of map, and reaching it means 100k distinct (scope, id) pairs
// spent money inside ONE window — see cur for what happens then.
const defaultMaxEntries = 100_000

type Memory struct {
	mu         sync.Mutex
	m          map[string]*win
	now        func() time.Time
	maxEntries int
	sweepTick  int
	rejected   int64
}

func NewMemory() *Memory {
	return &Memory{m: map[string]*win{}, now: time.Now, maxEntries: defaultMaxEntries}
}

// Rejections reports how many store OPERATIONS were refused because the
// store was at capacity (not how many distinct keys). Non-zero means some
// request was denied 402 by the capacity fail-safe rather than by a real
// budget, which is an operator-visible condition — a metric with a user
// dimension is forbidden (CLAUDE.md), so this counter is the seam.
func (b *Memory) Rejections() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rejected
}

func (b *Memory) Check(key string, estimateMicros, limitMicros int64, w Window) Decision {
	if limitMicros <= 0 {
		return Allow // unlimited: no counter needed, so capacity cannot bite
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	bkt := b.cur(key, w)
	if bkt == nil {
		// At capacity with a REAL limit to enforce: fail closed. The three
		// candidate failure modes and why this is the only safe one:
		//   - Evicting a LIVE bucket would discard spend already debited
		//     against a real cap; the counter restarts at 0 and the cap is
		//     spent again — the store silently under-counts spend. Forbidden.
		//   - Refusing the new key and returning Allow leaves that cap
		//     unenforced — also silently under-counts. Forbidden.
		//   - Refusing the new key and denying the request instead of
		//     serving one the store cannot account for — the same
		//     fail-closed posture SetLeaseGate takes when a hard-cap lease
		//     expires. This is the choice — as BlockCapacity, not Block, so a
		//     caller's warn policy can never downgrade it back to Allow.
		return BlockCapacity
	}
	if bkt.spent+estimateMicros > limitMicros {
		return Block
	}
	return Allow
}

func (b *Memory) Debit(key string, actualMicros int64, w Window) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if bkt := b.cur(key, w); bkt != nil {
		bkt.spent += actualMicros
	}
}

func (b *Memory) Spent(key string, w Window) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	bkt := b.cur(key, w)
	if bkt == nil {
		return 0
	}
	return bkt.spent
}

// ResetsAt reports when the current window ends. When the store is at
// capacity and cannot admit a new counter for key, the boundary is computed
// from the clock without storing anything — an honest display value, since
// windowEnd depends only on (now, w), never on the bucket.
func (b *Memory) ResetsAt(key string, w Window) time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	bkt := b.cur(key, w)
	if bkt == nil {
		return windowEnd(b.now(), w)
	}
	return bkt.windowEnd
}

// cur returns key's current bucket, creating or rolling it as needed, or NIL
// when the store is at capacity and cannot admit a genuinely new counter.
// Every caller must handle nil; see Check for the fail-closed rationale.
//
// Deleting an EXPIRED entry cannot lose spend, and that is the whole
// correctness argument for the sweep: cur already discards an expired
// bucket's spend on the next touch of that key (it builds a fresh &win{}),
// so "delete now" and "leave it for cur to replace" are the same observable
// behaviour. The sweep only reclaims memory.
func (b *Memory) cur(key string, w Window) *win {
	t := b.now()
	b.sweepTick++
	if b.sweepTick >= sweepEvery {
		b.sweepTick = 0
		b.sweepExpired(t)
	}
	bkt := b.m[key]
	if bkt != nil && t.Before(bkt.windowEnd) {
		return bkt
	}
	// ROLLING AN EXISTING KEY MUST NEVER BE REFUSED. It replaces an entry
	// rather than adding one, so it cannot grow the map — and refusing it
	// would permanently block a team whose month simply turned over while
	// the store happened to be full. Only bkt == nil is a new key.
	if bkt == nil && len(b.m) >= b.maxEntries {
		b.sweepExpired(t) // last chance: reclaim dead windows before refusing
		if len(b.m) >= b.maxEntries {
			b.rejected++
			return nil
		}
	}
	bkt = &win{windowEnd: windowEnd(t, w)}
	b.m[key] = bkt
	return bkt
}

// sweepExpired deletes every bucket whose window has ended, using cur's own
// expiry predicate so there is exactly one definition of "expired".
func (b *Memory) sweepExpired(t time.Time) {
	for k, v := range b.m {
		if !t.Before(v.windowEnd) {
			delete(b.m, k)
		}
	}
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
