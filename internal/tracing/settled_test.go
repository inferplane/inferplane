package tracing

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestSpanCarriesCacheCostAndPartial pins the settled-usage view of a span: the
// two GenAI token counts keep their semconv names, and everything the spec does
// not define (cache tiers, integer-µUSD cost, the partial marker) lands under an
// `inferplane.` prefix rather than an invented `gen_ai.*` name.
func TestSpanCarriesCacheCostAndPartial(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr)))
	t.Cleanup(Disable)

	_, span := Start(context.Background(), "chat m")
	SetGenAIResponse(span, "mock", "up", 10, 5)
	SetUsageDetail(span, 40, 20, 4)
	SetCost(span, 52, true)
	SetPartial(span)
	SetStatus(span, false, "upstream stream interrupted")
	span.End()

	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("want 1 span, got %d", len(ended))
	}
	ints := map[string]int64{}
	bools := map[string]bool{}
	for _, a := range ended[0].Attributes() {
		switch a.Value.Type().String() {
		case "INT64":
			ints[string(a.Key)] = a.Value.AsInt64()
		case "BOOL":
			bools[string(a.Key)] = a.Value.AsBool()
		}
	}
	for k, want := range map[string]int64{
		"gen_ai.usage.input_tokens":                    10,
		"gen_ai.usage.output_tokens":                   5,
		"inferplane.usage.cache_read_input_tokens":     40,
		"inferplane.usage.cache_write_5m_input_tokens": 20,
		"inferplane.usage.cache_write_1h_input_tokens": 4,
		"inferplane.cost.amount_usd_micros":            52,
	} {
		if ints[k] != want {
			t.Errorf("%s = %d, want %d (all int attrs: %v)", k, ints[k], want, ints)
		}
	}
	if !bools["inferplane.cost.pricing_missing"] {
		t.Error("pricing_missing not recorded")
	}
	if !bools["inferplane.response.partial"] {
		t.Error("partial marker not recorded")
	}
	if got := ended[0].Status().Code.String(); got != "Error" {
		t.Errorf("partial span status = %s, want Error", got)
	}
}

// A later zero-usage attempt must not inherit a prior attempt's cache counts.
func TestSpanClearsPreviousAttemptUsage(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr)))
	t.Cleanup(Disable)

	_, span := Start(context.Background(), "chat m")
	SetGenAIResponse(span, "bedrock", "first", 10, 20)
	SetUsageDetail(span, 30, 40, 50)
	SetGenAIResponse(span, "bedrock", "second", 0, 0)
	SetUsageDetail(span, 0, 0, 0)
	span.End()

	found := 0
	for _, a := range sr.Ended()[0].Attributes() {
		if k := string(a.Key); k == "inferplane.usage.cache_read_input_tokens" ||
			k == "inferplane.usage.cache_write_5m_input_tokens" ||
			k == "inferplane.usage.cache_write_1h_input_tokens" ||
			k == "gen_ai.usage.input_tokens" || k == "gen_ai.usage.output_tokens" {
			found++
			if a.Value.AsInt64() != 0 {
				t.Errorf("%s retained prior attempt's count", k)
			}
		}
	}
	if found != 5 {
		t.Fatal("final usage counts missing")
	}
}
