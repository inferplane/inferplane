package bedrockapi

import (
	"testing"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/pricing"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
)

func holderFor(provs map[string]providers.Provider, models map[string]config.ModelConfig) *live.Holder {
	ids := make(map[string]string, len(provs))
	for n := range provs {
		ids[n] = n
	}
	h := &live.Holder{}
	h.Swap(live.NewState(provs, models, pricing.New(pricing.OnMissingAllow, nil), ids))
	return h
}

func TestResolveModelCanonicalName(t *testing.T) {
	provs := map[string]providers.Provider{"p": mockprovider.New("claude-x")}
	models := map[string]config.ModelConfig{
		"claude-x": {Targets: []config.Target{{Provider: "p", Model: "global.anthropic.claude-x-v1:0"}}},
	}
	h := holderFor(provs, models)
	r := router.New(h)
	got, sub, ok := resolveModel(r, h, "claude-x")
	if sub {
		t.Fatalf("canonical name is not a substitution")
	}
	if !ok || got != "claude-x" {
		t.Fatalf("canonical-name resolution failed: %q %v", got, ok)
	}
}

func TestResolveModelReverseScanByUpstreamID(t *testing.T) {
	// The URL names the BEDROCK upstream id, not the canonical name — the
	// reverse scan must find the canonical model that targets it.
	provs := map[string]providers.Provider{"p": mockprovider.New("claude-x")}
	models := map[string]config.ModelConfig{
		"claude-x": {Targets: []config.Target{{Provider: "p", Model: "global.anthropic.claude-x-v1:0"}}},
	}
	h := holderFor(provs, models)
	r := router.New(h)
	got, sub, ok := resolveModel(r, h, "global.anthropic.claude-x-v1:0")
	if sub {
		t.Fatalf("upstream-id match is not a substitution")
	}
	if !ok || got != "claude-x" {
		t.Fatalf("reverse scan failed: %q %v", got, ok)
	}
}

func TestResolveModelReverseScanDeterministic(t *testing.T) {
	// Two canonicals share one upstream id: map iteration is randomized, so
	// the scan must sort model names and always return the same winner.
	provs := map[string]providers.Provider{"p": mockprovider.New("claude-x")}
	models := map[string]config.ModelConfig{
		"b-model": {Targets: []config.Target{{Provider: "p", Model: "shared.upstream.id-v1:0"}}},
		"a-model": {Targets: []config.Target{{Provider: "p", Model: "shared.upstream.id-v1:0"}}},
	}
	h := holderFor(provs, models)
	r := router.New(h)
	for i := 0; i < 20; i++ {
		got, _, ok := resolveModel(r, h, "shared.upstream.id-v1:0")
		if !ok || got != "a-model" {
			t.Fatalf("iteration %d: want deterministic sorted-first winner \"a-model\", got %q %v", i, got, ok)
		}
	}
}

// holderForWithFallbacks is holderFor plus model_fallbacks (D5).
func holderForWithFallbacks(provs map[string]providers.Provider, models map[string]config.ModelConfig, fallbacks map[string]string) *live.Holder {
	ids := make(map[string]string, len(provs))
	for n := range provs {
		ids[n] = n
	}
	h := &live.Holder{}
	h.Swap(live.NewStateWithFallbacks(provs, models, pricing.New(pricing.OnMissingAllow, nil), ids, fallbacks, false))
	return h
}

// D5: an unrouted canonical name (no route, and no upstream-id match either)
// substitutes for its configured model_fallbacks target, same as the
// Anthropic/OpenAI ingresses (router.ResolveModel) — before ever falling
// through to the raw-upstream-id reverse scan.
func TestResolveModelFallsBackToConfiguredFallback(t *testing.T) {
	provs := map[string]providers.Provider{"p": mockprovider.New("claude-opus-4-8")}
	models := map[string]config.ModelConfig{
		"claude-opus-4-8": {Targets: []config.Target{{Provider: "p", Model: "anthropic.claude-opus-4-8-v1:0"}}},
	}
	h := holderForWithFallbacks(provs, models, map[string]string{"claude-opus-5": "claude-opus-4-8"})
	r := router.New(h)
	got, sub, ok := resolveModel(r, h, "claude-opus-5")
	if !ok || got != "claude-opus-4-8" || !sub {
		t.Fatalf("fallback resolution failed: %q ok=%v substituted=%v, want claude-opus-4-8 substituted", got, ok, sub)
	}
}

func TestResolveModelUnknown(t *testing.T) {
	provs := map[string]providers.Provider{"p": mockprovider.New("claude-x")}
	models := map[string]config.ModelConfig{
		"claude-x": {Targets: []config.Target{{Provider: "p", Model: "up"}}},
	}
	h := holderFor(provs, models)
	r := router.New(h)
	if got, _, ok := resolveModel(r, h, "never-registered"); ok {
		t.Fatalf("unknown id must not resolve, got %q", got)
	}
}

func TestServesBedrockIngress(t *testing.T) {
	cases := map[string]bool{
		"bedrock": true,
		"mock":    true, // test-only allowance, same precedent as openaiapi.providerWire
		// Core purpose #1: Claude Code in Bedrock mode is a first-class
		// client of self-hosted vLLM/Ollama — openaicompat re-renders its
		// RawBody in Anthropic shape for non-openai ingresses, so the tee
		// stays wire-correct.
		"openai_compatible": true,
		"anthropic":         false,
	}
	for name, want := range cases {
		if got := servesBedrockIngress(name); got != want {
			t.Fatalf("servesBedrockIngress(%q) = %v, want %v", name, got, want)
		}
	}
}
