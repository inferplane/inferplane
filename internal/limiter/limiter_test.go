package limiter

import (
	"testing"
	"time"
)

func TestRateLimitBlocksOverBurst(t *testing.T) {
	l := NewMemory()
	// 60 rpm = 1/s, burst 2. clock injected for determinism.
	now := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return now }
	key := "team:rps"
	if !l.AllowRate(key, 1, 60, 2) { // burst 2 → first allowed
		t.Fatal("first request should be allowed")
	}
	if !l.AllowRate(key, 1, 60, 2) {
		t.Fatal("second (within burst) should be allowed")
	}
	if l.AllowRate(key, 1, 60, 2) {
		t.Fatal("third should be blocked (burst exhausted, no refill yet)")
	}
	// advance 1s → 1 token refilled at 1/s
	now = now.Add(time.Second)
	if !l.AllowRate(key, 1, 60, 2) {
		t.Fatal("after 1s refill, should be allowed")
	}
}

func TestQuotaTwoPhase(t *testing.T) {
	l := NewMemory()
	now := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return now }
	key := "team:daily"
	limit := int64(1000)
	// optimistic check with estimate 800 → ok (0 used)
	if d := l.CheckQuota(key, 800, limit, 24*time.Hour); d != Allow {
		t.Fatalf("first check: %v", d)
	}
	l.DebitQuota(key, 800, 24*time.Hour) // actual 800 used
	// next check estimate 300 → 800+300=1100 > 1000 → Block
	if d := l.CheckQuota(key, 300, limit, 24*time.Hour); d != Block {
		t.Fatalf("over-limit check should block: %v", d)
	}
	// estimate 100 → 800+100=900 ≤ 1000 → Allow
	if d := l.CheckQuota(key, 100, limit, 24*time.Hour); d != Allow {
		t.Fatalf("under-limit check should allow: %v", d)
	}
}

func TestQuotaWindowResets(t *testing.T) {
	l := NewMemory()
	now := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return now }
	key := "team:win"
	l.DebitQuota(key, 1000, time.Hour)
	if d := l.CheckQuota(key, 1, 1000, time.Hour); d != Block {
		t.Fatal("at limit, should block")
	}
	now = now.Add(2 * time.Hour) // window elapsed
	if d := l.CheckQuota(key, 500, 1000, time.Hour); d != Allow {
		t.Fatal("after window reset, should allow")
	}
}

func TestQuotaUsedReportsCurrentWindow(t *testing.T) {
	m := NewMemory()
	if u := m.QuotaUsed("q:t", time.Hour); u != 0 {
		t.Fatalf("fresh QuotaUsed = %d, want 0", u)
	}
	m.DebitQuota("q:t", 300, time.Hour)
	m.DebitQuota("q:t", 200, time.Hour)
	if u := m.QuotaUsed("q:t", time.Hour); u != 500 {
		t.Fatalf("QuotaUsed = %d, want 500", u)
	}
}

func TestRateUsedPeeksWithoutMutating(t *testing.T) {
	l := NewMemory()
	now := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return now }
	key := "key:rate"
	// never touched → 0 used (full capacity), and unlimited (ratePerMin<=0) → 0.
	if u := l.RateUsed(key, 60, 100); u != 0 {
		t.Fatalf("fresh RateUsed = %d, want 0", u)
	}
	if u := l.RateUsed(key, 0, 100); u != 0 {
		t.Fatalf("unlimited RateUsed = %d, want 0", u)
	}
	l.AllowRate(key, 40, 60, 100) // debit 40 of burst 100
	if u := l.RateUsed(key, 60, 100); u != 40 {
		t.Fatalf("RateUsed after debit = %d, want 40", u)
	}
	// a peek must not itself consume any tokens: repeated reads are stable.
	if u := l.RateUsed(key, 60, 100); u != 40 {
		t.Fatalf("RateUsed peek must be idempotent, got %d", u)
	}
	// advance 30s at 60/min=1/s → 30 tokens refill → used drops to 10.
	now = now.Add(30 * time.Second)
	if u := l.RateUsed(key, 60, 100); u != 10 {
		t.Fatalf("RateUsed after refill = %d, want 10", u)
	}
}

func TestAdjustRateCreditsBackAnOvercharge(t *testing.T) {
	l := NewMemory()
	key := "team:tpm"
	l.AllowRate(key, 50, 60, 100) // charged 50 of burst 100 (an estimate)
	l.AdjustRate(key, 30, 100)    // actual usage was 20; credit back the 30-token overcharge
	if u := l.RateUsed(key, 60, 100); u != 20 {
		t.Fatalf("RateUsed after credit = %d, want 20", u)
	}
}

func TestAdjustRateDebitsAnUndercharge(t *testing.T) {
	l := NewMemory()
	key := "team:tpm"
	l.AllowRate(key, 20, 60, 100) // charged 20 of burst 100 (an estimate)
	l.AdjustRate(key, -30, 100)   // actual usage was 50; debit the 30-token undercharge
	if u := l.RateUsed(key, 60, 100); u != 50 {
		t.Fatalf("RateUsed after debit = %d, want 50", u)
	}
}

func TestAdjustRateCapsAtBurstButNeverFloorsAtZero(t *testing.T) {
	l := NewMemory()
	key := "team:tpm"
	l.AllowRate(key, 10, 60, 100) // 90 tokens remain
	l.AdjustRate(key, 1000, 100)  // an over-large credit must still cap at burst
	if u := l.RateUsed(key, 60, 100); u != 0 {
		t.Fatalf("RateUsed after capped credit = %d, want 0 (full bucket)", u)
	}
	l.AdjustRate(key, -500, 100) // a large debit is allowed to drive the bucket into debt
	if u := l.RateUsed(key, 60, 100); u != 500 {
		t.Fatalf("RateUsed after debt-driving debit = %d, want 500 (not floored at burst)", u)
	}
}

func TestAdjustRateNoopOnUntouchedBucket(t *testing.T) {
	l := NewMemory()
	// A key that was never charged (e.g. TPM disabled for this team) must not
	// spring into existence just because Settle tries to correct it.
	l.AdjustRate("never:touched", -50, 100)
	if u := l.RateUsed("never:touched", 60, 100); u != 0 {
		t.Fatalf("RateUsed on an uncreated bucket = %d, want 0", u)
	}
}
