package otelexport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// Outcome label values of inferplane_otel_export_records_total.
const (
	outcomeSent        = "sent"
	outcomeQueueFull   = "dropped_queue_full"
	outcomeOversize    = "dropped_oversize"
	outcomeExportError = "dropped_export_error"
)

const (
	// The channel is sized from the byte budget so bytes, not a fixed item
	// count, bound the queue: every item accounts at least recordOverhead.
	minQueueItems = 1024
	maxQueueItems = 1 << 18
	maxBatchItems = 512
	// maxBatchBytes keeps one request under a collector's default 4 MiB gRPC
	// receive limit; a single larger record is still sent on its own.
	maxBatchBytes = 3*(1<<20) + (1 << 19)
	flushInterval = time.Second
	exportTimeout = 10 * time.Second
	scopeName     = "github.com/inferplane/inferplane"
)

// retryBackoff is the wait before each retry of a retryable failure (so its
// length is the retry count). A variable so tests can shorten it.
var retryBackoff = []time.Duration{time.Second, 2 * time.Second}

// item is one queued log record and its accounted size.
type item struct {
	signal string
	rec    *logspb.LogRecord
	size   int64
}

// pipeline batches one exporter's log records on a single worker goroutine.
type pipeline struct {
	name     string
	client   logClient
	resource *resourcepb.Resource
	st       *stats

	ch      chan item
	queued  atomic.Int64 // bytes enqueued and not yet exported or dropped
	limit   int64
	stopped atomic.Bool

	ctx     context.Context // cancelled when shutdown's deadline passes
	cancel  context.CancelFunc
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
	lastLog atomic.Int64 // unix seconds of the last failure log line
}

func newPipeline(name string, client logClient, resource *resourcepb.Resource, limit int64, st *stats) *pipeline {
	ctx, cancel := context.WithCancel(context.Background())
	p := &pipeline{name: name, client: client, resource: resource, st: st, limit: limit,
		ch: make(chan item, queueCapacity(limit)), ctx: ctx, cancel: cancel,
		stop: make(chan struct{}), done: make(chan struct{})}
	go p.run()
	return p
}

// queueCapacity is the most items the byte budget can admit, clamped.
func queueCapacity(limit int64) int {
	n := limit / recordOverhead
	switch {
	case n < minQueueItems:
		return minQueueItems
	case n > maxQueueItems:
		return maxQueueItems
	}
	return int(n)
}

// enqueue accepts the record if it fits the byte budget; it never blocks.
func (p *pipeline) enqueue(it item) {
	if p.stopped.Load() {
		p.st.count(p.name, it.signal, outcomeQueueFull, 1)
		return
	}
	if p.queued.Add(it.size) > p.limit {
		p.queued.Add(-it.size)
		p.st.count(p.name, it.signal, outcomeQueueFull, 1)
		return
	}
	select {
	case p.ch <- it:
	default:
		p.queued.Add(-it.size)
		p.st.count(p.name, it.signal, outcomeQueueFull, 1)
	}
}

func (p *pipeline) run() {
	defer close(p.done)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	var batch []item
	var bytes int64
	add := func(it item) {
		if len(batch) > 0 && bytes+it.size > maxBatchBytes {
			p.export(batch)
			batch, bytes = nil, 0
		}
		batch = append(batch, it)
		bytes += it.size
		if len(batch) >= maxBatchItems {
			p.export(batch)
			batch, bytes = nil, 0
		}
	}
	for {
		select {
		case it := <-p.ch:
			add(it)
		case <-ticker.C:
			if len(batch) > 0 {
				p.export(batch)
				batch, bytes = nil, 0
			}
		case <-p.stop:
			for {
				select {
				case it := <-p.ch:
					add(it)
				default:
					if len(batch) > 0 {
						p.export(batch)
					}
					p.client.close()
					return
				}
			}
		}
	}
}

// export sends one batch, retrying retryable failures, and settles the byte
// budget and counters for every record in it.
func (p *pipeline) export(batch []item) {
	var size int64
	recs := make([]*logspb.LogRecord, len(batch))
	for i, it := range batch {
		recs[i] = it.rec
		size += it.size
	}
	defer p.queued.Add(-size)
	req := &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource:  p.resource,
		ScopeLogs: []*logspb.ScopeLogs{{Scope: &commonpb.InstrumentationScope{Name: scopeName}, LogRecords: recs}},
	}}}
	var err error
	for attempt := 0; ; attempt++ {
		if p.ctx.Err() != nil {
			err = p.ctx.Err()
			break
		}
		ctx, cancel := context.WithTimeout(p.ctx, exportTimeout)
		err = p.client.export(ctx, req)
		cancel()
		var r retryable
		if err == nil || !errors.As(err, &r) || attempt >= len(retryBackoff) {
			break
		}
		select {
		case <-time.After(retryBackoff[attempt]):
		case <-p.ctx.Done():
		}
	}
	outcome := outcomeSent
	if err != nil {
		outcome = outcomeExportError
		p.logFailure(err)
	}
	counts := map[string]int{}
	for _, it := range batch {
		counts[it.signal]++
	}
	for signal, n := range counts {
		p.st.count(p.name, signal, outcome, n)
		if err != nil {
			p.st.failed(p.name, signal, err.Error()) // status code or fixed text (client.go)
		} else {
			p.st.delivered(p.name, signal)
		}
	}
}

// logFailure prints at most one line a minute per exporter. err carries only a
// status code or a fixed string, never headers or bodies.
func (p *pipeline) logFailure(err error) {
	now := time.Now().Unix()
	last := p.lastLog.Load()
	if now-last < 60 || !p.lastLog.CompareAndSwap(last, now) {
		return
	}
	fmt.Fprintf(os.Stderr, "inferplane: otel export to %q failed (records dropped): %v\n", p.name, err)
}

// shutdown stops intake, flushes what is queued, and gives up when ctx ends.
func (p *pipeline) shutdown(ctx context.Context) error {
	p.once.Do(func() {
		p.stopped.Store(true)
		close(p.stop)
	})
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		p.cancel() // abort in-flight exports and retry waits
		<-p.done
		return fmt.Errorf("flush incomplete: %w", ctx.Err())
	}
}
