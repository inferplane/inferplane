package budget

// Reserve/settle tests (strategy Phase 1): TryReserve counts spent PLUS
// outstanding holds atomically, so concurrent near-cap requests cannot all
// pass on the same remaining balance; Release frees exactly one hold; a
// leaked hold self-heals at its TTL.

import (
	"testing"
	"time"
)

func TestTryReserveCountsOutstandingHolds(t *testing.T) {
	b := NewMemory()
	now := time.Date(2027, 3, 15, 12, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return now }
	w := CalendarMonthIn(nil)

	// Limit 100, each request bounds at 60: the first reserves, the second
	// must NOT pass on the same remaining balance.
	if dec := b.TryReserve("k", 60, 100, w, time.Minute); dec != Allow {
		t.Fatalf("first reserve = %v, want Allow", dec)
	}
	if dec := b.TryReserve("k", 60, 100, w, time.Minute); dec != Block {
		t.Fatalf("concurrent second reserve = %v, want Block (hold not counted)", dec)
	}
	// A plain Check still sees spent only — warn windows keep their meaning.
	if dec := b.Check("k", 0, 100, w); dec != Allow {
		t.Fatalf("Check must not count holds: %v", dec)
	}

	// Settle: release the hold, debit the (smaller) actual. The freed
	// capacity admits the next request.
	b.Release("k", 60, w)
	b.Debit("k", 30, w)
	if dec := b.TryReserve("k", 60, 100, w, time.Minute); dec != Allow {
		t.Fatalf("post-settle reserve = %v, want Allow (30 spent + 60 fits 100)", dec)
	}
	// spent 30 + held 60 = 90: a 20-bound request must block, an 8 fits.
	if dec := b.TryReserve("k", 20, 100, w, time.Minute); dec != Block {
		t.Fatalf("over-cap reserve = %v, want Block", dec)
	}
	if dec := b.TryReserve("k", 8, 100, w, time.Minute); dec != Allow {
		t.Fatalf("fitting reserve = %v, want Allow", dec)
	}
}

func TestReservationExpiresAtTTL(t *testing.T) {
	b := NewMemory()
	now := time.Date(2027, 3, 15, 12, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return now }
	w := CalendarMonthIn(nil)

	if dec := b.TryReserve("k", 80, 100, w, time.Minute); dec != Allow {
		t.Fatalf("reserve: %v", dec)
	}
	if dec := b.TryReserve("k", 80, 100, w, time.Minute); dec != Block {
		t.Fatalf("held: %v, want Block", dec)
	}
	// The holder crashed; the TTL self-heals the leak.
	now = now.Add(2 * time.Minute)
	if dec := b.TryReserve("k", 80, 100, w, time.Minute); dec != Allow {
		t.Fatalf("post-TTL reserve = %v, want Allow (leaked hold expired)", dec)
	}
}

func TestReleaseNeverCreatesOrRollsACounter(t *testing.T) {
	b := NewMemory()
	now := time.Date(2027, 3, 15, 12, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return now }
	w := CalendarMonthIn(nil)

	b.Release("ghost", 50, w) // no counter: must not create one
	if got := len(b.m); got != 0 {
		t.Fatalf("release created a counter: %d entries", got)
	}
	if dec := b.TryReserve("k", 50, 100, w, time.Minute); dec != Allow {
		t.Fatalf("reserve: %v", dec)
	}
	// Releasing a DIFFERENT amount must not free the hold (a settle with a
	// mismatched estimate leaks until TTL — bounded, never a double-free).
	b.Release("k", 49, w)
	if dec := b.TryReserve("k", 60, 100, w, time.Minute); dec != Block {
		t.Fatalf("mismatched release freed the hold: %v", dec)
	}
	// Window rollover kills the hold with the window.
	now = now.Add(31 * 24 * time.Hour)
	b.Release("k", 50, w) // rolled: no-op, must not roll the bucket into existence
	if dec := b.TryReserve("k", 100, 100, w, time.Minute); dec != Allow {
		t.Fatalf("fresh window reserve = %v, want Allow", dec)
	}
}
