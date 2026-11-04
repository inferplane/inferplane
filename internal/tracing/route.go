package tracing

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// ExporterConfig is one OTLP trace destination. Headers are resolved values
// (for example a collector bearer token) and are never logged.
type ExporterConfig struct {
	Endpoint string // host:port
	Protocol string // "" | "http" | "grpc"
	Insecure bool
	Headers  map[string]string
}

// NewExporter builds an OTLP span exporter. It connects lazily, so an
// unreachable collector is not an error here.
func NewExporter(ctx context.Context, c ExporterConfig) (sdktrace.SpanExporter, error) {
	switch c.Protocol {
	case "grpc":
		opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(c.Endpoint)}
		if c.Insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		if len(c.Headers) > 0 {
			opts = append(opts, otlptracegrpc.WithHeaders(c.Headers))
		}
		return otlptracegrpc.New(ctx, opts...)
	case "", "http":
		opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(c.Endpoint)}
		if c.Insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		if len(c.Headers) > 0 {
			opts = append(opts, otlptracehttp.WithHeaders(c.Headers))
		}
		return otlptracehttp.New(ctx, opts...)
	default:
		return nil, errors.New("tracing: unsupported protocol " + c.Protocol)
	}
}

// Target is one export destination for a request's span: an exporter name
// and the probability (0..1) that this span is sent there.
type Target struct {
	Exporter string
	Ratio    float64
}

// Decider returns the export targets for the request a span starts in. It
// receives the context passed to Start (the request context, carrying the
// authenticated principal) and must be cheap.
type Decider func(ctx context.Context) []Target

// Observer receives per-exporter span counts for the observability console:
// OutcomeSent or OutcomeExportError per exported batch, OutcomeSampledOut per
// span a target's draw skipped. It runs on the request path and on batch
// export goroutines, so it must be cheap and must not block. Only counts cross
// it: the SDK's error text can quote collector responses.
type Observer func(exporter, outcome string, n int)

// Outcome values passed to an Observer (the same strings as the outcome label
// of inferplane_otel_export_records_total).
const (
	OutcomeSent        = "sent"
	OutcomeExportError = "dropped_export_error"
	OutcomeSampledOut  = "sampled_out"
)

// drawSample is the per-target sampling draw; a variable so tests can make it
// deterministic.
var drawSample = rand.Float64

// InitRouted installs a TracerProvider whose spans are exported only to the
// targets decide returns for their request. Start makes the per-target draw
// once per span, so an incoming traceparent's sampled flag or a client-chosen
// trace id cannot suppress or force export; the client's trace id is still
// joined for correlation, and a span no target selected is dropped
// (non-recording). Sampling is a random draw per request, not deterministic
// on the trace id. It returns the provider's Shutdown, which flushes every
// exporter.
func InitRouted(serviceName string, exporters map[string]sdktrace.SpanExporter, decide Decider) func(context.Context) error {
	return initRouted(serviceName, exporters, decide, nil)
}

// initRouted is InitRouted with an optional Observer; with one, every
// exporter is wrapped so its batch outcomes are counted.
func initRouted(serviceName string, exporters map[string]sdktrace.SpanExporter, decide Decider, observe Observer) func(context.Context) error {
	if serviceName == "" {
		serviceName = "inferplane"
	}
	r := &routingProcessor{procs: make(map[string]sdktrace.SpanProcessor, len(exporters)), decide: decide, observe: observe}
	for name, exp := range exporters {
		if observe != nil {
			exp = countingExporter{SpanExporter: exp, name: name, observe: observe}
		}
		r.procs[name] = sdktrace.NewBatchSpanProcessor(exp)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(routeSampler{}),
		sdktrace.WithSpanProcessor(r),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", serviceName))),
	)
	SetTracerProvider(tp)
	router = r
	return tp.Shutdown
}

// routeKey carries Start's chosen exporter names ([]string, possibly empty).
type routeKey struct{}

