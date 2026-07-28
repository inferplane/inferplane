package bedrockapi

import (
	"sort"

	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/router"
)

func servesBedrockIngress(name string) bool {
	// mock is a test-only allowance, matching openaiapi.providerWire's
	// treatment of mock as an Anthropic-wire provider.
	return name == "bedrock" || name == "mock"
}

// resolveModel returns the model to serve, whether that model SUBSTITUTES for
// the requested one (D5 model-level fallback — the caller must then set
// x-inferplane-model-fallback, same as the Anthropic/OpenAI ingresses do with
// router.ResolveModel), and whether it resolved at all.
func resolveModel(r *router.Router, holder *live.Holder, urlID string) (string, bool, bool) {
	canonical := r.Canonical(urlID)
	if _, _, err := r.ResolveChain(canonical); err == nil {
		return canonical, false, true
	}

	st := holder.Load()
	// D5 model-level fallback: an unrouted canonical name (e.g. a hardcoded
	// client requesting a not-yet-configured "anthropic.claude-opus-5-v1:0")
	// substitutes for its configured model_fallbacks/family-default target,
	// same as the Anthropic/OpenAI ingresses (router.ResolveModel).
	if fb := st.FallbackFor(canonical); fb != "" {
		if _, ok := st.Route(fb); ok {
			return fb, true, true
		}
	}

	models := st.Models()
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		for _, target := range models[name].Targets {
			if target.Model != urlID {
				continue
			}
			prov, ok := st.Provider(target.Provider)
			// Not a substitution: urlID is this model's own upstream id.
			if ok && servesBedrockIngress(prov.Name()) {
				return name, false, true
			}
		}
	}
	return "", false, false
}
