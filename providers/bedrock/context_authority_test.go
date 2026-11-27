package bedrock

import (
	"testing"

	"github.com/inferplane/inferplane/providers"
)

// InvokeModel (Claude) and Converse map the upstream's exact overflow verdict
// to the recoverable error; the Mantle egress relays an unverified shape.
func TestBedrockUpstreamDecidesContextForInvokeAndConverse(t *testing.T) {
	p := &provider{modelAPI: map[string]string{"x.mantle-model": "mantle"}}
	for upstream, want := range map[string]bool{
		"global.anthropic.claude-haiku-5-5": true,
		"global.openai.gpt-6.1-sol":         true,
		"x.mantle-model":                    false,
	} {
		if got := providers.EnforcesContextWindow(p, upstream); got != want {
			t.Errorf("%s: EnforcesContextWindow = %v, want %v", upstream, got, want)
		}
	}
}
