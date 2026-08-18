package telemetry

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// TestPostgresAggregator reruns the Task-2 interface suite verbatim against
// Postgres. Skips unless INFERPLANE_TEST_PG_DSN is set — CI stays
// memory-only, zero new infra.
func TestPostgresAggregator(t *testing.T) {
	dsn := os.Getenv("INFERPLANE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("INFERPLANE_TEST_PG_DSN not set")
	}
	aggregatorSuite(t, func(t *testing.T) Aggregator {
		p, err := NewPostgresAggregator(dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx := context.Background()
			_ = p.ensureSchema(ctx)
			_, _ = p.db.Exec(ctx, `TRUNCATE usage_windows`)
			p.Close()
		})
		return p
	})
}

// Construction must never dial: an unreachable DSN cannot block boot.
func TestPostgresConstructionIsLazy(t *testing.T) {
	start := time.Now()
	p, err := NewPostgresAggregator("postgres://user:secretpw@203.0.113.1:5432/nope?connect_timeout=1")
	if err != nil {
		t.Fatalf("lazy construction must not dial: %v", err)
	}
	defer p.Close()
	if time.Since(start) > 2*time.Second {
		t.Fatal("construction blocked on a dial")
	}
}

// The DSN (which may carry a password) must never appear in an error.
func TestPostgresDSNNeverInErrors(t *testing.T) {
	// Parse failure path: the constructor returns a fixed message.
	if _, err := NewPostgresAggregator("::::"); err == nil ||
		containsAny(err.Error(), "::::") {
		t.Fatalf("parse error leaked the dsn: %v", err)
	}
	// Runtime failure path: an unreachable host's Upsert error must not
	// carry the credential.
	p, err := NewPostgresAggregator("postgres://user:secretpw@203.0.113.1:5432/nope?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b := &UsageBatch{Dataplane: "dp", WindowStart: time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC),
		WindowEnd: time.Date(2026, 8, 4, 12, 1, 0, 0, time.UTC),
		Entries:   []UsageEntry{{Team: "t", Model: "m"}}}
	uerr := p.Upsert(ctx, b)
	if uerr == nil {
		t.Skip("unexpectedly reachable")
	}
	if containsAny(uerr.Error(), "secretpw") {
		t.Fatalf("upsert error leaked the password: %v", uerr)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub != "" && len(s) >= len(sub) && stringsContains(s, sub) {
			return true
		}
	}
	return false
}

func stringsContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// failingPG simulates a durable-store outage for the DurableAggregator
// contract tests (no real Postgres needed).
type failingPG struct{ fail bool }

func (f *failingPG) Upsert(context.Context, *UsageBatch) error {
	if f.fail {
		return errors.New("pg down")
	}
	return nil
}
func (f *failingPG) Query(ctx context.Context, q QueryFilter) (QueryResult, error) {
	if f.fail {
		return QueryResult{}, errors.New("pg down")
	}
	return QueryResult{TotalMicroUSD: 999, Rows: []QueryRow{{Key: "pg"}}}, nil
}
func (f *failingPG) Rows(context.Context, time.Time, time.Time, func(StoredRow) error) error {
	if f.fail {
		return errors.New("pg down")
	}
	return nil
}

func TestDurableUpsertNeverAcksWithoutPG(t *testing.T) {
	ctx := context.Background()
	mem := NewMemoryAggregator(24 * time.Hour)
	w0 := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	mem.now = func() time.Time { return w0 } // pin retention's anchor to w0 (see aggregate_test.go)
	pg := &failingPG{fail: true}
	d := NewDurableAggregator(mem, pg)

	b := &UsageBatch{Dataplane: "dp", WindowStart: w0, WindowEnd: w0.Add(time.Minute),
		Entries: []UsageEntry{{Team: "t", Model: "m", SpentMicroUSD: 5}}}
	if err := d.Upsert(ctx, b); err == nil {
		t.Fatal("PG-down Upsert must surface the error (503, mayu retries) — never a silent memory-only ack")
	}
	// And memory must NOT have absorbed it (a memory-only write with a
	// success-looking retry later would double-count).
	res, _ := mem.Query(ctx, QueryFilter{Since: w0, Until: w0.Add(time.Hour), GroupBy: "team"})
	if res.TotalMicroUSD != 0 {
		t.Fatalf("failed durable write leaked into memory: %+v", res)
	}
}

func TestDurableQueryFallsBackDegraded(t *testing.T) {
	ctx := context.Background()
	mem := NewMemoryAggregator(24 * time.Hour)
	w0 := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	mem.now = func() time.Time { return w0 } // pin retention's anchor to w0 (see aggregate_test.go)
	pg := &failingPG{}
	d := NewDurableAggregator(mem, pg)

	b := &UsageBatch{Dataplane: "dp", WindowStart: w0, WindowEnd: w0.Add(time.Minute),
		Entries: []UsageEntry{{Team: "t", Model: "m", SpentMicroUSD: 7}}}
	if err := d.Upsert(ctx, b); err != nil {
		t.Fatal(err)
	}

	// Healthy: PG answers (and is preferred).
	res, err := d.Query(ctx, QueryFilter{Since: w0, Until: w0.Add(time.Hour), GroupBy: "team"})
	if err != nil || res.Degraded || res.TotalMicroUSD != 999 {
		t.Fatalf("healthy query must come from PG: %+v %v", res, err)
	}

	// Outage: memory fallback, explicitly degraded.
	pg.fail = true
	res, err = d.Query(ctx, QueryFilter{Since: w0, Until: w0.Add(time.Hour), GroupBy: "team"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Degraded || res.TotalMicroUSD != 7 {
		t.Fatalf("outage query must serve memory MARKED degraded: %+v", res)
	}

	// Outage + invalid group_by stays a client error, not degraded data.
	if _, err := d.Query(ctx, QueryFilter{Since: w0, Until: w0.Add(time.Hour), GroupBy: "nope"}); err == nil {
		t.Fatal("invalid group_by must error even during an outage")
	}

	// Rows during an outage aborts — never mid-cursor mixing.
	if err := d.Rows(ctx, w0, w0.Add(time.Hour), func(StoredRow) error { return nil }); err == nil {
		t.Fatal("Rows during a PG outage must abort the export")
	}
}
