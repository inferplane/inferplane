package requestpolicy

import (
	"bytes"
	"encoding/json"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/pricing"
)

// PricingGuard keeps opt-in Standard context prices from silently charging a
// client-selected Ultrafast/Priority/Flex request at the Standard rate. Flat
// legacy tables retain their existing contract. Counts never enter this guard.
func PricingGuard(table *pricing.Table, targets []governance.PricedTarget, raw []byte) governance.GovDecision {
	decision := governance.PricingGuard(table, targets)
	if !decision.Allowed {
		return decision
	}
	tiered := false
	for _, target := range targets {
		tiered = tiered || table.ContextTier(target.Provider, target.Upstream, pricing.Usage{}) != ""
	}
	if !tiered {
		return decision
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return governance.GovDecision{Status: 400, Reason: "invalid request for configured pricing", Code: audit.DenyPricingMissing}
	}
	value := bytes.TrimSpace(fields["service_tier"])
	if len(value) == 0 || bytes.Equal(value, []byte("null")) {
		return decision
	}
	var service string
	if json.Unmarshal(value, &service) == nil && service == "default" {
		return decision
	}
	return governance.GovDecision{Status: 402, Reason: "no configured price for the requested service tier", Code: audit.DenyPricingMissing}
}
