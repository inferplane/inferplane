package telemetry

import (
	"sync"
	"time"

	"github.com/inferplane/inferplane/internal/pricing"
)

// Collector folds per-request settled usage into the current window's
// (team, user, model) buckets on the data plane. Record is called from every
// ingress handler's settle path — concurrently — and must stay cheap: one
// mutex-guarded map fold, no I/O, never on the request path's critical
// section longer than the fold itself.
type Collector struct {
	mu        sync.Mutex
	dataplane string
	start     time.Time
	buckets   map[[3]string]*UsageEntry
}

// NewCollector builds a collector for one data plane instance; dataplane is
// the same identity ControlPlaneConfig.Dataplane resolves to, stamped on
// every drained batch.
func NewCollector(dataplane string) *Collector {
	return &Collector{dataplane: dataplane, start: time.Now().UTC(), buckets: map[[3]string]*UsageEntry{}}
}

// WindowStart reports the currently-open window's start (test seam).
func (c *Collector) WindowStart() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.start
}

// Record folds one settled request into the current window. model is the
// RESOLVED model — the name pricing billed — so spend is attributed to what
// was actually served, never a requested alias.
func (c *Collector) Record(team, user, model string, u pricing.Usage, costMicros int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := [3]string{team, user, model}
	e := c.buckets[key]
	if e == nil {
		e = &UsageEntry{Team: team, User: user, Model: model}
		c.buckets[key] = e
	}
	e.SpentMicroUSD += costMicros
	e.InputTokens += u.Input
	e.OutputTokens += u.Output
	e.CacheReadTokens += u.CacheRead
	e.CacheWrite5mTokens += u.CacheWrite5m
	e.CacheWrite1hTokens += u.CacheWrite1h
}

// Drain closes the current window at now and opens the next one starting
// there (windows tile with no gap, so no request falls between them).
// Returns nil when nothing was recorded — an empty window is not sent.
func (c *Collector) Drain(now time.Time) *UsageBatch {
	now = now.UTC()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.buckets) == 0 {
		c.start = now
		return nil
	}
	b := &UsageBatch{
		Dataplane:   c.dataplane,
		WindowStart: c.start,
		WindowEnd:   now,
		Entries:     make([]UsageEntry, 0, len(c.buckets)),
	}
	for _, e := range c.buckets {
		b.Entries = append(b.Entries, *e)
	}
	c.buckets = map[[3]string]*UsageEntry{}
	c.start = now
	return b
}
