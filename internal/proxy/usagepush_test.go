package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/controlplane"
	"github.com/inferplane/inferplane/internal/pricing"
	"github.com/inferplane/inferplane/internal/telemetry"
)

func pusherFor(t *testing.T, url string) (*UsagePusher, *telemetry.Collector) {
	t.Helper()
	col := telemetry.NewCollector("dp-test")
	return &UsagePusher{URL: url, Token: "tok", Collector: col}, col
}

func record(col *telemetry.Collector) {
	col.Record("demo", "u1", "m1", pricing.Usage{Input: 1}, 10)
}

func TestPushSuccessClearsBuffer(t *testing.T) {
	var got atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1alpha1/usage" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("bad request: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		got.Add(1)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	p, col := pusherFor(t, srv.URL)
	record(col)
	p.Tick(context.Background())
	if got.Load() != 1 || len(p.buf) != 0 {
		t.Fatalf("posts=%d buffered=%d, want 1/0", got.Load(), len(p.buf))
	}
}

func TestBacklogDrainsFullyAfterOutage(t *testing.T) {
	var fail atomic.Bool
	var got atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		got.Add(1)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	p, col := pusherFor(t, srv.URL)
	fail.Store(true)
	for i := 0; i < 3; i++ { // 3 failed ticks accumulate 3 windows
		record(col)
		p.Tick(context.Background())
		time.Sleep(2 * time.Millisecond) // distinct window bounds
	}
	if len(p.buf) != 3 {
		t.Fatalf("want 3 buffered windows, got %d", len(p.buf))
	}
	fail.Store(false)
	record(col)
	p.Tick(context.Background()) // ONE successful tick drains everything
	if got.Load() != 4 || len(p.buf) != 0 {
		t.Fatalf("posts=%d buffered=%d, want 4/0 (backlog must fully drain)", got.Load(), len(p.buf))
	}
}

func TestPermanentRejectionDropsBatchAndContinues(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(400) // poison batch — permanent
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	p, col := pusherFor(t, srv.URL)
	var drops atomic.Int32
	p.OnDrop = func() { drops.Add(1) }

	record(col)
	p.Tick(context.Background())
	time.Sleep(2 * time.Millisecond)
	record(col)
	p.Tick(context.Background())

	if drops.Load() != 1 {
		t.Fatalf("400 must drop exactly the rejected batch (drops=%d)", drops.Load())
	}
	if len(p.buf) != 0 {
		t.Fatalf("the NEXT batch must still flush past a poison batch: %d buffered", len(p.buf))
	}
}

func TestTransient429IsRetryable(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(429)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	p, col := pusherFor(t, srv.URL)
	var drops atomic.Int32
	p.OnDrop = func() { drops.Add(1) }
	record(col)
	p.Tick(context.Background()) // 429 → kept
	if drops.Load() != 0 || len(p.buf) != 1 {
		t.Fatalf("429 must be retryable: drops=%d buffered=%d", drops.Load(), len(p.buf))
	}
	p.Tick(context.Background()) // retried → 200
	if len(p.buf) != 0 {
		t.Fatalf("retry after 429 did not flush: %d buffered", len(p.buf))
	}
}

func TestHungServerDoesNotStallPastClientTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("timeout test")
	}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	p, col := pusherFor(t, srv.URL)
	p.client = &http.Client{Timeout: 200 * time.Millisecond}
	record(col)
	done := make(chan struct{})
	go func() { p.Tick(context.Background()); close(done) }()
	select {
	case <-done: // returned promptly (timeout), batch retained
	case <-time.After(3 * time.Second):
		t.Fatal("Tick hung past the client timeout")
	}
	if len(p.buf) != 1 {
		t.Fatalf("timed-out batch must be retained: %d buffered", len(p.buf))
	}
}

func TestOverflowDropsOldestObservably(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503) // permanent outage
	}))
	defer srv.Close()

	p, col := pusherFor(t, srv.URL)
	var drops atomic.Int32
	p.OnDrop = func() { drops.Add(1) }

	base := time.Now()
	for i := 0; i <= usageBufferCap; i++ { // one past the cap
		col.Record("demo", "u1", "m1", pricing.Usage{Input: 1}, int64(i+1))
		p.Now = func() time.Time { return base.Add(time.Duration(i+1) * time.Minute) }
		p.Tick(context.Background())
	}
	if drops.Load() != 1 {
		t.Fatalf("cap+1 windows must drop exactly the oldest: drops=%d", drops.Load())
	}
	if len(p.buf) != usageBufferCap {
		t.Fatalf("buffer must hold exactly the cap: %d", len(p.buf))
	}
	// The oldest (cost=1) is gone; the survivor head is the second window.
	if p.buf[0].Entries[0].SpentMicroUSD != 2 {
		t.Fatalf("dropped the wrong end: head=%+v", p.buf[0].Entries[0])
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		followed.Store(true)
		w.WriteHeader(200)
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusMovedPermanently)
	}))
	defer srv.Close()

	p, col := pusherFor(t, srv.URL)
	var drops atomic.Int32
	p.OnDrop = func() { drops.Add(1) }
	record(col)
	p.Tick(context.Background())
	if followed.Load() {
		t.Fatal("redirect was followed — a 301 turns the POST into a GET")
	}
	// 3xx is permanent (never followed; retrying forever would wedge the FIFO).
	if drops.Load() != 1 || len(p.buf) != 0 {
		t.Fatalf("redirect must be a permanent drop: drops=%d buffered=%d", drops.Load(), len(p.buf))
	}
}

// The spec's integration round trip with no stubs between the seams: a real
// Collector drained by a real UsagePusher into the real control-plane usage
// handler, then queried back — µUSD and the ADR-030 cache tiers must survive
// the full mayu→inferplaned path bit-exact.
func TestUsageRoundTripEndToEnd(t *testing.T) {
	agg := telemetry.NewMemoryAggregator(24 * time.Hour)
	mux := http.NewServeMux()
	controlplane.NewUsageServer("tok", agg).Mount(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	col := telemetry.NewCollector("dp-e2e")
	col.Record("demo", "u1", "claude-opus-5", pricing.Usage{
		Input: 100, Output: 40, CacheRead: 30, CacheWrite5m: 20, CacheWrite1h: 8,
	}, 5230)
	p := &UsagePusher{URL: srv.URL, Token: "tok", Collector: col}
	p.Tick(context.Background())
	if len(p.buf) != 0 {
		t.Fatalf("push did not land: %d buffered", len(p.buf))
	}

	req, _ := http.NewRequest("GET", srv.URL+"/v1alpha1/usage?group_by=model", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var res telemetry.QueryResult
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res.TotalMicroUSD != 5230 || len(res.Rows) != 1 {
		t.Fatalf("round trip lost spend: %+v", res)
	}
	r := res.Rows[0]
	if r.Key != "claude-opus-5" || r.InputTokens != 100 || r.OutputTokens != 40 ||
		r.CacheReadTokens != 30 || r.CacheWrite5mTokens != 20 || r.CacheWrite1hTokens != 8 {
		t.Fatalf("round trip mangled fields (cache tiers must survive intact): %+v", r)
	}
}
