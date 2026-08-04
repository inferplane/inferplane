package telemetry

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// QueryFilter selects and groups stored usage. GroupBy is one of
// "team" | "user" | "model". The window range is inclusive-start,
// exclusive-end on WindowStart.
type QueryFilter struct {
	Team, User, Model string
	Since, Until      time.Time
	GroupBy           string
}

// QueryRow is one aggregated line of a QueryResult, keyed by the GroupBy
// dimension's value. Cache tiers stay separate all the way out (ADR-030).
type QueryRow struct {
	Key                string `json:"key"`
	SpentMicroUSD      int64  `json:"spent_micro_usd"`
	InputTokens        int64  `json:"input_tokens"`
	OutputTokens       int64  `json:"output_tokens"`
	CacheReadTokens    int64  `json:"cache_read_tokens"`
	CacheWrite5mTokens int64  `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens int64  `json:"cache_write_1h_tokens"`
}

// QueryResult is the GET /v1alpha1/usage response body. Degraded is set when
// the durable store was unavailable and the result came from the bounded
// in-memory fallback — a billing view silently missing history is worse than
// an error (P2 gate round 3).
type QueryResult struct {
	TotalMicroUSD int64      `json:"total_micro_usd"`
	Rows          []QueryRow `json:"rows"`
	Degraded      bool       `json:"degraded,omitempty"`
}

// StoredRow is one finest-granularity stored line — a UsageEntry plus its
// window identity — streamed by Rows for the export endpoint.
type StoredRow struct {
	Dataplane   string    `json:"dataplane"`
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
	UsageEntry
}

// Aggregator stores usage windows and answers grouped queries. Upsert is
// batch-replace on (dataplane, window_start): re-delivery replaces every row
// under that key atomically — a corrected retry with fewer entries leaves no
// stale rows and never double-counts. Rows streams raw rows through fn so no
// implementation materializes an unbounded slice.
type Aggregator interface {
	Upsert(ctx context.Context, b *UsageBatch) error
	Query(ctx context.Context, f QueryFilter) (QueryResult, error)
	Rows(ctx context.Context, since, until time.Time, fn func(StoredRow) error) error
}

func validGroupBy(g string) bool { return g == "team" || g == "user" || g == "model" }

type windowKey struct {
	dataplane string
	start     int64 // unix nanos — comparable map key
}

// MemoryAggregator is the always-on in-memory store with bounded retention:
// every Upsert evicts windows older than the retention horizon (relative to
// the newest window seen), so a long-running daemon never grows without
// bound — full history is the durable store's job (Task 8).
type MemoryAggregator struct {
	mu        sync.Mutex
	retention time.Duration
	windows   map[windowKey]*UsageBatch
	newest    time.Time
}

// NewMemoryAggregator builds a memory store keeping roughly `retention` of
// recent windows (24h is the standard default).
func NewMemoryAggregator(retention time.Duration) *MemoryAggregator {
	return &MemoryAggregator{retention: retention, windows: map[windowKey]*UsageBatch{}}
}

func (m *MemoryAggregator) Upsert(_ context.Context, b *UsageBatch) error {
	if err := b.Validate(); err != nil {
		return err
	}
	cp := *b
	cp.Entries = append([]UsageEntry(nil), b.Entries...) // never alias caller memory

	m.mu.Lock()
	defer m.mu.Unlock()
	m.windows[windowKey{b.Dataplane, b.WindowStart.UnixNano()}] = &cp
	if b.WindowStart.After(m.newest) {
		m.newest = b.WindowStart
	}
	horizon := m.newest.Add(-m.retention)
	for k := range m.windows {
		if time.Unix(0, k.start).Before(horizon) {
			delete(m.windows, k)
		}
	}
	return nil
}

func (m *MemoryAggregator) Query(_ context.Context, f QueryFilter) (QueryResult, error) {
	if !validGroupBy(f.GroupBy) {
		return QueryResult{}, fmt.Errorf("telemetry: invalid group_by %q", f.GroupBy)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	acc := map[string]*QueryRow{}
	var total int64
	for k, b := range m.windows {
		ws := time.Unix(0, k.start).UTC()
		if ws.Before(f.Since) || !ws.Before(f.Until) {
			continue
		}
		for _, e := range b.Entries {
			if (f.Team != "" && e.Team != f.Team) ||
				(f.User != "" && e.User != f.User) ||
				(f.Model != "" && e.Model != f.Model) {
				continue
			}
			var key string
			switch f.GroupBy {
			case "team":
				key = e.Team
			case "user":
				key = e.User
			case "model":
				key = e.Model
			}
			r := acc[key]
			if r == nil {
				r = &QueryRow{Key: key}
				acc[key] = r
			}
			r.SpentMicroUSD += e.SpentMicroUSD
			r.InputTokens += e.InputTokens
			r.OutputTokens += e.OutputTokens
			r.CacheReadTokens += e.CacheReadTokens
			r.CacheWrite5mTokens += e.CacheWrite5mTokens
			r.CacheWrite1hTokens += e.CacheWrite1hTokens
			total += e.SpentMicroUSD
		}
	}
	rows := make([]QueryRow, 0, len(acc))
	for _, r := range acc {
		rows = append(rows, *r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	return QueryResult{TotalMicroUSD: total, Rows: rows}, nil
}

// Rows snapshots the matching rows UNDER the lock, releases it, then invokes
// fn — the callback writes to a network client, and holding the mutex across
// client writes would let one slow export freeze all ingestion. The snapshot
// is bounded by the retention horizon, so this is not unbounded
// materialization.
func (m *MemoryAggregator) Rows(_ context.Context, since, until time.Time, fn func(StoredRow) error) error {
	m.mu.Lock()
	var snap []StoredRow
	for k, b := range m.windows {
		ws := time.Unix(0, k.start).UTC()
		if ws.Before(since) || !ws.Before(until) {
			continue
		}
		for _, e := range b.Entries {
			snap = append(snap, StoredRow{Dataplane: b.Dataplane, WindowStart: b.WindowStart, WindowEnd: b.WindowEnd, UsageEntry: e})
		}
	}
	m.mu.Unlock()

	sort.Slice(snap, func(i, j int) bool {
		if !snap[i].WindowStart.Equal(snap[j].WindowStart) {
			return snap[i].WindowStart.Before(snap[j].WindowStart)
		}
		return snap[i].Dataplane < snap[j].Dataplane
	})
	for _, r := range snap {
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

var _ Aggregator = (*MemoryAggregator)(nil)
