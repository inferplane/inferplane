package providerstore

import (
	"fmt"
	"sort"
)

// PricingOverrides folds every target-carried rate into a map keyed the way
// the pricing table is keyed — (provider, UPSTREAM model id), ADR-030 — plus
// the list of conflicts: two targets naming the same (provider, upstream) pair
// with DIFFERENT declared rates. The rate key is upstream-scoped, so one
// declaration prices that upstream for every route through it; a disagreement
// is therefore an authoring error, not a per-route preference. The write path
// rejects conflicts (a half-honored rate is the ADR-030 silent-0-billing bug
// class); the boot overlay logs them and proceeds with the deterministic fold
// (sorted model name, then target position — first declaration wins), because
// refusing to boot over rows only a direct DB edit can produce would be a new
// way to take the data plane down.
func PricingOverrides(models map[string]ModelRoute) (map[string]map[string]TargetPricing, []string) {
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)

	out := map[string]map[string]TargetPricing{}
	firstBy := map[[2]string]string{} // (provider, upstream) → model that declared it first
	var conflicts []string
	for _, name := range names {
		for _, t := range models[name].Targets {
			if t.Pricing == nil {
				continue
			}
			key := [2]string{t.Provider, t.Model}
			if prev, ok := out[t.Provider][t.Model]; ok {
				if prev != *t.Pricing {
					conflicts = append(conflicts, fmt.Sprintf(
						"pricing for (%s, %s) declared differently by models %q and %q",
						t.Provider, t.Model, firstBy[key], name))
				}
				continue
			}
			if out[t.Provider] == nil {
				out[t.Provider] = map[string]TargetPricing{}
			}
			out[t.Provider][t.Model] = *t.Pricing
			firstBy[key] = name
		}
	}
	return out, conflicts
}
