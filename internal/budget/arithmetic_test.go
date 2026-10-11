package budget

import (
	"math"
	"testing"
	"time"
)

func TestSpendCannotWrapOrAcceptNegativeDebits(t *testing.T) {
	b := NewMemory()
	w := CalendarMonthIn(time.UTC)
	b.Debit("team", math.MaxInt64-10, w)
	b.Debit("team", -100, w)
	if got := b.Spent("team", w); got != math.MaxInt64-10 {
		t.Errorf("negative debit changed spend to %d", got)
	}
	if got := b.Check("team", math.MaxInt64, math.MaxInt64, w); got != Block {
		t.Errorf("overflowing estimate allowed: %v", got)
	}
	if got := b.Check("team", -1, math.MaxInt64, w); got != Block {
		t.Errorf("negative estimate allowed: %v", got)
	}
	b.Debit("team", 111, w)
	if got := b.Spent("team", w); got != math.MaxInt64 {
		t.Errorf("overflowing debit changed spend to %d", got)
	}
	if got := b.Check("team", 0, math.MaxInt64-1, w); got != Block {
		t.Errorf("overflow restored budget capacity: %v", got)
	}
}
