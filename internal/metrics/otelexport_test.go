package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestObserveOTelExport(t *testing.T) {
	m := New()
	m.ObserveOTelExport("corp", "audit", "sent", 3)
	m.ObserveOTelExport("corp", "audit", "sent", 2)
	m.ObserveOTelExport("corp", "content", "dropped_oversize", 1)
	m.ObserveOTelExport("corp", "audit", "sent", 0)
	if got := testutil.ToFloat64(m.otelExport.WithLabelValues("corp", "audit", "sent")); got != 5 {
		t.Fatalf("sent = %v", got)
	}
	if got := testutil.ToFloat64(m.otelExport.WithLabelValues("corp", "content", "dropped_oversize")); got != 1 {
		t.Fatalf("dropped = %v", got)
	}
	var nilMetrics *Metrics
	nilMetrics.ObserveOTelExport("corp", "audit", "sent", 1)
}
