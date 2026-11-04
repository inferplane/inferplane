package tracing

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// recExporter keeps exported spans across Shutdown (tracetest's in-memory
// exporter resets on Shutdown, which is exactly when batches flush).
type recExporter struct {
	mu    sync.Mutex
	spans []sdktrace.ReadOnlySpan
}

func (e *recExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.mu.Lock()
	e.spans = append(e.spans, spans...)
	e.mu.Unlock()
	return nil
}
func (e *recExporter) Shutdown(context.Context) error { return nil }
func (e *recExporter) names() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, s := range e.spans {
		out = append(out, s.Name())
	}
	return out
}

type targetsKey struct{}

func withTargets(ctx context.Context, t ...Target) context.Context {
	return context.WithValue(ctx, targetsKey{}, t)
}

func ctxDecider(ctx context.Context) []Target {
	t, _ := ctx.Value(targetsKey{}).([]Target)
	return t
}

func routed(t *testing.T) (a, b *recExporter, shutdown func()) {
	t.Helper()
	a, b = &recExporter{}, &recExporter{}
	down := InitRouted("test", map[string]sdktrace.SpanExporter{"a": a, "b": b}, ctxDecider)
	return a, b, func() {
		if err := down(context.Background()); err != nil {
			t.Fatal(err)
		}
		Disable()
		otel.SetTracerProvider(nil)
	}
}

func TestRoutedExportFollowsDecider(t *testing.T) {
	a, b, shutdown := routed(t)
	for _, c := range []struct {
		name    string
		targets []Target
	}{
		{"only-a", []Target{{Exporter: "a", Ratio: 1}}},
		{"both", []Target{{Exporter: "a", Ratio: 1}, {Exporter: "b", Ratio: 1}}},
		{"unknown", []Target{{Exporter: "zzz", Ratio: 1}}},
		{"never", []Target{{Exporter: "a", Ratio: 0}}},
		{"none", nil},
	} {
		_, span := Start(withTargets(context.Background(), c.targets...), c.name)
		span.End()
	}
	shutdown()
	if got := a.names(); len(got) != 2 || got[0] != "only-a" || got[1] != "both" {
		t.Fatalf("exporter a got %v", got)
	}
	if got := b.names(); len(got) != 1 || got[0] != "both" {
		t.Fatalf("exporter b got %v", got)
	}
}

func TestClientTraceparentDoesNotControlExport(t *testing.T) {
	a, _, shutdown := routed(t)
	notSampled := http.Header{}
	notSampled.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00")
	ctx := withTargets(Extract(context.Background(), notSampled), Target{Exporter: "a", Ratio: 1})
	sctx, span := Start(ctx, "client-said-not-sampled")
	if !span.IsRecording() {
		t.Fatal("a client's not-sampled flag must not stop the gateway span from recording")
	}
	if got := TraceID(sctx); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("gateway span must still join the client's trace, got %q", got)
	}
	span.End()

	sampled := http.Header{}
	sampled.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	_, span = Start(Extract(context.Background(), sampled), "client-said-sampled")
	span.End()
	shutdown()

	if got := a.names(); len(got) != 1 || got[0] != "client-said-not-sampled" {
		t.Fatalf("exported %v: the governed decision, not the client's flag, decides export", got)
	}
}

func TestLegacyRatioTarget(t *testing.T) {
	a, _, shutdown := routed(t)
	for i := 0; i < 200; i++ {
		_, span := Start(withTargets(context.Background(), Target{Exporter: "a", Ratio: 0.5}), "half")
		span.End()
	}
	shutdown()
	if n := len(a.names()); n < 50 || n > 150 {
		t.Fatalf("ratio 0.5 exported %d of 200 spans", n)
	}
}

// A span no target selected must not record: unexported spans would otherwise
// pay attribute and cost-event allocation on every request (legacy
// sample_ratio users relied on non-recording unsampled spans).
func TestUnselectedSpanIsNotRecorded(t *testing.T) {
	_, _, shutdown := routed(t)
	defer shutdown()
	sampled := http.Header{}
	sampled.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	for _, c := range []struct {
		name      string
		ctx       context.Context
		recording bool
	}{
		{"no-target", context.Background(), false},
		{"ratio-zero", withTargets(context.Background(), Target{Exporter: "a", Ratio: 0}), false},
		{"unknown-exporter", withTargets(context.Background(), Target{Exporter: "zzz", Ratio: 1}), false},
		{"client-sampled-no-target", Extract(context.Background(), sampled), false},
		{"selected", withTargets(context.Background(), Target{Exporter: "a", Ratio: 1}), true},
	} {
		sctx, span := Start(c.ctx, c.name)
		if span.IsRecording() != c.recording {
			t.Errorf("%s: recording = %v, want %v", c.name, span.IsRecording(), c.recording)
		}
		if TraceID(sctx) == "" {
			t.Errorf("%s: a dropped span must still carry a trace id for audit correlation", c.name)
		}
		span.End()
	}
}
