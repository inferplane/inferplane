package requestpolicy

import (
	"context"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/tracing"
	"github.com/inferplane/inferplane/pkg/schema"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestCostEventsRetainAttemptUsageAndUncertainty(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tracing.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr)))
	t.Cleanup(tracing.Disable)
	_, span := tracing.Start(context.Background(), "responses")
	in, out, read, write := int64(10), int64(2), int64(100), int64(20)
	u := audit.UsageFrom(&schema.Usage{InputTokens: &in, OutputTokens: &out, CacheReadInputTokens: &read, CacheCreationInputTokens: &write})
	cost := &audit.CostRef{AmountUSDMicros: 42, PricingVersion: "v1", ContextTier: "short"}
	RecordCost(span, "openai_responses", "mantle", "first-model", u, cost, true)
	zero := int64(0)
	RecordCost(span, "bedrock", "runtime", "second-model", audit.UsageFrom(&schema.Usage{InputTokens: &zero, OutputTokens: &zero}), cost, false)
	RecordCost(span, "bedrock", "runtime", "unknown-input", audit.UsageFrom(&schema.Usage{OutputTokens: &out}), cost, false)
	span.End()
	events := sr.Ended()[0].Events()
	if len(events) != 3 {
		t.Fatalf("events=%d", len(events))
	}
	ids := map[string]bool{}
	for i, event := range events {
		attrs := map[string]attribute.Value{}
		for _, a := range event.Attributes {
			attrs[string(a.Key)] = a.Value
		}
		id := attrs["inferplane.cost.event_id"].AsString()
		if len(id) != 26 || ids[id] {
			t.Fatal("attempt event has no unique deduplication ID")
		}
		ids[id] = true
		if attrs["gen_ai.response.model"].AsString() != []string{"first-model", "second-model", "unknown-input"}[i] {
			t.Fatal("retry overwrote model evidence")
		}
		if attrs["inferplane.provider.name"].AsString() != []string{"mantle", "runtime", "runtime"}[i] {
			t.Fatal("config provider alias lost")
		}
		if attrs["inferplane.response.partial"].AsBool() != (i == 0) {
			t.Fatal("partial state lost")
		}
		if i == 0 && (attrs["inferplane.usage.cache_write_ttl_known"].AsBool() || !attrs["inferplane.usage.accounting_uncertain"].AsBool() || attrs["inferplane.usage.input_tokens_total"].AsInt64() != 130) {
			t.Fatal("fallback TTL misrepresented as fact or cache counted twice")
		}
		if i == 1 && (!attrs["inferplane.usage.cache_write_ttl_known"].AsBool() || attrs["inferplane.usage.input_tokens_total"].AsInt64() != 0 || attrs["inferplane.usage.cache_read_input_tokens"].AsInt64() != 0) {
			t.Fatal("zero attempt inherited earlier cache usage")
		}
		if i == 2 {
			if _, ok := attrs["inferplane.usage.input_tokens_total"]; ok {
				t.Fatal("missing input became a measured total")
			}
		}
	}
}
