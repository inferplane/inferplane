package limiter

import (
	"math"
	"testing"
	"time"
)

func TestQuotaCannotBeRefundedByNegativeOrOverflowingUsage(t *testing.T) {
	l := NewMemory()
	l.DebitQuota("quota", math.MaxInt64-10, time.Hour)
	l.DebitQuota("quota", -100, time.Hour)
	if got := l.QuotaUsed("quota", time.Hour); got != math.MaxInt64-10 {
		t.Fatalf("negative debit changed usage to %d", got)
	}
	if l.CheckQuota("quota", -1, math.MaxInt64, time.Hour) != Block ||
		l.CheckQuota("quota", 11, math.MaxInt64, time.Hour) != Block {
		t.Fatal("negative or overflowing admission allowed")
	}
	if l.CheckQuota("quota", 10, math.MaxInt64, time.Hour) != Allow {
		t.Fatal("exact remaining capacity refused")
	}
	l.DebitQuota("quota", 11, time.Hour)
	if got := l.QuotaUsed("quota", time.Hour); got != math.MaxInt64 {
		t.Fatalf("overflowing debit changed usage to %d", got)
	}
	if l.CheckQuota("quota", 1, math.MaxInt64, time.Hour) != Block {
		t.Fatal("overflow restored quota")
	}
}
