package telemetry

import (
	"sync"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/pricing"
)

func TestCollectorFoldsSameKey(t *testing.T) {
	c := NewCollector("dp-1")
	c.Record("demo", "u1", "m1", pricing.Usage{Input: 10, Output: 5, CacheRead: 2, CacheWrite5m: 1, CacheWrite1h: 4}, 100)
	c.Record("demo", "u1", "m1", pricing.Usage{Input: 20, Output: 10, CacheRead: 3, CacheWrite5m: 2, CacheWrite1h: 6}, 50)
	c.Record("demo", "u2", "m1", pricing.Usage{Input: 1}, 7)

	b := c.Drain(time.Now())
	if b == nil {
		t.Fatal("Drain returned nil with recorded usage")
	}
	if b.Dataplane != "dp-1" {
		t.Fatalf("dataplane not stamped: %q", b.Dataplane)
	}
	if len(b.Entries) != 2 {
		t.Fatalf("want 2 entries (2 distinct keys), got %d", len(b.Entries))
	}
	var got *UsageEntry
	for i := range b.Entries {
		if b.Entries[i].User == "u1" {
			got = &b.Entries[i]
		}
	}
	if got == nil {
		t.Fatal("u1 entry missing")
	}
	// Folded sums — and the 5m/1h cache tiers land in their OWN fields
	// (collapsing them is the ADR-030 class of mis-billing).
	if got.SpentMicroUSD != 150 || got.InputTokens != 30 || got.OutputTokens != 15 {
		t.Fatalf("fold wrong: %+v", got)
	}
	if got.CacheReadTokens != 5 || got.CacheWrite5mTokens != 3 || got.CacheWrite1hTokens != 10 {
		t.Fatalf("cache tiers wrong: %+v", got)
	}
}

func TestDrainClosesWindowAndEmpties(t *testing.T) {
	c := NewCollector("dp-1")
	start := c.WindowStart()
	c.Record("demo", "", "m1", pricing.Usage{Input: 1}, 10)

	now := time.Now()
	b := c.Drain(now)
	if b == nil {
		t.Fatal("nil batch")
	}
	if !b.WindowStart.Equal(start) || !b.WindowEnd.Equal(now) {
		t.Fatalf("window bounds wrong: %v..%v (want %v..%v)", b.WindowStart, b.WindowEnd, start, now)
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("drained batch invalid: %v", err)
	}
	// The next window opens where this one ended (windows tile, no gap) …
	if !c.WindowStart().Equal(now.UTC()) {
		t.Fatalf("next window did not open at previous end: %v", c.WindowStart())
	}
	// … and the collector is empty: the next Drain returns nil.
	if c.Drain(now.Add(time.Minute)) != nil {
		t.Fatal("second drain not nil")
	}
}

func TestDrainEmptyIsNil(t *testing.T) {
	c := NewCollector("dp-1")
	if b := c.Drain(time.Now()); b != nil {
		t.Fatalf("empty collector drained a batch: %+v", b)
	}
}

// Record is called concurrently from every ingress handler.
func TestConcurrentRecord(t *testing.T) {
	c := NewCollector("dp-1")
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				c.Record("demo", "u1", "m1", pricing.Usage{Input: 1}, 1)
			}
		}()
	}
	wg.Wait()
	b := c.Drain(time.Now())
	if b == nil || b.Entries[0].SpentMicroUSD != 800 || b.Entries[0].InputTokens != 800 {
		t.Fatalf("concurrent folds lost updates: %+v", b)
	}
}
