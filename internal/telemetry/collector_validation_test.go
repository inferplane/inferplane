package telemetry

import (
	"math"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/pricing"
)

func TestInvalidUsageCannotPoisonTelemetryBatch(t *testing.T) {
	c := NewCollector("dp")
	c.Record("t", "", "m", pricing.Usage{Input: 10}, 100)
	for _, u := range []pricing.Usage{
		{Output: -1}, {Input: math.MaxInt64, Output: 1}, {Input: 20, Invalid: true},
	} {
		c.Record("t", "", "m", u, 50)
	}
	c.Record("t", "", "m", pricing.Usage{Input: 20}, -1)
	b := c.Drain(time.Now())
	if b == nil || len(b.Entries) != 1 {
		t.Fatalf("missing good aggregate: %+v", b)
	}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	e := b.Entries[0]
	if e.InputTokens != 10 || e.OutputTokens != 0 || e.SpentMicroUSD != 100 {
		t.Fatalf("invalid usage changed aggregate: %+v", e)
	}
}

func TestCollectorSaturatesAccumulatedUsage(t *testing.T) {
	c := NewCollector("dp")
	c.Record("t", "", "m", pricing.Usage{Input: math.MaxInt64}, math.MaxInt64)
	c.Record("t", "", "m", pricing.Usage{Input: 1}, 1)
	b := c.Drain(time.Now())
	// This synthetic load exceeds the separate wire sanity bound. It must
	// remain positive for downstream rejection, never wrap into a refund.
	if e := b.Entries[0]; e.InputTokens != math.MaxInt64 || e.SpentMicroUSD != math.MaxInt64 {
		t.Fatalf("counter overflow: %+v", e)
	}
}
