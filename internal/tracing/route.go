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

// InitRouted installs a TracerProvider that records every span and exports
// each one only to the targets decide returns for its request. The export draw
// is made here, per target, so an incoming traceparent's sampled flag or a
// client-chosen trace id cannot suppress or force export; the client's trace
// id is still joined for correlation. It returns the provider's Shutdown,
// which flushes every exporter.
func InitRouted(serviceName string, exporters map[string]sdktrace.SpanExporter, decide Decider) func(context.Context) error {
	if serviceName == "" {
		serviceName = "inferplane"
	}
	r := &routingProcessor{procs: make(map[string]sdktrace.SpanProcessor, len(exporters)), decide: decide}
	for name, exp := range exporters {
		r.procs[name] = sdktrace.NewBatchSpanProcessor(exp)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(r),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", serviceName))),
	)
	SetTracerProvider(tp)
	return tp.Shutdown
}

// InitExporters builds one OTLP exporter per entry and installs InitRouted
// over them, so callers outside this package never import the SDK.
func InitExporters(ctx context.Context, serviceName string, exporters map[string]ExporterConfig, decide Decider) (func(context.Context) error, error) {
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
	return InitRouted(serviceName, built, decide), nil
}

// routingProcessor forwards each ended span to the batch processors chosen
// for it when it started.
type routingProcessor struct {
	procs  map[string]sdktrace.SpanProcessor
	decide Decider
	routes sync.Map // trace.SpanID → []string (exporter names)
}

func (r *routingProcessor) OnStart(parent context.Context, s sdktrace.ReadWriteSpan) {
	if r.decide == nil {
		return
	}
	var chosen []string
	for _, t := range r.decide(parent) {
		if _, ok := r.procs[t.Exporter]; !ok || t.Ratio <= 0 {
			continue
		}
		if t.Ratio < 1 && rand.Float64() >= t.Ratio {
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
	if len(chosen) > 0 {
		r.routes.Store(s.SpanContext().SpanID(), chosen)
	}
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
