// Package tracing is the opt-in OpenTelemetry seam (ADR-011). It owns the OTel
// SDK import (InitRouted builds the OTLP exporters + TracerProvider; Init is the
// legacy single-exporter form) and exposes small request-span helpers so the
// rest of the gateway depends only on this package, not the SDK. Under a routed
// provider, Start decides per span which exporters receive it (the injected
// Decider plus a gateway-side draw); unselected spans are dropped as
// non-recording. When Init is not called, every helper is a cheap no-op (the
// default tracer is the library no-op and an `enabled` guard skips header/context
// work), so a deployment without an `otel` config is byte-for-byte unchanged.
//
// It defines its OWN Config (mirror) so it never imports internal/config — the
// assembly maps config.OTelConfig → tracing.Config.
package tracing

import (
	"context"
	"net/http"

	"github.com/inferplane/inferplane/pkg/ulid"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
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
	// router is the routed provider's processor; nil for an explicitly
	// installed provider (SetTracerProvider), which then samples on its own.
	router *routingProcessor
)

// Init is the legacy single-exporter setup (the `otel` config block): every
// request span goes to cfg's collector with probability sample_ratio. It is
// InitRouted with one exporter, named LegacyExporter, and a constant decider,
// so the gateway's own draw — not the client's traceparent — decides export.
// An unreachable collector is non-fatal: the exporter connects lazily.
func Init(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	targets := []Target{LegacyTarget(cfg.SampleRatio)}
	return InitExporters(ctx, cfg.ServiceName, map[string]ExporterConfig{LegacyExporter: {Endpoint: cfg.Endpoint, Protocol: cfg.Protocol, Insecure: cfg.Insecure}},
		func(context.Context) []Target { return targets })
}

// LegacyExporter names the `otel` block's exporter among routed exporters.
const LegacyExporter = "otel"

// LegacyTarget is the `otel` block's baseline target (nil ratio → 1.0).
func LegacyTarget(sampleRatio *float64) Target {
	ratio := 1.0
	if sampleRatio != nil {
		ratio = *sampleRatio
	}
	return Target{Exporter: LegacyExporter, Ratio: ratio}
}

// SetTracerProvider installs an explicit TracerProvider and enables tracing.
// Init uses it; tests in other packages use it with a recorder-backed provider.
func SetTracerProvider(tp trace.TracerProvider) {
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(prop)
	tracer = tp.Tracer(scopeName)
	enabled = true
	router = nil
}

// Disable reverts to the no-op tracer (test cleanup).
func Disable() {
	tracer = tracenoop.NewTracerProvider().Tracer(scopeName)
	enabled = false
	router = nil
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
// Under a routed provider the export decision is made here, once per span, and
// carried in the context to both the sampler (a span no target selected is
// dropped: non-recording, no attribute allocation) and the routing processor.
func Start(ctx context.Context, name string) (context.Context, trace.Span) {
	if r := router; r != nil {
		ctx = context.WithValue(ctx, routeKey{}, r.choose(ctx))
	}
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
	// Explicit zeros replace a previous attempt's counts on a shared span.
	span.SetAttributes(attribute.Int64("gen_ai.usage.input_tokens", inputTokens),
		attribute.Int64("gen_ai.usage.output_tokens", outputTokens))
}

// SetUsageDetail adds the cache-tier token counts that GenAI semconv does not
// define. They go under an `inferplane.` prefix on purpose: inventing a
// `gen_ai.*` name the spec may later assign differently would leave a collector
// double-reporting the same number under two keys. Zeros replace any previous
// attempt's cache counts; usage evidence distinguishes estimates from facts.
func SetUsageDetail(span trace.Span, cacheReadTokens, cacheWrite5mTokens, cacheWrite1hTokens int64) {
	span.SetAttributes(attribute.Int64("inferplane.usage.cache_read_input_tokens", cacheReadTokens),
		attribute.Int64("inferplane.usage.cache_write_5m_input_tokens", cacheWrite5mTokens),
		attribute.Int64("inferplane.usage.cache_write_1h_input_tokens", cacheWrite1hTokens))
}

// CostDetails carries bounded tier evidence and the operator's price version.
type CostDetails struct {
	PricingVersion string
	ContextTier    string
	System         string
	Provider       string // config alias used to look up the rate
	Model          string // routed upstream ID, not an attested response model
	Partial        bool
	Usage          *UsageDetails
}

// SetCost adds the settled cost in integer µUSD (never a float — the audit and
// budget paths are integer-only, and a span must not be the one place a
// rounding artifact appears). pricingMissing is always set when a cost is
// recorded: a 0 with the flag is "no usable price", a 0 without it is
// "genuinely free", and a consumer cannot tell them apart otherwise.
func SetCost(span trace.Span, costUSDMicros int64, pricingMissing bool, details ...CostDetails) {
	if !span.IsRecording() {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.Int64("inferplane.cost.amount_usd_micros", costUSDMicros),
		attribute.Bool("inferplane.cost.pricing_missing", pricingMissing),
	}
	if len(details) > 0 {
		attrs = append(attrs, attribute.String("inferplane.cost.pricing_version", details[0].PricingVersion),
			attribute.String("inferplane.cost.context_tier", details[0].ContextTier))
	}
	span.SetAttributes(attrs...)
	attrs = append(attrs, attribute.String("inferplane.cost.event_id", ulid.New()))
	if len(details) > 0 {
		d := details[0]
		attrs = append(attrs, attribute.String("gen_ai.system", d.System),
			attribute.String("gen_ai.response.model", d.Model),
			attribute.String("inferplane.provider.name", d.Provider),
			attribute.Bool("inferplane.response.partial", d.Partial))
		usageAttrs := d.Usage.attributes()
		span.SetAttributes(usageAttrs...)
		attrs = append(attrs, usageAttrs...)
	}
	// The span-level amount remains the last attempt for compatibility. Each
	// settled attempt also gets an event so fallback cannot overwrite earlier
	// observed spend. Sampling/export loss still makes traces diagnostic only.
	span.AddEvent("inferplane.cost.settled", trace.WithAttributes(attrs...))
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
