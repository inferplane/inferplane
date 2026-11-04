package tracing

import (
	"context"
	"errors"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type observed struct {
	mu     sync.Mutex
	counts map[string]int
}

func (o *observed) observe(exporter, outcome string, n int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.counts == nil {
		o.counts = map[string]int{}
	}
	o.counts[exporter+"/"+outcome] += n
}

func (o *observed) get(key string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.counts[key]
}

// failingExporter returns an error whose text a collector could have chosen.
type failingExporter struct{}

func (failingExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return errors.New("collector said: private response body")
}
func (failingExporter) Shutdown(context.Context) error { return nil }

func TestObserverCountsSentFailedAndSampledOut(t *testing.T) {
	old := drawSample
	drawSample = func() float64 { return 0.9 }
	t.Cleanup(func() { drawSample = old })
	obs := &observed{}
	a := &recExporter{}
	down := initRouted("test", map[string]sdktrace.SpanExporter{"a": a, "bad": failingExporter{}}, ctxDecider, obs.observe)
	for _, targets := range [][]Target{
		{{Exporter: "a", Ratio: 1}},
		{{Exporter: "a", Ratio: 0.5}}, // the draw (0.9) skips it
		{{Exporter: "bad", Ratio: 1}},
	} {
		_, span := Start(withTargets(context.Background(), targets...), "span")
		span.End()
	}
	_ = down(context.Background()) // flushes both batch processors
	Disable()
	otel.SetTracerProvider(nil)
	if got := obs.get("a/" + OutcomeSent); got != 1 {
		t.Fatalf("a sent = %d", got)
	}
	if got := obs.get("a/" + OutcomeSampledOut); got != 1 {
		t.Fatalf("a sampled out = %d", got)
	}
	if got := obs.get("bad/" + OutcomeExportError); got != 1 {
		t.Fatalf("bad failed = %d", got)
	}
	if got := a.names(); len(got) != 1 {
		t.Fatalf("exporter a got %v", got)
	}
}
