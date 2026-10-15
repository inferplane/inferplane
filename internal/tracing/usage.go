package tracing

import (
	"math"

	"go.opentelemetry.io/otel/attribute"
)

// UsageDetails is a value snapshot for one attempt. Canonical input excludes
// cache; output includes reasoning. Keep this independent of audit/governance.
type UsageDetails struct {
	Input, Output, CacheRead, CacheWrite5m, CacheWrite1h             int64
	InputKnown, OutputKnown, CacheWriteTTLKnown, AccountingUncertain bool
	InputTotalKnown                                                  bool
}

func (u *UsageDetails) attributes() []attribute.KeyValue {
	if u == nil {
		return []attribute.KeyValue{attribute.Bool("inferplane.usage.accounting_uncertain", true),
			attribute.Bool("inferplane.usage.input_known", false),
			attribute.Bool("inferplane.usage.output_known", false),
			attribute.Bool("inferplane.usage.input_tokens_total_known", false),
			attribute.Bool("inferplane.usage.cache_write_ttl_known", false)}
	}
	attrs := []attribute.KeyValue{
		attribute.Bool("inferplane.usage.input_known", u.InputKnown),
		attribute.Bool("inferplane.usage.output_known", u.OutputKnown),
		attribute.Bool("inferplane.usage.cache_write_ttl_known", u.CacheWriteTTLKnown),
		attribute.Bool("inferplane.usage.accounting_uncertain", u.AccountingUncertain),
		attribute.Int64("inferplane.usage.cache_read_input_tokens", u.CacheRead),
		attribute.Int64("inferplane.usage.cache_write_5m_input_tokens", u.CacheWrite5m),
		attribute.Int64("inferplane.usage.cache_write_1h_input_tokens", u.CacheWrite1h),
	}
	if u.InputKnown {
		attrs = append(attrs, attribute.Int64("gen_ai.usage.input_tokens", u.Input))
	}
	if u.OutputKnown {
		attrs = append(attrs, attribute.Int64("gen_ai.usage.output_tokens", u.Output))
	}
	var total int64
	valid := u.InputKnown && u.InputTotalKnown
	for _, n := range []int64{u.Input, u.CacheRead, u.CacheWrite5m, u.CacheWrite1h} {
		if n < 0 || n > math.MaxInt64-total {
			valid = false
			break
		}
		total += n
	}
	attrs = append(attrs, attribute.Bool("inferplane.usage.input_tokens_total_known", valid))
	if valid {
		attrs = append(attrs, attribute.Int64("inferplane.usage.input_tokens_total", total))
	}
	return attrs
}
