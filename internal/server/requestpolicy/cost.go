package requestpolicy

import (
	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/tracing"
	"go.opentelemetry.io/otel/trace"
)

// RecordCost snapshots the same attempt's price, routed identity and usage in
// one event. Consumers must not join a retry's cost to final-span token counts.
func RecordCost(span trace.Span, system, provider, upstream string, usage *audit.UsageRef, cost *audit.CostRef, partial bool) {
	if cost == nil || !span.IsRecording() {
		return
	}
	d := tracing.CostDetails{PricingVersion: cost.PricingVersion, ContextTier: cost.ContextTier,
		System: system, Provider: provider, Model: upstream, Partial: partial}
	if usage != nil {
		d.Usage = &tracing.UsageDetails{Input: usage.InputTokens, Output: usage.OutputTokens,
			CacheRead: usage.CacheReadInputTokens, CacheWrite5m: usage.CacheCreation5mInputTokens,
			CacheWrite1h: usage.CacheCreation1hInputTokens, AccountingUncertain: true}
		if e := usage.Evidence; e != nil {
			d.Usage.InputKnown, d.Usage.OutputKnown = e.InputKnown, e.OutputKnown
			d.Usage.InputTotalKnown = e.InputTotalKnown
			d.Usage.CacheWriteTTLKnown, d.Usage.AccountingUncertain = e.CacheWriteTTLKnown, e.AccountingUncertain || partial
		}
	}
	tracing.SetCost(span, cost.AmountUSDMicros, cost.PricingMissing, d)
}
