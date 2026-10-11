package requestpolicy

import (
	"testing"

	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/pricing"
)

func TestTieredPricingRefusesUnpricedServiceBeforeBilling(t *testing.T) {
	table := pricing.New(pricing.OnMissingBlock, map[pricing.Key]pricing.Rate{{Provider: "p", Model: "m"}: {InputPerMTok: 1, ShortContextLimit: 100, LongContext: &pricing.Rate{InputPerMTok: 2}}})
	targets := []governance.PricedTarget{{Provider: "p", Upstream: "m"}}
	for _, tc := range []struct {
		body    string
		allowed bool
	}{
		{`{"input":"hello"}`, true}, {`{"service_tier":null}`, true}, {`{"service_tier":"default"}`, true},
		{`{"service_tier":"ultrafast"}`, false}, {`{"service_tier":"priority"}`, false}, {`{"service_tier":"flex"}`, false}, {`{"service_tier":true}`, false},
	} {
		if got := PricingGuard(table, targets, []byte(tc.body)); got.Allowed != tc.allowed {
			t.Fatalf("body=%s decision=%+v", tc.body, got)
		}
	}
}
