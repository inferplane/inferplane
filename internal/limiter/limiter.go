// Package limiter implements instance-local rate limiting (token bucket, TPM/
// RPM, pre-block) and quota windows (daily/monthly, two-phase optimistic check
// + post-debit). LimiterStore is the swappable backend; M5 ships in-memory,
// Redis (shared, multi-replica) is v0.2. Per §5.3 the in-memory limiter is
// per-instance, so multi-replica effective limits scale with replica count —
// documented, not hidden.
package limiter

import (
	"sync"
	"time"
)

type Decision int

const (
	Allow Decision = iota
	Block
)

type LimiterStore interface {
	// AllowRate token-bucket: cost tokens against ratePerMin (refill) with the
	// given burst. Returns false when the bucket lacks `cost` tokens.
	AllowRate(key string, cost, ratePerMin, burst int64) bool
	// CheckQuota optimistic: would used+estimate exceed limit in the window?
	CheckQuota(key string, estimate, limit int64, window time.Duration) Decision
	// DebitQuota records actual usage in the current window.
	DebitQuota(key string, actual int64, window time.Duration)
	// QuotaUsed reports tokens used in the current window (0 if none) — for the
	// quota-utilization observability gauge.
	QuotaUsed(key string, window time.Duration) int64
	// RateUsed reports how much of the bucket's burst capacity is currently
	// consumed (0 if the bucket has never been touched) — a read-only
	// projection of the same refill AllowRate computes, for usage reporting;
	// never writes back to the bucket (unlike AllowRate, which always
	// refills/debits on every call).
	RateUsed(key string, ratePerMin, burst int64) int64
	// AdjustRate corrects an AllowRate charge after actual usage is known
	// (post-response true-up, e.g. TPM billed on a coarse pre-request byte
	// estimate but settled against real token counts). delta is added to the
	// bucket: positive credits back an over-charge, negative debits an
	// under-charge. The result is capped at burst on the high side but is
	// deliberately NOT floored at zero on the low side — a large negative
	// correction is allowed to push the bucket into debt that only refill
	// time repays, so a chronic under-estimate can't be laundered into a
	// free true-up every time. A no-op if the bucket has never been touched
	// (nothing to correct — the request never actually charged this key).
	AdjustRate(key string, delta, burst int64)
}

type bucket struct {
	tokens float64
	last   time.Time
}

type quotaWin struct {
	used      int64
	windowEnd time.Time
}

type Memory struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	quotas  map[string]*quotaWin
	now     func() time.Time
}

func NewMemory() *Memory {
	return &Memory{buckets: map[string]*bucket{}, quotas: map[string]*quotaWin{}, now: time.Now}
}

func (m *Memory) AllowRate(key string, cost, ratePerMin, burst int64) bool {
	if ratePerMin <= 0 {
		return true // unlimited
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.now()
	b := m.buckets[key]
	if b == nil {
		b = &bucket{tokens: float64(burst), last: t}
		m.buckets[key] = b
	}
	// refill at ratePerMin/60 tokens per second, capped at burst
	elapsed := t.Sub(b.last).Seconds()
	b.tokens += elapsed * (float64(ratePerMin) / 60.0)
	if b.tokens > float64(burst) {
		b.tokens = float64(burst)
	}
	b.last = t
	if b.tokens >= float64(cost) {
		b.tokens -= float64(cost)
		return true
	}
	return false
}

func (m *Memory) CheckQuota(key string, estimate, limit int64, window time.Duration) Decision {
	if limit <= 0 {
		return Allow
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	q := m.curWindow(key, window)
	if q.used+estimate > limit {
		return Block
	}
	return Allow
}

func (m *Memory) DebitQuota(key string, actual int64, window time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := m.curWindow(key, window)
	q.used += actual
}

// QuotaUsed reports tokens used in the current window (0 if none/elapsed).
func (m *Memory) QuotaUsed(key string, window time.Duration) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.curWindow(key, window).used
}

// RateUsed peeks at the bucket's projected token level (same refill math as
// AllowRate) without writing it back, so a status read never perturbs
// enforcement. A never-touched bucket reports 0 used (full capacity).
func (m *Memory) RateUsed(key string, ratePerMin, burst int64) int64 {
	if ratePerMin <= 0 {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.buckets[key]
	if b == nil {
		return 0
	}
	elapsed := m.now().Sub(b.last).Seconds()
	tokens := b.tokens + elapsed*(float64(ratePerMin)/60.0)
	if tokens > float64(burst) {
		tokens = float64(burst)
	}
	used := float64(burst) - tokens
	if used < 0 {
		used = 0
	}
	// Round rather than truncate: the sub-token refill that accrues during
	// the microseconds between a debit and this read would otherwise always
	// bias the reported figure down by one (e.g. 199.9998 -> 199, not 200).
	return int64(used + 0.5)
}

// AdjustRate applies a post-hoc correction directly to a bucket's token
// count — no refill math, deliberately: the caller (Settle) runs moments
// after the AllowRate call it is correcting, so treating this as a pure
// balance adjustment rather than a second time-based refill keeps the two
// operations from double-counting elapsed time. Re-caps at burst on the high
// side, but — unlike AllowRate's refill cap — never floors at zero: a
// bucket driven negative by a debit stays blocked until real refill time
// repays it, which is the point (see the interface doc).
func (m *Memory) AdjustRate(key string, delta, burst int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.buckets[key]
	if b == nil {
		return // never charged; nothing to correct
	}
	b.tokens += float64(delta)
	if b.tokens > float64(burst) {
		b.tokens = float64(burst)
	}
}

// curWindow returns the live window for key, resetting if elapsed. Caller holds mu.
func (m *Memory) curWindow(key string, window time.Duration) *quotaWin {
	t := m.now()
	q := m.quotas[key]
	if q == nil || !t.Before(q.windowEnd) {
		q = &quotaWin{windowEnd: t.Add(window)}
		m.quotas[key] = q
	}
	return q
}

var _ LimiterStore = (*Memory)(nil)