// routeSampler records and samples exactly the spans Start chose at least one
// exporter for. It never reads the parent's sampled flag; the parent's
// tracestate is preserved either way.
type routeSampler struct{}

func (routeSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	ts := trace.SpanContextFromContext(p.ParentContext).TraceState()
	if chosen, _ := p.ParentContext.Value(routeKey{}).([]string); len(chosen) > 0 {
		return sdktrace.SamplingResult{Decision: sdktrace.RecordAndSample, Tracestate: ts}
	}
	return sdktrace.SamplingResult{Decision: sdktrace.Drop, Tracestate: ts}
}

func (routeSampler) Description() string { return "InferplaneGovernedRoute" }

// InitExporters builds one OTLP exporter per entry and installs InitRouted
// over them, so callers outside this package never import the SDK. observe,
// when non-nil, receives per-exporter span counts (see Observer).
func InitExporters(ctx context.Context, serviceName string, exporters map[string]ExporterConfig, decide Decider, observe Observer) (func(context.Context) error, error) {
	built := make(map[string]sdktrace.SpanExporter, len(exporters))
	for name, c := range exporters {
		exp, err := NewExporter(ctx, c)
		if err != nil {
			for _, b := range built {
				_ = b.Shutdown(ctx)
			}
			return nil, err
		}
		built[name] = exp
	}
	return initRouted(serviceName, built, decide, observe), nil
}

// routingProcessor forwards each ended span to the batch processors chosen
// for it when it started.
type routingProcessor struct {
	procs   map[string]sdktrace.SpanProcessor
	decide  Decider
	observe Observer // nil = no span counts
	routes  sync.Map // trace.SpanID → []string (exporter names)
}

// note forwards one count to the observer, if any.
func (r *routingProcessor) note(exporter, outcome string, n int) {
	if r.observe != nil {
		r.observe(exporter, outcome, n)
	}
}

func (r *routingProcessor) OnStart(parent context.Context, s sdktrace.ReadWriteSpan) {
	if chosen, _ := parent.Value(routeKey{}).([]string); len(chosen) > 0 {
		r.routes.Store(s.SpanContext().SpanID(), chosen)
	}
}

// choose runs the decider and the per-target draw for one span.
func (r *routingProcessor) choose(ctx context.Context) []string {
	if r.decide == nil {
		return nil
	}
	var chosen []string
	for _, t := range r.decide(ctx) {
		if _, ok := r.procs[t.Exporter]; !ok || t.Ratio <= 0 {
			continue
		}
		if t.Ratio < 1 && drawSample() >= t.Ratio {
			r.note(t.Exporter, OutcomeSampledOut, 1)
			continue
		}
		dup := false
		for _, c := range chosen {
			dup = dup || c == t.Exporter
		}
		if !dup {
			chosen = append(chosen, t.Exporter)
		}
	}
	return chosen
}

func (r *routingProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	v, ok := r.routes.LoadAndDelete(s.SpanContext().SpanID())
	if !ok {
		return
	}
	for _, name := range v.([]string) {
		r.procs[name].OnEnd(s)
	}
}

func (r *routingProcessor) Shutdown(ctx context.Context) error {
	var errs []error
	for _, p := range r.procs {
		errs = append(errs, p.Shutdown(ctx))
	}
	return errors.Join(errs...)
}

func (r *routingProcessor) ForceFlush(ctx context.Context) error {
	var errs []error
	for _, p := range r.procs {
		errs = append(errs, p.ForceFlush(ctx))
	}
	return errors.Join(errs...)
}

// countingExporter reports each batch's outcome to the observer. Only the span
// count leaves: the SDK error can carry collector response text.
type countingExporter struct {
	sdktrace.SpanExporter
	name    string
	observe Observer
}

func (e countingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	err := e.SpanExporter.ExportSpans(ctx, spans)
	outcome := OutcomeSent
	if err != nil {
		outcome = OutcomeExportError
	}
	e.observe(e.name, outcome, len(spans))
	return err
}
