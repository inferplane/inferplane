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
	if json.Unmarshal(raw, &fields) != nil || topLevelKeyCount(raw, "service_tier") > 1 {
		// Unmarshal keeps the last duplicate while the verbatim body may be read
		// first-wins upstream, so an ambiguous tier is refused rather than priced.
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

// topLevelKeyCount counts decoded occurrences of key in a JSON object's top
// level. Callers have already validated raw; a token error stops the count.
func topLevelKeyCount(raw []byte, key string) int {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return 0
	}
	count := 0
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return count
		}
		if name, _ := tok.(string); name == key {
			count++
		}
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return count
		}
	}
	return count
}
