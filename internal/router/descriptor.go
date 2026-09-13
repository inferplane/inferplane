package router

import (
	"sort"

	"github.com/inferplane/inferplane/internal/keystore"
)

// ModelDescriptor describes configured routing, independently of breaker health.
// Targets includes permitted cross-model fallbacks in configured priority order.
// It is internal metadata: upstream identifiers must not be exposed to clients.
type ModelDescriptor struct {
	Name          string
	ContextWindow int64
	Capabilities  []string
	Targets       []ChainTarget
}

// DescribeModels captures one topology generation for the entire sorted catalog,
// including model permissions, aliases, limits and provider metadata. A nil
// principal preserves unfiltered discovery for callers without auth middleware.
// Only a loaded team snapshot supplies authoritative region restrictions here.
func (r *Router) DescribeModels(p *keystore.Principal) []ModelDescriptor {
	st := r.live.Load()
	names := st.ModelNames()
	sort.Strings(names)
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = p == nil || r.allowsInState(*p, name, st)
	}
	var regions []string
	if p != nil && p.TeamSnapshotLoaded && p.TeamSnapshot != nil {
		regions = p.TeamSnapshot.AllowedRegions
	}
	out := make([]ModelDescriptor, 0, len(names))
	for _, name := range names {
		if !allowed[name] {
			continue
		}
		mc, _ := st.Route(name)
		// A configured model with no usable targets remains discoverable, but
		// its empty chain cannot advertise a supported ingress contract.
		chain, _ := configuredChain(st, name)
		var targets []ChainTarget
		for _, target := range chain {
			if allowed[target.Model] {
				targets = append(targets, target)
			}
		}
		out = append(out, ModelDescriptor{
			Name: name, ContextWindow: mc.ContextWindow, Capabilities: mc.Capabilities,
			Targets: FilterRegions(targets, regions),
		})
	}
	return out
}
