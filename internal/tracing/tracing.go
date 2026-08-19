// Package tracing is the opt-in OpenTelemetry seam (ADR-011). It owns the OTel
// SDK import (Init builds the OTLP exporter + TracerProvider) and exposes small
// request-span helpers so the rest of the gateway depends only on this package,
// not the SDK. When Init is not called, every helper is a cheap no-op (the
// default tracer is the library no-op and an `enabled` guard skips header/context
// work), so a deployment without an `otel` config is byte-for-byte unchanged.
//
// It defines its OWN Config (mirror) so it never imports internal/config — the
// assembly maps config.OTelConfig → tracing.Config.
package tracing

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

const scopeName = "github.com/inferplane/inferplane"

// Config mirrors config.OTelConfig (so tracing imports no config). SampleRatio
// nil → 1.0; explicit 0.0 → none.
type Config struct {
	Endpoint    string
	Protocol    string // "" | "http" | "grpc"
	Insecure    bool
	SampleRatio *float64
	ServiceName string
}

var (
	enabled bool
	// tracer defaults to the library no-op, so Start works (cheaply) before Init.
	tracer trace.Tracer                  = tracenoop.NewTracerProvider().Tracer(scopeName)
	prop   propagation.TextMapPropagator = propagation.TraceContext{}
)

// Init installs the OTLP exporter + TracerProvider and the W3C propagator, and
// returns the provider's Shutdown (the caller flushes it on teardown under a
// bounded context). An unreachable collector is non-fatal — the OTLP exporter
// connects lazily and the batch processor isolates export failures from serving.
func Init(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	var (
		exp sdktrace.SpanExporter
		err error
	)
	switch cfg.Protocol {
	case "grpc":
		opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		exp, err = otlptracegrpc.New(ctx, opts...)
	default: // "" | "http"
		opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		exp, err = otlptracehttp.New(ctx, opts...)
	}
	if err != nil {
		return nil, err
	}

	ratio := 1.0
	if cfg.SampleRatio != nil {
		ratio = *cfg.SampleRatio
	}
	svc := cfg.ServiceName
	if svc == "" {
		svc = "inferplane"
	}
	res := resource.NewSchemaless(attribute.String("service.name", svc))
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
		sdktrace.WithResource(res),
	)
	SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// SetTracerProvider installs an explicit TracerProvider and enables tracing.
// Init uses it; tests in other packages use it with a recorder-backed provider.
func SetTracerProvider(tp trace.TracerProvider) {
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(prop)
	tracer = tp.Tracer(scopeName)
	enabled = true
}

// Disable reverts to the no-op tracer (test cleanup).
func Disable() {
	tracer = tracenoop.NewTracerProvider().Tracer(scopeName)
	enabled = false
}

// Enabled reports whether tracing is installed (false → no-op fast path).
func Enabled() bool { return enabled }

// Extract joins an incoming W3C trace (traceparent) so the gateway span becomes
// a child of the client's trace. No-op (returns ctx) when tracing is off.
func Extract(ctx context.Context, h http.Header) context.Context {
	if !enabled {
		return ctx
	}
	return prop.Extract(ctx, propagation.HeaderCarrier(h))
}

// Start begins a server span (no-op span when tracing is off).
func Start(ctx context.Context, name string) (context.Context, trace.Span) {
	return tracer.Start(ctx, name, trace.WithSpanKind(trace.SpanKindServer))
}

// Inject writes the current trace context as a traceparent header into dst. The
// caller MUST pass a CLONE of the upstream headers (never the shared inbound
// map). No-op when tracing is off (dst is left untouched).
func Inject(ctx context.Context, dst http.Header) {
	if !enabled {
		return
	}
	prop.Inject(ctx, propagation.HeaderCarrier(dst))
}

// TraceID returns the 32-hex trace id in ctx, or "" if none (off / no span).
func TraceID(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}

// SetGenAIRequest sets the request-side GenAI attributes, known at span start
// (the provider/system is only known after routing → SetGenAIResponse).
func SetGenAIRequest(span trace.Span, model string) {
	span.SetAttributes(
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.request.model", model),
	)
}

// SetGenAIResponse sets the response-side GenAI attributes: the provider system,
// the resolved upstream model, and token usage (set once the target is known).
func SetGenAIResponse(span trace.Span, system, model string, inputTokens, outputTokens int64) {
	if system != "" {
		span.SetAttributes(attribute.String("gen_ai.system", system))
	}
	span.SetAttributes(attribute.String("gen_ai.response.model", model))
	if inputTokens > 0 {
		span.SetAttributes(attribute.Int64("gen_ai.usage.input_tokens", inputTokens))
	}
	if outputTokens > 0 {
		span.SetAttributes(attribute.Int64("gen_ai.usage.output_tokens", outputTokens))
	}
}

// SetUsageDetail adds the cache-tier token counts that GenAI semconv does not
// define. They go under an `inferplane.` prefix on purpose: inventing a
// `gen_ai.*` name the spec may later assign differently would leave a collector
// double-reporting the same number under two keys. A zero tier is omitted —
// absence of a cache write is not a fact worth an attribute (SetGenAIResponse
// applies the same rule to the token counts).
func SetUsageDetail(span trace.Span, cacheReadTokens, cacheWrite5mTokens, cacheWrite1hTokens int64) {
	if cacheReadTokens > 0 {
		span.SetAttributes(attribute.Int64("inferplane.usage.cache_read_input_tokens", cacheReadTokens))
	}
	if cacheWrite5mTokens > 0 {
		span.SetAttributes(attribute.Int64("inferplane.usage.cache_write_5m_input_tokens", cacheWrite5mTokens))
	}
	if cacheWrite1hTokens > 0 {
		span.SetAttributes(attribute.Int64("inferplane.usage.cache_write_1h_input_tokens", cacheWrite1hTokens))
	}
}

// SetCost adds the settled cost in integer µUSD (never a float — the audit and
// budget paths are integer-only, and a span must not be the one place a
// rounding artifact appears). pricingMissing is always set when a cost is
// recorded: a 0 with the flag is "no rate configured", a 0 without it is
// "genuinely free", and a consumer cannot tell them apart otherwise.
func SetCost(span trace.Span, costUSDMicros int64, pricingMissing bool) {
	span.SetAttributes(
		attribute.Int64("inferplane.cost.amount_usd_micros", costUSDMicros),
		attribute.Bool("inferplane.cost.pricing_missing", pricingMissing),
	)
}

// SetPartial marks a response that was committed to the client and then
// truncated by an upstream mid-stream failure. Without it a truncated stream is
// indistinguishable from a clean one in traces, since the wire status was
// already 200 before the break (the audit record distinguishes it via
// Outcome.Partial).
func SetPartial(span trace.Span) {
	span.SetAttributes(attribute.Bool("inferplane.response.partial", true))
}

// SetStatus marks the span ok or error (set Error only on a terminal outcome).
func SetStatus(span trace.Span, ok bool, desc string) {
	if ok {
		span.SetStatus(codes.Ok, "")
		return
	}
	span.SetStatus(codes.Error, desc)
}
