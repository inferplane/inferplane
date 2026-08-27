package budget

import (
	"fmt"
	"testing"
	"time"
)

// TestMemoryEvictsExpiredEntriesUnderLoad is the required red test for the
// bounded-eviction change: 5000 distinct keys, each debited once in its own
// already-expired window, must not leave 5000 buckets behind. The amortized
// sweep (every sweepEvery-th cur call) must reclaim them — asserted by the
// map staying bounded AND Rejections() staying 0, so the memory is reclaimed
// by SWEEPING, never by refusing.
func TestMemoryEvictsExpiredEntriesUnderLoad(t *testing.T) {
	b := NewMemory()
	base := time.Unix(1_700_000_000, 0)
	now := base
	b.now = func() time.Time { return now }
	rolling := Window{Kind: Rolling, Dur: time.Minute}
	for i := 0; i < 5000; i++ {
		// Every step advances the clock 2 minutes, so every previously
		// created 1-minute window has already expired.
		now = base.Add(time.Duration(i) * 2 * time.Minute)
		b.Debit(fmt.Sprintf("budget:r1m:user:t/u%d", i), 1_000, rolling)
	}
	if got := len(b.m); got > sweepEvery+1 {
		t.Fatalf("len(b.m) = %d, want <= %d (expired entries must be swept, not leaked)", got, sweepEvery+1)
	}
	if got := b.Rejections(); got != 0 {
		t.Fatalf("Rejections() = %d, want 0 (memory must be reclaimed by sweeping, not by refusing)", got)
	}
}

// TestMemorySweepKeepsLiveSpend is the over-fix guard: a sweep that drops
// LIVE buckets passes TestMemoryEvictsExpiredEntriesUnderLoad and fails this
// one. A long-lived key's spend must survive thousands of expired-key churns
// past the sweep threshold.
func TestMemorySweepKeepsLiveSpend(t *testing.T) {
	b := NewMemory()
	base := time.Unix(1_700_000_000, 0)
	now := base
	b.now = func() time.Time { return now }
	long := Window{Kind: Rolling, Dur: 24 * time.Hour}
	longKey := "budget:r24h0m0s:team:acme"
	b.Debit(longKey, 7_000_000, long) // $7
	short := Window{Kind: Rolling, Dur: time.Minute}
	// Create 3000 short-window keys with the clock frozen, then advance it
	// 2 minutes — every short window is now expired while the 24h key stays
	// live — and churn past the sweep threshold so the sweep actually runs.
	for i := 0; i < 3000; i++ {
		b.Debit(fmt.Sprintf("budget:r1m:user:t/u%d", i), 1_000, short)
	}
	now = base.Add(2 * time.Minute)
	for i := 0; i < 2*sweepEvery; i++ {
		b.Debit(fmt.Sprintf("budget:r1m:user:t/churn%d", i), 1_000, short)
	}
	if got := b.Spent(longKey, long); got != 7_000_000 {
		t.Fatalf("Spent(long-lived key) = %d, want 7000000 (sweep must never drop a live bucket)", got)
	}
}

// TestMemoryAtCapacityFailsClosedForNewKey pins the fail-safe's posture: at
// capacity a genuinely new key with a REAL limit is refused and Check returns
// Block — never Allow, which would admit spend the store cannot account for.
// A zero limit stays Allow: an uncapped dimension has no counter to enforce,
// so capacity must not deny it.
func TestMemoryAtCapacityFailsClosedForNewKey(t *testing.T) {
	b := NewMemory()
	b.maxEntries = 8
	now := time.Unix(1_700_000_000, 0)
	b.now = func() time.Time { return now }
	w := Window{Kind: Rolling, Dur: 24 * time.Hour}
	for i := 0; i < 8; i++ {
		b.Debit(fmt.Sprintf("budget:r24h0m0s:user:t/u%d", i), 1_000, w)
	}
	newKey := "budget:r24h0m0s:user:t/new"
	if d := b.Check(newKey, 0, 5_000_000, w); d != Block {
		t.Fatalf("Check(new key at capacity, real limit) = %v, want Block (fail closed)", d)
	}
	if got := b.Rejections(); got < 1 {
		t.Fatalf("Rejections() = %d, want >= 1", got)
	}
	if d := b.Check(newKey, 0, 0, w); d != Allow {
		t.Fatalf("Check(new key at capacity, zero limit) = %v, want Allow (unlimited never needs a counter)", d)
	}
	if got := b.Spent(newKey, w); got != 0 {
		t.Fatalf("Spent(refused key) = %d, want 0", got)
	}
	want := windowEnd(now, w)
	if got := b.ResetsAt(newKey, w); got.IsZero() || !got.Equal(want) {
		t.Fatalf("ResetsAt(refused key) = %s, want %s (computed, non-zero)", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	if got := len(b.m); got != 8 {
		t.Fatalf("len(b.m) = %d, want 8 (the refused key must not be stored)", got)
	}
}

// TestMemoryAtCapacityStillRollsAnExistingKey is the sharpest test in this
// file: an implementation that puts the capacity gate before the bkt == nil
// test passes every other test here and fails only this one — and the bug it
// ships is "a team whose month rolled over while the store was full is
// blocked forever". Rolling replaces an entry, it cannot grow the map, so it
// must never be refused.
func TestMemoryAtCapacityStillRollsAnExistingKey(t *testing.T) {
	b := NewMemory()
	b.maxEntries = 4
	base := time.Unix(1_700_000_000, 0)
	now := base
	b.now = func() time.Time { return now }
	w := Window{Kind: Rolling, Dur: time.Minute}
	for i := 0; i < 4; i++ {
		b.Debit(fmt.Sprintf("k%d", i), 1_000, w)
	}
	b.Debit("k0", 1_000_000, w) // $1 on k0
	// All four windows are now expired, but the call count is far below
	// sweepEvery, so nothing has been swept: the map is still full.
	now = base.Add(2 * time.Minute)
	if d := b.Check("k0", 0, 5_000_000, w); d != Allow {
		t.Fatalf("Check(existing key, expired window, store full) = %v, want Allow (a roll must never be refused)", d)
	}
	if got := b.Rejections(); got != 0 {
		t.Fatalf("Rejections() = %d, want 0 (rolling an existing key is not a capacity event)", got)
	}
}

// TestMemoryCapacitySweepsBeforeRefusing proves the last-chance sweepExpired
// inside the capacity branch runs before the refusal: a full store whose
// entries have all expired must admit a new key rather than reject it.
func TestMemoryCapacitySweepsBeforeRefusing(t *testing.T) {
	b := NewMemory()
	b.maxEntries = 4
	base := time.Unix(1_700_000_000, 0)
	now := base
	b.now = func() time.Time { return now }
	w := Window{Kind: Rolling, Dur: time.Minute}
	for i := 0; i < 4; i++ {
		b.Debit(fmt.Sprintf("k%d", i), 1_000, w)
	}
	now = base.Add(2 * time.Minute) // every entry expired
	if d := b.Check("fresh", 0, 5_000_000, w); d != Allow {
		t.Fatalf("Check(new key, full store of expired entries) = %v, want Allow (capacity branch must sweep before refusing)", d)
	}
	if got := b.Rejections(); got != 0 {
		t.Fatalf("Rejections() = %d, want 0", got)
	}
}
