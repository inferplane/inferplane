package limiter

import (
	"sync"
	"testing"
	"time"
)

func TestPendingRateSurvivesSweepAndSettlesOnce(t *testing.T) {
	now := time.Unix(1700000000, 0)
	l := NewMemory()
	l.now = func() time.Time { return now }
	finish, ok := l.ReserveRate("tpm:pending", 10, 60, 60)
	if !ok {
		t.Fatal("admission failed")
	}
	now = now.Add(10 * time.Second)
	for i := 0; i < 255; i++ {
		l.AllowRate("other", 1, 6000, 6000)
	}
	actual := int64(100)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); finish(&actual) }()
	}
	wg.Wait()
	finish(nil)
	if used := l.RateUsed("tpm:pending", 60, 60); used != 90 {
		t.Fatalf("used=%d want 90", used)
	}
	if l.AllowRate("tpm:pending", 1, 60, 60) {
		t.Fatal("settlement debt lost")
	}
	now = now.Add(2 * time.Minute)
	l.sweepReclaimable(now)
	if _, ok := l.buckets["tpm:pending"]; ok {
		t.Fatal("finished bucket leaked")
	}
}

func TestRateReservationUnknownAndConcurrentRequests(t *testing.T) {
	l := NewMemory()
	now := time.Unix(1700000000, 0)
	l.now = func() time.Time { return now }
	a, ok := l.ReserveRate("key", 30, 100, 100)
	if !ok {
		t.Fatal("first")
	}
	b, ok := l.ReserveRate("key", 40, 100, 100)
	if !ok {
		t.Fatal("second")
	}
	a(nil) // failure/cancellation retains its estimate
	actual := int64(10)
	b(&actual)
	a(&actual) // late completion cannot refund a closed request
	if used := l.RateUsed("key", 100, 100); used != 40 {
		t.Fatalf("used=%d want 30+10", used)
	}
	if l.buckets["key"].pending != 0 {
		t.Fatal("pending reservation leaked")
	}
}

func TestPendingReservationCannotBeEvictedAtCapacity(t *testing.T) {
	l := NewMemory()
	l.maxEntries = 1
	now := time.Unix(1700000000, 0)
	l.now = func() time.Time { return now }
	finish, ok := l.ReserveRate("key", 1, 60, 60)
	if !ok {
		t.Fatal("admission")
	}
	now = now.Add(time.Hour)
	if l.AllowRate("other", 1, 60, 60) {
		t.Fatal("evicted pending reservation")
	}
	finish(nil)
	if !l.AllowRate("other", 1, 60, 60) {
		t.Fatal("finished bucket not reclaimed")
	}
}
