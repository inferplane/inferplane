package tracing

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestSettledCostEventsPreserveEveryRetryAttempt(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(Disable)
	_, span := Start(context.Background(), "responses")
	SetCost(span, 1238004, false, CostDetails{PricingVersion: "v1", ContextTier: "long"})
	SetCost(span, 644000, false, CostDetails{PricingVersion: "v2", ContextTier: "short"})
	span.End()
	events := recorder.Ended()[0].Events()
	if len(events) != 2 {
		t.Fatalf("events=%d want two settlement attempts", len(events))
	}
	for i, want := range []int64{1238004, 644000} {
		if events[i].Name != "inferplane.cost.settled" {
			t.Fatal(events[i].Name)
		}
		found := false
		for _, attribute := range events[i].Attributes {
			if attribute.Key == "inferplane.cost.amount_usd_micros" {
				found = attribute.Value.AsInt64() == want
			}
			if attribute.Key == "inferplane.cost.context_tier" {
				if attribute.Value.AsString() != []string{"long", "short"}[i] {
					t.Fatal("tier drift")
				}
			}
		}
		if !found {
			t.Fatal("earlier settled cost was overwritten by retry")
		}
	}
}
